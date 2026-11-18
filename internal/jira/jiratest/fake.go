// Package jiratest is a fake Jira REST API for tests.
package jiratest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Server is just enough of Jira's REST API for the client: one project (OPS) with a
// Task and a Sub-task issue type, a To Do > In Progress > Done workflow, worklogs and
// comments.
type Server struct {
	mu       sync.Mutex
	t        *testing.T
	Cloud    bool                        // answers Cloud's /rest/api/3/search/jql, else Data Center's /rest/api/2/search
	Issues   map[string]map[string]any   // by key: the fields
	Worklogs map[string][]map[string]any // by issue key
	Comments map[string][]map[string]any // by issue key
	Searches []string                    // the JQL of each search
	nextID   int
}

// New returns a fake with issues OPS-1 and OPS-2 (To Do, assigned to acc-me) and OPS-3
// (Done). Issues it creates start at OPS-11.
func New(t *testing.T, cloud bool) *Server {
	f := &Server{t: t, Cloud: cloud, Issues: map[string]map[string]any{}, Worklogs: map[string][]map[string]any{},
		Comments: map[string][]map[string]any{}, nextID: 10}
	for index := 1; index <= 3; index++ {
		f.add(fmt.Sprintf("OPS-%d", index), "Issue "+fmt.Sprint(index), "To Do", "new", "")
	}
	f.Issues["OPS-3"]["status"] = statusJSON("Done", "done")
	return f
}

func statusJSON(name, category string) map[string]any {
	return map[string]any{"name": name, "statusCategory": map[string]any{"key": category}}
}

func (f *Server) add(key, summary, status, category, parent string) {
	fields := map[string]any{
		"summary": summary, "status": statusJSON(status, category), "labels": []string{},
		"project":   map[string]any{"id": "1", "key": "OPS", "name": "Operations"},
		"issuetype": map[string]any{"id": "100", "name": "Task"},
		"updated":   "2026-11-10T09:00:00.000+0000", "description": "h2. Goal\nShip *it*",
		"assignee": map[string]any{"accountId": "acc-me", "displayName": "Sam"},
	}
	if parent != "" {
		fields["parent"] = map[string]any{"id": "1" + strings.TrimPrefix(parent, "OPS-"), "key": parent}
		fields["issuetype"] = map[string]any{"id": "101", "name": "Sub-task", "subtask": true}
	}
	f.Issues[key] = fields
}

func (f *Server) issueJSON(key string) map[string]any {
	return map[string]any{"id": "1" + strings.TrimPrefix(key, "OPS-"), "key": key, "fields": f.Issues[key]}
}

