package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

var notesAnnotations = map[string]string{annotationNotes: "true", annotationTaskAction: "true"}

func newNotesCmd(app *App) *cobra.Command {
	return newGroup(app, "notes", "Write dated task notes and push them as comments",
		map[string]string{annotationNotes: "true"},
		newMakeNotesCmd(app), newMakeWorkspaceCmd(app), newPushNotesCmd(app), newUnpushedNotesCmd(app), newTLDRCmd(app))
}

func newMakeNotesCmd(app *App) *cobra.Command {
	var taskID string
	var withTimer bool
	cmd := &cobra.Command{
		Use:         "open",
		Short:       "Open today's task note in a new tmux window",
		Long:        "Creates today's <ID>.md and <ID>/ in the notes layout, asks whether to start the task's timer, then opens the note.",
		Annotations: notesAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{})
			if err != nil {
				return err
			}
			var timer *bool
			if withTimer {
				timer = &withTimer
			}
			return openTaskNote(app, app.WithSpaceName(task), timer)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().BoolVar(&withTimer, "start-timer", false, "start the task timer without asking")
	return cmd
}

// openTaskNote creates today's note and opens it. withTimer nil means ask (when the
// timer is not already running on this task).
func openTaskNote(app *App, task tracker.Task, withTimer *bool) error {
	root, err := app.NotesRoot()
	if err != nil {
		return err
	}
	if err := maybeStartTimer(app, task, withTimer); err != nil {
		return err
	}
	note, err := notes.Ensure(root, task)
	if err != nil {
		return err
	}
	return openNote(app, task, note)
}

// maybeStartTimer starts the task's timer when withTimer is true, or asks when it is nil.
func maybeStartTimer(app *App, task tracker.Task, withTimer *bool) error {
	if withTimer == nil && ui.Interactive() {
		answer := askStartTimer(app, task)
		withTimer = &answer
	}
	if withTimer != nil && *withTimer {
		if err := startTimer(app, task); err != nil && !errors.Is(err, ui.ErrCancelled) {
			return err
		}
	}
	return nil
}

// openNote opens a note in a tmux window named after the task, or in the terminal.
func openNote(app *App, task tracker.Task, note string) error {
	cfg, _ := app.Config()
	command := cfg.Editor() + " " + ui.ShellQuote(note)
	inTmux, err := ui.TmuxWindow(task.Ref(), filepath.Dir(note), command)
	if inTmux || err != nil {
		return err
	}
	editor := exec.Command("sh", "-c", command)
	editor.Stdin, editor.Stdout, editor.Stderr = os.Stdin, os.Stdout, os.Stderr
	return editor.Run()
}

func askStartTimer(app *App, task tracker.Task) bool {
	if timerRunningOn(app, task) {
		return false
	}
	index, err := ui.Pick([]string{"Yes", "No"}, "Start timer on "+task.Ref()+"? >")
	return err == nil && index == 0
}

