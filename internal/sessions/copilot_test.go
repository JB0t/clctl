package sessions

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copilotJSON is an older Copilot Chat session: the whole state in one object.
const copilotJSON = `{"version":3,"sessionId":"cj1","creationDate":1790000000000,"customTitle":"Fix the svc-ai build",
"requesterUsername":"me","inputState":{"inputText":"DRAFT INPUT"},
"requests":[{"timestamp":1790000000000,"modelId":"copilot/claude","message":{"text":"Why does the build fail? token=abc123secret","parts":[]},
"variableData":{"variables":[{"value":"ATTACHED FILE BODY"}]},
"result":{"timings":{"totalElapsed":60000},"metadata":{"toolCallResults":{"x":"TOOL RESULT BODY"}}},
"response":[
 {"kind":"thinking","value":"PRIVATE THOUGHTS"},
 {"value":"The build reads ","supportHtml":false},
 {"kind":"inlineReference","inlineReference":{"$mid":1,"path":"/home/u/workspace/svc-ai/Makefile","scheme":"file"}},
 {"value":" first."},
 {"kind":"toolInvocationSerialized","toolId":"copilot_readFile","invocationMessage":{"value":"Reading [](file:///home/u/workspace/svc-ai/build.sh#1-1)","uris":{"file:///home/u/workspace/svc-ai/build.sh#1-1":{}}},"resultDetails":{"output":"TOOL RESULT BODY"}},
 {"kind":"toolInvocationSerialized","toolId":"run_in_terminal","invocationMessage":"Running cat SECRET COMMAND","toolSpecificData":{"kind":"terminal","commandLine":{"original":"cd /home/u/workspace/ansible && echo SECRET COMMAND"}}},
 {"kind":"textEditGroup","uri":{"$mid":1,"fsPath":"/home/u/workspace/svc-ai/build.sh","path":"/home/u/workspace/svc-ai/build.sh","scheme":"vscode-remote"},"edits":[[{"text":"NEW BODY"}]]},
 {"kind":"confirmation","title":"Continue?","message":"CONFIRM BODY"}
]}]}`

// copilotLog is a newer session: a log of changes, replayed in order.
var copilotLog = strings.Join([]string{
	`{"kind":0,"v":{"version":3,"sessionId":"cl1","creationDate":1790100000000,"requests":[],"inputState":{"inputText":""}}}`,
	`{"kind":2,"k":["requests"],"v":[{"timestamp":1790100000000,"message":{"text":"Add a health check"},"response":[]}]}`,
	`{"kind":1,"k":["inputState","inputText"],"v":"DRAFT INPUT"}`,
	`{"kind":2,"k":["requests",0,"response"],"v":[{"kind":"thinking","value":"PRIVATE THOUGHTS"},{"value":"STALE TEXT"}]}`,
	// The reply is rewritten from index 1: STALE TEXT goes.
	`{"kind":2,"k":["requests",0,"response"],"i":1,"v":[{"value":"Added "},{"kind":"inlineReference","name":"healthCheck"},{"value":"."}]}`,
	`{"kind":2,"k":["requests",0,"response"],"v":[{"kind":"textEditGroup","uri":{"path":"/home/u/workspace/svc-mp/health.go"}}]}`,
	`{"kind":1,"k":["requests",0,"elapsedMs"],"v":120000}`,
	`not json`,
	`{"kind":9,"k":["requests",0],"v":"UNKNOWN KIND"}`,
	`{"kind":2,"k":["requests"],"v":[{"timestamp":1790100600000,"message":{"text":"Now add a readiness probe to svc-mp"},"response":[]}]}`,
	`{"kind":1,"k":["customTitle"],"v":"Health check for svc-mp"}`,
}, "\n") + "\n"

