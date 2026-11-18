package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/notes"
	"github.com/JB0t/clctl/internal/ui"
)

const (
	// meetingCronMarker ends the crontab line digest --init manages.
	meetingCronMarker  = "# clctl-managed meeting digest"
	defaultMeetingCron = "*/20 6-11 * * 1-5"
	// meetingScanDays is how far back --init looks for meeting note names.
	meetingScanDays = 120
)

// runDigestInit sets up the meeting digest: notes_dir in the config, which note is the
// meeting note, and the cron line. Each change is shown and confirmed first.
func runDigestInit(app *App, rootCmd *cobra.Command) error {
	if !ui.Interactive() {
		return errors.New("--init asks questions; run it in a terminal (clctl digest --check shows the set-up without changing it)")
	}
	cfg, err := app.Config()
	if err != nil {
		return err
	}

	fmt.Println("1. Notes folder")
	root := cfg.NotesDir()
	switch {
	case root == "":
		answer, err := ui.Ask("   Notes folder (the dated YYYY/MM/DD layout)", "")
		if err != nil {
			return err
		}
		if answer == "" {
			return errors.New("the meeting digest needs the notes folder")
		}
		if err := enableNotes(cfg, answer); err != nil {
			return err
		}
		root = config.ExpandHome(answer)
	case cfg.Stored(config.KeyNotesDir) == "":
		fmt.Printf("   %s comes only from $NOTES_DIR, which cron sees only if the crontab sets it.\n", root)
		save, err := ui.Confirm("   Save it as notes_dir in "+cfg.Path+"?", true)
		if err != nil {
			return err
		}
		if save {
			cfg.Set(config.KeyNotesDir, root)
			cfg.Set(config.KeyNotesEnabled, "true")
		}
	default:
		fmt.Printf("   %s\n", root)
	}

	fmt.Println("2. Meeting note")
	pattern, err := pickMeetingPattern(root, cfg.Get(config.KeyMeetingPattern))
	if err != nil {
		return err
	}
	if pattern != cfg.Get(config.KeyMeetingPattern) {
		cfg.Set(config.KeyMeetingPattern, pattern)
		fmt.Printf("   meeting_pattern = %s\n", orDefault(pattern, "(the default: weekly + it + meeting)"))
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Println("3. Cron")
	if err := setUpMeetingCron(); err != nil {
		fmt.Println("   " + err.Error())
	}

	fmt.Println("\nChecking the set-up:")
	return digestCheck(app, rootCmd, "", "", "")
}

// meetingName is a file name seen directly in day folders, with how often and when.
type meetingName struct {
	name  string
	days  int
	last  string
	match bool
}

// recentDayNotes counts the notes directly in the day folders of the last days.
func recentDayNotes(root string, now time.Time, days int, matches func(string) bool) []meetingName {
	byName := map[string]*meetingName{}
	for back := 0; back < days; back++ {
		day := now.AddDate(0, 0, -back)
		for _, name := range dayNotes(notes.DayDir(root, day)) {
			entry := byName[name]
			if entry == nil {
				entry = &meetingName{name: name, last: day.Format("2006-01-02"), match: matches(name)}
				byName[name] = entry
			}
			entry.days++
		}
	}
	list := make([]meetingName, 0, len(byName))
	for _, entry := range byName {
		list = append(list, *entry)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].match != list[j].match {
			return list[i].match
		}
		if list[i].days != list[j].days {
			return list[i].days > list[j].days
		}
		return list[i].last > list[j].last
	})
	return list
}

