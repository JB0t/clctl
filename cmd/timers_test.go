package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/clickup"
)

// fakeTimerServer is ClickUp's one-timer-per-user API: current, start, stop and logged entries.
type fakeTimerServer struct {
	mu      sync.Mutex
	current *clickup.TimeEntry
	logged  []string // task refs, in the order their time was logged
}

func (f *fakeTimerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case strings.HasSuffix(r.URL.Path, "/time_entries/current"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": f.current})
	case strings.HasSuffix(r.URL.Path, "/time_entries/start"):
		f.current = &clickup.TimeEntry{ID: "e", Start: clickup.FlexInt(time.Now().UnixMilli()), Duration: -1}
		if ref, ok := body["tid"].(string); ok {
			f.current.Task = &clickup.TimeEntryTask{ID: ref, CustomID: ref, Name: ref}
		}
		f.current.Description, _ = body["description"].(string)
		if tags, ok := body["tags"].([]any); ok {
			for _, tag := range tags {
				f.current.Tags = append(f.current.Tags, clickup.TimeEntryTag{Name: fmt.Sprint(tag.(map[string]any)["name"])})
			}
		}
		_, _ = w.Write([]byte("{}"))
	case strings.HasSuffix(r.URL.Path, "/time_entries/stop"):
		if f.current != nil {
			f.logged = append(f.logged, (timer{TimeEntry: f.current}).ref())
		}
		f.current = nil
		_, _ = w.Write([]byte("{}"))
	case strings.HasSuffix(r.URL.Path, "/time_entries") && r.Method == http.MethodPost:
		logged := fmt.Sprint(body["tid"])
		if _, ok := body["tid"]; !ok {
			logged = fmt.Sprintf("-%v%v", body["description"], body["tags"])
		}
		f.logged = append(f.logged, logged)
		_, _ = w.Write([]byte("{}"))
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

// queueRefs lists the running timers oldest first, marking local ones with *.
func queueRefs(t *testing.T, app *App) string {
	t.Helper()
	timers, err := timerQueue(app)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, running := range timers {
		ref := running.ref()
		if running.local {
			ref += "*"
		}
		refs = append(refs, ref)
	}
	return strings.Join(refs, ",")
}

func pausedRefs() string {
	var refs []string
	for _, paused := range loadPausedTimers() {
		refs = append(refs, paused.work().name())
	}
	return strings.Join(refs, ",")
}

func TestPauseResumeLIFO(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := &fakeTimerServer{}
	server := httptest.NewServer(fake)
	defer server.Close()
	app := &App{client: clickup.New("token", server.URL, "1"), CachePath: filepath.Join(t.TempDir(), "cache.json")}

	// T-1 on ClickUp, then T-2 and T-3 as local timers started after it.
	start := time.Now().Add(-time.Hour)
	fake.current = &clickup.TimeEntry{ID: "e", Task: &clickup.TimeEntryTask{ID: "T-1", CustomID: "T-1", Name: "one"},
		Start: clickup.FlexInt(start.UnixMilli()), Duration: -1}
	_ = saveLocalTimers([]localTimer{
		{workFields: workFields{TaskID: "T-3", CustomID: "T-3", Name: "three"}, Start: start.Add(20 * time.Minute).UnixMilli()},
		{workFields: workFields{TaskID: "T-2", CustomID: "T-2", Name: "two"}, Start: start.Add(10 * time.Minute).UnixMilli()},
	})
	if got := queueRefs(t, app); got != "T-1,T-2*,T-3*" {
		t.Fatalf("queue = %q", got)
	}

	// pause stops the most recently started timer.
	run := func(cmd string, args ...string) {
		t.Helper()
		command := map[string]func(*App) *cobra.Command{"pause": newPauseTimerCmd, "resume": newResumeTimerCmd}[cmd](app)
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatalf("%s %v: %v", cmd, args, err)
		}
	}
	run("pause")
	if got, paused := queueRefs(t, app), pausedRefs(); got != "T-1,T-2*" || paused != "T-3" {
		t.Fatalf("after pause: queue %q, paused %q", got, paused)
	}
	// resume puts it back as the newest; ClickUp is busy with T-1, so it runs locally.
	run("resume")
	if got, paused := queueRefs(t, app), pausedRefs(); got != "T-1,T-2*,T-3*" || paused != "" {
		t.Fatalf("after resume: queue %q, paused %q", got, paused)
	}

	// --all pauses newest first; resume --all restores the original order, with the
	// oldest taking ClickUp's free timer.
	run("pause", "--all")
	if got, paused := queueRefs(t, app), pausedRefs(); got != "" || paused != "T-3,T-2,T-1" {
		t.Fatalf("after pause --all: queue %q, paused %q", got, paused)
	}
	if got := strings.Join(fake.logged, ","); got != "T-3,T-3,T-2,T-1" {
		t.Fatalf("logged = %q", got)
	}
	time.Sleep(2 * time.Millisecond) // distinct start times for the resumed timers
	run("resume", "--all")
	if got, paused := queueRefs(t, app), pausedRefs(); got != "T-1,T-2*,T-3*" || paused != "" {
		t.Fatalf("after resume --all: queue %q, paused %q", got, paused)
	}

	// A paused task restarted elsewhere (ClickUp's UI) is dropped, not started twice.
	run("pause")
	_ = saveLocalTimers(append(loadLocalTimers(), localTimer{workFields: workFields{TaskID: "T-3", CustomID: "T-3", Name: "three"}, Start: time.Now().UnixMilli()}))
	run("resume")
	if got, paused := queueRefs(t, app), pausedRefs(); got != "T-1,T-2*,T-3*" || paused != "" {
		t.Fatalf("restarted elsewhere: queue %q, paused %q", got, paused)
	}
}

func TestClockOnNow(t *testing.T) {
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.Local)
	before := time.Now()
	got, err := clockOn(day, " NOW ")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if got.Format("2006-01-02") != "2026-09-14" || got.Second() != 0 {
		t.Fatalf("clockOn now = %v, want 2026-09-14 at the current minute", got)
	}
	if clock := got.Format("15:04"); clock != before.Format("15:04") && clock != after.Format("15:04") {
		t.Fatalf("clockOn now = %s, want %s", clock, before.Format("15:04"))
	}
	if _, err := clockOn(day, "later"); err == nil {
		t.Fatal("expected an error for a time that is neither HH:MM nor now")
	}
}

