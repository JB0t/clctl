package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/compress"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/tracker"
)

// tldrSubtaskName names the subtask that holds a task's daily tl;dr comments.
const tldrSubtaskName = "summary"

// Character budgets for the tl;dr prompt, sized like the digest's for an 8k context.
const (
	tldrNotesBudget    = 12000
	tldrPreviousBudget = 2000 // per earlier tl;dr
	tldrPreviousCount  = 3
)

const tldrSystem = `You keep the running tl;dr of one task: a short summary someone reads
instead of the whole task. You get the task's earlier tl;drs and the notes pushed to it
on one day. Write the tl;dr as of that day.

Format:
- First line: one sentence on where the task stands now.
- Then at most 6 Markdown bullet points: what was done, decisions, blockers, next steps.

Rules:
- The day's notes are newest: they win over earlier tl;drs. Keep points from earlier
  tl;drs that still hold, and drop ones the notes settle or replace.
- Separate done from planned: "should", "could", "will", "proposed" and lines starting
  "(planned)" are plans, never finished work.
- Text is compressed (articles and filler removed); read it as normal prose. Lines
  such as "… 57 lines elided …" stand for shortened output: never report the shortening.
- Use only facts from the input. No preamble, no heading, no labels such as "Status:".
` + cavemanStyle

// tldrRecord is the comment holding one day's tl;dr and the pushed notes it was made from.
type tldrRecord struct {
	CommentID string `json:"comment_id"`
	Hash      string `json:"hash"`
}

// tldrState remembers each task's summary subtask and its tl;dr comment per note day.
type tldrState struct {
	Subtasks map[string]string                 `json:"subtasks"` // task -> subtask ID
	Days     map[string]map[string]*tldrRecord `json:"days"`     // task -> note day -> record
}

func tldrStatePath() string { return statePath("tldr.json") }

func loadTLDRState() *tldrState {
	state := &tldrState{}
	if data, err := os.ReadFile(tldrStatePath()); err == nil {
		_ = json.Unmarshal(data, state)
	}
	if state.Subtasks == nil {
		state.Subtasks = map[string]string{}
	}
	if state.Days == nil {
		state.Days = map[string]map[string]*tldrRecord{}
	}
	return state
}

func (s *tldrState) save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tldrStatePath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(tldrStatePath(), data, 0o644)
}

func (s *tldrState) record(taskID, day string) *tldrRecord {
	if s.Days[taskID] == nil {
		return nil
	}
	return s.Days[taskID][day]
}

// pushedNote is one day's note for a task, cut to the sections notes push posted and
// that have not changed since.
type pushedNote struct {
	taskID, day string
	sections    []markdown.Section
	hash        string // of the sections as pushed
}

// findPushedNotes lists task notes from from onwards that have pushed sections, oldest
// day first (so each day's tl;dr can build on the one before).
func findPushedNotes(root, taskID string, from time.Time) []pushedNote {
	state := loadPushState()
	var found []pushedNote
	for _, path := range notes.TaskNotes(root) {
		day := notes.Day(root, path)
		if !from.IsZero() && day < from.Format("2006-01-02") {
			break // newest day first
		}
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		if taskID != "" && !strings.EqualFold(id, taskID) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		parsed := markdown.Sections(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil, true)
		key, legacyKeys := notes.Key(root, path, id)
		pushed := state[id][key]
		for _, legacy := range legacyKeys {
			if pushed == nil {
				pushed = state[id][legacy]
			}
		}
		note := pushedNote{taskID: id, day: day}
		digest := sha256.New()
		for index, sectionKey := range sectionKeys(parsed) {
			// Only what the backend has: an edit not pushed yet is left out until it is.
			if record := pushed[sectionKey]; record != nil && record.Hash == parsed[index].Fingerprint() {
				note.sections = append(note.sections, parsed[index])
				digest.Write([]byte(record.Hash))
			}
		}
		if len(note.sections) > 0 {
			note.hash = hex.EncodeToString(digest.Sum(nil))
			found = append(found, note)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].day < found[j].day })
	return found
}

// tldrPace is how much one tl;dr run does: at most limit task-days (0 = all), with a
// pause between model calls.
type tldrPace struct {
	limit int
	pause time.Duration
}

