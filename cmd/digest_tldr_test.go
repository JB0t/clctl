package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/clickup"
	"github.com/JB0t/clctl/internal/markdown"
)

// tldrComment is a comment as clctl notes tldr posts it: rich text from Markdown.
func tldrComment(id, day, text string) clickup.Comment {
	body := fmt.Sprintf("## tl;dr %s\n\n%s\n\n_From the notes pushed for %s; written by clctl notes tldr (m)._", day, text, day)
	ops := markdown.Sections(strings.Split(body, "\n"), nil, false)[0].Comment
	var rich []map[string]any
	data, _ := json.Marshal(ops)
	_ = json.Unmarshal(data, &rich)
	return clickup.Comment{ID: clickup.FlexString(id), Comment: rich, CommentText: body}
}

func TestParseAndWindowTLDRs(t *testing.T) {
	tldr, ok := parseTLDRComment(tldrComment("c1", "2026-10-14", "Certs rotated on prod.\n- staging next"))
	if !ok || tldr.day != "2026-10-14" || tldr.text != "Certs rotated on prod.\n- staging next" {
		t.Fatalf("parse = %+v, %v", tldr, ok)
	}
	if _, ok := parseTLDRComment(clickup.Comment{CommentText: "just a comment"}); ok {
		t.Fatal("a plain comment parsed as a tl;dr")
	}
	day := func(text string) time.Time {
		parsed, _ := time.ParseInLocation("2006-01-02", text, time.Local)
		return parsed
	}
	comments := []clickup.Comment{ // newest first, as ClickUp sends them
		tldrComment("c4", "2026-10-15", "newest"),
		tldrComment("c3", "2026-10-13", "rewritten"),
		tldrComment("c2", "2026-10-13", "first take"),
		tldrComment("c1", "2026-10-07", "before the window"),
		{ID: "x", CommentText: "chatter"},
	}
	got := windowTLDRs(comments, day("2026-10-08"), day("2026-10-15"))
	if len(got) != 2 || got[0].day != "2026-10-13" || got[0].text != "rewritten" || got[1].day != "2026-10-15" {
		t.Fatalf("windowTLDRs = %+v", got)
	}
	if source := tldrSource(got); source != "from 2 tl;drs, 2026-10-13 to 2026-10-15" {
		t.Fatalf("tldrSource = %q", source)
	}
}

func TestDigestFromTLDRs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	model := &fakeModel{}
	modelServer := httptest.NewServer(model)
	defer modelServer.Close()
	_ = os.MkdirAll(filepath.Join(home, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "config", "clctl"), []byte("[clickup]\nnotes_enabled = false\nllm_url = "+modelServer.URL+"/v1\n"), 0o644)

	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	// T-1 has two tl;drs in the window, T-2 one, T-3 no summary subtask.
	clickUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.URL.Path {
		case "/task/t1":
			reply(map[string]any{"subtasks": []any{map[string]any{"id": "s1", "name": "summary", "parent": "t1"}}})
		case "/task/t2":
			reply(map[string]any{"subtasks": []any{map[string]any{"id": "s2", "name": "Summary", "parent": "t2"}}})
		case "/task/t3":
			reply(map[string]any{"subtasks": []any{map[string]any{"id": "x3", "name": "design", "parent": "t3"}}})
		case "/task/s1/comment":
			reply(map[string]any{"comments": []clickup.Comment{tldrComment("a2", today, "Deployed."), tldrComment("a1", yesterday, "Built.")}})
		case "/task/s2/comment":
			reply(map[string]any{"comments": []clickup.Comment{tldrComment("b1", today, "Only one.\n- point")}})
		default:
			reply(map[string]any{})
		}
	}))
	defer clickUp.Close()
	app := &App{client: clickup.New("token", clickUp.URL, "1"), CachePath: filepath.Join(home, "cache.json")}
	now := fmt.Sprint(time.Now().UnixMilli())
	comment := clickup.Comment{ID: "k", CommentText: "Did the thing on the task.", Date: clickup.FlexString(now)}
	entries := []*digestEntry{
		{task: clickup.Task{ID: "t1", CustomID: "T-1", Name: "one"}, comments: []clickup.Comment{comment}, latest: time.Now()},
		{task: clickup.Task{ID: "t2", CustomID: "T-2", Name: "two"}, comments: []clickup.Comment{comment}, latest: time.Now()},
		{task: clickup.Task{ID: "t3", CustomID: "T-3", Name: "three"}, comments: []clickup.Comment{comment}, latest: time.Now()},
	}
	from := time.Now().AddDate(0, 0, -7)
	addTLDRs(app, entries, from, time.Time{})
	if len(entries[0].tldrs) != 2 || len(entries[1].tldrs) != 1 || len(entries[2].tldrs) != 0 {
		t.Fatalf("tl;drs: %d %d %d", len(entries[0].tldrs), len(entries[1].tldrs), len(entries[2].tldrs))
	}
	s, err := newSummarizer(app, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := summarizeEntries(s, entries); err != nil {
		t.Fatal(err)
	}
	// T-1: one model call merging both tl;drs; T-2: its tl;dr as it is; T-3: its comment.
	if len(model.prompts) != 2 || !strings.Contains(model.prompts[0], "Built.") || !strings.Contains(model.prompts[0], "Deployed.") ||
		!strings.Contains(model.prompts[1], "Did thing") {
		t.Fatalf("prompts = %q", model.prompts)
	}
	if entries[1].summary != "Only one.\n- point" {
		t.Fatalf("T-2 summary = %q", entries[1].summary)
	}
	text := renderDigest(entries, from, time.Time{}, 5, "m")
	for _, want := range []string{
		"**Summary** (from 2 tl;drs, " + yesterday + " to " + today + ")\n\nAnswer 1",
		"**Summary** (from the tl;dr of " + today + ")\n\nOnly one.\n- point\n\n**Comments**",
		"daily tl;drs (2 tasks)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("digest lacks %q:\n%s", want, text)
		}
	}
	// The next meeting reads these summaries back as its previous ones.
	if previous := parseDigestSummaries(text); previous["T-2"] != "Only one.\n- point" || !strings.HasPrefix(previous["T-1"], "Answer 1") {
		t.Fatalf("parseDigestSummaries = %q", previous)
	}
}
