package cmd

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/jira"
	"github.com/JB0t/clctl/internal/jira/jiratest"
	"github.com/JB0t/clctl/internal/local"
	"github.com/JB0t/clctl/internal/tracker"
)

// TestTimersAndTasksWorkTheSameOnEveryBackend runs one day's commands (start, pause,
// resume, stop, log, complete) against the local store and Jira, which have no running
// timer API of their own, and checks the same results.
func TestTimersAndTasksWorkTheSameOnEveryBackend(t *testing.T) {
	backends := map[string]func(t *testing.T) (tracker.Backend, string, string){
		"local": func(t *testing.T) (tracker.Backend, string, string) {
			store, err := local.Open(t.TempDir(), "Sam", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Write docs", "Review"} {
				if _, err := store.CreateTask("inbox", tracker.NewTask{Name: name, Assignees: []string{local.UserID}}); err != nil {
					t.Fatal(err)
				}
			}
			return store, "TASK-1", "TASK-2"
		},
		"jira": func(t *testing.T) (tracker.Backend, string, string) {
			server := httptest.NewServer(jiratest.New(t, true))
			t.Cleanup(server.Close)
			return jira.New(jira.Options{URL: server.URL, Email: "sam@example.com", Token: "token", Projects: []string{"OPS"},
				TimerPath: filepath.Join(t.TempDir(), "timer.json")}), "OPS-1", "OPS-2"
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			backend, first, second := open(t)
			dir := t.TempDir()
			app := &App{client: backend, ConfigPath: filepath.Join(dir, "config"), CachePath: filepath.Join(dir, "cache.json")}
			run := func(command func(*App) *cobra.Command, args ...string) {
				t.Helper()
				cmd := command(app)
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatalf("%s %v: %v", cmd.Name(), args, err)
				}
			}

			run(newStartTimerCmd, "--task-id", first)
			run(newStartTimerCmd, "--task-id", first) // already running: no change
			if got := queueRefs(t, app); got != first {
				t.Fatalf("after start: queue = %q", got)
			}
			run(newPauseTimerCmd)
			if got, paused := queueRefs(t, app), pausedRefs(); got != "" || paused != first {
				t.Fatalf("after pause: queue %q, paused %q", got, paused)
			}
			run(newResumeTimerCmd)
			if got, paused := queueRefs(t, app), pausedRefs(); got != first || paused != "" {
				t.Fatalf("after resume: queue %q, paused %q", got, paused)
			}
			run(newStopTimerCmd)
			if got := queueRefs(t, app); got != "" {
				t.Fatalf("after stop: queue = %q", got)
			}
			run(newLogTimeCmd, "--task-id", second, "--start", "09:00", "--end", "10:30", "--description", "review")

			day := time.Now()
			entries, err := backend.TimeEntries(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			byTask := map[string]int{}
			for _, entry := range entries {
				byTask[entry.Task.Ref()]++
			}
			// pause and stop each logged the first task's time; log added the second's.
			if byTask[first] != 2 || byTask[second] != 1 {
				t.Fatalf("time entries by task = %v", byTask)
			}

			run(newCompleteCmd, "--task-id", first)
			task, err := backend.Task(first)
			if err != nil || !task.Status.Done() {
				t.Fatalf("completed task status = %+v, %v", task.Status, err)
			}
			cache, err := app.Cache()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cache.Find(first); ok {
				t.Fatalf("a completed task stays in the cache")
			}
		})
	}
}

func TestInitSetsUpTheLocalBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLICKUP_API_TOKEN", "")
	app := &App{ConfigPath: filepath.Join(dir, "config"), CachePath: filepath.Join(dir, "cache.json")}
	options := initOptions{backend: "local", localDir: filepath.Join(dir, "tasks"), name: "Sam", prefix: "home", noNotes: true}
	if err := runInit(app, options); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend() != "local" || cfg.LocalDir() != options.localDir || cfg.Get(config.KeyUserName) != "Sam" {
		t.Fatalf("saved config: backend %q, dir %q", cfg.Backend(), cfg.LocalDir())
	}
	// A fresh App reads the saved config and creates tasks with the chosen prefix.
	fresh := &App{ConfigPath: app.ConfigPath, CachePath: app.CachePath}
	backend, err := fresh.Backend()
	if err != nil {
		t.Fatal(err)
	}
	created, err := createTask(fresh, "inbox", tracker.NewTask{Name: "First", Assignees: []string{local.UserID}})
	if err != nil || created.Ref() != "HOME-1" || backend.Kind() != "local" {
		t.Fatalf("created %+v, %v", created, err)
	}
	if me, _ := fresh.UserID(); me != local.UserID {
		t.Fatalf("user ID = %q", me)
	}
}
