package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/compress"
	"github.com/JB0t/clctl/internal/index"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
)

const statusItemSystem = `You read one note or comment about a task and list what it says about the
state of the work, as 1 to 5 tagged Markdown bullets.

Tags:
[done] = finished work
[doing] = work underway
[blocked] = a blocker or an open question
[next] = planned or suggested work
[decision] = a choice that was made
[fact] = a name, version, location or number needed to understand the state

Output only bullets in this form, with the text's own facts in place of <...>:
- [done] <what was finished>
- [next] <what is planned>

Rules:
- Never write a tag's meaning as a bullet; a bullet always states something the text says.
- Keep the certainty of the text: "will", "should", "could", "proposed", "plan" and
  lines starting "(planned)" are [next], never [done].
- Text is compressed (articles and filler removed); read it as normal prose. Lines
  such as "… 57 lines elided: all rows=500 …" stand for shortened output.
- Code blocks show commands that were run or written.
- Use only facts from the text. The background only explains names; never summarise it.
- No preamble.
` + cavemanStyle

const statusSystem = `You write the current state of one task from tagged notes about it, which are
dated and oldest first. Later notes override earlier ones: a fix supersedes the bug
report, finished work supersedes the plan for it, a newer decision supersedes an
older one.

Write these Markdown sections, in this order, leaving out any that would be empty:
## Done
## In progress
## Blocked / open questions
## Next

Rules:
- One bullet per piece of work, most important first; end each with the date of the
  latest note it comes from, as (YYYY-MM-DD).
- Merge repeats. Keep names, versions, numbers and commands exact.
- Planned or suggested work stays in Next; never report it as done.
- "Related notes from other tasks" may be about something else: use them only when
  they clearly concern this task, and say "(related: SOURCE)" when you do.
- No preamble, no closing remarks, no other headings.
` + cavemanStyle

// statusBudget bounds the tagged notes sent in one request (about 6k tokens); a task
// with more history is reduced in parts, oldest first, and the parts combined.
const statusBudget = 24000

// statusItem is one comment or one day's note.
type statusItem struct {
	kind, label string
	when        time.Time
	text        string
	summary     string
}

func newStatusCmd(app *App) *cobra.Command {
	var taskID, output, since, model, llmURL string
	var save, related bool
	var relatedCount int
	cmd := &cobra.Command{
		Use:   "status [TASK-ID]",
		Short: "Summarise where a task's work stands, from all its notes and comments",
		Long: `Reads everything about one task: every comment, every day's note in the notes layout
and its description. The model tags what each one says (done, doing, blocked, next,
decision, fact), then writes the current state in four sections: Done, In progress,
Blocked / open questions and Next. Later notes override earlier ones, so a fix
replaces the bug report and finished work replaces the plan.

Each comment and note is summarised once and cached, so running it again only
reads what is new. --related adds the closest matches from other tasks' notes and
comments in the search index ('clctl index --comments' builds it), for work recorded
elsewhere. --save writes the result to status.md in today's folder for the task.`,
		Example: `  clctl tasks status TECH-1234
  clctl tasks status TECH-1234 --related --save
  clctl tasks status TECH-1234 --since 2026-09-01 -o state.md`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{annotationTaskAction: "true"},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return completeTaskIDs(app), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				taskID = args[0]
			}
			var from time.Time
			if since != "" {
				var err error
				if from, err = parseSince(since); err != nil {
					return err
				}
			}
			task, err := app.SelectTask(taskID, TaskScope{Everyone: true, IncludeDone: true})
			if err != nil {
				return err
			}
			task = app.WithSpaceName(task)
			items, err := statusItems(app, task, from)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				if since != "" {
					return fmt.Errorf("%s has no comments or notes since %s", task.Ref(), from.Format("2006-01-02"))
				}
				return fmt.Errorf("%s has no comments or notes", task.Ref())
			}
			var relatedText string
			if related {
				if relatedText, err = relatedMaterial(app, task, relatedCount); err != nil {
					return fmt.Errorf("--related: %w", err)
				}
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
			text, err := taskStatus(summarizer, task, items, relatedText)
			if err != nil {
				return err
			}

			if save {
				root, err := app.NotesRoot()
				if err != nil {
					return err
				}
				note, err := notes.Ensure(root, task)
				if err != nil {
					return err
				}
				output = filepath.Join(strings.TrimSuffix(note, ".md"), "status.md")
			}
			if output == "" || output == "-" {
				fmt.Print(text)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(output, []byte(text), 0o644); err != nil {
				return err
			}
			fmt.Println("Wrote", output)
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "", "write to this file instead of the terminal")
	flags.BoolVar(&save, "save", false, "write to status.md in today's folder for the task (notes layout)")
	flags.StringVar(&since, "since", "", "only notes and comments from this far back (7d, 2w or YYYY-MM-DD)")
	flags.BoolVar(&related, "related", false, "also use the closest notes and comments from other tasks (search index)")
	flags.IntVar(&relatedCount, "related-count", 6, "how many related matches --related adds")
	flags.StringVar(&model, "model", "", "model name (default llm_model, else the server's first model)")
	flags.StringVar(&llmURL, "llm-url", "", "model server URL (default llm_url)")
	cmd.MarkFlagsMutuallyExclusive("output", "save")
	return cmd
}

