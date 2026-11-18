// Package webdavtest is an in-memory WebDAV server for tests: PROPFIND, MKCOL and PUT
// with basic auth, enough to stand in for Nextcloud.
package webdavtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"
)

// Stub is the server; Files and Dirs are keyed by unescaped path.
type Stub struct {
	URL   string
	Files map[string][]byte
	Dirs  map[string]bool
	Log   []string // "METHOD /path" for every authorised request
	// Fail makes PUT to these paths answer HTTP 500.
	Fail map[string]bool

	mu sync.Mutex
}

// NewStub starts a server that accepts user/password, with dirs already present.
func NewStub(t *testing.T, user, password string, dirs ...string) *Stub {
	t.Helper()
	stub := &Stub{Files: map[string][]byte{}, Dirs: map[string]bool{"/": true}, Fail: map[string]bool{}}
	for _, dir := range dirs {
		stub.Dirs[dir] = true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, password2, ok := r.BasicAuth()
		if !ok || name != user || password2 != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		target, err := url.PathUnescape(r.URL.EscapedPath())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		target = path.Clean(target)
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.Log = append(stub.Log, r.Method+" "+target)
		parent := path.Dir(target)
		switch r.Method {
		case "PROPFIND":
			if stub.Dirs[target] || stub.Files[target] != nil {
				w.WriteHeader(http.StatusMultiStatus)
				io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"/>`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case "MKCOL":
			switch {
			case stub.Dirs[target]:
				w.WriteHeader(http.StatusMethodNotAllowed)
			case !stub.Dirs[parent]:
				w.WriteHeader(http.StatusConflict)
			default:
				stub.Dirs[target] = true
				w.WriteHeader(http.StatusCreated)
			}
		case "PUT":
			if !stub.Dirs[parent] {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if stub.Fail[target] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			data, _ := io.ReadAll(r.Body)
			_, existed := stub.Files[target]
			stub.Files[target] = data
			if existed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	stub.URL = server.URL
	return stub
}

// Requests returns the request log, one "METHOD /path" per line.
func (s *Stub) Requests() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.Log, "\n")
}
