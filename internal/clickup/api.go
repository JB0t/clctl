package clickup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Me returns the user who owns the API token.
func (c *Client) Me() (User, error) {
	var response struct {
		User User `json:"user"`
	}
	err := c.Do(http.MethodGet, "/user", nil, nil, &response)
	return response.User, err
}

// Teams returns the workspaces ("teams" in the API) the token can access.
func (c *Client) Teams() ([]Team, error) {
	var response struct {
		Teams []Team `json:"teams"`
	}
	err := c.Do(http.MethodGet, "/team", nil, nil, &response)
	return response.Teams, err
}

// Spaces returns the workspace's non-archived spaces.
func (c *Client) Spaces() ([]Space, error) {
	var response struct {
		Spaces []Space `json:"spaces"`
	}
	query := url.Values{"archived": {"false"}}
	err := c.Do(http.MethodGet, "/team/"+c.TeamID+"/space", query, nil, &response)
	return response.Spaces, err
}

// Space fetches one space.
func (c *Client) Space(id string) (Space, error) {
	var space Space
	err := c.Do(http.MethodGet, "/space/"+id, nil, nil, &space)
	return space, err
}

// Members returns the workspace's members.
func (c *Client) Members() ([]User, error) {
	teams, err := c.Teams()
	if err != nil {
		return nil, err
	}
	for _, team := range teams {
		if team.ID == c.TeamID {
			users := make([]User, 0, len(team.Members))
			for _, member := range team.Members {
				users = append(users, member.User)
			}
			return users, nil
		}
	}
	return nil, fmt.Errorf("workspace %s not found for this token", c.TeamID)
}

// Tasks returns every task matching the filter. ClickUp pages 100 tasks at a time
// without a total count, so pages are fetched in parallel waves until one comes back short.
func (c *Client) Tasks(filter TaskFilter) ([]Task, error) {
	const wave = 4
	var tasks []Task
	for first := 0; ; first += wave {
		pages := make([][]Task, wave)
		lastPage := make([]bool, wave)
		errs := make([]error, wave)
		var group sync.WaitGroup
		for offset := range wave {
			group.Add(1)
			go func() {
				defer group.Done()
				pages[offset], lastPage[offset], errs[offset] = c.teamTaskPage(filter, first+offset)
			}()
		}
		group.Wait()
		for offset := range wave {
			if errs[offset] != nil {
				return nil, errs[offset]
			}
			tasks = append(tasks, pages[offset]...)
			if lastPage[offset] {
				return tasks, nil
			}
		}
	}
}

func (c *Client) teamTaskPage(filter TaskFilter, page int) ([]Task, bool, error) {
	query := url.Values{
		"subtasks":       {"true"},
		"include_closed": {strconv.FormatBool(filter.IncludeClosed)},
		"page":           {strconv.Itoa(page)},
	}
	for _, id := range filter.SpaceIDs {
		query.Add("space_ids[]", id)
	}
	for _, id := range filter.AssigneeIDs {
		query.Add("assignees[]", id)
	}
	var response struct {
		Tasks    []Task `json:"tasks"`
		LastPage *bool  `json:"last_page"`
	}
	if err := c.Do(http.MethodGet, "/team/"+c.TeamID+"/task", query, nil, &response); err != nil {
		return nil, false, err
	}
	last := len(response.Tasks) < 100 || (response.LastPage != nil && *response.LastPage)
	return response.Tasks, last, nil
}

// Task fetches one task by ID or custom ID.
func (c *Client) Task(id string) (Task, error) {
	var task Task
	err := c.Do(http.MethodGet, "/task/"+id, c.TaskQuery(id), nil, &task)
	return task, err
}

// TaskWithMarkdown fetches one task with its description as Markdown.
func (c *Client) TaskWithMarkdown(id string) (Task, error) {
	var task Task
	query := c.TaskQuery(id)
	query.Set("include_markdown_description", "true")
	err := c.Do(http.MethodGet, "/task/"+id, query, nil, &task)
	return task, err
}

// Subtasks returns a task's direct subtasks.
func (c *Client) Subtasks(id string) ([]Task, error) {
	var task struct {
		Subtasks []Task `json:"subtasks"`
	}
	query := c.TaskQuery(id)
	query.Set("include_subtasks", "true")
	err := c.Do(http.MethodGet, "/task/"+id, query, nil, &task)
	return task.Subtasks, err
}

// SetStatus changes a task's status and returns the updated task.
func (c *Client) SetStatus(id, status string) (Task, error) {
	var task Task
	err := c.Do(http.MethodPut, "/task/"+id, c.TaskQuery(id), map[string]string{"status": status}, &task)
	return task, err
}

