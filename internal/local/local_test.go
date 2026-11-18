package local

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

func open(t *testing.T) *Backend {
	t.Helper()
	b, err := Open(t.TempDir(), "Sam", "ops")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTasksStatusesAndTags(t *testing.T) {
	b := open(t)
	lists, err := b.SpaceLists("personal")
	if err != nil || len(lists) != 1 {
		t.Fatalf("lists = %v, %v", lists, err)
	}
	created, err := b.CreateTask(lists[0].ID, tracker.NewTask{Name: "Rotate certs", MarkdownContent: "**now**",
		Assignees: []string{UserID}, Tags: []string{"Infra"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Ref() != "OPS-1" || created.Status.Status != "to do" || !created.AssignedTo(UserID) || created.TagNames()[0] != "infra" {
		t.Fatalf("created %+v", created)
	}
	if _, err := b.SetStatus("ops-1", "In Progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetStatus("OPS-1", "nope"); err == nil || !strings.Contains(err.Error(), "choose from: to do, in progress") {
		t.Fatalf("bad status error = %v", err)
	}
	high, due := 2, time.Date(2026, 11, 20, 0, 0, 0, 0, time.Local).UnixMilli()
	updated, err := b.UpdateTask("OPS-1", tracker.TaskUpdate{Priority: &high, DueDate: &due, RemoveAssignees: []string{UserID}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.PriorityName() != "high" || int64(updated.DueDate) != due || len(updated.Assignees) != 0 || updated.Status.Status != "in progress" {
		t.Fatalf("updated %+v", updated)
	}
	if err := b.AddTag("OPS-1", "certs"); err != nil {
		t.Fatal(err)
	}
	if err := b.RemoveTag("OPS-1", "INFRA"); err != nil {
		t.Fatal(err)
	}
	sub, err := b.CreateTask(lists[0].ID, tracker.NewTask{Name: "summary", Parent: "OPS-1"})
	if err != nil || sub.Ref() != "OPS-2" || sub.Parent != "OPS-1" {
		t.Fatalf("subtask %+v, %v", sub, err)
	}
	if subtasks, _ := b.Subtasks("OPS-1"); len(subtasks) != 1 {
		t.Fatalf("subtasks = %v", subtasks)
	}
	if _, err := b.SetStatus("OPS-2", "done"); err != nil {
		t.Fatal(err)
	}
	open, _ := b.Tasks(tracker.TaskFilter{})
	all, _ := b.Tasks(tracker.TaskFilter{IncludeClosed: true})
	if len(open) != 1 || len(all) != 2 || open[0].TagNames()[0] != "certs" {
		t.Fatalf("open %v, all %d", open, len(all))
	}
}

func TestTimersAndTimeEntries(t *testing.T) {
	b := open(t)
	task, _ := b.CreateTask("inbox", tracker.NewTask{Name: "Write docs"})
	clock := time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local)
	b.now = func() time.Time { return clock }
	if err := b.StartTimer(task.ID, "", []string{"docs"}); err != nil {
		t.Fatal(err)
	}
	running, _ := b.CurrentTimer()
	if running == nil || running.Task.Ref() != "OPS-1" || !running.Running() {
		t.Fatalf("running = %+v", running)
	}
	clock = clock.Add(45 * time.Minute)
	// Starting another timer stops the first, as ClickUp does.
	if err := b.StartTimer("", "Standup", nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(15 * time.Minute)
	if err := b.StopTimer(); err != nil {
		t.Fatal(err)
	}
	if running, _ := b.CurrentTimer(); running != nil {
		t.Fatalf("still running: %+v", running)
	}
	if err := b.LogTime(task.Ref(), clock, clock.Add(time.Hour), "review", nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := b.TimeEntries(clock.Add(-2*time.Hour), clock.Add(2*time.Hour))
	if len(entries) != 3 || entries[0].EndTime().Sub(entries[0].StartTime()) != 45*time.Minute ||
		entries[1].Task != nil || entries[1].Description != "Standup" || entries[2].Description != "review" {
		t.Fatalf("entries = %+v", entries)
	}
	if err := b.UpdateTimeEntry(entries[2].ID, clock, clock.Add(30*time.Minute), "reviewed"); err != nil {
		t.Fatal(err)
	}
	if entry, _ := b.TimeEntry(entries[2].ID); entry.Description != "reviewed" || entry.Duration != tracker.FlexInt(30*time.Minute/time.Millisecond) {
		t.Fatalf("edited = %+v", entry)
	}
	if err := b.DeleteTimeEntry(entries[1].ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := b.TimeEntries(clock.Add(-2*time.Hour), clock.Add(2*time.Hour)); len(entries) != 2 {
		t.Fatalf("after delete: %d entries", len(entries))
	}
}

func TestCommentsAndAttachments(t *testing.T) {
	b := open(t)
	task, _ := b.CreateTask("inbox", tracker.NewTask{Name: "Plan"})
	sections := markdown.Sections([]string{"## Plan", "", "- **one**", "- two"}, nil, false)
	id, err := b.PostComment(task.Ref(), sections[len(sections)-1].Comment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.PostComment(task.Ref(), []markdown.Op{{Text: "second\n"}}); err != nil {
		t.Fatal(err)
	}
	comments, _ := b.Comments(task.Ref())
	if len(comments) != 2 || comments[0].CommentText != "second" || !strings.Contains(comments[1].CommentText, "- one") {
		t.Fatalf("comments = %+v", comments)
	}
	if err := b.UpdateComment(id, []markdown.Op{{Text: "edited\n"}}); err != nil {
		t.Fatal(err)
	}
	if comments, _ := b.Comments(task.Ref()); comments[1].CommentText != "edited" {
		t.Fatalf("not edited: %+v", comments[1])
	}
	file := filepath.Join(t.TempDir(), "log.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	attachment, err := b.UploadAttachment(task.Ref(), file)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(strings.TrimPrefix(attachment.URL, "file://")); err != nil || string(data) != "x" {
		t.Fatalf("attachment %+v: %v", attachment, err)
	}
}

func TestConcurrentWritesKeepEveryTask(t *testing.T) {
	b := open(t)
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := b.CreateTask("inbox", tracker.NewTask{Name: "t"}); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	tasks, _ := b.Tasks(tracker.TaskFilter{})
	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.ID] = true
	}
	if len(tasks) != 20 || len(seen) != 20 {
		t.Fatalf("%d tasks, %d distinct IDs", len(tasks), len(seen))
	}
}