func newMakeWorkspaceCmd(app *App) *cobra.Command {
	var taskID string
	cmd := &cobra.Command{
		Use:   "workspace [TASK-ID]",
		Short: "Symlink every day's notes and files for a task into today's workspace",
		Long: `Creates <notes>/<today>/workspaces/<ID>/ with one folder per day the task has notes,
each holding relative symlinks to that day's <ID>.md and <ID>/. Today's note is created
first. Re-running only adds missing links; anything else you add is left alone.
If today's note did not exist yet, it asks whether to start the timer, like notes open.
Inside tmux it opens a window named <ID>-ws in the workspace: on the left today's note in
your editor above a shell (70/30), on the right your editor on the workspace folder
(each pane keeps a shell after you quit the editor). When that window already exists
(an earlier day's), it switches to it and changes every shell pane in it to today's
workspace; panes still running an editor are left alone. Links you added to the previous
workspace yourself carry over to the new one. The path is printed to stdout.`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: notesAnnotations,
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return completeTaskIDs(app), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				taskID = args[0]
			}
			root, err := app.NotesRoot()
			if err != nil {
				return err
			}
			task, err := app.SelectTask(taskID, TaskScope{})
			if err != nil {
				return err
			}
			task = app.WithSpaceName(task)
			_, statErr := os.Stat(filepath.Join(notes.DayDir(root, time.Now()), notes.RelPath(task)))
			newNote := errors.Is(statErr, fs.ErrNotExist)
			note, err := notes.Ensure(root, task)
			if err != nil {
				return err
			}
			workspace, added, carried, warnings, err := notes.MakeWorkspace(root, task.Ref())
			if err != nil {
				return err
			}
			for _, warning := range warnings {
				fmt.Fprintln(os.Stderr, "Warning:", warning)
			}
			fmt.Fprintf(os.Stderr, "Workspace for %s: %d new link(s), %d of your own carried over.\n", task.Ref(), added, carried)
			if newNote {
				// First note of the day for this task: ask about the timer like notes open.
				if err := maybeStartTimer(app, task, nil); err != nil {
					return err
				}
			}
			fmt.Println(workspace)
			window := task.Ref() + "-ws"
			// The window already exists (an earlier day's workspace): every shell pane in
			// it, this one included, moves to today's workspace; panes running an editor
			// are left alone. From a menu this pane is left alone too, since the menu
			// reads the terminal again.
			found, changed, skipped, err := ui.TmuxRefreshWindow(window, workspace, !app.InMenu)
			if err != nil {
				return err
			}
			if found {
				fmt.Fprintf(os.Stderr, "tmux window '%s' exists: %d shell pane(s) changed to today's workspace.\n", window, changed)
				for _, pane := range skipped {
					fmt.Fprintf(os.Stderr, "Pane %s left alone; quit it for a shell on today's workspace.\n", pane)
				}
				if app.InMenu && ui.TmuxCurrentWindow() == window {
					fmt.Fprintf(os.Stderr, "This pane: cd %s\n", ui.ShellQuote(workspace))
				}
				return nil
			}
			// Open today's note through the workspace link, with the editor on the whole
			// workspace beside it and a shell below.
			todayNote := filepath.Join(workspace, time.Now().Format("2006-01-02"), filepath.Base(note))
			cfg, _ := app.Config()
			inTmux, err := ui.TmuxWorkspace(window, workspace, cfg.Editor()+" "+ui.ShellQuote(todayNote), cfg.Editor()+" .")
			if inTmux || err != nil || !newNote {
				return err
			}
			return openNote(app, task, note)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	return cmd
}

// pushState records which note sections were posted as which comments:
// task -> note day -> section key -> record. Shared with the Python clctl.
type pushState map[string]map[string]map[string]*pushRecord

type pushRecord struct {
	CommentID string `json:"comment_id"`
	Hash      string `json:"hash"`
}

// statePath returns $XDG_STATE_HOME/clctl/<name> (default ~/.local/state/clctl/<name>).
func statePath(name string) string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "clctl", name)
}

func pushStatePath() string { return statePath("pushed_notes.json") }

func loadPushState() pushState {
	state := pushState{}
	if data, err := os.ReadFile(pushStatePath()); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	return state
}

func (s pushState) save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := pushStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

type pushSection struct {
	markdown.Section
	position int
	key      string
	hash     string
	status   string // new, edited or pushed
	record   *pushRecord
}

var (
	whitespaceRE = regexp.MustCompile(`\s+`)
	positionsRE  = regexp.MustCompile(`^\d+(-\d+)?(,\d+(-\d+)?)*$`)
)

func plainText(text string) string {
	return strings.TrimSpace(whitespaceRE.ReplaceAllString(text, " "))
}