// pickMeetingPattern shows the note names of recent days and returns the pattern for
// the ones picked: the current pattern when kept.
func pickMeetingPattern(root, current string) (string, error) {
	matches, err := meetingMatcher(current)
	if err != nil {
		fmt.Printf("   The current meeting_pattern is broken (%v); pick the notes again.\n", err)
		matches = func(string) bool { return false }
	}
	names := recentDayNotes(root, time.Now(), meetingScanDays, matches)
	if len(names) == 0 {
		fmt.Printf("   No notes directly in the day folders of the last %d days; keeping the pattern.\n", meetingScanDays)
		return current, nil
	}
	rows := []string{"(keep the current pattern: " + orDefault(current, "weekly + it + meeting") + ")"}
	for _, entry := range names {
		row := fmt.Sprintf("%-40s %3d days, last %s", entry.name, entry.days, entry.last)
		if entry.match {
			row += "  (matches now)"
		}
		rows = append(rows, row)
	}
	rows = append(rows, "(type a regular expression)")
	picked, err := ui.PickMany(rows, "Which notes are the meeting's? >")
	if err != nil {
		return "", err
	}
	var chosen []string
	for _, index := range picked {
		switch index {
		case 0:
			return current, nil
		case len(rows) - 1:
			typed, err := ui.Ask("   Regular expression for the meeting note's file name", current)
			if err != nil {
				return "", err
			}
			if _, err := regexp.Compile(typed); err != nil {
				return "", fmt.Errorf("invalid pattern: %w", err)
			}
			return typed, nil
		default:
			chosen = append(chosen, names[index-1].name)
		}
	}
	if len(chosen) == 0 {
		return current, nil
	}
	return exactNamesPattern(chosen), nil
}

// exactNamesPattern matches exactly these file names.
func exactNamesPattern(names []string) string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = regexp.QuoteMeta(name)
	}
	sort.Strings(quoted)
	return "^(?:" + strings.Join(quoted, "|") + ")$"
}

// setUpMeetingCron adds the meeting digest's cron line, or replaces the one already
// there (a clctl-managed line, or any running digest --meeting), after confirmation.
func setUpMeetingCron() error {
	crontab, err := readCrontab()
	if err != nil {
		return err
	}
	existing := meetingCronLines(crontab)
	schedule := defaultMeetingCron
	if len(existing) > 0 {
		if fields := strings.Fields(existing[0]); len(fields) > 5 {
			schedule = strings.Join(fields[:5], " ")
		}
	}
	schedule, err = ui.Ask("   Schedule (cron: minute hour day month weekday)", schedule)
	if err != nil {
		return err
	}
	line, err := meetingCronLine(schedule)
	if err != nil {
		return err
	}
	if len(existing) == 1 && existing[0] == line {
		fmt.Println("   Your crontab already has this line:\n   " + line)
		return nil
	}
	for _, old := range existing {
		fmt.Println("   - " + old)
	}
	fmt.Println("   + " + line)
	question := "   Add it to your crontab?"
	if len(existing) > 0 {
		question = "   Replace the line(s) marked - with it? (a wrapper script they run is then unused)"
	}
	ok, err := ui.Confirm(question, true)
	if err != nil || !ok {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath("meeting-digest.log")), 0o755); err != nil {
		return err
	}
	if err := writeCrontab(replaceCronLines(crontab, existing, line)); err != nil {
		return err
	}
	fmt.Println("   Crontab updated.")
	return nil
}

// meetingCronLine runs this binary's meeting digest, logging to the state folder.
func meetingCronLine(schedule string) (string, error) {
	if len(strings.Fields(schedule)) != 5 {
		return "", fmt.Errorf("schedule %q: want five cron fields, like %q", schedule, defaultMeetingCron)
	}
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	return fmt.Sprintf("%s %s digest --meeting >> %s 2>&1 %s", strings.Join(strings.Fields(schedule), " "),
		ui.ShellQuote(binary), ui.ShellQuote(statePath("meeting-digest.log")), meetingCronMarker), nil
}

// meetingCronLines finds the crontab lines that make the meeting digest: the managed
// one, and any other running clctl's digest --meeting (commented-out lines do not count).
func meetingCronLines(crontab string) []string {
	var lines []string
	for _, line := range strings.Split(crontab, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, meetingCronMarker) || strings.Contains(trimmed, "clctl") && strings.Contains(trimmed, "digest --meeting") {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// replaceCronLines puts line where the first old line was and drops the other old ones;
// with no old lines it appends line.
func replaceCronLines(crontab string, old []string, line string) string {
	if len(old) == 0 {
		return appendCronLine(crontab, line)
	}
	drop := map[string]bool{}
	for _, entry := range old {
		drop[entry] = true
	}
	var out []string
	placed := false
	for _, entry := range strings.Split(strings.TrimRight(crontab, "\n"), "\n") {
		if drop[strings.TrimSpace(entry)] {
			if !placed {
				out = append(out, line)
				placed = true
			}
			continue
		}
		out = append(out, entry)
	}
	return strings.Join(out, "\n") + "\n"
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