// UpdateTask changes a task's fields and returns the updated task.
func (c *Client) UpdateTask(id string, update TaskUpdate) (Task, error) {
	body := map[string]any{}
	if update.Name != nil {
		body["name"] = *update.Name
	}
	if update.MarkdownContent != nil {
		body["markdown_content"] = *update.MarkdownContent
	}
	if update.Status != nil {
		body["status"] = *update.Status
	}
	// Clearing takes an explicit null.
	if update.Priority != nil {
		body["priority"] = nil
		if *update.Priority != 0 {
			body["priority"] = *update.Priority
		}
	}
	if update.DueDate != nil {
		body["due_date"] = nil
		if *update.DueDate != 0 {
			body["due_date"] = *update.DueDate
		}
	}
	if len(update.AddAssignees) > 0 || len(update.RemoveAssignees) > 0 {
		body["assignees"] = map[string][]int64{"add": userIDs(update.AddAssignees), "rem": userIDs(update.RemoveAssignees)}
	}
	var task Task
	err := c.Do(http.MethodPut, "/task/"+id, c.TaskQuery(id), body, &task)
	return task, err
}

// userIDs turns user IDs into the numbers ClickUp takes (never nil, which it rejects).
func userIDs(ids []string) []int64 {
	numbers := []int64{}
	for _, id := range ids {
		if number, err := strconv.ParseInt(id, 10, 64); err == nil {
			numbers = append(numbers, number)
		}
	}
	return numbers
}

// AddTag adds a tag to a task.
func (c *Client) AddTag(id, tag string) error {
	return c.Do(http.MethodPost, "/task/"+id+"/tag/"+url.PathEscape(tag), c.TaskQuery(id), nil, nil)
}

// RemoveTag removes a tag from a task.
func (c *Client) RemoveTag(id, tag string) error {
	return c.Do(http.MethodDelete, "/task/"+id+"/tag/"+url.PathEscape(tag), c.TaskQuery(id), nil, nil)
}

// List fetches a list, including its statuses.
func (c *Client) List(id string) (List, error) {
	var list List
	err := c.Do(http.MethodGet, "/list/"+id, nil, nil, &list)
	return list, err
}

// CurrentTimer returns the token owner's running time entry, or nil.
// ClickUp allows one running timer per user and leaves it out of the time-entry history.
func (c *Client) CurrentTimer() (*TimeEntry, error) {
	var response struct {
		Data *TimeEntry `json:"data"`
	}
	if err := c.Do(http.MethodGet, "/team/"+c.TeamID+"/time_entries/current", nil, nil, &response); err != nil {
		return nil, err
	}
	if response.Data == nil || response.Data.ID == "" {
		return nil, nil
	}
	return response.Data, nil
}

// StartTimer starts the token owner's timer on a task, or on no task when taskID is ""
// (then the description and tags say what it was for).
func (c *Client) StartTimer(taskID, description string, tags []string) error {
	body := entryBody(taskID, description, tags)
	return c.Do(http.MethodPost, "/team/"+c.TeamID+"/time_entries/start", c.TaskQuery(taskID), body, nil)
}

// entryBody is the shared part of a time entry: its task (left out when taskID is ""),
// description and tags.
func entryBody(taskID, description string, tags []string) map[string]any {
	body := map[string]any{"billable": false}
	if taskID != "" {
		body["tid"] = taskID
	}
	if description != "" {
		body["description"] = description
	}
	if len(tags) > 0 {
		named := make([]TimeEntryTag, len(tags))
		for index, tag := range tags {
			named[index] = TimeEntryTag{Name: tag}
		}
		body["tags"] = named
	}
	return body
}

// StopTimer stops the token owner's running timer.
func (c *Client) StopTimer() error {
	return c.Do(http.MethodPost, "/team/"+c.TeamID+"/time_entries/stop", nil, nil, nil)
}

// LogTime records a finished time entry against a task, or against no task when taskID
// is "".
func (c *Client) LogTime(taskID string, start, end time.Time, description string, tags []string) error {
	body := entryBody(taskID, description, tags)
	body["start"] = start.UnixMilli()
	body["end"] = end.UnixMilli()
	body["duration"] = end.Sub(start).Milliseconds()
	return c.Do(http.MethodPost, "/team/"+c.TeamID+"/time_entries", c.TaskQuery(taskID), body, nil)
}

// TimeEntries returns the token owner's time entries that started between from and to.
func (c *Client) TimeEntries(from, to time.Time) ([]TimeEntry, error) {
	var response struct {
		Data []TimeEntry `json:"data"`
	}
	query := url.Values{
		"start_date": {strconv.FormatInt(from.UnixMilli(), 10)},
		"end_date":   {strconv.FormatInt(to.UnixMilli(), 10)},
	}
	err := c.Do(http.MethodGet, "/team/"+c.TeamID+"/time_entries", query, nil, &response)
	return response.Data, err
}

