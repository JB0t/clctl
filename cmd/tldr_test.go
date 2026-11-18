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

	"github.com/JB0t/clctl/internal/clickup"
	"github.com/JB0t/clctl/internal/markdown"
)

// fakeTLDRClickUp serves one task (T-1, id t1) and the summary subtask clctl creates.
type fakeTLDRClickUp struct {
	mu       sync.Mutex
	created  []map[string]any // create-task bodies
	comments []clickup.Comment
	updated  []string // comment IDs updated
}

func (f *fakeTLDRClickUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/task/T-1" && r.URL.Query().Get("include_subtasks") == "true":
		var subtasks []clickup.Task
		if len(f.created) > 0 {
			subtasks = append(subtasks, clickup.Task{ID: "s1", Name: "summary", Parent: "t1"})
		}
		reply(map[string]any{"subtasks": subtasks})
	case r.Method == http.MethodGet && r.URL.Path == "/task/T-1":
		reply(clickup.Task{ID: "t1", CustomID: "T-1", Name: "Build the thing", List: clickup.Ref{ID: "L1"}})
	case r.Method == http.MethodPost && r.URL.Path == "/list/L1/task":
		f.created = append(f.created, body)
		reply(clickup.Task{ID: "s1", Name: "summary", Parent: "t1"})
	case r.Method == http.MethodGet && r.URL.Path == "/task/s1/comment":
		newestFirst := make([]clickup.Comment, len(f.comments))
		for index, comment := range f.comments {
			newestFirst[len(f.comments)-1-index] = comment
		}
		reply(map[string]any{"comments": newestFirst})
	case r.Method == http.MethodPost && r.URL.Path == "/task/s1/comment":
		id := fmt.Sprintf("c%d", len(f.comments)+1)
		f.comments = append(f.comments, clickup.Comment{ID: clickup.FlexString(id), CommentText: commentText(body)})
		reply(map[string]any{"id": id})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/comment/"):
		id := strings.TrimPrefix(r.URL.Path, "/comment/")
		f.updated = append(f.updated, id)
		for index := range f.comments {
			if string(f.comments[index].ID) == id {
				f.comments[index].CommentText = commentText(body)
			}
		}
		reply(map[string]any{})
	default:
		reply(map[string]any{}) // cache refresh and the like: nothing there
	}
}

func commentText(body map[string]any) string {
	var builder strings.Builder
	ops, _ := body["comment"].([]any)
	for _, op := range ops {
		builder.WriteString(fmt.Sprint(op.(map[string]any)["text"]))
	}
	return builder.String()
}

// fakeModel answers every chat with a numbered tl;dr and keeps the prompts it got.
type fakeModel struct {
	mu      sync.Mutex
	prompts []string
}

