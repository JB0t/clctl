package jira

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/jira/jiratest"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

func newClient(t *testing.T, cloud bool, misc string) (*Client, *jiratest.Server) {
	t.Helper()
	fake := jiratest.New(t, cloud)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	options := Options{URL: server.URL, Token: "token", Projects: []string{"OPS"}, MiscIssue: misc,
		TimerPath: filepath.Join(t.TempDir(), "timer.json")}
	if cloud {
		options.Email = "sam@example.com"
	}
	return New(options), fake
}

func TestTasksSearchPagesOnCloudAndDataCenter(t *testing.T) {
	for _, cloud := range []bool{true, false} {
		client, fake := newClient(t, cloud, "")
		tasks, err := client.Tasks(tracker.TaskFilter{AssigneeIDs: []string{"acc-me"}})
		if err != nil {
			t.Fatalf("cloud=%v: %v", cloud, err)
		}
		if len(tasks) != 2 || tasks[0].Ref() != "OPS-1" || tasks[1].Ref() != "OPS-2" {
			t.Fatalf("cloud=%v: tasks = %+v", cloud, tasks)
		}
		want := `project in ("OPS") AND assignee in ("acc-me") AND statusCategory != Done ORDER BY updated DESC`
		if fake.Searches[0] != want {
			t.Fatalf("jql = %q", fake.Searches[0])
		}
		task := tasks[0]
		if task.Space.ID != "OPS" || task.List.ID != "OPS/100" || task.List.Name != "Task" || task.Status.Type != "open" ||
			!task.AssignedTo("acc-me") || !strings.HasSuffix(task.URL, "/browse/OPS-1") {
			t.Fatalf("mapped task = %+v", task)
		}
		all, _ := client.Tasks(tracker.TaskFilter{IncludeClosed: true})
		if len(all) != 3 || all[2].Status.Type != "closed" || !all[2].Status.Done() {
			t.Fatalf("with closed: %+v", all)
		}
	}
}

func TestTaskUpdatesStatusAndCreate(t *testing.T) {
	client, fake := newClient(t, true, "")
	task, err := client.TaskWithMarkdown("OPS-1")
	if err != nil || task.MarkdownDescription != "## Goal\nShip **it**" {
		t.Fatalf("description = %q, %v", task.MarkdownDescription, err)
	}
	list, err := client.List(task.List.ID)
	if err != nil || len(list.Statuses) != 3 || list.Statuses[1].Type != "custom" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	moved, err := client.SetStatus("OPS-1", "in progress")
	if err != nil || moved.Status.Status != "In Progress" {
		t.Fatalf("moved = %+v, %v", moved.Status, err)
	}
	if _, err := client.SetStatus("OPS-1", "Archived"); err == nil || !strings.Contains(err.Error(), "allows: In Progress, Done") {
		t.Fatalf("bad transition error = %v", err)
	}
	high, due := 2, time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local).UnixMilli()
	description := "- **bold** item"
	updated, err := client.UpdateTask("OPS-1", tracker.TaskUpdate{Priority: &high, DueDate: &due, MarkdownContent: &description,
		RemoveAssignees: []string{"acc-me"}})
	if err != nil {
		t.Fatal(err)
	}
	fields := fake.Issues["OPS-1"]
	if fields["priority"].(map[string]any)["name"] != "High" || fields["duedate"] != "2026-12-01" ||
		fields["description"] != "* *bold* item" || fields["assignee"] != nil {
		t.Fatalf("sent fields = %v", fields)
	}
	if updated.PriorityName() != "high" || len(updated.Assignees) != 0 {
		t.Fatalf("updated = %+v", updated)
	}
	if err := client.AddTag("OPS-1", "needs review"); err != nil {
		t.Fatal(err)
	}
	if task, _ := client.Task("OPS-1"); strings.Join(task.TagNames(), ",") != "needs-review" {
		t.Fatalf("labels = %v", task.TagNames())
	}
	lists, _ := client.SpaceLists("OPS")
	if len(lists) != 1 || lists[0].ID != "OPS/100" {
		t.Fatalf("lists = %+v", lists)
	}
	created, err := client.CreateTask(lists[0].ID, tracker.NewTask{Name: "New", MarkdownContent: "`code`", Assignees: []string{"acc-me"}})
	if err != nil || created.Ref() != "OPS-11" || fake.Issues["OPS-11"]["description"] != "{{code}}" {
		t.Fatalf("created = %+v, %v", created, err)
	}
	sub, err := client.CreateTask(lists[0].ID, tracker.NewTask{Name: "summary", Parent: "OPS-11"})
	if err != nil || sub.List.Name != "Sub-task" {
		t.Fatalf("subtask = %+v, %v", sub, err)
	}
	if subtasks, err := client.Subtasks("OPS-11"); err != nil || len(subtasks) != 1 || subtasks[0].Ref() != "OPS-12" {
		t.Fatalf("subtasks = %+v, %v", subtasks, err)
	}
}