func newPushNotesCmd(app *App) *cobra.Command {
	var taskID, fileOption, date, selectPositions string
	var pushAll, dryRun, force bool
	cmd := &cobra.Command{
		Use:   "push [FILE]",
		Short: "Post a task note as comments, one per ## section",
		Long: `Posts a note to its task, one comment per ## section. Sections already posted are
skipped and edited ones update their comment, so you can keep writing and push again.
Sections are numbered top to bottom: the first ## is 1 (text before it is 0).
Local images are uploaded as attachments and linked from the comment.`,
		Example: `  clctl notes push                              # pick task, note and sections
  clctl notes push --task-id TECH-1 --date yesterday --all
  clctl notes push --task-id TECH-1 --select 1,3-4
  clctl notes push notes/TECH-1.md --all --dry-run`,
		Args:        cobra.MaximumNArgs(2), // FILE, plus a --select value written with a space
		Annotations: notesAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			// pflag only binds an optional value written as --select=1, so accept
			// `--select 1,3` by taking a positions-looking argument as the value.
			if cmd.Flags().Changed("select") && strings.TrimSpace(selectPositions) == "" {
				for index, arg := range args {
					if positionsRE.MatchString(arg) {
						selectPositions = arg
						args = append(args[:index:index], args[index+1:]...)
						break
					}
				}
			}
			if len(args) > 1 {
				return fmt.Errorf("expected at most one FILE, got %d arguments", len(args))
			}
			file := fileOption
			if len(args) == 1 {
				file = args[0]
			}
			if !pushAll && strings.TrimSpace(selectPositions) == "" && !ui.Interactive() {
				return errors.New("non-interactive notes push needs --all or --select POSITIONS")
			}
			return pushNotes(app, taskID, file, date, pushAll, selectPositions, dryRun, force)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	flags := cmd.Flags()
	flags.StringVar(&fileOption, "file", "", "Markdown file to post (same as FILE)")
	flags.StringVar(&date, "date", "", "day of the note (YYYY-MM-DD, today, yesterday)")
	flags.BoolVar(&pushAll, "all", false, "push every new or edited section")
	flags.StringVar(&selectPositions, "select", "", "positions like 1,3-4; with no value, pick with fzf")
	flags.Lookup("select").NoOptDefVal = " "
	flags.BoolVar(&dryRun, "dry-run", false, "print the plan and payloads without posting")
	flags.BoolVar(&force, "force", false, "re-render already pushed sections too (e.g. after a formatting change)")
	cmd.MarkFlagsMutuallyExclusive("all", "select")
	return cmd
}

func pushNotes(app *App, taskID, file, date string, pushAll bool, positions string, dryRun, force bool) error {
	positions = strings.TrimSpace(positions)
	if file != "" && taskID == "" {
		if stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)); tracker.IsTaskKey(stem) {
			taskID = stem
		}
	}
	root, _ := app.NotesRoot()
	var task tracker.Task
	var note string
	pickTask := func() (err error) {
		task, err = app.SelectTask(taskID, TaskScope{})
		return err
	}
	pickNote := func() (err error) {
		if file != "" {
			note = file
			return nil
		}
		if root == "" {
			return errors.New("pass the note file; notes are disabled so it cannot be looked up")
		}
		note, err = selectNote(root, task.Ref(), date)
		return err
	}
	// Esc in a later picker (the note, the sections) goes back to the one before it.
	return ui.Steps(pickTask, pickNote, func() error {
		return pushNote(app, task, note, root, pushAll, positions, dryRun, force)
	})
}

