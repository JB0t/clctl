package tracker

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// FlexInt decodes numbers that arrive either as JSON numbers or numeric strings
// (ClickUp's time-entry start/duration and dates).
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*f = FlexInt(value)
	return nil
}

// FlexString decodes IDs that arrive either as strings or numbers (user and comment IDs).
type FlexString string

func (f *FlexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*f = FlexString(text)
		return nil
	}
	*f = FlexString(strings.TrimSpace(string(data)))
	return nil
}

func (f FlexString) MarshalJSON() ([]byte, error) { return json.Marshal(string(f)) }

type User struct {
	ID       FlexString `json:"id"`
	Username string     `json:"username"`
	Email    string     `json:"email"`
}

// DisplayName returns the username, falling back to the email.
func (u User) DisplayName() string {
	if u.Username != "" {
		return u.Username
	}
	return u.Email
}

type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Status is a task status. Type is one of "open" (not started), "custom" (in progress),
// "done" (finished but still listed) or "closed" (finished and left out of the cache),
// the categories ClickUp uses; other backends map their own status categories onto them.
type Status struct {
	Status     string  `json:"status"`
	Type       string  `json:"type"`
	Color      string  `json:"color,omitempty"`
	OrderIndex FlexInt `json:"orderindex,omitempty"`
}

// Done reports whether the status is a done or closed one.
func (s Status) Done() bool { return s.Type == "done" || s.Type == "closed" }

type Priority struct {
	Priority string `json:"priority"`
}

type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type FolderRef struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// Task is a task as clctl sees it, whichever backend holds it. Its JSON form is the
// task cache's format (and ClickUp's wire format, which it started from).
type Task struct {
	ID          string    `json:"id"`
	CustomID    string    `json:"custom_id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      Status    `json:"status"`
	Priority    *Priority `json:"priority"`
	Assignees   []User    `json:"assignees"`
	Parent      string    `json:"parent,omitempty"`
	List        Ref       `json:"list"`
	Folder      FolderRef `json:"folder"`
	Space       Ref       `json:"space"`
	URL         string    `json:"url"`
	DateUpdated FlexInt   `json:"date_updated,omitempty"`
	DueDate     FlexInt   `json:"due_date,omitempty"` // Unix milliseconds, 0 when none
	Tags        []TaskTag `json:"tags,omitempty"`
	// MarkdownDescription is only sent when asked for (TaskWithMarkdown).
	MarkdownDescription string `json:"markdown_description,omitempty"`
}

// TaskTag is a tag on a task.
type TaskTag struct {
	Name string `json:"name"`
}

// TagNames returns the task's tag names.
func (t Task) TagNames() []string {
	names := make([]string, 0, len(t.Tags))
	for _, tag := range t.Tags {
		names = append(names, tag.Name)
	}
	return names
}

// Ref returns the ID people type for a task: its custom ID when it has one.
func (t Task) Ref() string {
	if t.CustomID != "" {
		return t.CustomID
	}
	return t.ID
}

// PriorityName returns the priority ("urgent", "high", ...) or "".
func (t Task) PriorityName() string {
	if t.Priority == nil {
		return ""
	}
	return t.Priority.Priority
}

// AssignedTo reports whether the user ID is among the task's assignees.
func (t Task) AssignedTo(userID string) bool {
	for _, assignee := range t.Assignees {
		if string(assignee.ID) == userID {
			return true
		}
	}
	return false
}

// Matches reports whether id is the task's ID or custom ID (custom IDs ignore case).
func (t Task) Matches(id string) bool {
	return t.ID == id || (t.CustomID != "" && strings.EqualFold(t.CustomID, id))
}

type List struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Statuses []Status `json:"statuses"`
}

type TimeEntryTask struct {
	ID       string `json:"id"`
	CustomID string `json:"custom_id,omitempty"`
	Name     string `json:"name"`
}

// Ref returns the custom ID when present, else the task ID.
func (t TimeEntryTask) Ref() string {
	if t.CustomID != "" {
		return t.CustomID
	}
	return t.ID
}

type TimeEntry struct {
	ID          string         `json:"id"`
	Task        *TimeEntryTask `json:"task"`
	Start       FlexInt        `json:"start"`
	End         FlexInt        `json:"end"`
	Duration    FlexInt        `json:"duration"` // negative while running
	Description string         `json:"description"`
	Tags        []TimeEntryTag `json:"tags,omitempty"` // labels; all a task-less entry has besides its description
}

// TimeEntryTag is a time-entry label (a ClickUp time-entry tag).
type TimeEntryTag struct {
	Name string `json:"name"`
}

// TagNames returns the entry's tag names.
func (e TimeEntry) TagNames() []string {
	names := make([]string, len(e.Tags))
	for index, tag := range e.Tags {
		names[index] = tag.Name
	}
	return names
}

// StartTime returns when the entry started.
func (e TimeEntry) StartTime() time.Time { return time.UnixMilli(int64(e.Start)) }

// EndTime returns when the entry ended, from start + duration when the end is missing.
func (e TimeEntry) EndTime() time.Time {
	if e.End != 0 {
		return time.UnixMilli(int64(e.End))
	}
	return time.UnixMilli(int64(e.Start + e.Duration))
}

// Running reports whether the entry is a timer that has not been stopped.
func (e TimeEntry) Running() bool { return e.Duration < 0 }