// statusItems collects a task's comments and dated notes since from (zero: all),
// compressed for the model, oldest first.
func statusItems(app *App, task tracker.Task, from time.Time) ([]statusItem, error) {
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	comments, err := client.Comments(task.Ref())
	if err != nil {
		return nil, err
	}
	var items []statusItem
	for _, comment := range comments {
		when := millisTime(string(comment.Date))
		if when.Before(from) {
			continue
		}
		text := markdown.PlainTextForModel(comment.Comment)
		if text == "" {
			text = strings.TrimSpace(comment.CommentText)
		}
		text, _ = compress.Text(text)
		if text == "" || text == "undefined" {
			continue
		}
		items = append(items, statusItem{kind: "comment", label: fmt.Sprintf("comment %s by %s", comment.ID, comment.User.DisplayName()),
			when: when, text: clip(text, commentBudget)})
	}
	firstDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	if root, err := app.NotesRoot(); err == nil {
		for _, path := range notes.Find(root, task.Ref()) {
			day, err := time.ParseInLocation("2006-01-02", notes.Day(root, path), time.Local)
			if err != nil || (!from.IsZero() && day.Before(firstDay)) {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			text, _ := compress.Text(string(data))
			// A note that is only its "# ID: name" title says nothing yet.
			if len(strings.Split(strings.TrimSpace(text), "\n")) < 2 {
				continue
			}
			// Notes are written through the day: date them at its end, after its comments.
			items = append(items, statusItem{kind: "note", label: "note of " + day.Format("2006-01-02"),
				when: day.Add(24*time.Hour - time.Second), text: clip(text, commentBudget)})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].when.Before(items[j].when) })
	return items, nil
}

// relatedMaterial returns the closest index matches about the task from other tasks'
// notes and comments, as text for the prompt.
func relatedMaterial(app *App, task tracker.Task, count int) (string, error) {
	idx := index.Load(indexPath())
	if len(idx.Chunks) == 0 {
		return "", errors.New("the search index is empty; run 'clctl index --comments' first")
	}
	client, stop, err := app.EmbedServer()
	if err != nil {
		return "", err
	}
	defer stop()
	query := task.Ref() + " " + task.Name
	if description := strings.TrimSpace(task.Description); description != "" {
		query += ". " + clip(description, 300)
	}
	vectors, err := client.Embed(idx.Model, []string{"search_query: " + query})
	if err != nil {
		return "", err
	}
	own := func(chunk index.Chunk) bool {
		base := filepath.Base(chunk.Source)
		return strings.HasPrefix(chunk.Source, task.Ref()+"#") || strings.EqualFold(base, task.Ref()+".md") ||
			strings.Contains("/"+chunk.Source+"/", "/"+task.Ref()+"/")
	}
	var builder strings.Builder
	for _, result := range idx.Search(vectors[0], count, func(chunk index.Chunk) bool { return !own(chunk) }) {
		where := result.Source
		if result.Title != "" {
			where += " > " + result.Title
		}
		text, _ := compress.Text(result.Text)
		fmt.Fprintf(&builder, "\n[%s, %s, match %.2f]\n%s\n", where, orUndated(result.Day), result.Score, clip(text, 800))
	}
	return builder.String(), nil
}

// statusBackground explains the task to the model: name, state, place and description.
func statusBackground(s *summarizer, task tracker.Task) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Task %s: %s\nStatus: %s. Location: %s.\n", task.Ref(), task.Name, task.Status.Status, tasks.Location(task))
	if task.Parent != "" {
		if parent, ok := s.tasks.Find(task.Parent); ok {
			fmt.Fprintf(&builder, "Parent task: %s %s\n", parent.Ref(), parent.Name)
		}
	}
	if description := strings.TrimSpace(task.Description); description != "" {
		compressed, _ := compress.Text(description)
		fmt.Fprintf(&builder, "Description:\n%s\n", clip(compressed, 1500))
	}
	return builder.String()
}