// pushNote posts a task's note, picking the sections when neither pushAll nor positions
// say which.
func pushNote(app *App, task tracker.Task, note, root string, pushAll bool, positions string, dryRun, force bool) error {
	data, err := os.ReadFile(note)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	parsed := markdown.Sections(lines, nil, true)
	if len(parsed) == 0 {
		fmt.Println("Nothing to post from", note)
		return nil
	}

	client, err := app.Backend()
	if err != nil {
		return err
	}
	state := loadPushState()
	noteKey, legacyKeys := notes.Key(root, note, task.Ref())
	if state[task.Ref()] == nil {
		state[task.Ref()] = map[string]map[string]*pushRecord{}
	}
	for _, legacy := range legacyKeys {
		// Earlier versions keyed some notes by absolute path; carry those records over.
		if records, ok := state[task.Ref()][legacy]; ok && legacy != noteKey {
			if state[task.Ref()][noteKey] == nil {
				state[task.Ref()][noteKey] = records
			}
			delete(state[task.Ref()], legacy)
		}
	}
	if state[task.Ref()][noteKey] == nil {
		state[task.Ref()][noteKey] = map[string]*pushRecord{}
	}
	pushed := state[task.Ref()][noteKey]

	comments, err := client.Comments(task.Ref())
	if err != nil {
		return err
	}
	commentIDs := map[string]bool{}
	commentByText := map[string]string{}
	for _, comment := range comments {
		commentIDs[string(comment.ID)] = true
		commentByText[plainText(comment.CommentText)] = string(comment.ID)
	}

	offset := 1
	if parsed[0].Title == "" {
		offset = 0
	}
	keys := sectionKeys(parsed)
	sections := make([]*pushSection, len(parsed))
	for index, section := range parsed {
		key := keys[index]
		item := &pushSection{Section: section, position: index + offset, key: key, hash: section.Fingerprint()}
		record := pushed[key]
		if record != nil && !commentIDs[record.CommentID] {
			delete(pushed, key) // deleted in the backend: post again
			record = nil
		}
		if record == nil {
			if id, ok := commentByText[plainText(section.PlainText())]; ok {
				record = &pushRecord{CommentID: id, Hash: item.hash}
				pushed[key] = record
			}
		}
		item.record = record
		switch {
		case record == nil:
			item.status = "new"
		case record.Hash == item.hash && !force:
			item.status = "pushed"
		default:
			item.status = "edited"
		}
		sections[index] = item
	}

	var chosen []*pushSection
	switch {
	case pushAll:
		chosen = sections
	case positions != "":
		byPosition := map[int]*pushSection{}
		var valid []int
		for _, section := range sections {
			byPosition[section.position] = section
			valid = append(valid, section.position)
		}
		picked, err := parsePositions(positions, valid)
		if err != nil {
			return err
		}
		for _, position := range picked {
			chosen = append(chosen, byPosition[position])
		}
	default:
		rows := make([]string, len(sections))
		for index, section := range sections {
			title := section.Title
			if title == "" {
				title = "(text before first ##)"
			}
			rows[index] = fmt.Sprintf("%2d  %-6s  %s", section.position, section.status, title)
		}
		picked, err := ui.PickMany(rows, "Sections >")
		if err != nil {
			return err
		}
		for _, index := range picked {
			chosen = append(chosen, sections[index])
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].position < chosen[j].position })

	uploads := map[string]string{}
	imageOps := func(alt, source string) []markdown.Op {
		if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
			return markdown.DefaultImage(alt, source)
		}
		path := filepath.Join(filepath.Dir(note), source)
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintln(os.Stderr, "Warning: image not found, skipping upload:", path)
			return markdown.DefaultImage(alt, source)
		}
		if _, ok := uploads[path]; !ok {
			if dryRun {
				uploads[path] = "<upload " + filepath.Base(path) + ">"
			} else {
				attachment, err := client.UploadAttachment(task.Ref(), path)
				if err != nil {
					fmt.Fprintln(os.Stderr, "Warning: upload failed:", err)
					return markdown.DefaultImage(alt, source)
				}
				uploads[path] = attachment.URL
				fmt.Println("Uploaded", filepath.Base(path))
			}
		}
		if alt == "" {
			alt = filepath.Base(path)
		}
		return []markdown.Op{{Text: alt, Attributes: map[string]any{"link": uploads[path]}}}
	}

	posted, updated, skipped := 0, 0, 0
	for _, section := range chosen {
		title := section.Title
		if title == "" {
			title = "(intro)"
		}
		label := fmt.Sprintf("%d. %s", section.position, title)
		if section.status == "pushed" {
			fmt.Printf("Skipping %s: already pushed.\n", label)
			skipped++
			continue
		}
		comment := section.Comment
		if converted := markdown.Sections(section.Source, imageOps, false); len(converted) > 0 {
			comment = converted[0].Comment
		}
		if dryRun {
			action := "post"
			if section.status == "edited" {
				action = "update"
			}
			payload, _ := json.MarshalIndent(comment, "", "  ")
			fmt.Printf("--- would %s %s\n%s\n", action, label, payload)
			continue
		}
		commentID := ""
		if section.status == "edited" {
			commentID = section.record.CommentID
			if err := client.UpdateComment(commentID, comment); err != nil {
				return err
			}
			updated++
			fmt.Println("Updated", label+".")
		} else {
			if commentID, err = client.PostComment(task.Ref(), comment); err != nil {
				return err
			}
			posted++
			fmt.Println("Posted", label+".")
		}
		pushed[section.key] = &pushRecord{CommentID: commentID, Hash: section.hash}
		if err := state.save(); err != nil {
			return err
		}
	}
	if dryRun {
		return nil
	}
	if err := state.save(); err != nil {
		return err
	}
	fmt.Printf("%s: %d posted, %d updated, %d already pushed.\n", task.Ref(), posted, updated, skipped)
	return nil
}