func TestTimerLogsWorklogs(t *testing.T) {
	client, fake := newClient(t, true, "")
	if err := client.StartTimer("", "Standup", nil); err != errNoMiscIssue {
		t.Fatalf("a timer without a task and no misc issue: %v", err)
	}
	if err := client.StartTimer("OPS-2", "pairing", []string{"review"}); err != nil {
		t.Fatal(err)
	}
	running, _ := client.CurrentTimer()
	if running == nil || running.Task.Ref() != "OPS-2" || running.Task.Name != "Issue 2" {
		t.Fatalf("running = %+v", running)
	}
	if err := client.StopTimer(); err != nil {
		t.Fatal(err)
	}
	if running, _ := client.CurrentTimer(); running != nil {
		t.Fatalf("still running: %+v", running)
	}
	logged := fake.Worklogs["OPS-2"]
	if len(logged) != 1 || logged[0]["timeSpentSeconds"] != float64(60) || logged[0]["comment"] != "pairing #review" {
		t.Fatalf("worklog = %v", logged)
	}
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	if err := client.LogTime("OPS-1", start, start.Add(90*time.Minute), "", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := client.TimeEntries(time.Now().Add(-3*time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Task.Ref() != "OPS-1" || entries[0].EndTime().Sub(entries[0].StartTime()) != 90*time.Minute ||
		entries[1].Description != "pairing" || strings.Join(entries[1].TagNames(), ",") != "review" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := client.UpdateTimeEntry(entries[1].ID, start, start.Add(time.Hour), "pairing with Ana"); err != nil {
		t.Fatal(err)
	}
	if entry, _ := client.TimeEntry(entries[1].ID); entry.Description != "pairing with Ana" || entry.TagNames()[0] != "review" {
		t.Fatalf("edited = %+v", entry)
	}
}

func TestTimeWithoutATaskGoesOnTheMiscIssue(t *testing.T) {
	client, fake := newClient(t, false, "OPS-1")
	if err := client.LogTime("", time.Now().Add(-time.Hour), time.Now(), "Standup", []string{"meetings"}); err != nil {
		t.Fatal(err)
	}
	if logged := fake.Worklogs["OPS-1"]; len(logged) != 1 || logged[0]["comment"] != "Standup #meetings" {
		t.Fatalf("worklog = %v", logged)
	}
	entries, _ := client.TimeEntries(time.Now().Add(-2*time.Hour), time.Now())
	if len(entries) != 1 || entries[0].Task != nil || entries[0].Description != "Standup" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestCommentsConvertToAndFromWiki(t *testing.T) {
	client, fake := newClient(t, true, "")
	sections := markdown.Sections([]string{"## Update", "", "- **done** with `make`", "- see [PR](https://x.io/1)"}, nil, false)
	id, err := client.PostComment("OPS-1", sections[len(sections)-1].Comment)
	if err != nil || id != "OPS-1:1" {
		t.Fatalf("id = %q, %v", id, err)
	}
	if body := fake.Comments["OPS-1"][0]["body"]; body != "h2. Update\n* *done* with {{make}}\n* see [PR|https://x.io/1]" {
		t.Fatalf("wiki body = %q", body)
	}
	if _, err := client.PostComment("OPS-1", []markdown.Op{{Text: "second\n"}}); err != nil {
		t.Fatal(err)
	}
	comments, err := client.Comments("OPS-1")
	if err != nil || len(comments) != 2 || comments[0].CommentText != "second" {
		t.Fatalf("comments = %+v, %v", comments, err)
	}
	if text := markdown.PlainText(comments[1].Comment); text != "## Update\n- done with make\n- see PR" {
		t.Fatalf("ops render as %q", text)
	}
	if err := client.UpdateComment(id, []markdown.Op{{Text: "edited\n"}}); err != nil {
		t.Fatal(err)
	}
	if fake.Comments["OPS-1"][0]["body"] != "edited" {
		t.Fatalf("not edited: %v", fake.Comments["OPS-1"][0])
	}
}