func TestLogTimeEndNow(t *testing.T) {
	if time.Now().Format("15:04") == "00:00" {
		t.Skip("an end of now at 00:00 is not after a 00:00 start")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/time_entries"):
			_ = json.NewDecoder(r.Body).Decode(&body)
		case strings.HasSuffix(r.URL.Path, "/task/abc"):
			_, _ = w.Write([]byte(`{"id":"abc","custom_id":"TECH-1","name":"one"}`))
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	_ = os.WriteFile(cachePath, []byte(`{"tasks":[{"id":"abc","custom_id":"TECH-1","name":"one"}]}`), 0o644)
	app := &App{client: clickup.New("token", server.URL, "1"), CachePath: cachePath}
	command := newLogTimeCmd(app)
	command.SetArgs([]string{"--task-id", "abc", "--start", "00:00", "--end", "now"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	end, _ := body["end"].(float64)
	if got, want := time.UnixMilli(int64(end)).Format("2006-01-02 15:04"), time.Now().Format("2006-01-02 15:04"); got != want {
		t.Fatalf("logged end = %s, want %s", got, want)
	}
}

func TestTimersWithoutTask(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := &fakeTimerServer{}
	server := httptest.NewServer(fake)
	defer server.Close()
	app := &App{client: clickup.New("token", server.URL, "1"), CachePath: filepath.Join(t.TempDir(), "cache.json")}

	// No task on ClickUp's timer: the description and labels go with it.
	if err := startWork(app, work{Description: "Sales call", Tags: []string{"meetings"}}, "sales"); err != nil {
		t.Fatal(err)
	}
	if fake.current == nil || fake.current.Task != nil || fake.current.Description != "Sales call" || fmt.Sprint(fake.current.TagNames()) != "[meetings]" {
		t.Fatalf("started = %+v", fake.current)
	}
	// A second one runs locally, survives pause and resume, and is logged with its labels.
	_ = saveLocalTimers([]localTimer{{workFields: fieldsOf(work{Tags: []string{"admin"}}), Start: time.Now().UnixMilli() + 1}})
	if got := queueRefs(t, app); got != `"Sales call",#admin*` {
		t.Fatalf("queue = %q", got)
	}
	if text, err := renderTimers(app); err != nil || !strings.Contains(text, "(no task)") || !strings.Contains(text, "Sales call  #meetings") {
		t.Fatalf("list = %q, %v", text, err)
	}
	command := newPauseTimerCmd(app)
	command.SetArgs([]string{"--all"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.logged, ","); got != `-<nil>[map[name:admin]],"Sales call"` {
		t.Fatalf("logged = %q", got)
	}
	command = newResumeTimerCmd(app)
	command.SetArgs([]string{"--all"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := queueRefs(t, app); got != `"Sales call",#admin*` {
		t.Fatalf("resumed queue = %q", got)
	}

	// timers log --no-task needs a description or a label.
	logCmd := newLogTimeCmd(app)
	logCmd.SetArgs([]string{"--no-task", "--start", "09:00", "--end", "09:30"})
	if err := logCmd.Execute(); err == nil || !strings.Contains(err.Error(), "--description or --label") {
		t.Fatalf("log without description or label: %v", err)
	}
	logCmd = newLogTimeCmd(app)
	logCmd.SetArgs([]string{"--no-task", "--label", "admin,email", "--start", "09:00", "--end", "09:30"})
	if err := logCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := fake.logged[len(fake.logged)-1]; got != "-<nil>[map[name:admin] map[name:email]]" {
		t.Fatalf("logged = %q", got)
	}
}

func TestSwitchTimer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := &fakeTimerServer{}
	server := httptest.NewServer(fake)
	defer server.Close()
	app := &App{client: clickup.New("token", server.URL, "1"), CachePath: filepath.Join(t.TempDir(), "cache.json")}
	switchTo := func(target work, pausedIndex int) {
		t.Helper()
		queue, err := timerQueue(app)
		if err != nil {
			t.Fatal(err)
		}
		if err := switchTo(app, queue, target, target.name(), pausedIndex); err != nil {
			t.Fatal(err)
		}
	}
	task := func(ref string) work { return work{Task: &clickup.TimeEntryTask{ID: ref, CustomID: ref, Name: ref}} }

	// T-1 on ClickUp, T-2 local, T-3 paused with a label: switching to T-3 pauses both
	// (newest first) and resumes T-3 as it was paused.
	start := time.Now().Add(-time.Hour)
	fake.current = &clickup.TimeEntry{ID: "e", Task: &clickup.TimeEntryTask{ID: "T-1", CustomID: "T-1", Name: "T-1"},
		Start: clickup.FlexInt(start.UnixMilli()), Duration: -1}
	_ = saveLocalTimers([]localTimer{{workFields: workFields{TaskID: "T-2", CustomID: "T-2", Name: "T-2"}, Start: start.Add(time.Minute).UnixMilli()}})
	_ = savePausedTimers([]pausedTimer{{workFields: workFields{TaskID: "T-3", CustomID: "T-3", Name: "T-3", Tags: []string{"ops"}}}})
	switchTo(loadPausedTimers()[0].work(), 0)
	if got, paused := queueRefs(t, app), pausedRefs(); got != "T-3" || paused != "T-2,T-1" {
		t.Fatalf("after switch: queue %q, paused %q", got, paused)
	}
	if fmt.Sprint(fake.logged) != "[T-2 T-1]" || len(fake.current.Tags) != 1 {
		t.Fatalf("logged %v, current %+v", fake.logged, fake.current)
	}

	// Switching to the task already running pauses only the others.
	_ = saveLocalTimers([]localTimer{{workFields: workFields{TaskID: "T-4", CustomID: "T-4", Name: "T-4"}, Start: time.Now().UnixMilli()}})
	switchTo(task("T-3"), -1)
	if got, paused := queueRefs(t, app), pausedRefs(); got != "T-3" || paused != "T-2,T-1,T-4" {
		t.Fatalf("switch to running: queue %q, paused %q", got, paused)
	}

	// Time without a task; resume then brings back T-3, the last one paused.
	switchTo(work{Description: "support call", Tags: []string{"support"}}, -1)
	if got, paused := queueRefs(t, app), pausedRefs(); got != `"support call"` || paused != "T-2,T-1,T-4,T-3" {
		t.Fatalf("switch to no task: queue %q, paused %q", got, paused)
	}
}

func TestSameWork(t *testing.T) {
	a := work{Task: &clickup.TimeEntryTask{ID: "abc", CustomID: "TECH-1"}}
	if !sameWork(a, work{Task: &clickup.TimeEntryTask{ID: "TECH-1", CustomID: "tech-1"}}) || sameWork(a, work{Description: "x"}) ||
		!sameWork(work{Description: "x", Tags: []string{"a"}}, work{Description: "x", Tags: []string{"a"}}) ||
		sameWork(work{Description: "x"}, work{Description: "y"}) {
		t.Fatal("sameWork matched wrongly")
	}
}
