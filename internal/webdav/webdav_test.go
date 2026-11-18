package webdav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JB0t/clctl/internal/webdav/webdavtest"
)

func TestNextcloudURL(t *testing.T) {
	client := Nextcloud("https://cloud.example/", "data bot", "pw")
	if got := client.URL("Shared/Sessions/jo ann/a.jsonl.gz"); got != "https://cloud.example/remote.php/dav/files/data%20bot/Shared/Sessions/jo%20ann/a.jsonl.gz" {
		t.Errorf("URL = %s", got)
	}
}

func TestMkdirAllAndPut(t *testing.T) {
	server := webdavtest.NewStub(t, "u", "p", "/Shared")
	client := New(server.URL, "u", "p")
	if err := client.MkdirAll("Shared/Sessions/alice"); err != nil {
		t.Fatal(err)
	}
	if err := client.MkdirAll("Shared/Sessions/alice"); err != nil { // already there: no MKCOL
		t.Fatal(err)
	}
	if err := client.Put("Shared/Sessions/alice/s.jsonl.gz", []byte("data"), "application/gzip"); err != nil {
		t.Fatal(err)
	}
	if got := server.Files["/Shared/Sessions/alice/s.jsonl.gz"]; string(got) != "data" {
		t.Errorf("stored %q", got)
	}
	if got := server.Requests(); got != strings.Join([]string{
		"PROPFIND /Shared/Sessions/alice",
		"PROPFIND /Shared", "PROPFIND /Shared/Sessions", "MKCOL /Shared/Sessions", "PROPFIND /Shared/Sessions/alice", "MKCOL /Shared/Sessions/alice",
		"PROPFIND /Shared/Sessions/alice",
		"PUT /Shared/Sessions/alice/s.jsonl.gz",
	}, "\n") {
		t.Errorf("requests:\n%s", got)
	}
}

func TestPutIntoMissingFolderFails(t *testing.T) {
	server := webdavtest.NewStub(t, "u", "p")
	if err := New(server.URL, "u", "p").Put("Nope/x", []byte("x"), "text/plain"); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadPassword(t *testing.T) {
	server := webdavtest.NewStub(t, "u", "p")
	if _, err := New(server.URL, "u", "wrong").Exists("x"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

// Nextcloud's login and user ID can differ: the file paths must use the ID.
func TestNextcloudUsesUserID(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, _ := r.BasicAuth(); user != "data-bot" || pass != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/ocs/v2.php/cloud/user" {
			w.Write([]byte(`{"ocs":{"data":{"id":"Data Bot"}}}`))
			return
		}
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer server.Close()
	client := Nextcloud(server.URL, "data-bot", "pw")
	for range 2 {
		if exists, err := client.Exists("Shared/Sessions"); err != nil || !exists {
			t.Fatalf("Exists = %v, %v", exists, err)
		}
	}
	if got := strings.Join(paths, "\n"); got != "PROPFIND /remote.php/dav/files/Data%20Bot/Shared/Sessions\nPROPFIND /remote.php/dav/files/Data%20Bot/Shared/Sessions" {
		t.Errorf("requests:\n%s", got)
	}
	if _, err := Nextcloud(server.URL, "data-bot", "wrong").Exists("Shared"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: err = %v", err)
	}
}