func (f *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	path := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(path, "/rest/api/2/issue/"), "/")
	switch {
	case path == "/rest/api/2/myself":
		reply(map[string]any{"accountId": "acc-me", "displayName": "Sam"})
	case path == "/rest/api/2/project":
		reply([]map[string]any{{"id": "1", "key": "OPS", "name": "Operations"}})
	case path == "/rest/api/2/project/OPS":
		reply(map[string]any{"id": "1", "key": "OPS", "name": "Operations", "issueTypes": []map[string]any{
			{"id": "100", "name": "Task"}, {"id": "101", "name": "Sub-task", "subtask": true}}})
	case path == "/rest/api/2/project/OPS/statuses":
		reply([]map[string]any{{"id": "100", "name": "Task", "statuses": []any{
			statusJSON("To Do", "new"), statusJSON("In Progress", "indeterminate"), statusJSON("Done", "done")}}})
	case path == "/rest/api/2/priority":
		reply([]map[string]any{{"name": "Highest"}, {"name": "High"}, {"name": "Medium"}, {"name": "Low"}, {"name": "Lowest"}})
	case path == "/rest/api/3/search/jql" || path == "/rest/api/2/search":
		if (path == "/rest/api/2/search") == f.Cloud {
			http.Error(w, `{"errorMessages":["gone"]}`, http.StatusGone)
			return
		}
		jql := r.URL.Query().Get("jql")
		f.Searches = append(f.Searches, jql)
		var matches []map[string]any
		for index := 1; index <= f.nextID+5; index++ {
			key := fmt.Sprintf("OPS-%d", index)
			fields, ok := f.Issues[key]
			if !ok {
				continue
			}
			category := fields["status"].(map[string]any)["statusCategory"].(map[string]any)["key"]
			parent, _ := fields["parent"].(map[string]any)
			switch {
			case strings.Contains(jql, "statusCategory != Done") && category == "done":
			case strings.HasPrefix(jql, "parent = ") && (parent == nil || !strings.Contains(jql, fmt.Sprint(parent["key"]))):
			case strings.HasPrefix(jql, "worklogAuthor") && len(f.Worklogs[key]) == 0:
			default:
				matches = append(matches, f.issueJSON(key))
			}
		}
		// One issue per page, to exercise paging.
		page := 0
		if token := r.URL.Query().Get("nextPageToken"); token != "" {
			fmt.Sscan(token, &page)
		}
		if start := r.URL.Query().Get("startAt"); start != "" {
			fmt.Sscan(start, &page)
		}
		result := map[string]any{"total": len(matches), "issues": matches[min(page, len(matches)):min(page+1, len(matches))]}
		if path == "/rest/api/3/search/jql" && page+1 < len(matches) {
			result["nextPageToken"] = fmt.Sprint(page + 1)
		}
		reply(result)
	case path == "/rest/api/2/issue" && r.Method == http.MethodPost:
		fields := body["fields"].(map[string]any)
		f.nextID++
		key := fmt.Sprintf("OPS-%d", f.nextID)
		parent := ""
		if p, ok := fields["parent"].(map[string]any); ok {
			parent = fmt.Sprint(p["key"])
		}
		f.add(key, fmt.Sprint(fields["summary"]), "To Do", "new", parent)
		if description, ok := fields["description"].(string); ok {
			f.Issues[key]["description"] = description
		}
		if fields["issuetype"].(map[string]any)["id"] == "101" != (parent != "") {
			f.t.Errorf("subtask type mismatch: %v", fields)
		}
		reply(map[string]any{"id": "1" + fmt.Sprint(f.nextID), "key": key})
	case len(parts) >= 1 && f.Issues[parts[0]] != nil:
		key := parts[0]
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			reply(f.issueJSON(key))
		case len(parts) == 1 && r.Method == http.MethodPut:
			if fields, ok := body["fields"].(map[string]any); ok {
				for name, value := range fields {
					f.Issues[key][name] = value
				}
			}
			if update, ok := body["update"].(map[string]any); ok {
				for _, change := range update["labels"].([]any) {
					labels := f.Issues[key]["labels"].([]string)
					for verb, value := range change.(map[string]any) {
						if verb == "add" {
							labels = append(labels, value.(string))
						} else {
							labels = slicesDelete(labels, value.(string))
						}
					}
					f.Issues[key]["labels"] = labels
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case parts[1] == "transitions" && r.Method == http.MethodGet:
			reply(map[string]any{"transitions": []map[string]any{
				{"id": "11", "name": "Start", "to": statusJSON("In Progress", "indeterminate")},
				{"id": "31", "name": "Finish", "to": statusJSON("Done", "done")}}})
		case parts[1] == "transitions":
			to := map[string]any{"11": statusJSON("In Progress", "indeterminate"), "31": statusJSON("Done", "done")}
			f.Issues[key]["status"] = to[body["transition"].(map[string]any)["id"].(string)]
			w.WriteHeader(http.StatusNoContent)
		case parts[1] == "worklog" && r.Method == http.MethodPost:
			body["id"] = fmt.Sprint(len(f.Worklogs[key]) + 1)
			body["author"] = map[string]any{"accountId": "acc-me"}
			f.Worklogs[key] = append(f.Worklogs[key], body)
			reply(body)
		case parts[1] == "worklog" && len(parts) == 2:
			reply(map[string]any{"worklogs": f.Worklogs[key]})
		case parts[1] == "worklog" && len(parts) == 3:
			for _, log := range f.Worklogs[key] {
				if log["id"] == parts[2] {
					if r.Method == http.MethodPut {
						for name, value := range body {
							log[name] = value
						}
					}
					reply(log)
					return
				}
			}
			http.Error(w, "no worklog", http.StatusNotFound)
		case parts[1] == "comment" && r.Method == http.MethodPost:
			comment := map[string]any{"id": fmt.Sprint(len(f.Comments[key]) + 1), "body": body["body"],
				"author":  map[string]any{"accountId": "acc-me", "displayName": "Sam"},
				"created": fmt.Sprintf("2026-11-10T10:0%d:00.000+0000", len(f.Comments[key]))}
			f.Comments[key] = append(f.Comments[key], comment)
			reply(comment)
		case parts[1] == "comment" && len(parts) == 3:
			for _, comment := range f.Comments[key] {
				if comment["id"] == parts[2] {
					comment["body"] = body["body"]
				}
			}
			reply(map[string]any{})
		case parts[1] == "comment":
			reply(map[string]any{"comments": f.Comments[key], "total": len(f.Comments[key])})
		default:
			http.Error(w, "unexpected "+r.Method+" "+path, http.StatusNotFound)
		}
	default:
		http.Error(w, `{"errorMessages":["Issue does not exist"]}`, http.StatusNotFound)
	}
}

func slicesDelete(list []string, value string) []string {
	var kept []string
	for _, item := range list {
		if item != value {
			kept = append(kept, item)
		}
	}
	return kept
}