// RawTimeEntries returns the token owner's time entries that started between from and
// to as ClickUp sent them, for inspecting the stored fields.
func (c *Client) RawTimeEntries(from, to time.Time) ([]json.RawMessage, error) {
	var response struct {
		Data []json.RawMessage `json:"data"`
	}
	query := url.Values{
		"start_date": {strconv.FormatInt(from.UnixMilli(), 10)},
		"end_date":   {strconv.FormatInt(to.UnixMilli(), 10)},
	}
	err := c.Do(http.MethodGet, "/team/"+c.TeamID+"/time_entries", query, nil, &response)
	return response.Data, err
}

// TimeEntry fetches one time entry.
func (c *Client) TimeEntry(id string) (TimeEntry, error) {
	var response struct {
		Data TimeEntry `json:"data"`
	}
	err := c.Do(http.MethodGet, "/team/"+c.TeamID+"/time_entries/"+id, nil, nil, &response)
	return response.Data, err
}

// UpdateTimeEntry changes a finished time entry's range and description.
func (c *Client) UpdateTimeEntry(id string, start, end time.Time, description string) error {
	body := map[string]any{
		"start":       start.UnixMilli(),
		"end":         end.UnixMilli(),
		"duration":    end.Sub(start).Milliseconds(),
		"description": description,
	}
	return c.Do(http.MethodPut, "/team/"+c.TeamID+"/time_entries/"+id, nil, body, nil)
}

// DeleteTimeEntry removes a time entry.
func (c *Client) DeleteTimeEntry(id string) error {
	return c.Do(http.MethodDelete, "/team/"+c.TeamID+"/time_entries/"+id, nil, nil, nil)
}

// Comments returns every comment on a task (the API pages 25 at a time, newest first).
func (c *Client) Comments(taskID string) ([]Comment, error) {
	var all []Comment
	query := c.TaskQuery(taskID)
	for {
		var response struct {
			Comments []Comment `json:"comments"`
		}
		if err := c.Do(http.MethodGet, "/task/"+taskID+"/comment", query, nil, &response); err != nil {
			return nil, err
		}
		all = append(all, response.Comments...)
		if len(response.Comments) < 25 {
			return all, nil
		}
		last := response.Comments[len(response.Comments)-1]
		query.Set("start", string(last.Date))
		query.Set("start_id", string(last.ID))
	}
}

// PostComment adds a comment built from ops and returns its ID.
func (c *Client) PostComment(taskID string, ops any) (string, error) {
	var response struct {
		ID FlexString `json:"id"`
	}
	body := map[string]any{"comment": ops, "notify_all": false}
	err := c.Do(http.MethodPost, "/task/"+taskID+"/comment", c.TaskQuery(taskID), body, &response)
	return string(response.ID), err
}

// UpdateComment replaces a comment's content, keeping its formatting.
func (c *Client) UpdateComment(commentID string, ops any) error {
	return c.Do(http.MethodPut, "/comment/"+commentID, nil, map[string]any{"comment": ops}, nil)
}

// UploadAttachment attaches a local file to a task.
func (c *Client) UploadAttachment(taskID, path string) (Attachment, error) {
	file, err := os.Open(path)
	if err != nil {
		return Attachment{}, err
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("attachment", filepath.Base(path))
	if err != nil {
		return Attachment{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return Attachment{}, err
	}
	if err := writer.Close(); err != nil {
		return Attachment{}, err
	}
	var attachment Attachment
	err = c.Upload("/task/"+taskID+"/attachment", c.TaskQuery(taskID), writer.FormDataContentType(), body.Bytes(), &attachment)
	return attachment, err
}

// SpaceFolders returns a space's non-archived folders, including their lists.
func (c *Client) SpaceFolders(spaceID string) ([]Folder, error) {
	var response struct {
		Folders []Folder `json:"folders"`
	}
	err := c.Do(http.MethodGet, "/space/"+spaceID+"/folder", url.Values{"archived": {"false"}}, nil, &response)
	return response.Folders, err
}

// SpaceLists returns the lists sitting directly in a space (not in a folder).
func (c *Client) SpaceLists(spaceID string) ([]List, error) {
	var response struct {
		Lists []List `json:"lists"`
	}
	err := c.Do(http.MethodGet, "/space/"+spaceID+"/list", url.Values{"archived": {"false"}}, nil, &response)
	return response.Lists, err
}

// CreateTask creates a task in a list.
func (c *Client) CreateTask(listID string, task NewTask) (Task, error) {
	payload := struct {
		NewTask
		Assignees []int64 `json:"assignees,omitempty"`
	}{NewTask: task, Assignees: userIDs(task.Assignees)}
	if len(payload.Assignees) == 0 {
		payload.Assignees = nil
	}
	var created Task
	err := c.Do(http.MethodPost, "/list/"+listID+"/task", nil, payload, &created)
	return created, err
}