// sectionKeys names sections in the push state: the title ("(intro)" for text before the
// first ##), with " (2)", " (3)"... added to repeated titles.
func sectionKeys(sections []markdown.Section) []string {
	seen := map[string]int{}
	keys := make([]string, len(sections))
	for index, section := range sections {
		title := section.Title
		if title == "" {
			title = "(intro)"
		}
		seen[title]++
		keys[index] = title
		if seen[title] > 1 {
			keys[index] = fmt.Sprintf("%s (%d)", title, seen[title])
		}
	}
	return keys
}

// unpushedNote is a task note with sections that are new or edited since they were pushed.
type unpushedNote struct {
	path, taskID, day string
	sections          []string // "new  2. Title" lines
	added, edited     int
}

// findUnpushed checks every task note since from (all when zero), or one task's notes,
// against the push record.
func findUnpushed(root, taskID string, from time.Time) []unpushedNote {
	state := loadPushState()
	var found []unpushedNote
	for _, path := range notes.TaskNotes(root) {
		day := notes.Day(root, path)
		if !from.IsZero() && day < from.Format("2006-01-02") {
			break // newest day first
		}
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		if taskID != "" && !strings.EqualFold(id, taskID) {
			continue
		}
		progress := noteProgress(state, root, path, id)
		if len(progress.sections) > 0 {
			found = append(found, unpushedNote{path: path, taskID: id, day: day, sections: progress.sections,
				added: progress.added, edited: progress.edited})
		}
	}
	return found
}

// pushProgress is how far notes push got with a note: its sections not pushed yet or
// edited since, as "new     2. Title" lines, and how many are new, edited and pushed.
type pushProgress struct {
	sections              []string
	added, edited, pushed int
}

// label sums the progress up for a list: "pushed", "2 pushed, 1 new", "3 new", "empty".
func (p pushProgress) label() string {
	var parts []string
	if p.pushed > 0 {
		parts = append(parts, fmt.Sprintf("%d pushed", p.pushed))
	}
	if p.added > 0 {
		parts = append(parts, fmt.Sprintf("%d new", p.added))
	}
	if p.edited > 0 {
		parts = append(parts, fmt.Sprintf("%d edited", p.edited))
	}
	switch {
	case len(parts) == 0:
		return "empty"
	case p.added == 0 && p.edited == 0:
		return "pushed"
	}
	return strings.Join(parts, ", ")
}

// noteProgress compares a note's ## sections with the push record.
func noteProgress(state pushState, root, path, id string) pushProgress {
	var progress pushProgress
	data, err := os.ReadFile(path)
	if err != nil {
		return progress
	}
	parsed := markdown.Sections(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil, true)
	if len(parsed) == 0 {
		return progress
	}
	key, legacyKeys := notes.Key(root, path, id)
	pushed := state[id][key]
	for _, legacy := range legacyKeys {
		if pushed == nil {
			pushed = state[id][legacy]
		}
	}
	offset := 1
	if parsed[0].Title == "" {
		offset = 0
	}
	for index, sectionKey := range sectionKeys(parsed) {
		status := ""
		switch record := pushed[sectionKey]; {
		case record == nil:
			status = "new"
			progress.added++
		case record.Hash != parsed[index].Fingerprint():
			status = "edited"
			progress.edited++
		default:
			progress.pushed++
			continue
		}
		progress.sections = append(progress.sections, fmt.Sprintf("%-6s  %d. %s", status, index+offset, sectionKey))
	}
	return progress
}

