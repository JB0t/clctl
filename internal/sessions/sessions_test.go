package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transcript has one line of each kind the digest must keep or drop.
const transcript = `{"type":"permission-mode","permissionMode":"auto","sessionId":"s1"}
{"type":"user","sessionId":"s1","timestamp":"2026-09-17T19:34:46.034Z","cwd":"/home/u/workspace/svc-mp","message":{"role":"user","content":"Deploy failed, password=hunter2 <system-reminder>injected context</system-reminder>"}}
{"type":"attachment","sessionId":"s1","timestamp":"2026-09-17T19:34:47Z","attachment":{"content":"ATTACHED FILE BODY"}}
{"type":"assistant","sessionId":"s1","timestamp":"2026-09-17T19:35:00Z","cwd":"/home/u/workspace/svc-mp","message":{"role":"assistant","content":[{"type":"thinking","thinking":"PRIVATE THOUGHTS"},{"type":"text","text":"Reading the playbook."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/home/u/workspace/ansible/deploy-svc-mp.yml"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"cat /home/u/workspace/svc-ai/x && curl -H 'Authorization: Bearer abc'","description":"Show svc-ai file"}},{"type":"tool_use","id":"t3","name":"Edit","input":{"file_path":"/home/u/workspace/svc-mp/start.sh","old_string":"OLD BODY","new_string":"NEW BODY"}}]}}
{"type":"user","sessionId":"s1","timestamp":"2026-09-17T19:35:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"TOOL RESULT BODY"}]}}
{"type":"system","sessionId":"s1","timestamp":"2026-09-17T19:36:00Z","subtype":"stop_hook_summary"}
not json at all
{"type":"user","isMeta":true,"sessionId":"s1","timestamp":"2026-09-17T19:37:00Z","message":{"role":"user","content":"META CAVEAT"}}
{"type":"ai-title","aiTitle":"Fix the svc-mp deploy","sessionId":"s1"}
{"type":"assistant","sessionId":"s1","timestamp":"2026-09-17T21:17:40.043Z","message":{"role":"assistant","content":[{"type":"text","text":"Fixed; see ~/.claude/projects/-home-u-workspace/memory and /home/u/workspace/SOUL.md"}]}}
{"type":"file-history-snapshot","messageId":"m","snapshot":{"trackedFileBackups":{"a":"FILE HISTORY BODY"}}}
{"type":"queue-operation","operation":"enqueue","sessionId":"s1","content":"QUEUED"}
`

func TestParse(t *testing.T) {
	sessions, err := Parse(strings.NewReader(transcript), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	session := sessions[0]
	if session.ID != "s1" || session.Title != "Fix the svc-mp deploy" {
		t.Errorf("id %q title %q", session.ID, session.Title)
	}
	if got := session.First.Format("15:04:05"); got != "19:34:46" {
		t.Errorf("first = %s", got)
	}
	if got := session.Last.Format("15:04:05"); got != "21:17:40" {
		t.Errorf("last = %s", got)
	}
	if got := strings.Join(session.Repos, ","); got != "ansible,svc-ai,svc-mp" {
		t.Errorf("repos = %s", got)
	}
	if len(session.Turns) != 3 { // the assistant's text and tool calls are one message
		t.Fatalf("got %d turns: %+v", len(session.Turns), session.Turns)
	}
	if tools := strings.Join(session.Turns[1].Tools, "; "); tools != "Read ~/workspace/ansible/deploy-svc-mp.yml; Bash (Show svc-ai file); Edit ~/workspace/svc-mp/start.sh" {
		t.Errorf("tools = %q", tools)
	}
	var all strings.Builder
	for _, chunk := range Chunks(session, 100000) {
		all.WriteString(chunk)
	}
	text := all.String()
	for _, dropped := range []string{"hunter2", "injected context", "ATTACHED FILE BODY", "PRIVATE THOUGHTS", "TOOL RESULT BODY",
		"OLD BODY", "NEW BODY", "Bearer", "META CAVEAT", "FILE HISTORY BODY", "QUEUED", "stop_hook_summary"} {
		if strings.Contains(text, dropped) {
			t.Errorf("%q reached the model text:\n%s", dropped, text)
		}
	}
	for _, kept := range []string{"[2026-09-17 19:34 user]", "Deploy failed", "password=[REDACTED:secret]", "Reading", "playbook", "[2026-09-17 21:17 assistant]"} {
		if !strings.Contains(text, kept) {
			t.Errorf("%q missing from:\n%s", kept, text)
		}
	}
}

func TestChunks(t *testing.T) {
	session := &Session{ID: "s"}
	for range 10 {
		session.Turns = append(session.Turns, Turn{Role: "user", Text: strings.Repeat("word ", 40)}) // ~230 characters rendered
	}
	session.Turns = append(session.Turns, Turn{Role: "assistant", Text: strings.Repeat("é line\n", 300)}) // longer than the budget
	chunks := Chunks(session, 1000)
	if len(chunks) < 4 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	total := 0
	for index, chunk := range chunks {
		if len(chunk) > 1000 {
			t.Errorf("chunk %d has %d characters", index, len(chunk))
		}
		if !strings.HasPrefix(chunk, "[") && !strings.HasPrefix(chunk, "é") {
			t.Errorf("chunk %d does not start on a message or line: %q", index, chunk[:20])
		}
		total += strings.Count(chunk, "word")
	}
	if total != 400 {
		t.Errorf("lost words: %d", total)
	}
}

func TestReadDirNewestFirst(t *testing.T) {
	dir := t.TempDir()
	older := `{"type":"user","sessionId":"old","timestamp":"2026-09-01T10:00:00Z","message":{"role":"user","content":"a"}}` + "\n"
	newer := `{"type":"user","sessionId":"new","timestamp":"2026-09-02T10:00:00Z","message":{"role":"user","content":"b"}}` // no final newline
	empty := `{"type":"ai-title","aiTitle":"nothing said","sessionId":"empty"}` + "\n"
	for name, body := range map[string]string{"a.jsonl": older, "b.jsonl": newer, "c.jsonl": empty, "notes.txt": older} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].ID != "new" || sessions[1].ID != "old" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if _, err := ReadDir(t.TempDir()); err == nil {
		t.Error("an empty directory should be an error")
	}
}

