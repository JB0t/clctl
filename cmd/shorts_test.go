package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/clickup"
)

func TestActionShorts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newRoot(&App{})
	for _, group := range root.Commands() {
		if !group.IsAvailableCommand() {
			continue
		}
		letters := map[string]string{}
		for _, action := range group.Commands() {
			if !action.IsAvailableCommand() || !action.Runnable() {
				continue
			}
			path := group.Name() + " " + action.Name()
			letter, ok := actionShorts[group.Name()][action.Name()]
			if !ok {
				if !slices.Contains(noActionShort, path) {
					t.Errorf("%s has no action short and is not in noActionShort", path)
				}
				continue
			}
			if strings.Contains("hrv", letter) || letters[letter] != "" {
				t.Errorf("%s: -%s is taken (by %q, or help/refresh/version)", path, letter, letters[letter])
			}
			letters[letter] = path
			if flag := group.Flags().ShorthandLookup(letter); flag == nil || flag.Name != action.Name() {
				t.Errorf("%s: -%s is not registered on %s", path, letter, group.Name())
			}
		}
	}
	for name, short := range commandShorts {
		if subcommand(root, name) == nil {
			continue // notes, when notes are off
		}
		if found, _, err := root.Find([]string{short}); err != nil || found.Name() != name {
			t.Errorf("%s does not resolve to %s: %v", short, name, err)
		}
	}
}

func TestRewriteActionFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newRoot(&App{})
	for line, want := range map[string]string{
		"t -l --assignee jo":            "t list --assignee jo",
		"tasks --update --task-id X":    "tasks update --task-id X",
		"-r ti -s":                      "-r ti start",
		"--config /c t -c":              "--config /c t create",
		"sessions --export":             "sessions export",
		"sessions -i":                   "sessions init",
		"t list -l":                     "t list -l",      // a leaf's own flags are left alone
		"t -h":                          "t -h",           // help stays help
		"timers -l -s":                  "timers list -s", // only the first is an action
		"-- t -l":                       "-- t -l",
		"__complete t -l --as":          "__complete t list --as",
		"__complete t -l":               "__complete t -l", // the word being completed
		"__completeNoDesc ti -e --date": "__completeNoDesc ti edit --date",
	} {
		if got := strings.Join(rewriteActionFlags(root, splitArgs(line)), " "); got != want {
			t.Errorf("rewrite(%q) = %q, want %q", line, got, want)
		}
	}
}

func complete(t *testing.T, line string) string {
	t.Helper()
	root := newRoot(&App{})
	var out bytes.Buffer
	root.SetOut(&out)
	args := splitArgs(line)
	if strings.HasSuffix(line, " ") {
		args = append(args, "")
	}
	root.SetArgs(rewriteActionFlags(root, append([]string{"__complete"}, args...)))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestShortCompletion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := complete(t, "t")
	for _, want := range []string{"tasks\t", "timers\t", "t\ttasks: ", "ti\ttimers: "} {
		if !strings.Contains(got, want) {
			t.Errorf("completing t lacks %q:\n%s", want, got)
		}
	}
	if got := complete(t, "t -"); !strings.Contains(got, "-l\t") || !strings.Contains(got, "--update\t") {
		t.Errorf("completing t - lacks the action flags:\n%s", got)
	}
	if got := complete(t, "t -l --"); !strings.Contains(got, "--assignee") {
		t.Errorf("completing t -l -- lacks list's flags:\n%s", got)
	}
}

// fakeUpdateClickUp serves task T-1 for tasks update and records the writes.
type fakeUpdateClickUp struct {
	mu     sync.Mutex
	writes []string
	bodies []map[string]any
}

