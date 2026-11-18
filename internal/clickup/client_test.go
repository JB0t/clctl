package clickup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestTeamTasksFollowsPages(t *testing.T) {
	const total = 250
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pk_test" {
			t.Errorf("missing token header")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var tasks []Task
		for i := page * 100; i < min(total, (page+1)*100); i++ {
			tasks = append(tasks, Task{ID: fmt.Sprint(i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": tasks, "last_page": (page+1)*100 >= total})
	}))
	defer server.Close()

	tasks, err := New("pk_test", server.URL, "9").Tasks(TaskFilter{SpaceIDs: []string{"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != total {
		t.Fatalf("got %d tasks, want %d", len(tasks), total)
	}
	for index, task := range tasks {
		if task.ID != fmt.Sprint(index) {
			t.Fatalf("task %d out of order: %s", index, task.ID)
		}
	}
}

func TestRetriesOnRateLimitAndParsesErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`{"user":{"id":105,"username":"me"}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"err":"Token invalid","ECODE":"OAUTH_025"}`))
		}
	}))
	defer server.Close()
	client := New("pk", server.URL, "9")

	me, err := client.Me()
	if err != nil || string(me.ID) != "105" {
		t.Fatalf("Me() = %+v, %v (numeric user IDs must decode)", me, err)
	}
	_, err = client.Teams()
	if err == nil || err.Error() != "ClickUp API: Token invalid (OAUTH_025, HTTP 401)" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskQueryAndTimeEntryDecoding(t *testing.T) {
	client := New("pk", "", "9")
	if query := client.TaskQuery("TECH-12"); query.Get("custom_task_ids") != "true" || query.Get("team_id") != "9" {
		t.Errorf("custom ID query = %v", query)
	}
	if query := client.TaskQuery("869f5rbpr"); len(query) != 0 {
		t.Errorf("raw ID should not add params: %v", query)
	}
	var entry TimeEntry
	if err := json.Unmarshal([]byte(`{"id":"1","start":"1790145084110","duration":-6304047}`), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Start != 1790145084110 || entry.Duration != -6304047 {
		t.Fatalf("decoded %+v", entry)
	}
}