func writeCopilotFixtures(t *testing.T) string {
	t.Helper()
	user := filepath.Join(t.TempDir(), "Code", "User")
	write := func(name, text string) {
		path := filepath.Join(user, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspaceStorage/aaa/workspace.json", `{"folder":"vscode-remote://ssh-remote%2Bbox/home/u/workspace/svc-ai"}`)
	write("workspaceStorage/aaa/chatSessions/cj1.json", copilotJSON)
	write("globalStorage/emptyWindowChatSessions/cl1.jsonl", copilotLog)
	// The same session's older copy, from before VS Code kept a log: not read.
	write("globalStorage/emptyWindowChatSessions/cl1.json", strings.ReplaceAll(copilotJSON, `"cj1"`, `"cl1"`))
	// Other files in VS Code's storage are not transcripts, even Claude-shaped ones.
	write("globalStorage/some.extension/log.jsonl", `{"type":"user","sessionId":"x","message":{"role":"user","content":"NOT A SESSION"}}`+"\n")
	return user
}

func TestCopilotSessions(t *testing.T) {
	user := writeCopilotFixtures(t)
	found, err := ReadDir(user)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d sessions", len(found))
	}
	byID := map[string]*Session{}
	for _, session := range found {
		byID[session.ID] = session
		if session.Source != SourceCopilot {
			t.Errorf("%s: source %q", session.ID, session.Source)
		}
	}

	old := byID["cj1"]
	if old == nil || old.Title != "Fix the svc-ai build" || len(old.Turns) != 2 {
		t.Fatalf("cj1 = %+v", old)
	}
	if got := strings.Join(old.Repos, ","); got != "ansible,svc-ai" {
		t.Errorf("cj1 repos = %s", got)
	}
	if got := old.Last.Sub(old.First).Minutes(); got != 1 {
		t.Errorf("cj1 reply %v minutes after the prompt, want 1", got)
	}
	reply := old.Turns[1]
	if reply.Role != "assistant" || !strings.Contains(reply.Text, "build reads `Makefile` first.") {
		t.Errorf("cj1 reply text = %q", reply.Text)
	}
	if tools := strings.Join(reply.Tools, "; "); tools != "readFile ~/workspace/svc-ai/build.sh; run_in_terminal; Edit ~/workspace/svc-ai/build.sh" {
		t.Errorf("cj1 tools = %q", tools)
	}

	log := byID["cl1"]
	if log == nil || log.Title != "Health check for svc-mp" || len(log.Turns) != 3 {
		t.Fatalf("cl1 = %+v", log)
	}
	if text := log.Turns[1].Text; text != "Added `healthCheck`." {
		t.Errorf("cl1 reply = %q", text)
	}
	if tools := strings.Join(log.Turns[1].Tools, "; "); tools != "Edit ~/workspace/svc-mp/health.go" {
		t.Errorf("cl1 tools = %q", tools)
	}
	if got := log.Turns[1].Time.Sub(log.Turns[0].Time).Minutes(); got != 2 {
		t.Errorf("cl1 reply %v minutes after the prompt, want 2", got)
	}

	var all strings.Builder
	for _, session := range found {
		for _, chunk := range Chunks(session, 100000) {
			all.WriteString(chunk)
		}
	}
	text := all.String()
	for _, dropped := range []string{"abc123secret", "PRIVATE THOUGHTS", "TOOL RESULT BODY", "SECRET COMMAND", "NEW BODY", "CONFIRM BODY",
		"ATTACHED FILE BODY", "DRAFT INPUT", "STALE TEXT", "UNKNOWN KIND", "NOT A SESSION"} {
		if strings.Contains(text, dropped) {
			t.Errorf("%q reached the model text:\n%s", dropped, text)
		}
	}
	if !strings.Contains(text, "[REDACTED:") {
		t.Errorf("the token was not redacted:\n%s", text)
	}
}

func TestCopilotReducedRoundTrip(t *testing.T) {
	user := writeCopilotFixtures(t)
	path := filepath.Join(user, "workspaceStorage", "aaa", "chatSessions", "cj1.json")
	data, _ := os.ReadFile(path)
	var zipped bytes.Buffer
	kept, err := WriteReducedFile(path, bytes.NewReader(data), &zipped)
	if err != nil || kept != 3 { // prompt, reply, title
		t.Fatalf("kept %d, err %v", kept, err)
	}
	reader, _ := gzip.NewReader(&zipped)
	reduced, _ := io.ReadAll(reader)
	for _, line := range strings.Split(strings.TrimSpace(string(reduced)), "\n") {
		if !strings.Contains(line, `"source":"copilot"`) {
			t.Errorf("no source: %s", line)
		}
		var again bytes.Buffer
		if _, err := WriteReduced(strings.NewReader(line+"\n"), &again); err != nil {
			t.Fatal(err)
		}
		unzipped, _ := gzip.NewReader(&again)
		second, _ := io.ReadAll(unzipped)
		if strings.TrimSpace(string(second)) != line {
			t.Errorf("reducing again changed\n%s\nto\n%s", line, second)
		}
	}
	if name := ReducedName(path); name != "copilot-cj1.jsonl.gz" {
		t.Errorf("ReducedName = %s", name)
	}
	if name := ReducedName("/x/projects/-home/s1.jsonl"); name != "s1.jsonl.gz" {
		t.Errorf("ReducedName = %s", name)
	}
}