func newTLDRCmd(app *App) *cobra.Command {
	var taskID, since, model, llmURL string
	var dryRun, again, gentle bool
	var pace tldrPace
	cmd := &cobra.Command{
		Use:   "tldr",
		Short: "Write each task's daily tl;dr into its 'summary' subtask from pushed notes",
		Long: `For each task whose pushed notes are new or changed, asks the local model for the
task's tl;dr as of that note day and posts it as a comment on the task's subtask named
"summary" (created when missing). The model gets the day's pushed note sections and
the latest earlier tl;dr comments, so the tl;dr runs on from day to day. A day's
tl;dr is updated in place when more notes for that day are pushed later. Sections
edited but not pushed again are left out until they are. Meant for cron, after the
day's notes are pushed; it shares the digest's lock and model server settings.

--gentle trades speed for a lighter load, with the same model and prompts, so the
tl;drs are as good: a server clctl starts gets LLM_PROFILE=low (at most 4 threads,
small batches, 8k context), it pauses --pause (default 30s) between tl;drs, and it
writes at most --limit (default 5) per run, oldest first; the next run does the rest.`,
		Example: `  clctl notes tldr                     # notes pushed for today and yesterday
  clctl notes tldr --since 7d --dry-run
  clctl notes tldr --task-id TECH-1234 --again
  clctl notes tldr --since 2026-09-01 --gentle   # a backfill, a few days per run`,
		Annotations: notesAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := app.NotesRoot()
			if err != nil {
				return err
			}
			from, err := parseSince(since)
			if err != nil {
				return err
			}
			if gentle {
				if !cmd.Flags().Changed("limit") {
					pace.limit = 5
				}
				if !cmd.Flags().Changed("pause") {
					pace.pause = 30 * time.Second
				}
				// Read by llm-server when clctl starts it (llm_start); a server already
				// running keeps its settings.
				if os.Getenv("LLM_PROFILE") == "" {
					_ = os.Setenv("LLM_PROFILE", "low")
				}
			}
			return writeTLDRs(app, root, taskID, from, model, llmURL, dryRun, again, pace)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	flags := cmd.Flags()
	flags.StringVar(&since, "since", "2d", "note days to look at: 2d, 1w or a YYYY-MM-DD date")
	flags.BoolVar(&dryRun, "dry-run", false, "print the tl;drs instead of posting them")
	flags.BoolVar(&again, "again", false, "rewrite tl;drs whose notes have not changed")
	flags.StringVar(&model, "model", "", "model name (default llm_model, else the server's first model)")
	flags.StringVar(&llmURL, "llm-url", "", "model server URL (default llm_url, else http://localhost:11434)")
	flags.BoolVar(&gentle, "gentle", false, "lighter load, same quality: low-profile server, pauses, a few tl;drs per run")
	flags.IntVar(&pace.limit, "limit", 0, "at most this many tl;drs per run, oldest first (0 = all; --gentle: 5)")
	flags.DurationVar(&pace.pause, "pause", 0, "wait between tl;drs, e.g. 30s (--gentle: 30s)")
	return cmd
}

func writeTLDRs(app *App, root, taskID string, from time.Time, model, llmURL string, dryRun, again bool, pace tldrPace) error {
	state := loadTLDRState()
	var due []pushedNote
	for _, note := range findPushedNotes(root, taskID, from) {
		if record := state.record(note.taskID, note.day); again || record == nil || record.Hash != note.hash {
			due = append(due, note)
		}
	}
	if len(due) == 0 {
		fmt.Println("No new pushed notes since", from.Format("2006-01-02")+"; nothing to do.")
		return nil
	}
	if pace.limit > 0 && len(due) > pace.limit {
		fmt.Printf("%d tl;drs due; writing the oldest %d now, the rest on later runs.\n", len(due), pace.limit)
		due = due[:pace.limit]
	}
	unlock, err := lockDigest()
	if err != nil {
		return err
	}
	defer unlock()
	summarizer, err := newSummarizer(app, model, llmURL)
	if err != nil {
		return err
	}
	defer summarizer.Close()
	client, err := app.Backend()
	if err != nil {
		return err
	}
	failed := 0
	for index, note := range due {
		if index > 0 && pace.pause > 0 {
			time.Sleep(pace.pause)
		}
		fmt.Fprintf(os.Stderr, "tl;dr %s for %s (%d/%d)...\n", note.taskID, note.day, index+1, len(due))
		if err := writeTLDR(app, client, summarizer, state, note, dryRun); err != nil {
			// One task's failure (a deleted task, a model hiccup) leaves the rest to run.
			fmt.Fprintf(os.Stderr, "  %s %s: %v\n", note.taskID, note.day, err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tl;drs failed", failed, len(due))
	}
	return nil
}

func writeTLDR(app *App, client tracker.Backend, s *summarizer, state *tldrState, note pushedNote, dryRun bool) error {
	task, err := app.SelectTask(note.taskID, TaskScope{Everyone: true, IncludeDone: true})
	if err != nil {
		return err
	}
	subtaskID := state.Subtasks[note.taskID]
	if subtaskID == "" {
		if subtaskID, err = findSummarySubtask(client, task); err != nil {
			return err
		}
	}
	record := state.record(note.taskID, note.day)
	var previous []string
	if subtaskID != "" {
		comments, err := client.Comments(subtaskID)
		if err != nil {
			return err
		}
		// Days of the tl;drs clctl wrote, so a rewrite of an older day only sees earlier ones.
		commentDays := map[string]string{}
		for day, written := range state.Days[note.taskID] {
			commentDays[written.CommentID] = day
		}
		exists := false
		for _, comment := range comments { // newest first
			if record != nil && string(comment.ID) == record.CommentID {
				exists = true
				continue // this day's own tl;dr is being rewritten
			}
			if day, ok := commentDays[string(comment.ID)]; ok && day >= note.day {
				continue
			}
			if len(previous) < tldrPreviousCount {
				text := markdown.PlainTextForModel(comment.Comment)
				if text == "" {
					text = strings.TrimSpace(comment.CommentText)
				}
				previous = append([]string{clip(text, tldrPreviousBudget)}, previous...) // oldest first
			}
		}
		if record != nil && !exists {
			record = nil // deleted in the backend: post again
		}
	}

	var source []string
	for _, section := range note.sections {
		source = append(source, section.Source...)
	}
	text, _ := compress.Text(strings.Join(source, "\n"))
	history := "(none yet)"
	if len(previous) > 0 {
		history = strings.Join(previous, "\n\n---\n\n")
	}
	prompt := fmt.Sprintf("BACKGROUND (for understanding only)\n%s\nEARLIER TL;DRS, oldest first\n%s\n\nNOTES PUSHED FOR %s\n%s\n\nWrite the tl;dr as of %s.",
		s.background(task), history, note.day, clip(text, tldrNotesBudget), note.day)
	summary, err := s.generate("tldr "+note.taskID+" "+note.day, tldrSystem, prompt)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("## tl;dr %s\n\n%s\n\n_From the notes pushed for %s; written by clctl notes tldr (%s)._",
		note.day, strings.TrimSpace(summary), note.day, s.client.Model)
	if dryRun {
		fmt.Printf("--- %s %s (into its %q subtask)\n%s\n\n", task.Ref(), note.day, tldrSubtaskName, body)
		return nil
	}
	if subtaskID == "" {
		subtask, err := client.CreateTask(task.List.ID, tracker.NewTask{
			Name:        tldrSubtaskName,
			Parent:      task.ID,
			Description: "Daily tl;dr of the parent task, one comment per day, written by clctl notes tldr from the notes pushed to it.",
		})
		if err != nil {
			return fmt.Errorf("create the %q subtask: %w", tldrSubtaskName, err)
		}
		subtaskID = subtask.ID
		fmt.Printf("Created the %q subtask of %s.\n", tldrSubtaskName, task.Ref())
	}
	state.Subtasks[note.taskID] = subtaskID
	converted := markdown.Sections(strings.Split(body, "\n"), nil, false)
	if len(converted) == 0 {
		return errors.New("the tl;dr came out empty")
	}
	comment := converted[0].Comment
	if record != nil {
		if err := client.UpdateComment(record.CommentID, comment); err != nil {
			return err
		}
		fmt.Printf("Updated the %s tl;dr of %s.\n", note.day, task.Ref())
	} else {
		id, err := client.PostComment(subtaskID, comment)
		if err != nil {
			return err
		}
		record = &tldrRecord{CommentID: id}
		fmt.Printf("Posted the %s tl;dr of %s.\n", note.day, task.Ref())
	}
	record.Hash = note.hash
	if state.Days[note.taskID] == nil {
		state.Days[note.taskID] = map[string]*tldrRecord{}
	}
	state.Days[note.taskID][note.day] = record
	return state.save()
}

// findSummarySubtask returns the ID of the task's subtask named "summary", or "".
func findSummarySubtask(client tracker.Backend, task tracker.Task) (string, error) {
	subtasks, err := client.Subtasks(task.ID)
	if err != nil {
		return "", err
	}
	for _, subtask := range subtasks {
		if isTLDRSubtask(subtask) {
			return subtask.ID, nil
		}
	}
	return "", nil
}

// isTLDRSubtask reports whether a task is a tl;dr holder, which digests leave out: its
// comments restate its parent's.
func isTLDRSubtask(task tracker.Task) bool {
	return task.Parent != "" && strings.EqualFold(strings.TrimSpace(task.Name), tldrSubtaskName)
}
