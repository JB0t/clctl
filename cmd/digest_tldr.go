package cmd

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

// A task with a "summary" subtask holding tl;drs from the digest window is summarised
// from those, not from its comments: each tl;dr already condenses a day's notes. Tasks
// without one keep the per-comment summaries.

// taskTLDR is one day's tl;dr from a task's summary subtask.
type taskTLDR struct {
	day  string // YYYY-MM-DD, the note day it covers
	text string // the status line and bullets, without the heading and footer
}

const tldrMeetingSystem = `You turn the daily tl;drs of one task into its briefing for a weekly IT
team meeting. Each tl;dr is a running summary of the task as of its day, so the newest
one is where the task stands now; earlier ones show what happened on the way.

Format:
- First line: one sentence on where the task stands now.
- Then Markdown bullet points: what was done since the first tl;dr, decisions,
  blockers and next steps, most important first.

Rules:
- The newest tl;dr wins where they disagree.
- Keep tense and certainty: planned work stays planned ("will", "proposed"),
  suggestions stay suggestions.
- Merge duplicates and keep names, numbers and commands exact.
- Use only facts from the tl;drs. No preamble, no heading, no labels such as "Status:".
` + cavemanStyle

var (
	tldrHeadingRE = regexp.MustCompile(`(?i)^#*\s*tl;dr\s+(\d{4}-\d{2}-\d{2})\s*$`)
	tldrFooterRE  = regexp.MustCompile(`^_?From the notes pushed for `)
)

// parseTLDRComment reads a comment clctl notes tldr wrote: "## tl;dr DAY", the tl;dr,
// then a footer line. Other comments are not tl;drs.
func parseTLDRComment(comment tracker.Comment) (taskTLDR, bool) {
	text := markdown.PlainText(comment.Comment)
	if text == "" {
		text = strings.TrimSpace(comment.CommentText)
	}
	heading, body, _ := strings.Cut(strings.TrimSpace(text), "\n")
	match := tldrHeadingRE.FindStringSubmatch(strings.TrimSpace(heading))
	if match == nil {
		return taskTLDR{}, false
	}
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if !tldrFooterRE.MatchString(strings.TrimSpace(line)) {
			kept = append(kept, line)
		}
	}
	body = strings.TrimSpace(strings.Join(kept, "\n"))
	return taskTLDR{day: match[1], text: body}, body != ""
}

// windowTLDRs keeps one tl;dr per day (the newest comment) from the first day of the
// window to its last, oldest first.
func windowTLDRs(comments []tracker.Comment, from, until time.Time) []taskTLDR {
	first := from.Format("2006-01-02")
	if until.IsZero() {
		until = time.Now()
	}
	last := until.Format("2006-01-02")
	byDay := map[string]taskTLDR{}
	for _, comment := range comments { // newest first
		tldr, ok := parseTLDRComment(comment)
		if !ok || tldr.day < first || tldr.day > last {
			continue
		}
		if _, seen := byDay[tldr.day]; !seen {
			byDay[tldr.day] = tldr
		}
	}
	list := make([]taskTLDR, 0, len(byDay))
	for _, tldr := range byDay {
		list = append(list, tldr)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].day < list[j].day })
	return list
}

// addTLDRs gives each entry the tl;drs in its summary subtask from the window. A task
// without the subtask, or whose subtask cannot be read, keeps none and falls back to
// its comments.
func addTLDRs(app *App, entries []*digestEntry, from, until time.Time) {
	client, err := app.Backend()
	if err != nil {
		return
	}
	known := loadTLDRState().Subtasks
	var group sync.WaitGroup
	limiter := make(chan struct{}, 6)
	for _, entry := range entries {
		group.Add(1)
		go func() {
			defer group.Done()
			limiter <- struct{}{}
			defer func() { <-limiter }()
			task := entry.task
			var err error
			subtaskID := known[task.Ref()]
			if subtaskID == "" {
				subtaskID = known[task.ID]
			}
			if subtaskID == "" {
				if subtaskID, err = findSummarySubtask(client, task); err != nil {
					fmt.Fprintf(os.Stderr, "%s: summary subtask: %v; summarising its comments instead\n", task.Ref(), err)
					return
				}
			}
			if subtaskID == "" {
				return
			}
			comments, err := client.Comments(subtaskID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: tl;drs: %v; summarising its comments instead\n", task.Ref(), err)
				return
			}
			entry.tldrs = windowTLDRs(comments, from, until)
		}()
	}
	group.Wait()
}

// SummarizeTLDRs is a task's summary from its tl;drs: a single tl;dr as it is, several
// merged by the model.
func (s *summarizer) SummarizeTLDRs(entry *digestEntry) (string, error) {
	if len(entry.tldrs) == 1 {
		return entry.tldrs[0].text, nil
	}
	var parts []string
	for _, tldr := range entry.tldrs {
		parts = append(parts, fmt.Sprintf("[tl;dr %s]\n%s", tldr.day, clip(tldr.text, tldrPreviousBudget)))
	}
	history, system := "", tldrMeetingSystem
	if previous := s.previous[entry.task.Ref()]; previous != "" {
		history = fmt.Sprintf("PREVIOUS MEETING SUMMARY (%s)\n%s\n\n", s.previousDay, previous)
		system += historyRule
	}
	limit := bulletLimit(len(parts) * 2)
	prompt := fmt.Sprintf("Task %s: %s (%s)\n\n%sDAILY TL;DRS since then, oldest first\n%s\n\nWrite the briefing: one status sentence, then at most %d bullet points.",
		entry.task.Ref(), entry.task.Name, entry.task.Status.Status, history, strings.Join(parts, "\n\n"), limit)
	summary, err := s.generate("tl;drs "+entry.task.Ref(), system, prompt)
	if err != nil {
		return "", err
	}
	return capBullets(summary, limit), nil
}

// tldrSource names the tl;drs a summary came from, for the digest.
func tldrSource(tldrs []taskTLDR) string {
	switch len(tldrs) {
	case 0:
		return ""
	case 1:
		return "from the tl;dr of " + tldrs[0].day
	default:
		return fmt.Sprintf("from %d tl;drs, %s to %s", len(tldrs), tldrs[0].day, tldrs[len(tldrs)-1].day)
	}
}
