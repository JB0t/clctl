// Package local is clctl's own task backend: tasks, statuses, time entries and comments
// kept in a JSON file on disk, so clctl works standalone with no task-management account.
//
// The store is one file, <dir>/tasks.json, written atomically under a lock file so
// concurrent clctl runs (a cron digest and an interactive timer) do not lose changes.
// Attachments are copied into <dir>/attachments/<TASK-ID>/. The folder can live anywhere,
// including a synced or git-tracked one; it is separate from the notes folder.
//
// Spaces, lists and statuses work as they do in ClickUp. A new store has one space,
// "Personal" (task keys TASK-1, TASK-2, ...), with one list, "Inbox"; more can be added
// to tasks.json by hand (see the README).
package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

// UserID is the ID of the store's only user, you.
const UserID = "me"

// DefaultStatuses are a new list's statuses.
var DefaultStatuses = []tracker.Status{
	{Status: "to do", Type: "open", OrderIndex: 0},
	{Status: "in progress", Type: "custom", OrderIndex: 1},
	{Status: "blocked", Type: "custom", OrderIndex: 2},
	{Status: "done", Type: "closed", OrderIndex: 3},
}

// Space is a group of lists whose tasks share a key prefix.
type Space struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Prefix string         `json:"prefix"` // task keys are <prefix>-<number>
	Lists  []tracker.List `json:"lists"`
}

// Store is the content of tasks.json.
type Store struct {
	Version  int                          `json:"version"`
	Spaces   []Space                      `json:"spaces"`
	Tasks    []tracker.Task               `json:"tasks"`
	Comments map[string][]tracker.Comment `json:"comments,omitempty"` // by task ID, oldest first
	Entries  []tracker.TimeEntry          `json:"time_entries,omitempty"`
	Running  *tracker.TimeEntry           `json:"running,omitempty"`
	// NextIDs holds the next number per task prefix, and for "comment" and "entry".
	NextIDs map[string]int `json:"next_ids"`
}

// Backend is the local tracker.Backend.
type Backend struct {
	Dir      string
	UserName string
	now      func() time.Time
}

var _ tracker.Backend = (*Backend)(nil)