func newUnpushedNotesCmd(app *App) *cobra.Command {
	var taskID, since string
	cmd := &cobra.Command{
		Use:   "unpushed",
		Short: "List task notes with sections not pushed yet, or edited since",
		Long: `Lists task notes whose ## sections are new or edited since notes push last posted
them, newest day first, then offers to push one. It reads only the local push record, so
a section posted by hand in the task backend shows as new until notes push matches it up.`,
		Example: `  clctl notes unpushed
  clctl notes unpushed --since 7d
  clctl notes unpushed --task-id TECH-1234`,
		Annotations: notesAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := app.NotesRoot()
			if err != nil {
				return err
			}
			var from time.Time
			if since != "" {
				if from, err = parseSince(since); err != nil {
					return err
				}
			}
			found := findUnpushed(root, taskID, from)
			if len(found) == 0 {
				fmt.Println("Every note section is pushed.")
				return nil
			}
			rows := make([]string, len(found))
			for index, note := range found {
				rel, _ := filepath.Rel(root, note.path)
				rows[index] = fmt.Sprintf("%s  %-12s %d new, %d edited  %s", note.day, note.taskID, note.added, note.edited, rel)
				fmt.Println(rows[index])
				for _, line := range note.sections {
					fmt.Println("    " + line)
				}
			}
			if !ui.Interactive() {
				return nil
			}
			if push, err := ui.Confirm("Push one of these notes now?", false); err != nil || !push {
				return err
			}
			index, err := ui.Pick(rows, "Note to push >")
			if err != nil {
				return err
			}
			return pushNotes(app, found[index].taskID, found[index].path, "", false, "", false, false)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().StringVar(&since, "since", "", "only notes from this far back (7d, 2w or YYYY-MM-DD; default all)")
	return cmd
}

// selectNote picks the task's note for a day, or asks when several days have one.
func selectNote(root, taskID, date string) (string, error) {
	found := notes.Find(root, taskID)
	if len(found) == 0 {
		return "", fmt.Errorf("no notes named %s.md under %s", taskID, root)
	}
	if date != "" {
		day, err := notes.ParseDay(date)
		if err != nil {
			return "", err
		}
		var matching []string
		days := map[string]bool{}
		for _, path := range found {
			days[orUndated(notes.Day(root, path))] = true
			if notes.Day(root, path) == day {
				matching = append(matching, path)
			}
		}
		if len(matching) == 0 {
			var list []string
			for day := range days {
				list = append(list, day)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(list)))
			return "", fmt.Errorf("no %s note on %s; notes exist for: %s", taskID, day, strings.Join(list, ", "))
		}
		found = matching
	}
	if len(found) == 1 {
		return found[0], nil
	}
	// Each day says how far its push got, so the days already pushed stand out.
	state := loadPushState()
	rows := make([]string, len(found))
	for index, path := range found {
		rel, _ := filepath.Rel(root, path)
		label := noteProgress(state, root, path, taskID).label()
		rows[index] = fmt.Sprintf("%-10s  %-18s  %s", orUndated(notes.Day(root, path)), label, rel)
	}
	index, err := ui.Pick(rows, taskID+" notes (by day) >")
	if err != nil {
		return "", err
	}
	return found[index], nil
}

func orUndated(day string) string {
	if day == "" {
		return "undated"
	}
	return day
}

// parsePositions parses "1,3,5-7" into positions, in the order given.
func parsePositions(value string, valid []int) ([]int, error) {
	allowed := map[int]bool{}
	var names []string
	for _, position := range valid {
		allowed[position] = true
		names = append(names, strconv.Itoa(position))
	}
	var positions []int
	seen := map[int]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		first, last, isRange := strings.Cut(part, "-")
		from, err1 := strconv.Atoi(first)
		to := from
		var err2 error
		if isRange {
			to, err2 = strconv.Atoi(last)
		}
		if err1 != nil || err2 != nil || to < from {
			return nil, fmt.Errorf("invalid section position %q; use e.g. 1,3,5-7", part)
		}
		for position := from; position <= to; position++ {
			if !allowed[position] {
				return nil, fmt.Errorf("section %d does not exist; valid positions: %s", position, strings.Join(names, ", "))
			}
			if !seen[position] {
				seen[position] = true
				positions = append(positions, position)
			}
		}
	}
	return positions, nil
}
