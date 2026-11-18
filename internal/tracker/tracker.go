// Package tracker is clctl's task-management model: tasks, statuses, time entries and
// comments, and the Backend interface that every task-management system implements.
//
// clctl's commands only talk to a Backend, so the same `clctl timers start` or
// `clctl tasks update` works against any of them:
//
//   - local: clctl's own store, a JSON file on disk (package local), so clctl works
//     standalone with no account anywhere;
//   - clickup: ClickUp's API (package clickup);
//   - jira: Jira Cloud or Data Center (package jira).
//
// The model keeps ClickUp's shape, where clctl started: a Space holds Lists (optionally
// in Folders), and a List holds Tasks and defines their statuses. Other backends map onto
// it: Jira projects are spaces and their issue types are lists, for example.
//
// Comments are Quill-style op lists ([]map[string]any of {"text", "attributes"}, block
// formatting on the "\n" that ends a line, tables as "table-embed" ops), as package
// markdown builds them. Each backend stores or converts them as it needs to.
package tracker

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// Backend is a task-management system clctl works with.
type Backend interface {
	// Kind is the backend's name in the config: "local", "clickup" or "jira".
	Kind() string
	// WorkspaceID identifies the workspace the backend is set up for; the task cache is
	// refreshed when it changes.
	WorkspaceID() string

	// Me returns the user clctl acts as.
	Me() (User, error)
	// Members returns the people tasks can be assigned to.
	Members() ([]User, error)

	Spaces() ([]Space, error)
	Space(id string) (Space, error)
	// SpaceLists returns the lists directly in a space; SpaceFolders its folders with
	// their lists (nil for backends without folders).
	SpaceLists(spaceID string) ([]List, error)
	SpaceFolders(spaceID string) ([]Folder, error)
	// List returns a list with the statuses its tasks can have.
	List(id string) (List, error)

	// Tasks returns every task matching the filter.
	Tasks(filter TaskFilter) ([]Task, error)
	// Task fetches one task by ID or key; TaskWithMarkdown also fills in
	// MarkdownDescription.
	Task(id string) (Task, error)
	TaskWithMarkdown(id string) (Task, error)
	// Subtasks returns a task's direct subtasks.
	Subtasks(id string) ([]Task, error)
	CreateTask(listID string, task NewTask) (Task, error)
	SetStatus(id, status string) (Task, error)
	UpdateTask(id string, update TaskUpdate) (Task, error)
	AddTag(id, tag string) error
	RemoveTag(id, tag string) error

	// CurrentTimer returns the backend's running timer, or nil. Each backend runs one
	// timer per person; clctl runs any others locally (see cmd/timers.go).
	CurrentTimer() (*TimeEntry, error)
	// StartTimer starts the timer on a task, or on no task when taskID is "" (then the
	// description and tags say what it is for).
	StartTimer(taskID, description string, tags []string) error
	// StopTimer stops the running timer and records its time.
	StopTimer() error
	// LogTime records a finished time range against a task, or no task when taskID is "".
	LogTime(taskID string, start, end time.Time, description string, tags []string) error
	// TimeEntries returns the user's time entries that started between from and to.
	TimeEntries(from, to time.Time) ([]TimeEntry, error)
	// RawTimeEntries is TimeEntries as the backend stores them, for inspection.
	RawTimeEntries(from, to time.Time) ([]json.RawMessage, error)
	TimeEntry(id string) (TimeEntry, error)
	UpdateTimeEntry(id string, start, end time.Time, description string) error
	DeleteTimeEntry(id string) error

	// Comments returns a task's comments, newest first.
	Comments(taskID string) ([]Comment, error)
	// PostComment adds a comment built from ops ([]markdown.Op or []map[string]any) and
	// returns its ID; UpdateComment replaces one's content.
	PostComment(taskID string, ops any) (string, error)
	UpdateComment(commentID string, ops any) error
	UploadAttachment(taskID, path string) (Attachment, error)
}

// ErrUnsupported is returned for an action a backend cannot do.
var ErrUnsupported = errors.New("not supported by this task backend")

// taskKey matches task keys such as TECH-1234 (ClickUp custom IDs, Jira issue keys,
// local task IDs).
var taskKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+$`)

// IsTaskKey reports whether id looks like a task key (PROJ-123) rather than a raw ID.
func IsTaskKey(id string) bool { return taskKey.MatchString(id) }

// TaskFilter narrows a task search.
type TaskFilter struct {
	SpaceIDs      []string
	AssigneeIDs   []string
	IncludeClosed bool
}

// NewTask is a task to create.
type NewTask struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	MarkdownContent string   `json:"markdown_content,omitempty"`
	Assignees       []string `json:"assignees,omitempty"` // user IDs
	Tags            []string `json:"tags,omitempty"`
	Parent          string   `json:"parent,omitempty"` // the parent's task ID, for a subtask
}

// TaskUpdate holds the fields to change on a task; nil fields are left alone.
type TaskUpdate struct {
	Name            *string
	MarkdownContent *string // the description, as Markdown
	Status          *string
	Priority        *int   // 1 (urgent) to 4 (low); 0 clears it
	DueDate         *int64 // Unix milliseconds; 0 clears it
	AddAssignees    []string
	RemoveAssignees []string
}

// Empty reports whether the update changes nothing.
func (u TaskUpdate) Empty() bool {
	return u.Name == nil && u.MarkdownContent == nil && u.Status == nil && u.Priority == nil && u.DueDate == nil &&
		len(u.AddAssignees) == 0 && len(u.RemoveAssignees) == 0
}

// PriorityNames are the priorities in order; a name's index + 1 is its TaskUpdate value.
var PriorityNames = []string{"urgent", "high", "normal", "low"}

// Comment is a task comment.
type Comment struct {
	ID          FlexString       `json:"id"`
	CommentText string           `json:"comment_text"` // plain text
	Comment     []map[string]any `json:"comment"`      // rich text ops
	Date        FlexString       `json:"date"`         // Unix milliseconds
	User        User             `json:"user"`
}

// Attachment is a file attached to a task.
type Attachment struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Folder is a group of lists in a space.
type Folder struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Hidden bool   `json:"hidden"`
	Lists  []List `json:"lists"`
}

// Ops normalises comment ops given as []markdown.Op, []map[string]any or anything else
// that marshals to a JSON op list.
func Ops(ops any) ([]map[string]any, error) {
	if list, ok := ops.([]map[string]any); ok {
		return list, nil
	}
	data, err := json.Marshal(ops)
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	err = json.Unmarshal(data, &list)
	return list, err
}
