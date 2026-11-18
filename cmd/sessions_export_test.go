package cmd

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/webdav/webdavtest"
)

const exportLine = `{"type":"user","sessionId":"s1","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"deploy svc-mp, password=hunter2"}}` + "\n"
const exportToolResult = `{"type":"user","sessionId":"s1","timestamp":"2026-10-01T10:00:05Z","message":{"role":"user","content":[{"type":"tool_result","content":"FILE BODY"}]}}` + "\n"

type exportFixture struct {
	t        *testing.T
	stub     *webdavtest.Stub
	dir      string
	state    string
	env      map[string]string
	out      bytes.Buffer
	since    time.Time
	dryRun   bool
	projects string
	extra    []string // more DIRs, after projects
}

func newExportFixture(t *testing.T) *exportFixture {
	t.Helper()
	root := t.TempDir()
	f := &exportFixture{t: t, stub: webdavtest.NewStub(t, "data-bot", "s3cret",
		"/remote.php", "/remote.php/dav", "/remote.php/dav/files", "/remote.php/dav/files/data-bot", "/remote.php/dav/files/data-bot/Shared"), dir: root,
		projects: filepath.Join(root, "projects"), state: filepath.Join(root, "state", "sessions-export.json"),
		since: time.Now().AddDate(0, 0, -30)}
	passFile := filepath.Join(root, "pass")
	if err := os.WriteFile(passFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = map[string]string{"NC_USER": "data-bot", "NC_PASS_FILE": passFile}
	f.write("-home-jo-workspace/s1.jsonl", exportLine+exportToolResult)
	return f
}

func (f *exportFixture) write(name, text string) string {
	f.t.Helper()
	path := filepath.Join(f.projects, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *exportFixture) run() error {
	f.out.Reset()
	return runSessionsExport(exportOptions{dirs: append([]string{f.projects}, f.extra...), user: "jo", server: f.stub.URL, since: f.since, dryRun: f.dryRun,
		statePath: f.state, getenv: func(key string) string { return f.env[key] }, out: &f.out})
}

func (f *exportFixture) uploaded(name string) string {
	f.t.Helper()
	data, ok := f.stub.Files["/remote.php/dav/files/data-bot/Shared/Sessions/jo/"+name]
	if !ok {
		f.t.Fatalf("%s was not uploaded; requests:\n%s", name, f.stub.Requests())
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		f.t.Fatal(err)
	}
	plain, _ := io.ReadAll(reader)
	return string(plain)
}

func TestSessionsExportUploadsRedacted(t *testing.T) {
	f := newExportFixture(t)
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	text := f.uploaded("s1.jsonl.gz")
	if strings.Contains(text, "hunter2") || strings.Contains(text, "FILE BODY") || !strings.Contains(text, "password=[REDACTED:secret]") {
		t.Errorf("upload not reduced and redacted:\n%s", text)
	}
	if !f.stub.Dirs["/remote.php/dav/files/data-bot/Shared/Sessions/jo"] {
		t.Error("folder not created")
	}
	var state map[string]exportRecord
	data, _ := os.ReadFile(f.state)
	if json.Unmarshal(data, &state) != nil || state["s1.jsonl"].Size != int64(len(exportLine+exportToolResult)) {
		t.Errorf("state = %s", data)
	}

	// Nothing changed: no requests at all, so no credentials are needed either.
	before := len(f.stub.Log)
	f.env = map[string]string{}
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if len(f.stub.Log) != before || !strings.Contains(f.out.String(), "0 uploaded, 1 unchanged") {
		t.Errorf("second run: %s\nrequests:\n%s", f.out.String(), f.stub.Requests())
	}

	// The transcript grew: it goes up again, whole.
	f.env = map[string]string{"NC_USER": "data-bot", "NC_PASS_FILE": filepath.Join(f.dir, "pass")}
	f.write("-home-jo-workspace/s1.jsonl", exportLine+exportToolResult+strings.Replace(exportLine, "deploy svc-mp", "and svc-ai", 1))
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if text := f.uploaded("s1.jsonl.gz"); !strings.Contains(text, "deploy svc-mp") || !strings.Contains(text, "and svc-ai") {
		t.Errorf("re-upload not whole:\n%s", text)
	}
}

func TestSessionsExportDryRun(t *testing.T) {
	f := newExportFixture(t)
	f.dryRun = true
	f.env = map[string]string{} // a dry run needs no account
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if len(f.stub.Log) != 0 {
		t.Errorf("dry run sent requests:\n%s", f.stub.Requests())
	}
	if !strings.Contains(f.out.String(), fmt.Sprintf("would upload s1.jsonl: %d bytes -> ", len(exportLine+exportToolResult))) || !strings.Contains(f.out.String(), "Shared/Sessions/jo/s1.jsonl.gz") {
		t.Errorf("output:\n%s", f.out.String())
	}
	if _, err := os.Stat(f.state); err == nil {
		t.Error("dry run wrote the state file")
	}
}

func TestSessionsExportSkips(t *testing.T) {
	f := newExportFixture(t)
	old := f.write("old/o.jsonl", exportLine)
	past := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	f.write("bookkeeping/b.jsonl", `{"type":"permission-mode","permissionMode":"auto"}`+"\n")
	f.dryRun = true
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); !strings.Contains(got, "1 would upload, 0 unchanged, 1 older than --since, 1 with nothing to keep") || strings.Contains(got, "o.jsonl:") {
		t.Errorf("output:\n%s", got)
	}
}

func TestSessionsExportNeedsAccount(t *testing.T) {
	f := newExportFixture(t)
	f.env = map[string]string{"NC_USER": "data-bot"}
	if err := f.run(); err == nil || !strings.Contains(err.Error(), "NC_PASS_FILE") {
		t.Fatalf("err = %v", err)
	}
	f.env["NC_PASS_FILE"] = filepath.Join(f.dir, "missing")
	if err := f.run(); err == nil || !strings.Contains(err.Error(), "NC_PASS_FILE") {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionsExportFailedPutIsRetried(t *testing.T) {
	f := newExportFixture(t)
	f.write("other/s2.jsonl", strings.ReplaceAll(exportLine, "s1", "s2"))
	f.stub.Fail["/remote.php/dav/files/data-bot/Shared/Sessions/jo/s1.jsonl.gz"] = true
	if err := f.run(); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	f.uploaded("s2.jsonl.gz") // the other one still went up
	delete(f.stub.Fail, "/remote.php/dav/files/data-bot/Shared/Sessions/jo/s1.jsonl.gz")
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "1 uploaded, 1 unchanged") {
		t.Errorf("retry: %s", f.out.String())
	}
}

func TestSessionsExportUserName(t *testing.T) {
	for _, name := range []string{"", "..", "a/b"} {
		err := runSessionsExport(exportOptions{user: name})
		if err == nil || !strings.Contains(err.Error(), "--user") {
			t.Errorf("%q: err = %v", name, err)
		}
	}
}

func TestSessionsExportCopilot(t *testing.T) {
	f := newExportFixture(t)
	user := filepath.Join(f.dir, "Code", "User")
	f.extra = []string{user}
	session := filepath.Join(user, "workspaceStorage", "h1", "chatSessions", "c1.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"kind":0,"v":{"sessionId":"c1","requests":[{"timestamp":1790000000000,"message":{"text":"rotate it, password=hunter2"},` +
		`"response":[{"kind":"thinking","value":"PRIVATE"},{"value":"Rotated."}]}]}}` + "\n"
	if err := os.WriteFile(session, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.run(); err != nil {
		t.Fatal(err)
	}
	text := f.uploaded("copilot-c1.jsonl.gz")
	if !strings.Contains(text, `"source":"copilot"`) || !strings.Contains(text, "Rotated.") || strings.Contains(text, "hunter2") || strings.Contains(text, "PRIVATE") {
		t.Errorf("copilot upload:\n%s", text)
	}
	f.uploaded("s1.jsonl.gz") // the Claude Code transcript went up too
	var state map[string]exportRecord
	data, _ := os.ReadFile(f.state)
	if json.Unmarshal(data, &state) != nil || state["copilot:c1.jsonl"].SHA256 == "" {
		t.Errorf("state = %s", data)
	}
}