// taskStatus tags each item (cached), then writes the task's state from the tags,
// reducing a long history in parts so each request stays within the model's context.
func taskStatus(s *summarizer, task tracker.Task, items []statusItem, related string) (string, error) {
	background := statusBackground(s, task)
	started := time.Now()
	for index := range items {
		item := &items[index]
		fmt.Fprintf(os.Stderr, "Reading %s (%d/%d)...\n", item.label, index+1, len(items))
		prompt := fmt.Sprintf("BACKGROUND (for understanding only)\n%s\n%s, %s\n%s\n\nList what it says about the state of the work.",
			background, strings.ToUpper(item.label[:1])+item.label[1:], item.when.Format("Mon 2 Jan 2006"), item.text)
		summary, err := s.generate(item.label, statusItemSystem, prompt)
		if err != nil {
			// One bad answer (a model stuck repeating itself) should not lose the rest:
			// the final step reads this item's own compressed text instead.
			fmt.Fprintf(os.Stderr, "  Warning: %s: %v; using its text as it is\n", item.label, err)
			summary = "(not tagged; the text itself:)\n" + clip(item.text, 1500)
		}
		item.summary = summary
	}

	// Oldest first, in parts that fit the budget; each part becomes a partial state.
	var parts []string
	var current strings.Builder
	for _, item := range items {
		entry := fmt.Sprintf("%s (%s):\n%s\n\n", item.when.Format("2006-01-02"), item.label, item.summary)
		if current.Len() > 0 && current.Len()+len(entry) > statusBudget {
			parts = append(parts, current.String())
			current.Reset()
		}
		current.WriteString(entry)
	}
	parts = append(parts, current.String())
	state := ""
	for index, part := range parts {
		var prompt strings.Builder
		fmt.Fprintf(&prompt, "BACKGROUND\n%s\n", background)
		if state != "" {
			fmt.Fprintf(&prompt, "STATE FROM EARLIER NOTES (older than the notes below; update it)\n%s\n\n", state)
		}
		fmt.Fprintf(&prompt, "NOTES, OLDEST FIRST\n%s", part)
		if related != "" && index == len(parts)-1 {
			fmt.Fprintf(&prompt, "RELATED NOTES FROM OTHER TASKS\n%s\n", related)
		}
		prompt.WriteString("\nWrite the current state of the task.")
		label := "state"
		if len(parts) > 1 {
			label = fmt.Sprintf("state part %d/%d", index+1, len(parts))
		}
		result, err := s.generate(label, statusSystem, prompt.String())
		if err != nil {
			return "", err
		}
		state = result
	}
	fmt.Fprintf(os.Stderr, "Done in %s.\n", time.Since(started).Round(time.Second))

	comments, noteDays := 0, 0
	for _, item := range items {
		if item.kind == "comment" {
			comments++
		} else {
			noteDays++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# %s %s: status on %s\n\n", task.Ref(), task.Name, time.Now().Format("2006-01-02"))
	priority := task.PriorityName()
	if priority == "" {
		priority = "no priority"
	}
	fmt.Fprintf(&out, "%s · %s · %s · [open](%s)\n\n", task.Status.Status, priority, tasks.Location(task), task.URL)
	fmt.Fprintf(&out, "From %d comment(s) and %d day(s) of notes, %s to %s", comments, noteDays,
		items[0].when.Format("2006-01-02"), items[len(items)-1].when.Format("2006-01-02"))
	if related != "" {
		out.WriteString(", plus related notes from other tasks")
	}
	fmt.Fprintf(&out, ". Written by %s (local); check anything important against the notes.\n\n%s\n", s.client.Model, dropEmptySections(state))
	return out.String(), nil
}

// dropEmptySections removes "## " headings with nothing under them.
func dropEmptySections(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var kept []string
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			empty := true
			for _, next := range lines[index+1:] {
				if strings.HasPrefix(strings.TrimSpace(next), "## ") {
					break
				}
				if strings.TrimSpace(next) != "" {
					empty = false
					break
				}
			}
			if empty {
				continue
			}
		}
		kept = append(kept, strings.TrimRight(line, " "))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