func (m *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/models") {
		_, _ = w.Write([]byte(`{"data":[{"id":"fake"}]}`))
		return
	}
	var body struct {
		Messages []struct{ Content string } `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.prompts = append(m.prompts, body.Messages[len(body.Messages)-1].Content)
	answer := fmt.Sprintf("Answer %d stands.\n- point %d", len(m.prompts), len(m.prompts))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": answer}, "finish_reason": "stop"}},
	})
}

func TestNotesTLDR(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	_ = os.MkdirAll(filepath.Join(home, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "config", "clctl"), []byte("[clickup]\nnotes_enabled = true\nnotes_dir = "+root+"\n"), 0o644)

	clickUp, model := &fakeTLDRClickUp{}, &fakeModel{}
	clickUpServer, modelServer := httptest.NewServer(clickUp), httptest.NewServer(model)
	defer clickUpServer.Close()
	defer modelServer.Close()
	app := &App{client: clickup.New("token", clickUpServer.URL, "1"), CachePath: filepath.Join(home, "cache.json")}

	// Two days of notes; push records cover sections A, C and (later) D, not B.
	write := func(day, text string) string {
		path := filepath.Join(root, strings.ReplaceAll(day, "-", string(filepath.Separator)), "S", "L", "T-1.md")
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(text), 0o644)
		return path
	}
	push := func(day, text string, pushed ...string) {
		parsed := markdown.Sections(strings.Split(strings.TrimSuffix(text, "\n"), "\n"), nil, true)
		state := loadPushState()
		if state["T-1"] == nil {
			state["T-1"] = map[string]map[string]*pushRecord{}
		}
		records := map[string]*pushRecord{}
		for index, key := range sectionKeys(parsed) {
			for _, name := range pushed {
				if key == name {
					records[key] = &pushRecord{CommentID: "x", Hash: parsed[index].Fingerprint()}
				}
			}
		}
		state["T-1"][day] = records
		if err := state.save(); err != nil {
			t.Fatal(err)
		}
	}
	day1 := "# T-1\n\n## A\n\nfirst pushed\n\n## B\n\nnot pushed yet\n"
	write("2026-10-08", day1)
	push("2026-10-08", day1, "A")
	day2 := "# T-1\n\n## C\n\nsecond day\n"
	write("2026-10-09", day2)
	push("2026-10-09", day2, "C")

	from, _ := time.ParseInLocation("2006-01-02", "2026-10-01", time.Local)
	run := func() {
		t.Helper()
		if err := writeTLDRs(app, root, "", from, "", modelServer.URL+"/v1", false, false, tldrPace{}); err != nil {
			t.Fatal(err)
		}
	}
	// --limit 1: only the oldest day now; the next run does the rest.
	if err := writeTLDRs(app, root, "", from, "", modelServer.URL+"/v1", false, false, tldrPace{limit: 1, pause: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if len(clickUp.comments) != 1 || !strings.Contains(clickUp.comments[0].CommentText, "tl;dr 2026-10-08") {
		t.Fatalf("limited run: comments = %+v", clickUp.comments)
	}
	run()
	if len(clickUp.created) != 1 || clickUp.created[0]["parent"] != "t1" || clickUp.created[0]["name"] != "summary" {
		t.Fatalf("created = %v", clickUp.created)
	}
	if len(clickUp.comments) != 2 || !strings.Contains(clickUp.comments[0].CommentText, "tl;dr 2026-10-08") ||
		!strings.Contains(clickUp.comments[1].CommentText, "Answer 2 stands.") {
		t.Fatalf("comments = %+v", clickUp.comments)
	}
	if len(model.prompts) != 2 || !strings.Contains(model.prompts[0], "first pushed") || strings.Contains(model.prompts[0], "not pushed yet") {
		t.Fatalf("first prompt = %q", model.prompts)
	}
	if !strings.Contains(model.prompts[1], "Answer 1 stands.") || !strings.Contains(model.prompts[1], "second day") {
		t.Fatalf("second prompt lacks the earlier tl;dr or its notes: %q", model.prompts[1])
	}

	// Nothing new: no model call, no comment.
	run()
	if len(model.prompts) != 2 || len(clickUp.comments) != 2 {
		t.Fatalf("re-run did work: %d prompts, %d comments", len(model.prompts), len(clickUp.comments))
	}

	// More notes pushed for day 2: its comment is updated, with day 1's tl;dr as context.
	day2 += "\n## D\n\nlater that day\n"
	write("2026-10-09", day2)
	push("2026-10-09", day2, "C", "D")
	run()
	if len(clickUp.comments) != 2 || fmt.Sprint(clickUp.updated) != "[c2]" || !strings.Contains(clickUp.comments[1].CommentText, "Answer 3 stands.") {
		t.Fatalf("update: comments %+v, updated %v", clickUp.comments, clickUp.updated)
	}
	if last := model.prompts[2]; !strings.Contains(last, "Answer 1 stands.") || strings.Contains(last, "Answer 2 stands.") || !strings.Contains(last, "later that day") {
		t.Fatalf("update prompt = %q", last)
	}

	// Rewriting day 1 (--again) must not take day 2's tl;dr as an earlier one.
	if err := writeTLDRs(app, root, "T-1", from, "", modelServer.URL+"/v1", false, true, tldrPace{}); err != nil {
		t.Fatal(err)
	}
	// Both prompts match earlier ones exactly, so they come from the summary cache: day 1's
	// had no earlier tl;dr then either. A later tl;dr in it would have meant a model call.
	if len(model.prompts) != 3 || fmt.Sprint(clickUp.updated) != "[c2 c1 c2]" {
		t.Fatalf("rewrite: %d prompts, updated %v", len(model.prompts), clickUp.updated)
	}
}

func TestDigestSkipsTLDRSubtasks(t *testing.T) {
	if !isTLDRSubtask(clickup.Task{Name: " Summary ", Parent: "t1"}) || isTLDRSubtask(clickup.Task{Name: "summary"}) {
		t.Fatal("isTLDRSubtask matched wrongly")
	}
}