func (f *fakeUpdateClickUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/task/T-1":
		reply(map[string]any{"id": "t1", "custom_id": "T-1", "name": "Build", "status": map[string]any{"status": "to do"},
			"list": map[string]any{"id": "L1"}, "priority": map[string]any{"priority": "low"}, "markdown_description": "old",
			"tags": []any{map[string]any{"name": "old"}, map[string]any{"name": "keep"}}, "assignees": []any{map[string]any{"id": 5}}})
	case r.Method == http.MethodGet && r.URL.Path == "/list/L1":
		reply(map[string]any{"name": "Work", "statuses": []any{map[string]any{"status": "to do"}, map[string]any{"status": "In Progress"}}})
	case r.Method == http.MethodGet && r.URL.Path == "/team":
		reply(map[string]any{"teams": []any{map[string]any{"id": "1", "members": []any{
			map[string]any{"user": map[string]any{"id": 5, "username": "me"}}, map[string]any{"user": map[string]any{"id": 7, "username": "jo"}}}}}})
	case r.Method != http.MethodGet:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, body)
		reply(map[string]any{})
	default:
		reply(map[string]any{})
	}
}

func TestUpdateTask(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_STATE_HOME", home)
	fake := &fakeUpdateClickUp{}
	server := httptest.NewServer(fake)
	defer server.Close()
	app := &App{client: clickup.New("token", server.URL, "1"), CachePath: filepath.Join(home, "cache.json")}
	cmd := newUpdateTaskCmd(app)
	cmd.SetArgs([]string{"--task-id", "T-1", "--status", "in progress", "--priority", "high", "--due", "2026-10-31",
		"--add-tag", "Infra", "--remove-tag", "old", "--assignee", "jo", "--unassign", "me", "--name", "Build it", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "PUT /task/T-1,POST /task/T-1/tag/infra,DELETE /task/T-1/tag/old"
	if got := strings.Join(fake.writes, ","); got != want {
		t.Fatalf("writes = %s, want %s", got, want)
	}
	body := fake.bodies[0]
	due, _ := time.ParseInLocation("2006-01-02", "2026-10-31", time.Local)
	if body["status"] != "In Progress" || body["priority"] != float64(2) || body["name"] != "Build it" ||
		body["due_date"] != float64(due.UnixMilli()) || body["markdown_content"] != nil {
		t.Fatalf("update body = %v", body)
	}
	if assignees, _ := json.Marshal(body["assignees"]); string(assignees) != `{"add":[7],"rem":[5]}` {
		t.Fatalf("assignees = %s", assignees)
	}

	// --tag sets the list; --priority none clears; an unknown status is an error.
	fake.writes, fake.bodies = nil, nil
	cmd = newUpdateTaskCmd(app)
	cmd.SetArgs([]string{"--task-id", "T-1", "--tag", "keep,new", "--priority", "none", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.writes, ","); got != "PUT /task/T-1,POST /task/T-1/tag/new,DELETE /task/T-1/tag/old" {
		t.Fatalf("writes = %s", got)
	}
	if value, ok := fake.bodies[0]["priority"]; !ok || value != nil {
		t.Fatalf("priority not cleared: %v", fake.bodies[0])
	}
	cmd = newUpdateTaskCmd(app)
	cmd.SetArgs([]string{"--task-id", "T-1", "--status", "nope", "--yes"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "In Progress") {
		t.Fatalf("unknown status: %v", err)
	}
}

func TestParseDue(t *testing.T) {
	now := time.Date(2026, 10, 16, 15, 4, 0, 0, time.Local)
	for text, want := range map[string]string{"today": "2026-10-16", "tomorrow": "2026-10-17", "+3d": "2026-10-19", "2026-12-01": "2026-12-01"} {
		got, err := parseDue(text, now)
		if err != nil || time.UnixMilli(got).Format("2006-01-02") != want {
			t.Errorf("parseDue(%q) = %v, %v", text, got, err)
		}
	}
	if got, err := parseDue("none", now); got != 0 || err != nil {
		t.Errorf("parseDue(none) = %v, %v", got, err)
	}
	if _, err := parseDue("soon", now); err == nil {
		t.Error("parseDue(soon) did not fail")
	}
}
