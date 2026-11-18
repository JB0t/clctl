package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/clickup"
	"github.com/JB0t/clctl/internal/markdown"
)

func TestDoneStatus(t *testing.T) {
	statuses := []clickup.Status{{Status: "to do", Type: "open"}, {Status: "stopped", Type: "done"}, {Status: "completed", Type: "closed"}}
	if got := doneStatus(statuses); got != "completed" {
		t.Fatalf("doneStatus = %q", got)
	}
	statuses = []clickup.Status{{Status: "open", Type: "open"}, {Status: "shipped", Type: "closed"}}
	if got := doneStatus(statuses); got != "shipped" {
		t.Fatalf("doneStatus = %q", got)
	}
}

func TestConfigFlag(t *testing.T) {
	for args, want := range map[string]string{
		"tasks list --config /a": "/a",
		"--config=/b tasks":      "/b",
		"tasks -- --config /c":   "",
		"tasks list":             "",
	} {
		if got := configFlag(splitArgs(args)); got != want {
			t.Errorf("configFlag(%q) = %q, want %q", args, got, want)
		}
	}
}

func splitArgs(line string) []string {
	var args []string
	current := ""
	for _, r := range line + " " {
		if r == ' ' {
			if current != "" {
				args = append(args, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	return args
}

func TestParsePositionsAndTodo(t *testing.T) {
	got, err := parsePositions("4,1-2,2", []int{1, 2, 3, 4})
	if err != nil || fmt.Sprint(got) != "[4 1 2]" {
		t.Fatalf("parsePositions = %v, %v", got, err)
	}
	if _, err := parsePositions("9", []int{1, 2}); err == nil {
		t.Fatal("expected an error for a missing section")
	}
	entries := parseTodo([]string{"# TODO", "new: First", "    detail", "", "keep", "new: Second"})
	if len(entries) != 2 || entries[0].title != "First" || entries[0].description != "detail\n\nkeep" || entries[1].start != 5 {
		t.Fatalf("parseTodo = %+v", entries)
	}
}

func TestParseSinceAndClip(t *testing.T) {
	week, err := parseSince("7d")
	if err != nil || time.Since(week) < 6*24*time.Hour {
		t.Fatalf("7d = %v, %v", week, err)
	}
	if day, err := parseSince("2026-09-20"); err != nil || day.Day() != 20 {
		t.Fatalf("date = %v, %v", day, err)
	}
	if _, err := parseSince("soon"); err == nil {
		t.Fatal("expected an error")
	}
	if got := clip("line one\nline two\nline three", 20); got != "line one\nline two\n[…]" {
		t.Fatalf("clip = %q", got)
	}
}

func TestMeetingHelpers(t *testing.T) {
	matches, _ := meetingMatcher("")
	for name, want := range map[string]bool{"weekly-it-meeting.md": true, "Weekly_IT_Meeting.md": true, "weekly-commit-meeting.md": false, "it-meeting.md": false} {
		if matches(name) != want {
			t.Errorf("match(%q) = %v", name, !want)
		}
	}
	root := t.TempDir()
	write := func(rel, text string) string {
		path := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(text), 0o644)
		return path
	}
	write("2026/09/14/weekly-it-meeting.md", "# Weekly IT Meeting\n")
	write("2026/09/21/weekly-it-meeting.md", "# Weekly IT Meeting\n")
	write("2026/10/07/weekly-it-meeting.md", "# future\n")
	note := write("2026/09/30/weekly-it-meeting.md", "# Weekly IT Meeting\n\n## Dask\n")
	today := filepath.Join(root, "2026", "09", "30")
	if findMeetingNote(today, matches) != note {
		t.Fatal("today's meeting note not found")
	}
	if previous := previousMeetingDay(root, today, matches); previous.Format("2006-01-02") != "2026-09-21" {
		t.Fatalf("previous meeting = %v", previous)
	}
	from, _ := time.Parse("2006-01-02", "2026-09-21")
	for range 2 {
		if err := linkDigest(note, filepath.Join(today, "digest.md"), from); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(note)
	if string(data) != "# Weekly IT Meeting\n\nTask digest since Mon 21 Sep: [digest.md](digest.md)\n\n## Dask\n" {
		t.Fatalf("note = %q", data)
	}
}

func TestCapBullets(t *testing.T) {
	got := capBullets("* a\n* b\n  continued\n* c\n* d\n* e\n* f\n* g", 5)
	if got != "- a\n- b\n  continued\n- c\n- d\n- e" {
		t.Fatalf("capBullets = %q", got)
	}
	for comments, want := range map[int]int{0: 3, 1: 3, 2: 5, 4: 7, 8: 9, 16: 11, 100: 12} {
		if got := bulletLimit(comments); got != want {
			t.Errorf("bulletLimit(%d) = %d, want %d", comments, got, want)
		}
	}
}

func TestParseDigestSummaries(t *testing.T) {
	text := "# ClickUp digest\n\n## TECH-1 One\n\nmeta\n\n**Summary**\n\n- a\n- b\n\n**Comments**\n\n- c\n\n## TECH-2 Two\n\nmeta\n"
	got := parseDigestSummaries(text)
	if len(got) != 1 || got["TECH-1"] != "- a\n- b" {
		t.Fatalf("parseDigestSummaries = %q", got)
	}
}

func TestEditedRangeKeepsUnchangedTimes(t *testing.T) {
	start := time.Date(2026, 10, 8, 9, 0, 42, 0, time.Local)
	end := time.Date(2026, 10, 8, 10, 30, 17, 0, time.Local)
	entry := clickup.TimeEntry{Start: clickup.FlexInt(start.UnixMilli()), Duration: clickup.FlexInt(end.Sub(start).Milliseconds())}
	from, to, err := editedRange(entry, "2026-10-08", "09:00", "11:00")
	if err != nil || !from.Equal(start) || to.Format("15:04:05") != "11:00:00" {
		t.Fatalf("edited end = %v-%v, %v", from, to, err)
	}
	from, to, err = editedRange(entry, "2026-10-07", "09:00", "10:30")
	if err != nil || from.Format("2006-01-02 15:04") != "2026-10-07 09:00" || to.Format("2006-01-02 15:04") != "2026-10-07 10:30" {
		t.Fatalf("moved day = %v-%v, %v", from, to, err)
	}
	if _, _, err := editedRange(entry, "2026-10-08", "11:00", "10:30"); err == nil {
		t.Fatal("expected an error for an end before the start")
	}
}

func TestUnpushedNotes(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	note := filepath.Join(root, "2026", "10", "08", "S", "L", "T-1.md")
	_ = os.MkdirAll(filepath.Dir(note), 0o755)
	_ = os.WriteFile(note, []byte("# T-1\n\n## A\n\nsame\n\n## A\n\nchanged\n\n## B\n\nnew\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "2026", "10", "08", "digest.md"), []byte("## X\n\nnot a task note\n"), 0o644)
	sections := markdown.Sections([]string{"## A", "", "same", "", "## A", "", "changed"}, nil, true)
	if keys := sectionKeys(sections); fmt.Sprint(keys) != "[A A (2)]" {
		t.Fatalf("sectionKeys = %v", keys)
	}
	saved := pushState{"T-1": {"2026-10-08": {"A": {CommentID: "1", Hash: sections[0].Fingerprint()}, "A (2)": {CommentID: "2", Hash: "old"}}}}
	if err := saved.save(); err != nil {
		t.Fatal(err)
	}
	found := findUnpushed(root, "", time.Time{})
	if len(found) != 1 || found[0].taskID != "T-1" || found[0].added != 1 || found[0].edited != 1 {
		t.Fatalf("findUnpushed = %+v", found)
	}
	if got := fmt.Sprint(found[0].sections); got != "[edited  2. A (2) new     3. B]" {
		t.Fatalf("sections = %q", got)
	}
	since, _ := time.Parse("2006-01-02", "2026-10-09")
	if found := findUnpushed(root, "", since); len(found) != 0 {
		t.Fatalf("--since kept older notes: %+v", found)
	}
}

func TestTimerOn(t *testing.T) {
	running := timer{TimeEntry: &clickup.TimeEntry{Task: &clickup.TimeEntryTask{ID: "abc", CustomID: "TECH-1"}}}
	if !running.on("abc") || !running.on("tech-1") || running.on("TECH-2") {
		t.Fatal("timer.on matched wrongly")
	}
}

func TestMenuAndLegacyCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newRoot(&App{})
	_, names := menuActions(root)
	got := strings.Join(names, ",")
	for _, want := range []string{"tasks list", "timers start", "comments add", "digest"} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Errorf("root menu %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "start-timer") || strings.Contains(got, "init") {
		t.Errorf("root menu offers old or set-up commands: %q", got)
	}
	for _, path := range [][]string{{"tasks", "start-timer"}, {"tasks", "digest"}, {"timers", "edit"}} {
		if found, _, err := root.Find(path); err != nil || found.Name() != path[1] {
			t.Errorf("%v not found: %v", path, err)
		}
	}
}

func TestLockDigest(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	unlock, err := lockDigest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockDigest(); err == nil || !strings.Contains(err.Error(), "another clctl run") {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	if _, err := os.Stat(statePath("digest.lock")); !os.IsNotExist(err) {
		t.Fatal("unlock left the lock file")
	}
	// A lock whose process has gone (a killed run) is taken over at once.
	_ = os.WriteFile(statePath("digest.lock"), []byte("pid 999999999 since 2026-10-09 10:00\n"), 0o644)
	again, err := lockDigest()
	if err != nil {
		t.Fatalf("dead owner's lock not taken over: %v", err)
	}
	again()
}

func TestWindowEntries(t *testing.T) {
	at := func(day string) time.Time {
		parsed, _ := time.ParseInLocation("2006-01-02 15:04", day, time.Local)
		return parsed
	}
	millis := func(when time.Time) clickup.FlexString { return clickup.FlexString(fmt.Sprint(when.UnixMilli())) }
	tasks := []clickup.Task{
		{ID: "a", DateUpdated: clickup.FlexInt(at("2026-10-08 10:00").UnixMilli())}, // edited after the window; one comment in it
		{ID: "b", DateUpdated: clickup.FlexInt(at("2026-10-02 10:00").UnixMilli())}, // edited in the window, no comments
		{ID: "c", DateUpdated: clickup.FlexInt(at("2026-10-08 10:00").UnixMilli())}, // only a comment after the window
	}
	comments := map[string][]clickup.Comment{
		"a": {{ID: "2", Date: millis(at("2026-10-07 09:00"))}, {ID: "1", Date: millis(at("2026-10-03 09:00"))}},
		"c": {{ID: "3", Date: millis(at("2026-10-07 09:00"))}},
	}
	got := windowEntries(tasks, comments, at("2026-10-01 00:00"), at("2026-10-05 23:59"), 0)
	if len(got) != 2 || got[0].task.ID != "a" || len(got[0].comments) != 1 || got[1].task.ID != "b" {
		t.Fatalf("past window = %+v", got)
	}
	if now := windowEntries(tasks, comments, at("2026-10-01 00:00"), time.Time{}, 0); len(now) != 3 || len(now[0].comments)+len(now[1].comments)+len(now[2].comments) != 3 {
		t.Fatalf("open window = %+v", now)
	}
}

func TestFindDigestsAndPreviousContext(t *testing.T) {
	root := t.TempDir()
	write := func(day, text string) {
		dir := filepath.Join(root, strings.ReplaceAll(day, "-", "/"))
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "digest.md"), []byte(text), 0o644)
	}
	write("2026-09-29", "# ClickUp digest: 2026-09-22 to 2026-09-29\n\n1 tasks, 1 comments. Summaries by m (local), from the comments only.\n\n## T-1 One\n\nmeta\n\n**Summary**\n\n- old summary\n\n**Comments**\n\n- c\n")
	write("2026-10-06", "# ClickUp digest: 2026-09-29 to 2026-10-06\n\n2 tasks, 3 comments.\n")
	write("2026-10-07", "not a digest\n")
	found := findDigests(root)
	if len(found) != 2 || !found[0].summarized || found[1].summarized {
		t.Fatalf("findDigests = %+v", found)
	}
	if found[1].from.Format("2006-01-02") != "2026-09-29" || found[1].until.Format("2006-01-02") != "2026-10-06" {
		t.Fatalf("window = %v to %v", found[1].from, found[1].until)
	}
	day, summaries := previousDigest(root, found[1].day)
	if day != "2026-09-29" || summaries["T-1"] != "- old summary" {
		t.Fatalf("previousDigest = %q %v", day, summaries)
	}
	if day, _ := previousDigest(root, found[0].day); day != "" {
		t.Fatalf("nothing should precede the first digest, got %q", day)
	}
}

func TestDropEmptySections(t *testing.T) {
	got := dropEmptySections("## Done  \n- shipped x (2026-10-05)\n## In progress  \n## Blocked / open questions\n\n## Next\n- y (2026-10-06)")
	if got != "## Done\n- shipped x (2026-10-05)\n\n## Next\n- y (2026-10-06)" {
		t.Fatalf("got %q", got)
	}
}

func TestCompletionWithAliases(t *testing.T) {
	bash := completionWithAliases("bash", "clctl", "complete -o default -F __start_clctl clctl\n", []string{"cl", " ", "clctl"})
	if !strings.HasSuffix(bash, "complete -o default -F __start_clctl clctl\ncomplete -o default -F __start_clctl cl\n") {
		t.Errorf("bash:\n%s", bash)
	}
	zsh := completionWithAliases("zsh", "clctl", "#compdef clctl\ncompdef _clctl clctl\n", []string{"cl,c"})
	if zsh != "#compdef clctl cl,c\ncompdef _clctl clctl cl,c\n" {
		t.Errorf("zsh:\n%s", zsh)
	}
	fish := completionWithAliases("fish", "clctl", "x\n", []string{"cl"})
	if fish != "x\ncomplete -c cl -w clctl\n" {
		t.Errorf("fish:\n%s", fish)
	}
	ps := completionWithAliases("powershell", "clctl", "Register-ArgumentCompleter -CommandName 'clctl' -ScriptBlock x\n", []string{"cl"})
	if ps != "Register-ArgumentCompleter -CommandName 'clctl','cl' -ScriptBlock x\n" {
		t.Errorf("powershell:\n%s", ps)
	}
	if got := completionWithAliases("bash", "clctl", "same\n", nil); got != "same\n" {
		t.Errorf("no aliases changed the script: %q", got)
	}
}

func TestPushProgressLabel(t *testing.T) {
	for label, progress := range map[string]pushProgress{
		"empty":           {},
		"pushed":          {pushed: 3},
		"3 new":           {added: 3},
		"2 pushed, 1 new": {pushed: 2, added: 1},
		"1 edited":        {edited: 1},
	} {
		if got := progress.label(); got != label {
			t.Errorf("%+v.label() = %q, want %q", progress, got, label)
		}
	}
}

func TestInspectLine(t *testing.T) {
	entry := clickup.TimeEntry{ID: "1", Start: 1000, End: 4000, Duration: 3000}
	fields := map[string]any{"start": 1000.0, "end": 4000.0, "duration": 3000.0, "at": 5000.0, "source": "clickup"}
	if line := inspectLine(entry, fields); strings.Contains(line, "!") {
		t.Errorf("consistent entry flagged: %s", line)
	}
	entry.End = 3500
	if line := inspectLine(entry, fields); !strings.Contains(line, "!end is not start + duration") {
		t.Errorf("mismatch not flagged: %s", line)
	}
	delete(fields, "end")
	if line := inspectLine(entry, fields); !strings.Contains(line, "!no end") {
		t.Errorf("missing end not flagged: %s", line)
	}
	fields["end"], fields["start"] = "4000", "1000"
	entry.End = 4000
	if line := inspectLine(entry, fields); !strings.Contains(line, "!start stored as string") {
		t.Errorf("string field not flagged: %s", line)
	}
	entry.Duration = -1
	if line := inspectLine(entry, fields); !strings.Contains(line, "(running)") {
		t.Errorf("running entry: %s", line)
	}
}
