package clui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/JB0t/clctl/internal/local"
	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

// fixture is a local store with TASK-1 (described, commented, with time logged and a
// subtask TASK-2), and a view of TASK-1 sized 100x40.
type fixture struct {
	t       *testing.T
	store   *local.Backend
	model   *Model
	started []string // timer starts: "<ref> keep=<bool>"
	stopped []string
	running string // ref the fake timer runs on
	saved   int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := local.Open(t.TempDir(), "Sam", "")
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := store.CreateTask("inbox", tracker.NewTask{Name: "Rotate certificates", MarkdownContent: "## Plan\n\n- **renew** the root CA",
		Assignees: []string{local.UserID}, Tags: []string{"infra"}})
	_, _ = store.CreateTask("inbox", tracker.NewTask{Name: "Write the summary", Parent: parent.ID})
	_, _ = store.PostComment(parent.Ref(), markdown.CommentOps("First pass done, see `certs.md`"))
	now := time.Now()
	_ = store.LogTime(parent.Ref(), now.Add(-3*time.Hour), now.Add(-90*time.Minute), "renewal", []string{"ops"})

	f := &fixture{t: t, store: store}
	actions := Actions{
		Backend: store,
		Timer: func(task tracker.Task) TimerState {
			switch {
			case f.running == task.Ref():
				return TimerState{Running: true, Since: time.Now().Add(-12 * time.Minute)}
			case f.running != "":
				return TimerState{Other: f.running}
			}
			return TimerState{}
		},
		StartTimer: func(task tracker.Task, keep bool) (string, error) {
			f.started = append(f.started, task.Ref()+" keep="+map[bool]string{true: "true", false: "false"}[keep])
			f.running = task.Ref()
			return "Started timer: " + task.Ref(), nil
		},
		StopTimer: func(task tracker.Task) (string, error) {
			f.stopped = append(f.stopped, task.Ref())
			f.running = ""
			return "Stopped timer on " + task.Ref() + ".", nil
		},
		Saved: func(tracker.Task) { f.saved++ },
		Style: "notty",
	}
	f.model = New(actions, "TASK-1")
	f.model.input.Cursor.SetMode(cursor.CursorStatic) // a blinking cursor's ticks would run here
	f.send(tea.WindowSizeMsg{Width: 100, Height: 40})
	f.send(f.model.load()())
	return f
}

// send delivers a message and then every message its commands produce (no ticks).
func (f *fixture) send(msg tea.Msg) {
	f.t.Helper()
	_, cmd := f.model.Update(msg)
	f.run(cmd)
}

func (f *fixture) run(cmd tea.Cmd) {
	f.t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, next := range msg {
			f.run(next)
		}
	case tea.QuitMsg:
	default:
		f.send(msg)
	}
}

func (f *fixture) keys(keys ...string) {
	f.t.Helper()
	for _, name := range keys {
		var msg tea.KeyMsg
		switch name {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
		}
		f.send(msg)
	}
}

func (f *fixture) view() string { return f.model.View() }

func (f *fixture) task(id string) tracker.Task {
	task, err := f.store.Task(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

func TestShowsTheTaskLikeItsPage(t *testing.T) {
	f := newFixture(t)
	view := f.view()
	for _, want := range []string{"TASK-1", "Rotate certificates", "Personal / Inbox", "TO DO", "@Sam", "#infra",
		"Description", "renew", "Subtasks (1)", "TASK-2", "Write the summary",
		"Time logged · last 90 days · 1h 30m", "renewal", "#ops", "Comments (1)", "First pass done", "s status"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if lines := strings.Count(view, "\n") + 1; lines > 40 {
		t.Errorf("view is %d lines, taller than the 40-line terminal", lines)
	}
}

func TestChangesStatusAndPriority(t *testing.T) {
	f := newFixture(t)
	f.keys("s", "down", "enter") // to do -> in progress
	if got := f.task("TASK-1").Status.Status; got != "in progress" {
		t.Fatalf("status = %q", got)
	}
	if !strings.Contains(f.view(), "IN PROGRESS") || !strings.Contains(f.view(), "TASK-1 -> in progress") {
		t.Fatalf("view not updated:\n%s", f.view())
	}
	f.keys("p") // the picker starts on the current priority, none (the last row)
	if f.model.picker.cursor != len(tracker.PriorityNames) {
		t.Fatalf("priority picker starts at row %d", f.model.picker.cursor)
	}
	f.model.picker.cursor = 1 // high
	f.keys("enter")
	if got := f.task("TASK-1").PriorityName(); got != "high" {
		t.Fatalf("priority = %q", got)
	}
	if f.saved < 2 {
		t.Fatalf("saved %d times, want the cache told about each change", f.saved)
	}
}

func TestDoneStopsItsTimer(t *testing.T) {
	f := newFixture(t)
	f.keys("t")
	if strings.Join(f.started, ",") != "TASK-1 keep=false" || !strings.Contains(f.view(), "⏱ 0h 12m") {
		t.Fatalf("started %v; view:\n%s", f.started, f.view())
	}
	f.keys("s", "down", "down", "down", "enter") // done
	if !f.task("TASK-1").Status.Done() || strings.Join(f.stopped, ",") != "TASK-1" {
		t.Fatalf("status %+v, stopped %v", f.task("TASK-1").Status, f.stopped)
	}
}

func TestTimerElsewhereOffersToKeepIt(t *testing.T) {
	f := newFixture(t)
	f.running = "TASK-9"
	f.send(f.model.load()())
	f.keys("t")
	if f.model.mode != modePick || !strings.Contains(f.view(), "A timer is running on TASK-9") {
		t.Fatalf("no choice offered:\n%s", f.view())
	}
	f.keys("down", "enter") // keep it, time this with a local timer
	if strings.Join(f.started, ",") != "TASK-1 keep=true" {
		t.Fatalf("started %v", f.started)
	}
}

func TestCommentsAndSubtaskNavigation(t *testing.T) {
	f := newFixture(t)
	f.keys("c")
	for _, r := range "Renewed, **all good**" {
		f.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	f.keys("ctrl+s")
	comments, _ := f.store.Comments("TASK-1")
	if len(comments) != 2 || comments[0].CommentText != "Renewed, all good" {
		t.Fatalf("comments = %+v", comments)
	}
	if !strings.Contains(f.view(), "Comments (2)") {
		t.Fatalf("view not reloaded:\n%s", f.view())
	}

	f.keys("o", "enter")
	if !strings.Contains(f.view(), "Write the summary") || f.model.task.Ref() != "TASK-2" || !strings.Contains(f.view(), "⌫ back") {
		t.Fatalf("subtask not opened:\n%s", f.view())
	}
	f.keys("backspace")
	if f.model.task.Ref() != "TASK-1" {
		t.Fatalf("back went to %s", f.model.task.Ref())
	}
	f.keys("o", "enter", "u")
	if f.model.task.Ref() != "TASK-1" {
		t.Fatalf("parent is %s", f.model.task.Ref())
	}
}

func TestMissingTaskQuitsWithTheError(t *testing.T) {
	store, _ := local.Open(t.TempDir(), "Sam", "")
	model := New(Actions{Backend: store}, "TASK-404")
	_, cmd := model.Update(model.load()())
	if model.fatal == nil || !strings.Contains((*model.fatal).Error(), "no task TASK-404") {
		t.Fatalf("fatal = %v", model.fatal)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("the view did not quit")
	}
}