// Open returns the store in dir, creating it with a "Personal" space (task keys
// prefix-1, prefix-2, ...; TASK when prefix is "") when there is none yet.
func Open(dir, userName, prefix string) (*Backend, error) {
	if userName == "" {
		userName = os.Getenv("USER")
	}
	b := &Backend{Dir: dir, UserName: userName, now: time.Now}
	if _, err := os.Stat(b.path()); errors.Is(err, os.ErrNotExist) {
		if prefix == "" {
			prefix = "TASK"
		}
		err := b.update(func(s *Store) error {
			if len(s.Spaces) == 0 {
				s.Spaces = []Space{{ID: "personal", Name: "Personal", Prefix: strings.ToUpper(prefix),
					Lists: []tracker.List{{ID: "inbox", Name: "Inbox", Statuses: DefaultStatuses}}}}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *Backend) path() string { return filepath.Join(b.Dir, "tasks.json") }

func (b *Backend) Kind() string        { return "local" }
func (b *Backend) WorkspaceID() string { return "local:" + b.Dir }

// load reads the store without locking (writes are atomic renames).
func (b *Backend) load() (*Store, error) {
	store := &Store{Version: 1, NextIDs: map[string]int{}}
	data, err := os.ReadFile(b.path())
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("reading %s: %w", b.path(), err)
	}
	if store.NextIDs == nil {
		store.NextIDs = map[string]int{}
	}
	return store, nil
}

// update loads the store under the lock, applies change and saves it.
func (b *Backend) update(change func(*Store) error) error {
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(b.Dir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	store, err := b.load()
	if err != nil {
		return err
	}
	if err := change(store); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	temp := b.path() + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, b.path())
}

// lock takes an exclusive lock file, waiting up to 10s and breaking a lock older than 30s
// (left by a crashed run).
func lock(path string) (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the task store is locked by another clctl run (%s)", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (b *Backend) me() tracker.User {
	return tracker.User{ID: UserID, Username: b.UserName}
}

func (b *Backend) Me() (tracker.User, error)        { return b.me(), nil }
func (b *Backend) Members() ([]tracker.User, error) { return []tracker.User{b.me()}, nil }

func (b *Backend) Spaces() ([]tracker.Space, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	spaces := make([]tracker.Space, len(store.Spaces))
	for index, space := range store.Spaces {
		spaces[index] = tracker.Space{ID: space.ID, Name: space.Name}
	}
	return spaces, nil
}

func (b *Backend) Space(id string) (tracker.Space, error) {
	store, err := b.load()
	if err != nil {
		return tracker.Space{}, err
	}
	space, err := store.space(id)
	if err != nil {
		return tracker.Space{}, err
	}
	return tracker.Space{ID: space.ID, Name: space.Name}, nil
}

func (s *Store) space(id string) (*Space, error) {
	for index := range s.Spaces {
		if s.Spaces[index].ID == id {
			return &s.Spaces[index], nil
		}
	}
	return nil, fmt.Errorf("no space %q in the task store", id)
}

// list returns a list and its space.
func (s *Store) list(id string) (*Space, *tracker.List, error) {
	for spaceIndex := range s.Spaces {
		for listIndex := range s.Spaces[spaceIndex].Lists {
			if s.Spaces[spaceIndex].Lists[listIndex].ID == id {
				return &s.Spaces[spaceIndex], &s.Spaces[spaceIndex].Lists[listIndex], nil
			}
		}
	}
	return nil, nil, fmt.Errorf("no list %q in the task store", id)
}

func (b *Backend) SpaceLists(spaceID string) ([]tracker.List, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	space, err := store.space(spaceID)
	if err != nil {
		return nil, err
	}
	return space.Lists, nil
}

func (b *Backend) SpaceFolders(string) ([]tracker.Folder, error) { return nil, nil }

func (b *Backend) List(id string) (tracker.List, error) {
	store, err := b.load()
	if err != nil {
		return tracker.List{}, err
	}
	_, list, err := store.list(id)
	if err != nil {
		return tracker.List{}, err
	}
	return *list, nil
}

func (b *Backend) Tasks(filter tracker.TaskFilter) ([]tracker.Task, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	var found []tracker.Task
	for _, task := range store.Tasks {
		if len(filter.SpaceIDs) > 0 && !slices.Contains(filter.SpaceIDs, task.Space.ID) {
			continue
		}
		if !filter.IncludeClosed && task.Status.Type == "closed" {
			continue
		}
		if len(filter.AssigneeIDs) > 0 && !slices.ContainsFunc(filter.AssigneeIDs, task.AssignedTo) {
			continue
		}
		found = append(found, task)
	}
	return found, nil
}

// task returns a pointer to the stored task with this ID (keys ignore case).
func (s *Store) task(id string) (*tracker.Task, error) {
	for index := range s.Tasks {
		if s.Tasks[index].Matches(id) {
			return &s.Tasks[index], nil
		}
	}
	return nil, fmt.Errorf("no task %s in the task store", id)
}

func (b *Backend) Task(id string) (tracker.Task, error) {
	store, err := b.load()
	if err != nil {
		return tracker.Task{}, err
	}
	task, err := store.task(id)
	if err != nil {
		return tracker.Task{}, err
	}
	return *task, nil
}

func (b *Backend) TaskWithMarkdown(id string) (tracker.Task, error) { return b.Task(id) }

func (b *Backend) Subtasks(id string) ([]tracker.Task, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	parent, err := store.task(id)
	if err != nil {
		return nil, err
	}
	var subtasks []tracker.Task
	for _, task := range store.Tasks {
		if task.Parent == parent.ID {
			subtasks = append(subtasks, task)
		}
	}
	return subtasks, nil
}

func (b *Backend) CreateTask(listID string, task tracker.NewTask) (tracker.Task, error) {
	var created tracker.Task
	err := b.update(func(s *Store) error {
		space, list, err := s.list(listID)
		if err != nil {
			return err
		}
		if len(list.Statuses) == 0 {
			return fmt.Errorf("list %s has no statuses", list.Name)
		}
		if task.Parent != "" {
			parent, err := s.task(task.Parent)
			if err != nil {
				return err
			}
			task.Parent = parent.ID
		}
		s.NextIDs[space.Prefix]++
		key := fmt.Sprintf("%s-%d", space.Prefix, s.NextIDs[space.Prefix])
		description := task.MarkdownContent
		if description == "" {
			description = task.Description
		}
		created = tracker.Task{
			ID: key, CustomID: key, Name: task.Name, Description: description, MarkdownDescription: description,
			Status: list.Statuses[0], Parent: task.Parent,
			List: tracker.Ref{ID: list.ID, Name: list.Name}, Space: tracker.Ref{ID: space.ID, Name: space.Name},
			DateUpdated: tracker.FlexInt(b.now().UnixMilli()),
		}
		for _, id := range task.Assignees {
			if id == UserID {
				created.Assignees = []tracker.User{b.me()}
			}
		}
		for _, tag := range task.Tags {
			created.Tags = append(created.Tags, tracker.TaskTag{Name: strings.ToLower(tag)})
		}
		s.Tasks = append(s.Tasks, created)
		return nil
	})
	return created, err
}

// changeTask applies change to a stored task, stamps it updated and returns the result.
func (b *Backend) changeTask(id string, change func(*Store, *tracker.Task) error) (tracker.Task, error) {
	var changed tracker.Task
	err := b.update(func(s *Store) error {
		task, err := s.task(id)
		if err != nil {
			return err
		}
		if err := change(s, task); err != nil {
			return err
		}
		task.DateUpdated = tracker.FlexInt(b.now().UnixMilli())
		changed = *task
		return nil
	})
	return changed, err
}

func setStatus(s *Store, task *tracker.Task, status string) error {
	_, list, err := s.list(task.List.ID)
	if err != nil {
		return err
	}
	var names []string
	for _, candidate := range list.Statuses {
		if strings.EqualFold(candidate.Status, status) {
			task.Status = candidate
			return nil
		}
		names = append(names, candidate.Status)
	}
	return fmt.Errorf("%q is not a status of list %s; choose from: %s", status, list.Name, strings.Join(names, ", "))
}

func (b *Backend) SetStatus(id, status string) (tracker.Task, error) {
	return b.changeTask(id, func(s *Store, task *tracker.Task) error { return setStatus(s, task, status) })
}

func (b *Backend) UpdateTask(id string, update tracker.TaskUpdate) (tracker.Task, error) {
	return b.changeTask(id, func(s *Store, task *tracker.Task) error {
		if update.Status != nil {
			if err := setStatus(s, task, *update.Status); err != nil {
				return err
			}
		}
		if update.Name != nil {
			task.Name = *update.Name
		}
		if update.MarkdownContent != nil {
			task.Description, task.MarkdownDescription = *update.MarkdownContent, *update.MarkdownContent
		}
		if update.Priority != nil {
			task.Priority = nil
			if value := *update.Priority; value >= 1 && value <= len(tracker.PriorityNames) {
				task.Priority = &tracker.Priority{Priority: tracker.PriorityNames[value-1]}
			}
		}
		if update.DueDate != nil {
			task.DueDate = tracker.FlexInt(*update.DueDate)
		}
		if slices.Contains(update.RemoveAssignees, UserID) {
			task.Assignees = nil
		}
		if slices.Contains(update.AddAssignees, UserID) {
			task.Assignees = []tracker.User{b.me()}
		}
		return nil
	})
}

func (b *Backend) AddTag(id, tag string) error {
	_, err := b.changeTask(id, func(_ *Store, task *tracker.Task) error {
		tag = strings.ToLower(tag)
		if !slices.Contains(task.TagNames(), tag) {
			task.Tags = append(task.Tags, tracker.TaskTag{Name: tag})
		}
		return nil
	})
	return err
}

func (b *Backend) RemoveTag(id, tag string) error {
	_, err := b.changeTask(id, func(_ *Store, task *tracker.Task) error {
		task.Tags = slices.DeleteFunc(task.Tags, func(t tracker.TaskTag) bool { return strings.EqualFold(t.Name, tag) })
		return nil
	})
	return err
}

func (b *Backend) CurrentTimer() (*tracker.TimeEntry, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	return store.Running, nil
}

// entryTask returns the time-entry reference for a task ID, nil for "".
func (s *Store) entryTask(taskID string) (*tracker.TimeEntryTask, error) {
	if taskID == "" {
		return nil, nil
	}
	task, err := s.task(taskID)
	if err != nil {
		return nil, err
	}
	return &tracker.TimeEntryTask{ID: task.ID, CustomID: task.CustomID, Name: task.Name}, nil
}

func entryTags(tags []string) []tracker.TimeEntryTag {
	var named []tracker.TimeEntryTag
	for _, tag := range tags {
		named = append(named, tracker.TimeEntryTag{Name: tag})
	}
	return named
}

// finish turns the running timer, if any, into a logged entry ending now.
func (b *Backend) finish(s *Store) {
	if s.Running == nil {
		return
	}
	entry := *s.Running
	now := b.now().UnixMilli()
	entry.End, entry.Duration = tracker.FlexInt(now), tracker.FlexInt(now-int64(entry.Start))
	s.NextIDs["entry"]++
	entry.ID = strconv.Itoa(s.NextIDs["entry"])
	s.Entries = append(s.Entries, entry)
	s.Running = nil
}

func (b *Backend) StartTimer(taskID, description string, tags []string) error {
	return b.update(func(s *Store) error {
		task, err := s.entryTask(taskID)
		if err != nil {
			return err
		}
		b.finish(s) // one running timer, as in ClickUp: starting another stops it
		s.Running = &tracker.TimeEntry{ID: "running", Task: task, Description: description, Tags: entryTags(tags),
			Start: tracker.FlexInt(b.now().UnixMilli()), Duration: -1}
		return nil
	})
}

func (b *Backend) StopTimer() error {
	return b.update(func(s *Store) error {
		b.finish(s)
		return nil
	})
}

func (b *Backend) LogTime(taskID string, start, end time.Time, description string, tags []string) error {
	return b.update(func(s *Store) error {
		task, err := s.entryTask(taskID)
		if err != nil {
			return err
		}
		s.NextIDs["entry"]++
		s.Entries = append(s.Entries, tracker.TimeEntry{ID: strconv.Itoa(s.NextIDs["entry"]), Task: task,
			Description: description, Tags: entryTags(tags), Start: tracker.FlexInt(start.UnixMilli()),
			End: tracker.FlexInt(end.UnixMilli()), Duration: tracker.FlexInt(end.Sub(start).Milliseconds())})
		return nil
	})
}

func (b *Backend) TimeEntries(from, to time.Time) ([]tracker.TimeEntry, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	var entries []tracker.TimeEntry
	for _, entry := range store.Entries {
		if start := entry.StartTime(); !start.Before(from) && !start.After(to) {
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Start < entries[j].Start })
	return entries, nil
}

func (b *Backend) RawTimeEntries(from, to time.Time) ([]json.RawMessage, error) {
	entries, err := b.TimeEntries(from, to)
	if err != nil {
		return nil, err
	}
	raw := make([]json.RawMessage, len(entries))
	for index, entry := range entries {
		if raw[index], err = json.Marshal(entry); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func (b *Backend) TimeEntry(id string) (tracker.TimeEntry, error) {
	store, err := b.load()
	if err != nil {
		return tracker.TimeEntry{}, err
	}
	for _, entry := range store.Entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return tracker.TimeEntry{}, fmt.Errorf("no time entry %s", id)
}

func (b *Backend) UpdateTimeEntry(id string, start, end time.Time, description string) error {
	return b.update(func(s *Store) error {
		for index := range s.Entries {
			if entry := &s.Entries[index]; entry.ID == id {
				entry.Start, entry.End = tracker.FlexInt(start.UnixMilli()), tracker.FlexInt(end.UnixMilli())
				entry.Duration, entry.Description = tracker.FlexInt(end.Sub(start).Milliseconds()), description
				return nil
			}
		}
		return fmt.Errorf("no time entry %s", id)
	})
}

func (b *Backend) DeleteTimeEntry(id string) error {
	return b.update(func(s *Store) error {
		before := len(s.Entries)
		s.Entries = slices.DeleteFunc(s.Entries, func(entry tracker.TimeEntry) bool { return entry.ID == id })
		if len(s.Entries) == before {
			return fmt.Errorf("no time entry %s", id)
		}
		return nil
	})
}

func (b *Backend) Comments(taskID string) ([]tracker.Comment, error) {
	store, err := b.load()
	if err != nil {
		return nil, err
	}
	task, err := store.task(taskID)
	if err != nil {
		return nil, err
	}
	stored := store.Comments[task.ID]
	comments := make([]tracker.Comment, len(stored))
	for index, comment := range stored { // newest first, like the other backends
		comments[len(stored)-1-index] = comment
	}
	return comments, nil
}

func (b *Backend) PostComment(taskID string, ops any) (string, error) {
	list, err := tracker.Ops(ops)
	if err != nil {
		return "", err
	}
	var id string
	err = b.update(func(s *Store) error {
		task, err := s.task(taskID)
		if err != nil {
			return err
		}
		s.NextIDs["comment"]++
		id = strconv.Itoa(s.NextIDs["comment"])
		if s.Comments == nil {
			s.Comments = map[string][]tracker.Comment{}
		}
		s.Comments[task.ID] = append(s.Comments[task.ID], tracker.Comment{ID: tracker.FlexString(id),
			CommentText: markdown.PlainText(list), Comment: list,
			Date: tracker.FlexString(strconv.FormatInt(b.now().UnixMilli(), 10)), User: b.me()})
		return nil
	})
	return id, err
}

func (b *Backend) UpdateComment(commentID string, ops any) error {
	list, err := tracker.Ops(ops)
	if err != nil {
		return err
	}
	return b.update(func(s *Store) error {
		for taskID, comments := range s.Comments {
			for index := range comments {
				if string(comments[index].ID) == commentID {
					s.Comments[taskID][index].Comment = list
					s.Comments[taskID][index].CommentText = markdown.PlainText(list)
					return nil
				}
			}
		}
		return fmt.Errorf("no comment %s", commentID)
	})
}

// UploadAttachment copies the file into <dir>/attachments/<TASK-ID>/.
func (b *Backend) UploadAttachment(taskID, path string) (tracker.Attachment, error) {
	task, err := b.Task(taskID)
	if err != nil {
		return tracker.Attachment{}, err
	}
	source, err := os.Open(path)
	if err != nil {
		return tracker.Attachment{}, err
	}
	defer source.Close()
	dir := filepath.Join(b.Dir, "attachments", task.Ref())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tracker.Attachment{}, err
	}
	target := filepath.Join(dir, filepath.Base(path))
	file, err := os.Create(target)
	if err != nil {
		return tracker.Attachment{}, err
	}
	if _, err := io.Copy(file, source); err != nil {
		file.Close()
		return tracker.Attachment{}, err
	}
	if err := file.Close(); err != nil {
		return tracker.Attachment{}, err
	}
	return tracker.Attachment{ID: filepath.Base(path), URL: "file://" + target, Title: filepath.Base(path)}, nil
}
