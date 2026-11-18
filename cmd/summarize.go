package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JB0t/clctl/internal/cache"
	"github.com/JB0t/clctl/internal/compress"
	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/llm"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
)

// promptVersion is part of the summary cache keys; bump it when the prompts change.
const promptVersion = "10"

// Character budgets (roughly 4 characters per token) sized for an 8k-token context.
const (
	notesBudget   = 2500
	commentBudget = 18000
)

// bulletLimit grows logarithmically with the number of comments: 1 comment gets 3
// bullets, 4 get 7, 8 get 9, 16 get 11, never more than 12.
func bulletLimit(comments int) int {
	return min(3+int(math.Round(2*math.Log2(float64(max(comments, 1))))), 12)
}

const commentSystem = `You summarise one comment from a task, for a weekly IT team meeting.
Write 1 to 3 short Markdown bullet points stating what the comment says.

Rules:
- Separate what was done from what was only suggested, proposed or planned. Phrases
  such as "provision via", "should", "could", "potentially", "mentions", "proposed",
  "plan" and "next steps" mark suggestions or plans: report them as such, never as done.
- Lines starting "(planned)" come from sections such as Overview, Goals, Plan or Next
  steps: they are intentions or requirements, never finished work.
- Text is compressed (articles and filler removed); read it as normal prose. Lines
  such as "… 57 lines elided: all rows=500 …" stand for output that was shortened
  before you saw it: use what they state, never report the shortening itself.
- Keep the tense and certainty of the comment: "we will build" stays "will build",
  "could" stays "could". A proposal or design document describes intended work.
- Only say how something was done when the comment shows it. Code blocks show the
  commands that were actually run or written, e.g. "created with az CLI commands".
- Use only facts from the comment. The background only explains names; never
  summarise it and never invent anything.
- No preamble, no labels such as "Progress:", no filler such as "no updates".
` + cavemanStyle

const combineSystem = `You combine per-comment summaries of one task into a briefing for a
weekly IT team meeting. Write short Markdown bullet points, most important first.

Rules:
- State only what the summaries say, keeping their tense: planned work stays planned
  ("will", "proposed"), suggestions stay suggestions.
- Merge duplicates and keep names, numbers and commands exact.
- No preamble, no labels such as "Progress:", no filler such as "no new information".
` + cavemanStyle

// cavemanStyle keeps the bullets terse: a digest is read at a glance, and every token
// the model writes costs time on a CPU (around 10 tokens a second for a 30B model).
const cavemanStyle = `- Write caveman style: terse fragments, no articles, filler or pleasantries, at
  most 15 words per bullet. Keep every name, number, path, command, error and negation
  exact, and keep "will", "could" and "planned".`

const historyRule = `
- A summary from the previous meeting is included: focus on what is new or changed
  since then, and leave out points that have not changed.`

type summarizer struct {
	client *llm.Client
	stop   func() // stops the model container if this run started it
	cache  map[string]string
	path   string
	root   string // notes root, "" when notes are off
	tasks  *cache.Cache
	// previous holds each task's summary from the last earlier digest, for continuity.
	previous    map[string]string
	previousDay string
	// fullBackground adds the task description and your notes to the background.
	// Off by default: a small model tends to report plans found there as done work.
	fullBackground bool
	// before/after count comment characters around compression, for reporting.
	before, after int
	// promptTokens/outputTokens count what the model processed (cached answers cost none).
	promptTokens, outputTokens int
}

func summaryCachePath() string {
	return filepath.Join(filepath.Dir(cache.DefaultPath()), "summaries.json")
}

func newSummarizer(app *App, model, url string) (*summarizer, error) {
	cfg, err := app.Config()
	if err != nil {
		return nil, err
	}
	client, stop, err := app.ModelServer(model, url)
	if err != nil {
		return nil, err
	}
	c, err := app.Cache()
	if err != nil {
		stop()
		return nil, err
	}
	root, _ := app.NotesRoot()
	s := &summarizer{client: client, stop: stop, fullBackground: cfg.Get(config.KeyLLMBackground) == "full", cache: map[string]string{}, path: summaryCachePath(), root: root, tasks: c}
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, &s.cache)
	}
	return s, nil
}

// usePreviousBefore takes the task summaries of the latest summarised digest from a
// day before day as context, so a briefing focuses on what changed since then.
func (s *summarizer) usePreviousBefore(day time.Time) {
	s.previousDay, s.previous = "", nil
	if s.root == "" {
		return
	}
	s.previousDay, s.previous = previousDigest(s.root, day)
	if s.previousDay != "" {
		fmt.Fprintf(os.Stderr, "Using %d task summaries from the %s digest as context.\n", len(s.previous), s.previousDay)
	}
}

// Close stops the model container if this run started it.
func (s *summarizer) Close() { s.stop() }

func (s *summarizer) save() {
	data, _ := json.MarshalIndent(s.cache, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, data, 0o644)
}

// background tells the model which task a comment belongs to: its name, location and
// parent (plus description and notes with llm_background = full). It is kept stable
// from day to day so cached per-comment summaries stay valid.
func (s *summarizer) background(task tracker.Task) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Task %s: %s\nStatus: %s. Location: %s.\n", task.Ref(), task.Name, task.Status.Status, tasks.Location(task))
	if task.Parent != "" {
		if parent, ok := s.tasks.Find(task.Parent); ok {
			fmt.Fprintf(&builder, "Parent task: %s %s\n", parent.Ref(), parent.Name)
		}
	}
	if !s.fullBackground {
		return builder.String()
	}
	if description := strings.TrimSpace(task.Description); description != "" {
		fmt.Fprintf(&builder, "\nTask description:\n%s\n", clip(description, 1500))
	}
	if s.root != "" {
		remaining := notesBudget
		for _, path := range notes.Find(s.root, task.Ref()) { // newest day first
			data, err := os.ReadFile(path)
			if err != nil || remaining <= 0 {
				continue
			}
			excerpt := clip(string(data), remaining)
			remaining -= len(excerpt)
			fmt.Fprintf(&builder, "\nAuthor's notes (%s):\n%s\n", orUndated(notes.Day(s.root, path)), excerpt)
		}
	}
	return builder.String()
}

