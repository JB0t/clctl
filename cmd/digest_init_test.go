package cmd

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/clickup"
	"github.com/JB0t/clctl/internal/notes"
)

// userCrontab is the shape of a real crontab: env lines, a managed wrapper line.
const userCrontab = `NOTES_DIR=/home/jo/notes
PATH=/home/jo/.local/bin:/usr/bin:/bin

30 6 * * * /home/jo/scripts/shells/meeting-notes --days 30 >> /home/jo/.local/state/meeting-notes.log 2>&1
# Make my meeting digest
*/20 6-11 * * 1-5 /home/jo/.local/bin/clctl-meeting-digest >> /home/jo/.local/state/clctl-meeting-digest.log 2>&1 # clctl-managed meeting digest
# 0 7 * * * clctl digest --meeting
30 22 * * * $HOME/.local/bin/clctl notes tldr --gentle >> $HOME/.local/state/clctl-tldr.log 2>&1
`

func TestCronHelpers(t *testing.T) {
	if got := cronVar(userCrontab, "NOTES_DIR"); got != "/home/jo/notes" {
		t.Fatalf("cronVar = %q", got)
	}
	if got := cronVar(userCrontab, "LLM_URL"); got != "" {
		t.Fatalf("cronVar(missing) = %q", got)
	}
	existing := meetingCronLines(userCrontab)
	if len(existing) != 1 || !strings.Contains(existing[0], "clctl-meeting-digest") {
		t.Fatalf("meetingCronLines = %q", existing)
	}
	updated := replaceCronLines(userCrontab, existing, "*/20 6-11 * * 1-5 clctl digest --meeting "+meetingCronMarker)
	if strings.Contains(updated, "clctl-meeting-digest >>") || !strings.Contains(updated, "# Make my meeting digest\n*/20 6-11 * * 1-5 clctl digest --meeting") ||
		!strings.Contains(updated, "# 0 7 * * * clctl digest --meeting") || !strings.Contains(updated, "notes tldr") {
		t.Fatalf("replaceCronLines:\n%s", updated)
	}
	if got := replaceCronLines("A=1", nil, "x"); got != "A=1\nx\n" {
		t.Fatalf("append = %q", got)
	}
	line, err := meetingCronLine("*/20  6-11 * * 1-5")
	if err != nil || !strings.HasPrefix(line, "*/20 6-11 * * 1-5 ") || !strings.HasSuffix(line, meetingCronMarker) || !strings.Contains(line, " digest --meeting >> ") {
		t.Fatalf("meetingCronLine = %q, %v", line, err)
	}
	if _, err := meetingCronLine("daily"); err == nil {
		t.Fatal("meetingCronLine(daily) did not fail")
	}
}

func TestLegacyCronSpellings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "clctl-meeting-digest")
	_ = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$HOME/.local/bin/clctl\" tasks digest --meeting \"$@\"\n"), 0o755)
	crontab := "*/20 6-11 * * 1-5 " + wrapper + " >> /tmp/log 2>&1 # clctl-managed meeting digest\n" +
		"0 * * * * clctl --refresh tasks start-timer\n0 1 * * * clctl digest --meeting\n# 0 2 * * * clctl tasks push-notes\n"
	found := legacyCronSpellings(crontab, newRoot(&App{}))
	var got []string
	for _, item := range found {
		got = append(got, filepath.Base(item.where)+": "+item.old+" -> "+item.new)
	}
	want := "clctl-meeting-digest: clctl tasks digest -> clctl digest|your crontab: clctl tasks start-timer -> clctl timers start"
	if strings.Join(got, "|") != want {
		t.Fatalf("legacy spellings:\n%s\nwant\n%s", strings.Join(got, "|"), strings.ReplaceAll(want, "|", "\n"))
	}
}