func TestLongLine(t *testing.T) {
	body := strings.Repeat("x", 3<<20) // a 3 MB tool result, past the reader's buffer
	text := `{"type":"user","sessionId":"s","timestamp":"2026-09-01T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","content":"` + body + `"}]}}` + "\n" +
		`{"type":"user","sessionId":"s","timestamp":"2026-09-01T10:01:00Z","message":{"role":"user","content":"after"}}` + "\n"
	sessions, err := Parse(strings.NewReader(text), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 1 || sessions[0].Turns[0].Text != "after" {
		t.Fatalf("sessions = %+v", sessions)
	}
}

func TestMergeAssistantEntries(t *testing.T) {
	lines := `{"type":"assistant","sessionId":"s","timestamp":"2026-09-01T10:00:00Z","message":{"content":[{"type":"text","text":"Checking."}]}}
{"type":"assistant","sessionId":"s","timestamp":"2026-09-01T10:00:30Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a"}}]}}
{"type":"assistant","sessionId":"s","timestamp":"2026-09-01T10:02:00Z","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/b"}}]}}
{"type":"assistant","sessionId":"s","timestamp":"2026-09-01T10:30:00Z","message":{"content":[{"type":"text","text":"Later."}]}}
{"type":"user","sessionId":"s","timestamp":"2026-09-01T10:30:10Z","message":{"content":"ok"}}
`
	sessions, err := Parse(strings.NewReader(lines), "s")
	if err != nil {
		t.Fatal(err)
	}
	turns := sessions[0].Turns
	if len(turns) != 3 || strings.Join(turns[0].Tools, ",") != "Read /a,Read /b" || turns[1].Text != "Later." {
		t.Fatalf("turns = %+v", turns)
	}
}

// A line longer than the read buffer (1 MiB) is read whole, not corrupted.
func TestParseLongLine(t *testing.T) {
	long := `{"type":"assistant","sessionId":"s1","timestamp":"2026-09-17T19:35:00Z","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","name":"Write","input":{"file_path":"/home/u/workspace/big/x.txt","content":"` + strings.Repeat("x", 3<<20) + `"}}]}}`
	short := `{"type":"user","sessionId":"s1","timestamp":"2026-09-17T19:36:00Z","message":{"role":"user","content":"thanks, now deploy it"}}`
	found, err := Parse(strings.NewReader(long+"\n"+short+"\n"), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || len(found[0].Turns) != 2 || strings.Join(found[0].Turns[0].Tools, "") != "Write ~/workspace/big/x.txt" {
		t.Fatalf("got %+v", found)
	}
}
