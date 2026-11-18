package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/llm"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
)

// meetingMatcher matches meeting note file names: a regex when given, otherwise names
// containing the words weekly, it and meeting ("weekly-it-meeting.md").
func meetingMatcher(pattern string) (func(string) bool, error) {
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid meeting pattern: %w", err)
		}
		return re.MatchString, nil
	}
	return func(name string) bool {
		words := map[string]bool{}
		for _, word := range regexp.MustCompile(`[^a-z0-9]+`).Split(strings.ToLower(strings.TrimSuffix(name, ".md")), -1) {
			words[word] = true
		}
		return words["weekly"] && words["it"] && words["meeting"]
	}, nil
}

func findMeetingNote(dayDir string, matches func(string) bool) string {
	entries, _ := os.ReadDir(dayDir)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && matches(entry.Name()) {
			return filepath.Join(dayDir, entry.Name())
		}
	}
	return ""
}

// namedMeetingNote finds the note --meeting-note names: a file in today's folder, or a path.
func namedMeetingNote(today, name string) (string, error) {
	path := config.ExpandHome(name)
	if !strings.ContainsRune(name, filepath.Separator) {
		path = filepath.Join(today, name)
		if !strings.HasSuffix(path, ".md") {
			if _, err := os.Stat(path); err != nil {
				path += ".md"
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("--meeting-note: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("--meeting-note: %s is a folder", path)
	}
	return path, nil
}

// dayNotes lists the Markdown files directly in a day folder (meeting notes and the
// like; task notes live in subfolders), leaving out the digest.
func dayNotes(dayDir string) []string {
	entries, _ := os.ReadDir(dayDir)
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && entry.Name() != "digest.md" {
			names = append(names, entry.Name())
		}
	}
	return names
}

// previousMeetingDay returns the latest day before today with a meeting note.
func previousMeetingDay(root, today string, matches func(string) bool) time.Time {
	days, _ := filepath.Glob(filepath.Join(root, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]"))
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for _, day := range days {
		if day >= today {
			continue
		}
		if findMeetingNote(day, matches) != "" {
			parsed, err := time.ParseInLocation("2006/01/02", strings.TrimPrefix(day, root+string(filepath.Separator)), time.Local)
			if err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

// linkDigest adds a link to the digest under the meeting note's title, once.
func linkDigest(note, digest string, from time.Time) error {
	data, err := os.ReadFile(note)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(note), digest)
	if err != nil {
		rel = digest
	}
	text := string(data)
	if strings.Contains(text, "]("+rel+")") {
		return nil
	}
	link := fmt.Sprintf("Task digest since %s: [%s](%s)\n", from.Format("Mon 2 Jan"), filepath.Base(digest), rel)
	title, rest, found := strings.Cut(text, "\n")
	if found && strings.HasPrefix(title, "# ") {
		text = title + "\n\n" + link + "\n" + strings.TrimLeft(rest, "\n")
	} else {
		text = link + "\n" + text
	}
	return os.WriteFile(note, []byte(text), 0o644)
}

// parseSince accepts "7d", "36h", "2w" or a YYYY-MM-DD date.
func parseSince(value string) (time.Time, error) {
	if day, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return day, nil
	}
	if len(value) > 1 {
		if count, err := strconv.Atoi(value[:len(value)-1]); err == nil && count >= 0 {
			switch value[len(value)-1] {
			case 'h':
				return time.Now().Add(-time.Duration(count) * time.Hour), nil
			case 'd':
				return time.Now().AddDate(0, 0, -count), nil
			case 'w':
				return time.Now().AddDate(0, 0, -7*count), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since %q; use e.g. 7d, 36h, 2w or YYYY-MM-DD", value)
}

func millisTime(value string) time.Time {
	millis, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(millis)
}

type digestEntry struct {
	task     tracker.Task
	comments []tracker.Comment // newest first, within the window
	latest   time.Time
	summary  string     // AI summary of the comments (or the tl;drs), with --summarize
	tldrs    []taskTLDR // tl;drs in its summary subtask from the window, oldest first
}

func newDigestCmd(app *App) *cobra.Command {
	var since, output, model, llmURL, taskID string
	var summarize, meeting, again, check, dryRun, force, setUp bool
	var meetingNoteName string
	var redo string
	var meetingPattern string
	var assignees []string
	var everyone bool
	var perTask, limit int
	cmd := &cobra.Command{
		Use:         "digest",
		Annotations: map[string]string{annotationMenu: "true"},
		Short:       "Write a Markdown digest of recently edited tasks and their comments",
		Long: `Collects tasks edited or commented on since --since (default 7 days) and writes them,
newest first, with a short summary of each recent comment: headings and the opening
sentences are kept, lists, code blocks, tables and images become one-line markers.

--summarize adds a few AI bullet points per task from a local model server: an
OpenAI-compatible one such as llama.cpp's llama-server when llm_url ends in /v1, else Ollama
(llm_url / llm_model in the config, default http://localhost:11434 and its first
model). The model summarises each comment (with the task's description, neighbours and your
notes on it as background), then merges those per task. Everything is cached, so
re-runs only summarise new or edited comments.

The digest goes to <notes>/<today>/digest.md (./clctl-digest-<date>.md with notes off),
or --output (- for stdout).

--redo rewrites earlier digests in the notes folder with AI summaries: those written
without them (--redo, the default) or every one (--redo=all), oldest first. Each keeps
its own window (read from its title line) and only counts comments from that window,
including on tasks closed since. With --since, only digests from that day on are
redone. --dry-run lists them without changing anything.`,
		Example: `  clctl digest --summarize
  clctl digest --meeting
  clctl digest --init                    # set up the meeting digest and its cron line
  clctl digest --meeting --force         # no meeting note matched today: digest anyway
  clctl digest --meeting-note it-catch-up.md   # use this note as today's meeting note
  clctl digest --redo --dry-run          # which digests lack AI summaries
  clctl digest --redo                    # rewrite them with the model
  clctl digest --redo=all --since 2026-09-01`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if setUp {
				return runDigestInit(app, cmd.Root())
			}
			if check {
				return digestCheck(app, cmd.Root(), meetingPattern, model, llmURL)
			}
			// Naming the note or forcing the digest only makes sense for a meeting digest.
			meeting = meeting || force || meetingNoteName != ""
			if redo != "" {
				if meeting || output != "" || taskID != "" {
					return errors.New("--redo rewrites the digests in the notes folder; it does not combine with --meeting, --output or --task-id")
				}
				redoSince := ""
				if cmd.Flags().Changed("since") {
					day, err := parseSince(since)
					if err != nil {
						return err
					}
					redoSince = day.Format("2006-01-02")
				}
				return redoDigests(app, redo, redoSince, dryRun, digestScope{everyone: everyone, assignees: assignees}, perTask, limit, model, llmURL)
			}
			var meetingNote string
			if meeting {
				root, err := app.NotesRoot()
				if err != nil {
					return err
				}
				cfg, _ := app.Config()
				if meetingPattern == "" {
					meetingPattern = cfg.Get(config.KeyMeetingPattern)
				}
				matches, err := meetingMatcher(meetingPattern)
				if err != nil {
					return err
				}
				today := notes.DayDir(root, time.Now())
				if meetingNoteName != "" {
					if meetingNote, err = namedMeetingNote(today, meetingNoteName); err != nil {
						return err
					}
				} else {
					meetingNote = findMeetingNote(today, matches)
				}
				switch {
				case meetingNote == "" && !force:
					fmt.Println("No meeting note today; nothing to do (--force writes the digest anyway, --meeting-note FILE names the note).")
					return nil
				case meetingNote == "":
					if _, err := os.Stat(filepath.Join(today, "digest.md")); err == nil && !again {
						fmt.Println("Today's digest.md is already written; nothing to do (--again to redo).")
						return nil
					}
					fmt.Println("No meeting note today; writing the meeting digest anyway (--force), without linking it.")
				default:
					if data, _ := os.ReadFile(meetingNote); strings.Contains(string(data), "](digest.md)") && !again {
						fmt.Printf("%s already links today's digest; nothing to do (--again to redo).\n", filepath.Base(meetingNote))
						return nil
					}
				}
				if !cmd.Flags().Changed("since") {
					if previous := previousMeetingDay(root, today, matches); !previous.IsZero() {
						since = previous.Format("2006-01-02")
					}
				}
				summarize = true
				output = filepath.Join(today, "digest.md")
			}
			if since == "last-meeting" {
				// The window a meeting-day digest would use, so a nightly warm-up run
				// caches exactly the summaries and merges the meeting run will need.
				root, err := app.NotesRoot()
				if err != nil {
					return err
				}
				cfg, _ := app.Config()
				pattern := meetingPattern
				if pattern == "" {
					pattern = cfg.Get(config.KeyMeetingPattern)
				}
				matches, err := meetingMatcher(pattern)
				if err != nil {
					return err
				}
				since = "7d"
				if previous := previousMeetingDay(root, notes.DayDir(root, time.Now()), matches); !previous.IsZero() {
					since = previous.Format("2006-01-02")
				}
			}
			from, err := parseSince(since)
			if err != nil {
				return err
			}
			if summarize {
				unlock, err := lockDigest()
				if err != nil {
					if meeting {
						// Cron runs --meeting repeatedly: the next run picks it up.
						fmt.Println(err.Error() + "; leaving it to that run.")
						return nil
					}
					return err
				}
				defer unlock()
			}
			candidates, err := digestCandidates(app, digestScope{taskID: taskID, everyone: everyone, assignees: assignees}, from)
			if err != nil {
				return err
			}
			comments, err := fetchComments(app, candidates)
			if err != nil {
				return err
			}
			recent := windowEntries(candidates, comments, from, time.Time{}, limit)
			modelName := ""
			if summarize {
				summarizer, err := newSummarizer(app, model, llmURL)
				if err != nil {
					return err
				}
				defer summarizer.Close()
				summarizer.usePreviousBefore(time.Now())
				modelName = summarizer.client.Model
				addTLDRs(app, recent, from, time.Time{})
				if err := summarizeEntries(summarizer, recent); err != nil {
					return err
				}
			}
			text := renderDigest(recent, from, time.Time{}, perTask, modelName)

			if output == "" {
				if root, err := app.NotesRoot(); err == nil {
					output = filepath.Join(notes.DayDir(root, time.Now()), "digest.md")
				} else {
					output = "clctl-digest-" + time.Now().Format("2006-01-02") + ".md"
				}
			}
			if output == "-" {
				fmt.Print(text)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(output, []byte(text), 0o644); err != nil {
				return err
			}
			fmt.Printf("Wrote %d tasks to %s\n", len(recent), output)
			if meetingNote != "" {
				if err := linkDigest(meetingNote, output, from); err != nil {
					return err
				}
				fmt.Println("Linked it from", meetingNote)
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&since, "since", "7d", "window: 7d, 36h, 2w, a YYYY-MM-DD date, or last-meeting")
	flags.StringArrayVar(&assignees, "assignee", nil, "tasks of these people instead of yours")
	flags.BoolVar(&everyone, "everyone", false, "every cached task edited in the window")
	flags.IntVar(&perTask, "comments", 5, "most recent comments shown per task")
	flags.IntVar(&limit, "limit", 0, "at most this many tasks (0 = all)")
	flags.StringVar(&taskID, "task-id", "", "only this task")
	flags.StringVarP(&output, "output", "o", "", "output file, or - for stdout")
	flags.BoolVar(&summarize, "summarize", false, "add AI summaries from a local model")
	flags.BoolVar(&meeting, "meeting", false, "only on a meeting day: summarise since the previous meeting and link the digest from today's meeting note")
	flags.StringVar(&meetingPattern, "meeting-pattern", "", "regex for the meeting note's file name (default meeting_pattern, else weekly+it+meeting)")
	flags.BoolVar(&again, "again", false, "with --meeting, redo a digest that is already linked")
	flags.BoolVar(&force, "force", false, "write the meeting digest even when no file matches the meeting pattern today (implies --meeting)")
	flags.StringVar(&meetingNoteName, "meeting-note", "", "today's meeting note, by file name in today's folder or path, when the pattern misses it (implies --meeting)")
	flags.StringVar(&model, "model", "", "model name (default llm_model, else the server's first model)")
	flags.StringVar(&llmURL, "llm-url", "", "model server URL (default llm_url, else http://localhost:11434)")
	flags.BoolVar(&check, "check", false, "check the notes, meeting note and model server set-up, then exit")
	flags.BoolVar(&setUp, "init", false, "set up the meeting digest: notes_dir, which note is the meeting's, and the cron line (asks before each change)")
	flags.StringVar(&redo, "redo", "", "rewrite earlier digests with AI summaries: missing (those without) or all")
	flags.Lookup("redo").NoOptDefVal = "missing"
	flags.BoolVar(&dryRun, "dry-run", false, "with --redo, list the digests it would rewrite")
	return cmd
}

// lockDigest stops runs that use the model from overlapping (a slow cron digest and the
// next one, or a digest and tasks status): the first to finish would stop a model
// server the other still needs. The lock holds the owner's process ID; a lock whose
// process is gone (a killed run) is taken over, as is one older than 3 hours where
// processes cannot be checked. Ctrl+C and SIGTERM remove it.
func lockDigest() (func(), error) {
	path := statePath("digest.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	for range 2 {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(file, "pid %d since %s\n", os.Getpid(), time.Now().Format("2006-01-02 15:04"))
			file.Close()
			unregister := onInterrupt(func() { _ = os.Remove(path) })
			return func() {
				unregister()
				_ = os.Remove(path)
			}, nil
		}
		data, _ := os.ReadFile(path)
		holder := strings.TrimSpace(string(data))
		if lockHeld(path, holder) {
			return nil, fmt.Errorf("another clctl run is using the model (%s); if it is not running, delete %s", holder, path)
		}
		fmt.Fprintf(os.Stderr, "Taking over a lock left by a run that ended without cleaning up (%s).\n", holder)
		_ = os.Remove(path)
	}
	return nil, fmt.Errorf("could not take the model lock %s", path)
}

// lockHeld reports whether the lock's owner ("pid N since ...") is still running.
func lockHeld(path, holder string) bool {
	var pid int
	if _, err := fmt.Sscanf(holder, "pid %d", &pid); err == nil && pid > 0 {
		if alive, known := processAlive(pid); known {
			return alive
		}
	}
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) < 3*time.Hour
}

// processAlive reports whether a process exists; known is false where that cannot be
// checked (no /proc), so the caller falls back to the lock's age.
func processAlive(pid int) (alive, known bool) {
	if _, err := os.Stat("/proc/self"); err != nil {
		return false, false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil, true
}

// digestCheck reports, without changing anything, whether a meeting digest can run:
// notes folder, meeting notes, lock and model server.
func digestCheck(app *App, rootCmd *cobra.Command, pattern, model, url string) error {
	problems := 0
	report := func(ok bool, format string, args ...any) {
		mark := "ok  "
		if !ok {
			mark = "!!  "
			problems++
		}
		fmt.Printf(mark+format+"\n", args...)
	}
	// advise points out something worth changing that does not stop the digest.
	advise := func(format string, args ...any) { fmt.Printf("--  "+format+"\n", args...) }
	crontab, _ := readCrontab()
	cfg, err := app.Config()
	if err != nil {
		return err
	}
	report(true, "config: %s", cfg.Path)
	if _, err := app.Backend(); err != nil {
		report(false, "%s: %v", app.backendName(), err)
	}

	root := cfg.NotesDir()
	switch {
	case root == "":
		report(false, "notes are disabled, so --meeting has no meeting notes to look at: run 'clctl init' or set notes_dir")
	case cfg.Stored(config.KeyNotesDir) == "" && cronVar(crontab, "NOTES_DIR") != "":
		if cronDir := config.ExpandHome(cronVar(crontab, "NOTES_DIR")); filepath.Clean(cronDir) != filepath.Clean(root) {
			report(false, "your crontab sets NOTES_DIR=%s, but this shell has %s: cron runs use the crontab's", cronDir, root)
		} else {
			report(true, "notes dir: %s (from $NOTES_DIR, which your crontab sets too)", root)
		}
	case cfg.Stored(config.KeyNotesDir) == "":
		report(false, "notes_dir comes only from $NOTES_DIR (%s); cron does not read your shell profile, so set notes_dir in %s or NOTES_DIR in the crontab", root, cfg.Path)
	default:
		report(true, "notes dir: %s", root)
	}
	if root != "" {
		if _, err := os.Stat(root); err != nil {
			report(false, "notes dir: %v", err)
		}
		if pattern == "" {
			pattern = cfg.Get(config.KeyMeetingPattern)
		}
		matches, err := meetingMatcher(pattern)
		if err != nil {
			report(false, "%v", err)
		} else {
			looking := "a file name with the words weekly, it and meeting"
			if pattern != "" {
				looking = "a file name matching " + pattern
			}
			today := notes.DayDir(root, time.Now())
			if note := findMeetingNote(today, matches); note == "" {
				report(true, "no meeting note in %s (looking for %s directly in the day folder); --meeting does nothing today", today, looking)
				if others := dayNotes(today); len(others) > 0 {
					advise("today's folder has %s; if one is the meeting note, run 'clctl digest --meeting-note <name>' now, and 'clctl digest --init' to match it from now on", strings.Join(others, ", "))
				}
			} else if data, _ := os.ReadFile(note); strings.Contains(string(data), "](digest.md)") {
				report(true, "today's meeting note %s already links its digest (--again redoes it)", note)
			} else {
				report(true, "today's meeting note: %s", note)
			}
			if previous := previousMeetingDay(root, today, matches); previous.IsZero() {
				report(true, "no earlier meeting note, so a meeting digest covers the last 7 days")
			} else {
				report(true, "previous meeting note: %s (a meeting digest starts there)", previous.Format("Mon 2006-01-02"))
			}
		}
	}
	for _, found := range legacyCronSpellings(crontab, rootCmd) {
		advise("%s runs '%s', the old spelling, which logs a deprecation notice every run: change it to '%s'", found.where, found.old, found.new)
	}
	if data, err := os.ReadFile(statePath("digest.lock")); err == nil {
		holder := strings.TrimSpace(string(data))
		if lockHeld(statePath("digest.lock"), holder) {
			report(false, "a clctl run is using the model right now (%s); summarising waits for it to finish", holder)
		} else {
			report(true, "a lock was left by a run that ended without cleaning up (%s); the next run takes it over", holder)
		}
	}

	if url == "" {
		url = cfg.Get(config.KeyLLMURL)
	}
	if model == "" {
		model = cfg.Get(config.KeyLLMModel)
	}
	contextTokens, _ := strconv.Atoi(cfg.Get(config.KeyLLMContext))
	client := llm.New(url, model, contextTokens)
	client.Threads, _ = strconv.Atoi(cfg.Get(config.KeyLLMThreads))
	client.MaxTokens, _ = strconv.Atoi(cfg.Get(config.KeyLLMMaxTokens))
	launcher := chatLauncher(cfg)
	if launcher.start != "" {
		// Cron's PATH is short (/usr/bin:/bin), so the start command needs a full path.
		program := ""
		for _, word := range strings.Fields(launcher.start) {
			if !strings.Contains(word, "=") { // skip VAR=value prefixes
				program = config.ExpandHome(word)
				break
			}
		}
		if _, err := exec.LookPath(program); err != nil || !filepath.IsAbs(program) {
			report(false, "llm_start %q: give the full path of %s, since cron's PATH is only /usr/bin:/bin", launcher.start, program)
		}
		if launcher.stop == "" {
			report(false, "llm_start is set but llm_stop is not, so the model stays loaded after each run")
		}
	}
	if _, err := client.Models(); err != nil {
		switch {
		case launcher.start != "":
			report(true, "model server %s is down; a summarising run %s and stops it afterwards", client.URL, launcher.describe())
		case launcher.container == "":
			report(false, "model server: %v", err)
		default:
			if output, dockerErr := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", launcher.container).CombinedOutput(); dockerErr != nil {
				report(false, "model server %s is down and llm_container %s cannot be inspected: %s", client.URL, launcher.container, strings.TrimSpace(string(output)))
			} else {
				report(true, "model server %s is down; a summarising run %s (now %s) and stops it afterwards", client.URL, launcher.describe(), strings.TrimSpace(string(output)))
			}
		}
	} else if err := client.ResolveModel(); err != nil {
		report(false, "model: %v", err)
	} else {
		report(true, "model server %s, model %s", client.URL, client.Model)
		fmt.Println("    asking the model for one word (the first call loads it; minutes on a CPU)...")
		started := time.Now()
		reply, _, err := client.Generate("Answer with one word.", "Say OK.")
		if err != nil {
			report(false, "model did not answer: %v", err)
		} else {
			report(true, "model answered %q in %s", strings.TrimSpace(reply), time.Since(started).Round(time.Second))
		}
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	fmt.Println("All checks passed.")
	return nil
}

func renderDigest(entries []*digestEntry, from, until time.Time, perTask int, model string) string {
	var builder strings.Builder
	if until.IsZero() {
		until = time.Now()
	}
	fmt.Fprintf(&builder, "# Task digest: %s to %s\n\n", from.Format("2006-01-02"), until.Format("2006-01-02"))
	commentCount := 0
	for _, entry := range entries {
		commentCount += len(entry.comments)
	}
	fmt.Fprintf(&builder, "%d tasks, %d comments.", len(entries), commentCount)
	if model != "" {
		fromTLDRs := 0
		for _, entry := range entries {
			if len(entry.tldrs) > 0 {
				fromTLDRs++
			}
		}
		source := "from the comments only"
		if fromTLDRs > 0 {
			source = fmt.Sprintf("from the tasks' daily tl;drs (%d tasks) or else their comments", fromTLDRs)
		}
		fmt.Fprintf(&builder, " Summaries by %s (local), %s; check anything important against the comments.", model, source)
	}
	builder.WriteString("\n")
	for _, entry := range entries {
		task := entry.task
		priority := task.PriorityName()
		if priority == "" {
			priority = "no priority"
		}
		fmt.Fprintf(&builder, "\n## %s %s\n\n", task.Ref(), task.Name)
		fmt.Fprintf(&builder, "%s · %s · %s · last activity %s · [open](%s)\n",
			task.Status.Status, priority, tasks.Location(task), entry.latest.Format("2006-01-02 15:04"), task.URL)
		if entry.summary != "" {
			heading := "**Summary**"
			if source := tldrSource(entry.tldrs); source != "" {
				heading += " (" + source + ")"
			}
			fmt.Fprintf(&builder, "\n%s\n\n%s\n", heading, entry.summary)
			if len(entry.comments) > 0 {
				builder.WriteString("\n**Comments**\n")
			}
		}
		if len(entry.comments) == 0 {
			continue
		}
		builder.WriteString("\n")
		for index, comment := range entry.comments {
			if index == perTask {
				fmt.Fprintf(&builder, "- … %d older comment(s) in this window\n", len(entry.comments)-perTask)
				break
			}
			summary := markdown.Summarize(comment.Comment, 400)
			if summary == "" {
				summary = "(empty)"
			}
			fmt.Fprintf(&builder, "- **%s %s**: %s\n", millisTime(string(comment.Date)).Format("01-02 15:04"), comment.User.DisplayName(), summary)
		}
	}
	return builder.String()
}

// digestScope says whose tasks a digest covers.
type digestScope struct {
	taskID    string
	everyone  bool
	assignees []string
	// closed adds tasks closed since and updated after from (fetched from the backend, as
	// the cache holds open tasks only), for digests of past windows.
	closed bool
}

// digestCandidates returns the tasks whose comments a digest looks at.
func digestCandidates(app *App, scope digestScope, from time.Time) ([]tracker.Task, error) {
	c, err := app.Cache()
	if err != nil {
		return nil, err
	}
	var candidates []tracker.Task
	for _, task := range c.Tasks {
		if !isTLDRSubtask(task) {
			candidates = append(candidates, task)
		}
	}
	if scope.closed && scope.taskID == "" {
		closed, err := app.ClosedTasks(scope.everyone || len(scope.assignees) > 0)
		if err != nil {
			return nil, err
		}
		for _, task := range closed {
			if isTLDRSubtask(task) {
				continue
			}
			// Closing a task updates it, so one last updated before the window was
			// closed before it too.
			if time.UnixMilli(int64(task.DateUpdated)).After(from) {
				candidates = append(candidates, task)
			}
		}
	}
	switch {
	case scope.taskID != "":
		task, err := app.SelectTask(scope.taskID, TaskScope{Everyone: true, IncludeDone: true})
		if err != nil {
			return nil, err
		}
		return []tracker.Task{task}, nil
	case scope.everyone:
		// Everyone's tasks are too many to scan comments on: use the edit date.
		var recent []tracker.Task
		for _, task := range candidates {
			if time.UnixMilli(int64(task.DateUpdated)).After(from) {
				recent = append(recent, task)
			}
		}
		return recent, nil
	case len(scope.assignees) > 0:
		ids, err := resolveAssignees(app, scope.assignees)
		if err != nil {
			return nil, err
		}
		return tasks.AssignedTo(candidates, ids...), nil
	default:
		me, err := app.UserID()
		if err != nil {
			return nil, err
		}
		return tasks.AssignedTo(candidates, me), nil
	}
}

// fetchComments returns every comment on each task (newest first), keyed by task ID.
func fetchComments(app *App, candidates []tracker.Task) (map[string][]tracker.Comment, error) {
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	results := make([][]tracker.Comment, len(candidates))
	errs := make([]error, len(candidates))
	var group sync.WaitGroup
	limiter := make(chan struct{}, 6)
	for index, task := range candidates {
		group.Add(1)
		go func() {
			defer group.Done()
			limiter <- struct{}{}
			defer func() { <-limiter }()
			results[index], errs[index] = client.Comments(task.Ref())
			if errs[index] != nil {
				errs[index] = fmt.Errorf("%s: %w", task.Ref(), errs[index])
			}
		}()
	}
	group.Wait()
	comments := map[string][]tracker.Comment{}
	for index, task := range candidates {
		if errs[index] != nil {
			return nil, errs[index]
		}
		comments[task.ID] = results[index]
	}
	return comments, nil
}

// windowEntries keeps the tasks active between from and until (zero: up to now) with
// their comments from that window, newest first.
func windowEntries(candidates []tracker.Task, comments map[string][]tracker.Comment, from, until time.Time, limit int) []*digestEntry {
	inWindow := func(when time.Time) bool { return when.After(from) && (until.IsZero() || !when.After(until)) }
	var recent []*digestEntry
	for _, task := range candidates {
		entry := &digestEntry{task: task}
		// A task's last edit only counts when it falls in the window; for a past window
		// it has usually moved on, so its comments decide.
		if updated := time.UnixMilli(int64(task.DateUpdated)); inWindow(updated) {
			entry.latest = updated
		}
		for _, comment := range comments[task.ID] {
			if when := millisTime(string(comment.Date)); inWindow(when) {
				entry.comments = append(entry.comments, comment)
				if when.After(entry.latest) {
					entry.latest = when
				}
			}
		}
		if inWindow(entry.latest) || len(entry.comments) > 0 {
			recent = append(recent, entry)
		}
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].latest.After(recent[j].latest) })
	if limit > 0 && len(recent) > limit {
		recent = recent[:limit]
	}
	return recent
}

// summarizeEntries adds the model's summary to every entry that has comments.
func summarizeEntries(summarizer *summarizer, entries []*digestEntry) error {
	started := time.Now()
	before, after := summarizer.before, summarizer.after
	for index, entry := range entries {
		if len(entry.tldrs) > 0 {
			fmt.Fprintf(os.Stderr, "Summarising %s from its tl;drs (%d/%d)...\n", entry.task.Ref(), index+1, len(entries))
			summary, err := summarizer.SummarizeTLDRs(entry)
			if err != nil {
				return fmt.Errorf("summarising %s: %w", entry.task.Ref(), err)
			}
			entry.summary = summary
			continue
		}
		if len(entry.comments) == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "Summarising %s (%d/%d)...\n", entry.task.Ref(), index+1, len(entries))
		summary, err := summarizer.Summarize(entry)
		if err != nil {
			return fmt.Errorf("summarising %s: %w", entry.task.Ref(), err)
		}
		entry.summary = summary
	}
	fmt.Fprintf(os.Stderr, "Summaries done in %s.", time.Since(started).Round(time.Second))
	if compressedBefore := summarizer.before - before; compressedBefore > 0 {
		compressedAfter := summarizer.after - after
		fmt.Fprintf(os.Stderr, " Comment text compressed %d -> %d characters (%d%% shorter).",
			compressedBefore, compressedAfter, 100-100*compressedAfter/compressedBefore)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// pastDigest is a digest.md in the notes folder, with the window its title names.
type pastDigest struct {
	path       string
	day        time.Time // the day folder it is in
	from       time.Time
	until      time.Time
	summarized bool
}

// Digests written before the task backends were pluggable say "ClickUp digest".
var digestTitleRE = regexp.MustCompile(`^# (?:Task|ClickUp) digest: (\d{4}-\d{2}-\d{2}) to (\d{4}-\d{2}-\d{2})`)

// findDigests returns the digests in the notes folder, oldest first.
func findDigests(root string) []pastDigest {
	paths, _ := filepath.Glob(filepath.Join(root, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]", "digest.md"))
	sort.Strings(paths)
	var found []pastDigest
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		match := digestTitleRE.FindStringSubmatch(text)
		day, dayErr := time.ParseInLocation("2006-01-02", notes.Day(root, path), time.Local)
		if match == nil || dayErr != nil {
			continue
		}
		from, _ := time.ParseInLocation("2006-01-02", match[1], time.Local)
		to, _ := time.ParseInLocation("2006-01-02", match[2], time.Local)
		// The title's "from" is a date, but runs counted from the time of day they ran
		// (7d before 06:40 is 06:40), so start at the beginning of that day. The window
		// ends when the digest was written, or at the end of that day if the file has
		// been touched since (a git checkout, an edit).
		until := to.AddDate(0, 0, 1).Add(-time.Second)
		if info, err := os.Stat(path); err == nil && !info.ModTime().Before(to) && info.ModTime().Before(until) {
			until = info.ModTime()
		}
		found = append(found, pastDigest{path: path, day: day, from: from, until: until,
			summarized: strings.Contains(text, " Summaries by ")})
	}
	return found
}

// redoDigests rewrites earlier digests with AI summaries, oldest first, so each one
// gets the one before it as context. which is "missing" (digests without summaries)
// or "all"; sinceDay (YYYY-MM-DD, or "") skips digests from earlier days.
func redoDigests(app *App, which, sinceDay string, dryRun bool, scope digestScope, perTask, limit int, model, llmURL string) error {
	if which != "missing" && which != "all" {
		return fmt.Errorf("--redo takes missing or all, not %q", which)
	}
	root, err := app.NotesRoot()
	if err != nil {
		return err
	}
	var todo []pastDigest
	for _, digest := range findDigests(root) {
		if (which == "missing" && digest.summarized) || (sinceDay != "" && digest.day.Format("2006-01-02") < sinceDay) {
			continue
		}
		todo = append(todo, digest)
	}
	if len(todo) == 0 {
		fmt.Println("No digests to redo.")
		return nil
	}
	fmt.Printf("%d digest(s) to rewrite with AI summaries, oldest first:\n", len(todo))
	for _, digest := range todo {
		state := "no AI summaries"
		if digest.summarized {
			state = "has AI summaries"
		}
		rel, _ := filepath.Rel(root, digest.path)
		fmt.Printf("  %s  comments %s to %s  (%s)\n", rel, digest.from.Format("2006-01-02"), digest.until.Format("2006-01-02 15:04"), state)
	}
	if dryRun {
		return nil
	}

	unlock, err := lockDigest()
	if err != nil {
		return err
	}
	defer unlock()
	earliest := todo[0].from
	scope.closed = true
	candidates, err := digestCandidates(app, scope, earliest)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Fetching comments on %d task(s)...\n", len(candidates))
	comments, err := fetchComments(app, candidates)
	if err != nil {
		return err
	}
	// One model server for the whole run: loading a large model takes minutes.
	summarizer, err := newSummarizer(app, model, llmURL)
	if err != nil {
		return err
	}
	defer summarizer.Close()
	for index, digest := range todo {
		rel, _ := filepath.Rel(root, digest.path)
		fmt.Printf("[%d/%d] %s\n", index+1, len(todo), rel)
		summarizer.usePreviousBefore(digest.day)
		entries := windowEntries(candidates, comments, digest.from, digest.until, limit)
		addTLDRs(app, entries, digest.from, digest.until)
		if err := summarizeEntries(summarizer, entries); err != nil {
			return err
		}
		text := renderDigest(entries, digest.from, digest.until, perTask, summarizer.client.Model)
		if err := os.WriteFile(digest.path, []byte(text), 0o644); err != nil {
			return err
		}
		fmt.Printf("  rewrote it: %d task(s)\n", len(entries))
	}
	return nil
}

// cronVar returns the value a crontab assigns to name (NAME=value lines), or "".
func cronVar(crontab, name string) string {
	value := ""
	for _, line := range strings.Split(crontab, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), name+"="); ok {
			value = strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}
	return value
}

type legacySpelling struct{ where, old, new string }

// legacyCronSpellings finds old command spellings (hidden, deprecated commands) in the
// crontab's clctl lines and in the scripts those lines run.
func legacyCronSpellings(crontab string, root *cobra.Command) []legacySpelling {
	// old spelling -> new, e.g. "tasks digest" -> "digest"
	spellings := map[string]string{}
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, child := range parent.Commands() {
			if child.Deprecated != "" {
				old := strings.TrimPrefix(child.CommandPath(), root.Name()+" ")
				spellings[old] = strings.TrimPrefix(strings.Trim(strings.TrimPrefix(child.Deprecated, "use "), "'"), "clctl ")
			}
			walk(child)
		}
	}
	walk(root)
	var found []legacySpelling
	seen := map[string]bool{}
	scan := func(where, text string) {
		for old, replacement := range spellings {
			pattern := regexp.MustCompile(`\bclctl["']?(?:\s+--?\S+)*\s+` + strings.ReplaceAll(regexp.QuoteMeta(old), " ", `\s+`) + `\b`)
			if pattern.MatchString(text) && !seen[where+old] {
				seen[where+old] = true
				found = append(found, legacySpelling{where: where, old: "clctl " + old, new: "clctl " + replacement})
			}
		}
	}
	for _, line := range strings.Split(crontab, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "clctl") {
			continue
		}
		scan("your crontab", line)
		// Wrapper scripts it runs, such as ~/.local/bin/clctl-meeting-digest.
		for _, word := range strings.Fields(line) {
			path := os.ExpandEnv(config.ExpandHome(word))
			if !filepath.IsAbs(path) || !strings.Contains(filepath.Base(path), "clctl-") {
				continue
			}
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() < 64<<10 {
				if data, err := os.ReadFile(path); err == nil {
					scan(path, string(data))
				}
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].where+found[i].old < found[j].where+found[j].old })
	return found
}
