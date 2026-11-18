package sessions

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteReducedRoundTrip(t *testing.T) {
	var zipped bytes.Buffer
	kept, err := WriteReduced(strings.NewReader(transcript), &zipped)
	if err != nil {
		t.Fatal(err)
	}
	if kept != 4 { // the user's message, the assistant's two, the title
		t.Errorf("kept %d entries, want 4", kept)
	}
	reader, err := gzip.NewReader(&zipped)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, dropped := range []string{"hunter2", "injected context", "ATTACHED FILE BODY", "PRIVATE THOUGHTS", "TOOL RESULT BODY",
		"OLD BODY", "NEW BODY", "Bearer", "cat /home", "META CAVEAT", "FILE HISTORY BODY", "QUEUED", "stop_hook_summary", "permission-mode"} {
		if bytes.Contains(plain, []byte(dropped)) {
			t.Errorf("%q is in the export:\n%s", dropped, plain)
		}
	}

	// The digest reads the export like the original transcript.
	dir := t.TempDir()
	var again bytes.Buffer // zipped was consumed by the reader above
	if _, err := WriteReduced(strings.NewReader(transcript), &again); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl.gz"), again.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	fromExport, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fromTranscript, err := Parse(strings.NewReader(transcript), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromExport, fromTranscript) {
		t.Errorf("export parses differently:\n%+v\n%+v", fromExport[0], fromTranscript[0])
	}
}

func TestReduceIsIdempotent(t *testing.T) {
	for _, line := range strings.Split(transcript, "\n") {
		first, ok := Reduce([]byte(line))
		if !ok {
			continue
		}
		data := mustJSON(t, first)
		second, ok := Reduce(data)
		if !ok || !reflect.DeepEqual(mustJSON(t, second), data) {
			t.Errorf("reducing again changed\n%s\nto\n%s", data, mustJSON(t, second))
		}
	}
}

func TestReadDirFindsNestedAndGzipped(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "alice")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","sessionId":"a","timestamp":"2026-09-01T10:00:00Z","message":{"role":"user","content":"hi"}}` + "\n"
	var zipped bytes.Buffer
	if _, err := WriteReduced(strings.NewReader(line), &zipped); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "a.jsonl.gz"), zipped.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions, err := ReadDir(dir)
	if err != nil || len(sessions) != 1 || sessions[0].ID != "a" {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	if got := SessionName("/x/a.jsonl.gz"); got != "a" {
		t.Errorf("SessionName = %q", got)
	}
}

func mustJSON(t *testing.T, e Entry) []byte {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