// generate returns a cached completion, or runs and caches it.
func (s *summarizer) generate(label, system, prompt string) (string, error) {
	sum := sha256.Sum256([]byte(promptVersion + "\x00" + s.client.Model + "\x00" + system + "\x00" + prompt))
	key := hex.EncodeToString(sum[:])
	if summary, ok := s.cache[key]; ok {
		return summary, nil
	}
	summary, stats, err := s.client.Generate(system, prompt)
	if err != nil {
		return "", err
	}
	summary = compress.CleanSummary(summary)
	s.promptTokens += stats.PromptTokens
	s.outputTokens += stats.OutputTokens
	fmt.Fprintf(os.Stderr, "  %s: %d prompt + %d output tokens, %s\n", label, stats.PromptTokens, stats.OutputTokens, stats.Duration.Round(time.Second))
	s.cache[key] = summary
	s.save()
	return summary, nil
}

// Summarize summarises each comment in the window (cached per comment), then merges
// those into one briefing for the task.
func (s *summarizer) Summarize(entry *digestEntry) (string, error) {
	background := s.background(entry.task)
	var parts []string
	for index := len(entry.comments) - 1; index >= 0; index-- { // oldest first
		comment := entry.comments[index]
		// ClickUp's comment_text drops tables, so render the rich comment instead.
		text := markdown.PlainTextForModel(comment.Comment)
		if text == "" {
			text = strings.TrimSpace(comment.CommentText)
		}
		text, stats := compress.Text(text)
		s.before += stats.Before
		s.after += stats.After
		if text == "" || text == "undefined" {
			continue
		}
		when := millisTime(string(comment.Date)).Format("Mon 2 Jan 15:04")
		prompt := fmt.Sprintf("BACKGROUND (for understanding only)\n%s\nCOMMENT by %s, %s\n%s\n\nSummarise the comment.",
			background, comment.User.DisplayName(), when, clip(text, commentBudget))
		summary, err := s.generate("comment "+string(comment.ID), commentSystem, prompt)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("[%s, %s]\n%s", when, comment.User.DisplayName(), summary))
	}
	previous := s.previous[entry.task.Ref()]
	switch {
	case len(parts) == 0:
		return "", nil
	case len(parts) == 1 && previous == "":
		return capBullets(parts[0][strings.Index(parts[0], "\n")+1:], bulletLimit(1)), nil
	}
	limit := bulletLimit(len(parts))
	history := ""
	if previous != "" {
		history = fmt.Sprintf("PREVIOUS MEETING SUMMARY (%s)\n%s\n\n", s.previousDay, previous)
	}
	prompt := fmt.Sprintf("Task %s: %s (%s)\n\n%sPER-COMMENT SUMMARIES since then, oldest first\n%s\n\nWrite the briefing in at most %d bullet points.",
		entry.task.Ref(), entry.task.Name, entry.task.Status.Status, history, strings.Join(parts, "\n\n"), limit)
	system := combineSystem
	if previous != "" {
		system += historyRule
	}
	combined, err := s.generate("combined", system, prompt)
	if err != nil {
		return "", err
	}
	return capBullets(combined, limit), nil
}

// capBullets keeps at most limit bullet lines; small models overshoot limits.
func capBullets(text string, limit int) string {
	var kept []string
	bullets := 0
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ") {
			bullets++
			if bullets > limit {
				continue
			}
			line = "- " + strings.TrimSpace(trimmed[2:])
		} else if bullets > limit {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// clip cuts text to at most limit bytes on a line boundary where possible.
func clip(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := text[:limit]
	if index := strings.LastIndex(cut, "\n"); index > limit/2 {
		cut = cut[:index]
	}
	return strings.ToValidUTF8(cut, "") + "\n[…]"
}

// previousDigest reads the latest digest.md with AI summaries from a day before day and
// returns its day and each task's summary, keyed by task ID.
func previousDigest(root string, day time.Time) (string, map[string]string) {
	today := notes.DayDir(root, day)
	days, _ := filepath.Glob(filepath.Join(root, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]", "digest.md"))
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for _, path := range days {
		if filepath.Dir(path) >= today {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if summaries := parseDigestSummaries(string(data)); len(summaries) > 0 {
			return notes.Day(root, path), summaries
		}
	}
	return "", nil
}

// parseDigestSummaries extracts the **Summary** block of each "## <ID> <name>" section.
func parseDigestSummaries(text string) map[string]string {
	summaries := map[string]string{}
	current := ""
	var lines []string
	collecting := false
	flush := func() {
		if current != "" && len(lines) > 0 {
			summaries[current] = strings.TrimSpace(strings.Join(lines, "\n"))
		}
		lines, collecting = nil, false
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			current, _, _ = strings.Cut(strings.TrimPrefix(line, "## "), " ")
		case strings.HasPrefix(line, "**Summary**"): // "**Summary** (from the tl;dr of ...)" too
			collecting = true
		case line == "**Comments**":
			flush()
		case collecting:
			lines = append(lines, line)
		}
	}
	flush()
	return summaries
}