func TestMeetingNamesAndPattern(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	write := func(day time.Time, name string) {
		dir := notes.DayDir(root, day)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), []byte("# x\n"), 0o644)
	}
	write(now.AddDate(0, 0, -16), "weekly-it-meeting.md")
	write(now.AddDate(0, 0, -2), "it-team-sync.md")
	write(now.AddDate(0, 0, -9), "it-team-sync.md")
	write(now.AddDate(0, 0, -2), "digest.md")
	write(now.AddDate(0, 0, -200), "ancient.md")
	matches, _ := meetingMatcher("")
	names := recentDayNotes(root, now, meetingScanDays, matches)
	var got []string
	for _, entry := range names {
		got = append(got, entry.name)
	}
	if strings.Join(got, ",") != "weekly-it-meeting.md,it-team-sync.md" || names[1].days != 2 || names[1].last != "2026-10-14" {
		t.Fatalf("recentDayNotes = %+v", names)
	}
	pattern := exactNamesPattern([]string{"weekly-it-meeting.md", "it-team-sync.md"})
	matches, err := meetingMatcher(pattern)
	if err != nil || !matches("it-team-sync.md") || !matches("weekly-it-meeting.md") || matches("it-team-sync.md.bak") || matches("x-it-team-sync.md") {
		t.Fatalf("pattern %q matched wrongly (%v)", pattern, err)
	}
}

func TestMeetingDigestForceAndNamedNote(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("NOTES_DIR", "")
	model := &fakeModel{}
	modelServer := httptest.NewServer(model)
	defer modelServer.Close()
	clickUpServer := httptest.NewServer(&fakeTLDRClickUp{})
	defer clickUpServer.Close()
	_ = os.MkdirAll(filepath.Join(home, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "config", "clctl"), []byte("[clickup]\nnotes_enabled = true\nnotes_dir = "+root+
		"\nllm_url = "+modelServer.URL+"/v1\nuser_id = 1\n"), 0o644)
	today := notes.DayDir(root, time.Now())
	_ = os.MkdirAll(today, 0o755)
	_ = os.WriteFile(filepath.Join(today, "it-catch-up.md"), []byte("# IT catch-up\n\nagenda\n"), 0o644)

	run := func(args ...string) string {
		t.Helper()
		app := &App{client: clickup.New("token", clickUpServer.URL, "1"), CachePath: filepath.Join(home, "cache.json")}
		cmd := newDigestCmd(app)
		cmd.SetArgs(args)
		stdout := os.Stdout
		reader, writer, _ := os.Pipe()
		os.Stdout = writer
		err := cmd.Execute()
		writer.Close()
		os.Stdout = stdout
		var out bytes.Buffer
		_, _ = io.Copy(&out, reader)
		if err != nil {
			t.Fatalf("digest %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	digest := filepath.Join(today, "digest.md")

	// The default pattern misses it-catch-up.md: nothing happens without --force.
	if out := run("--meeting"); !strings.Contains(out, "--force") {
		t.Fatalf("no-note run printed %q", out)
	}
	if _, err := os.Stat(digest); err == nil {
		t.Fatal("digest written without --force")
	}
	// --force writes it, unlinked; a second --force run leaves it alone.
	if out := run("--force"); !strings.Contains(out, "anyway") || !strings.Contains(out, "Wrote") {
		t.Fatalf("--force printed %q", out)
	}
	if _, err := os.Stat(digest); err != nil {
		t.Fatal(err)
	}
	if out := run("--meeting", "--force"); !strings.Contains(out, "already written") {
		t.Fatalf("second --force printed %q", out)
	}
	// --meeting-note links the named note (by name, .md optional).
	if out := run("--meeting-note", "it-catch-up", "--again"); !strings.Contains(out, "Linked it from") {
		t.Fatalf("--meeting-note printed %q", out)
	}
	if data, _ := os.ReadFile(filepath.Join(today, "it-catch-up.md")); !strings.Contains(string(data), "](digest.md)") {
		t.Fatalf("meeting note not linked:\n%s", data)
	}
	app := &App{}
	cmd := newDigestCmd(app)
	cmd.SetArgs([]string{"--meeting-note", "missing.md"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--meeting-note") {
		t.Fatalf("missing note: %v", err)
	}
}
