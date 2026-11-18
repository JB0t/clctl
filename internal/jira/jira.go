// Package jira is clctl's Jira backend, for Jira Cloud and Jira Data Center / Server,
// over the REST API (v2, plus v3's paged issue search on Cloud).
//
// Jira maps onto clctl's model like this:
//
//   - a project is a space (its key is the space ID, so `spaces` in the config lists
//     project keys);
//   - each of a project's issue types is a list (ID "<PROJECT>/<issue type ID>"), whose
//     statuses are that issue type's workflow statuses;
//   - an issue is a task: its key (PROJ-123) is the key people type, its labels are tags,
//     and status changes go through the workflow's transitions;
//   - Jira's status categories become clctl's status types: To Do is "open", In Progress
//     "custom" and Done "closed";
//   - time is logged as worklogs. Jira has no running timer, so clctl keeps it in a
//     local file and logs a worklog when it stops. A worklog needs an issue, so time
//     without a task goes on jira_misc_issue when that is set;
//   - descriptions and comments are Jira wiki markup, converted to and from Markdown.
//
// Time-entry IDs are "<issue key>:<worklog ID>" and comment IDs "<issue key>:<comment
// ID>", since Jira addresses both through their issue.
package jira

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

// Options configure a Client.
type Options struct {
	URL       string   // site URL, e.g. https://example.atlassian.net
	Email     string   // Cloud account email (basic auth); "" signs in with a bearer token
	Token     string   // Cloud API token, or a Data Center personal access token
	Projects  []string // project keys clctl works with; nil means all visible projects
	MiscIssue string   // issue key that time without a task is logged on
	TimerPath string   // file that holds the running timer
}

// Client is a Jira tracker.Backend.
type Client struct {
	options Options
	base    string
	timer   tracker.TimerFile
	HTTP    *http.Client
	// MaxRetries bounds retries on HTTP 429 (rate limited).
	MaxRetries int

	mu         sync.Mutex
	me         *tracker.User
	priorities []string // the site's priority names, highest first
	searchAPI  string   // the issue-search endpoint that answered
}

var _ tracker.Backend = (*Client)(nil)

// New returns a client for the Jira site in options.
func New(options Options) *Client {
	return &Client{
		options:    options,
		base:       strings.TrimRight(options.URL, "/"),
		timer:      tracker.TimerFile{Path: options.TimerPath},
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		MaxRetries: 3,
	}
}

func (c *Client) Kind() string        { return "jira" }
func (c *Client) WorkspaceID() string { return c.base }

// cloud reports whether the site is Jira Cloud (accounts have account IDs, and issue
// search uses the paged /search/jql endpoint).
func (c *Client) cloud() bool {
	return c.options.Email != "" || strings.HasSuffix(hostOf(c.base), ".atlassian.net")
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// APIError is a non-2xx response from Jira.
type APIError struct {
	Status   int
	Messages []string
}

func (e *APIError) Error() string {
	if len(e.Messages) == 0 {
		return fmt.Sprintf("Jira API returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("Jira API: %s (HTTP %d)", strings.Join(e.Messages, "; "), e.Status)
}

func statusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// do sends a JSON request and decodes the JSON response into out (which may be nil).
func (c *Client) do(method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	header := http.Header{}
	if body != nil {
		header.Set("Content-Type", "application/json")
	}
	return c.send(method, path, query, header, payload, out)
}

func (c *Client) send(method, path string, query url.Values, header http.Header, payload []byte, out any) error {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		request, err := http.NewRequest(method, target, reader)
		if err != nil {
			return err
		}
		for key, values := range header {
			request.Header[key] = values
		}
		request.Header.Set("Accept", "application/json")
		if c.options.Email != "" {
			request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.options.Email+":"+c.options.Token)))
		} else {
			request.Header.Set("Authorization", "Bearer "+c.options.Token)
		}
		response, err := c.HTTP.Do(request)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt < c.MaxRetries {
			wait := 2 * time.Second
			if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
				wait = time.Duration(min(seconds, 60)) * time.Second
			}
			time.Sleep(wait)
			continue
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			apiErr := &APIError{Status: response.StatusCode}
			var detail struct {
				ErrorMessages []string          `json:"errorMessages"`
				Errors        map[string]string `json:"errors"`
				Message       string            `json:"message"`
			}
			if json.Unmarshal(data, &detail) == nil {
				apiErr.Messages = append(apiErr.Messages, detail.ErrorMessages...)
				for field, message := range detail.Errors {
					apiErr.Messages = append(apiErr.Messages, field+": "+message)
				}
				if detail.Message != "" {
					apiErr.Messages = append(apiErr.Messages, detail.Message)
				}
				sort.Strings(apiErr.Messages[len(detail.ErrorMessages):])
			}
			return apiErr
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		return json.Unmarshal(data, out)
	}
}

// Jira payloads.

type user struct {
	AccountID    string `json:"accountId,omitempty"` // Cloud
	Name         string `json:"name,omitempty"`      // Data Center
	Key          string `json:"key,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
	EmailAddress string `json:"emailAddress,omitempty"`
}

func (u user) id() string {
	if u.AccountID != "" {
		return u.AccountID
	}
	return u.Name
}

func (u user) toUser() tracker.User {
	return tracker.User{ID: tracker.FlexString(u.id()), Username: u.DisplayName, Email: u.EmailAddress}
}

// userRef is how a user is written in a request: by account ID on Cloud, by name on
// Data Center.
func (c *Client) userRef(id string) map[string]string {
	if c.cloud() {
		return map[string]string{"accountId": id}
	}
	return map[string]string{"name": id}
}

type project struct {
	ID         string      `json:"id"`
	Key        string      `json:"key"`
	Name       string      `json:"name"`
	IssueTypes []issueType `json:"issueTypes,omitempty"`
}

type issueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type status struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"` // new, indeterminate, done
	} `json:"statusCategory"`
}

type issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"` // wiki markup (v2), or a document (v3)
		Status      status          `json:"status"`
		Priority    *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Assignee *user `json:"assignee"`
		Parent   *struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"parent"`
		Project   project   `json:"project"`
		IssueType issueType `json:"issuetype"`
		Labels    []string  `json:"labels"`
		DueDate   string    `json:"duedate"`
		Updated   string    `json:"updated"`
	} `json:"fields"`
}

const issueFields = "summary,status,priority,assignee,parent,project,issuetype,labels,duedate,updated"

// jiraTime is Jira's timestamp format.
const jiraTime = "2006-01-02T15:04:05.000-0700"

// statusType maps a Jira status category onto clctl's status types.
func statusType(category string) string {
	switch category {
	case "new":
		return "open"
	case "done":
		return "closed"
	}
	return "custom"
}

func toStatus(s status) tracker.Status {
	return tracker.Status{Status: s.Name, Type: statusType(s.StatusCategory.Key)}
}

// priorityGroups map Jira's usual priority names onto clctl's four.
var priorityGroups = map[string][]string{
	"urgent": {"highest", "blocker", "critical", "urgent"},
	"high":   {"high", "major"},
	"normal": {"medium", "normal"},
	"low":    {"low", "minor", "lowest", "trivial"},
}

func priorityName(jira string) string {
	jira = strings.ToLower(jira)
	for name, group := range priorityGroups {
		if slices.Contains(group, jira) {
			return name
		}
	}
	return ""
}

func listID(projectKey, issueTypeID string) string { return projectKey + "/" + issueTypeID }

func (c *Client) toTask(item issue) tracker.Task {
	fields := item.Fields
	task := tracker.Task{
		ID: item.ID, CustomID: item.Key, Name: fields.Summary, Status: toStatus(fields.Status),
		List:  tracker.Ref{ID: listID(fields.Project.Key, fields.IssueType.ID), Name: fields.IssueType.Name},
		Space: tracker.Ref{ID: fields.Project.Key, Name: fields.Project.Name},
		URL:   c.base + "/browse/" + item.Key,
	}
	if fields.Priority != nil {
		if name := priorityName(fields.Priority.Name); name != "" {
			task.Priority = &tracker.Priority{Priority: name}
		}
	}
	if fields.Assignee != nil {
		task.Assignees = []tracker.User{fields.Assignee.toUser()}
	}
	if fields.Parent != nil {
		task.Parent = fields.Parent.ID
	}
	for _, label := range fields.Labels {
		task.Tags = append(task.Tags, tracker.TaskTag{Name: label})
	}
	if updated, err := time.Parse(jiraTime, fields.Updated); err == nil {
		task.DateUpdated = tracker.FlexInt(updated.UnixMilli())
	}
	if due, err := time.ParseInLocation("2006-01-02", fields.DueDate, time.Local); err == nil {
		task.DueDate = tracker.FlexInt(due.UnixMilli())
	}
	var wiki string
	if json.Unmarshal(fields.Description, &wiki) == nil && wiki != "" {
		task.MarkdownDescription = WikiToMarkdown(wiki)
		task.Description = task.MarkdownDescription
	}
	return task
}

// People.

func (c *Client) Me() (tracker.User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.me != nil {
		return *c.me, nil
	}
	var me user
	if err := c.do(http.MethodGet, "/rest/api/2/myself", nil, nil, &me); err != nil {
		return tracker.User{}, err
	}
	converted := me.toUser()
	c.me = &converted
	return converted, nil
}

func (c *Client) projectKeys() ([]string, error) {
	if len(c.options.Projects) > 0 {
		return c.options.Projects, nil
	}
	spaces, err := c.Spaces()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, space := range spaces {
		keys = append(keys, space.ID)
	}
	return keys, nil
}

// Members returns the people issues in the configured projects can be assigned to.
func (c *Client) Members() ([]tracker.User, error) {
	keys, err := c.projectKeys()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		me, err := c.Me()
		return []tracker.User{me}, err
	}
	var users []user
	query := url.Values{"projectKeys": {strings.Join(keys, ",")}, "maxResults": {"1000"}}
	if err := c.do(http.MethodGet, "/rest/api/2/user/assignable/multiProjectSearch", query, nil, &users); err != nil {
		return nil, err
	}
	members := make([]tracker.User, 0, len(users))
	for _, member := range users {
		members = append(members, member.toUser())
	}
	return members, nil
}

// Projects, issue types and statuses.

func (c *Client) Spaces() ([]tracker.Space, error) {
	var projects []project
	if err := c.do(http.MethodGet, "/rest/api/2/project", nil, nil, &projects); err != nil {
		return nil, err
	}
	spaces := make([]tracker.Space, 0, len(projects))
	for _, p := range projects {
		spaces = append(spaces, tracker.Space{ID: p.Key, Name: p.Name})
	}
	return spaces, nil
}

func (c *Client) project(key string) (project, error) {
	var p project
	err := c.do(http.MethodGet, "/rest/api/2/project/"+url.PathEscape(key), nil, nil, &p)
	return p, err
}

func (c *Client) Space(id string) (tracker.Space, error) {
	p, err := c.project(id)
	return tracker.Space{ID: p.Key, Name: p.Name}, err
}

// SpaceLists returns a project's issue types (other than subtask types) as lists.
func (c *Client) SpaceLists(spaceID string) ([]tracker.List, error) {
	p, err := c.project(spaceID)
	if err != nil {
		return nil, err
	}
	var lists []tracker.List
	for _, kind := range p.IssueTypes {
		if !kind.Subtask {
			lists = append(lists, tracker.List{ID: listID(p.Key, kind.ID), Name: kind.Name})
		}
	}
	return lists, nil
}

func (c *Client) SpaceFolders(string) ([]tracker.Folder, error) { return nil, nil }

// List returns an issue type's workflow statuses.
func (c *Client) List(id string) (tracker.List, error) {
	projectKey, typeID, _ := strings.Cut(id, "/")
	var types []struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Statuses []status `json:"statuses"`
	}
	if err := c.do(http.MethodGet, "/rest/api/2/project/"+url.PathEscape(projectKey)+"/statuses", nil, nil, &types); err != nil {
		return tracker.List{}, err
	}
	list := tracker.List{ID: id, Name: projectKey}
	seen := map[string]bool{}
	for _, kind := range types {
		if typeID != "" && kind.ID != typeID {
			continue
		}
		if typeID != "" {
			list.Name = kind.Name
		}
		for _, s := range kind.Statuses {
			if !seen[strings.ToLower(s.Name)] {
				seen[strings.ToLower(s.Name)] = true
				converted := toStatus(s)
				converted.OrderIndex = tracker.FlexInt(len(list.Statuses))
				list.Statuses = append(list.Statuses, converted)
			}
		}
	}
	if len(list.Statuses) == 0 {
		return list, fmt.Errorf("no statuses found for %s", id)
	}
	return list, nil
}

// Issues.

func jqlList(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(value)
	}
	return "(" + strings.Join(quoted, ",") + ")"
}

// search runs a JQL query, following pages. Jira Cloud pages with nextPageToken on
// /rest/api/3/search/jql (it has retired /search); Data Center uses /rest/api/2/search.
func (c *Client) search(jql, fields string) ([]issue, error) {
	c.mu.Lock()
	api := c.searchAPI
	c.mu.Unlock()
	candidates := []string{"/rest/api/2/search"}
	if c.cloud() {
		candidates = []string{"/rest/api/3/search/jql", "/rest/api/2/search"}
	}
	if api != "" {
		candidates = []string{api}
	}
	var lastErr error
	for _, path := range candidates {
		issues, err := c.searchWith(path, jql, fields)
		if status := statusOf(err); status == http.StatusNotFound || status == http.StatusGone {
			lastErr = err
			continue
		}
		if err == nil {
			c.mu.Lock()
			c.searchAPI = path
			c.mu.Unlock()
		}
		return issues, err
	}
	return nil, lastErr
}

func (c *Client) searchWith(path, jql, fields string) ([]issue, error) {
	var all []issue
	query := url.Values{"jql": {jql}, "fields": {fields}, "maxResults": {"100"}}
	for startAt := 0; ; {
		var page struct {
			Issues        []issue `json:"issues"`
			Total         int     `json:"total"`
			NextPageToken string  `json:"nextPageToken"`
			IsLast        *bool   `json:"isLast"`
		}
		if strings.HasSuffix(path, "/jql") {
			if err := c.do(http.MethodGet, path, query, nil, &page); err != nil {
				return nil, err
			}
			all = append(all, page.Issues...)
			if page.NextPageToken == "" || (page.IsLast != nil && *page.IsLast) {
				return all, nil
			}
			query.Set("nextPageToken", page.NextPageToken)
			continue
		}
		query.Set("startAt", strconv.Itoa(startAt))
		if err := c.do(http.MethodGet, path, query, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Issues...)
		startAt += len(page.Issues)
		if len(page.Issues) == 0 || startAt >= page.Total {
			return all, nil
		}
	}
}

func (c *Client) Tasks(filter tracker.TaskFilter) ([]tracker.Task, error) {
	var clauses []string
	projects := filter.SpaceIDs
	if len(projects) == 0 {
		projects = c.options.Projects
	}
	if len(projects) > 0 {
		clauses = append(clauses, "project in "+jqlList(projects))
	}
	if len(filter.AssigneeIDs) > 0 {
		clauses = append(clauses, "assignee in "+jqlList(filter.AssigneeIDs))
	}
	if !filter.IncludeClosed {
		clauses = append(clauses, "statusCategory != Done")
	}
	jql := strings.Join(clauses, " AND ") + " ORDER BY updated DESC"
	issues, err := c.search(strings.TrimSpace(jql), issueFields)
	if err != nil {
		return nil, err
	}
	tasks := make([]tracker.Task, 0, len(issues))
	for _, item := range issues {
		tasks = append(tasks, c.toTask(item))
	}
	return tasks, nil
}

func (c *Client) issue(id string) (issue, error) {
	var item issue
	query := url.Values{"fields": {issueFields + ",description"}}
	err := c.do(http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(id), query, nil, &item)
	return item, err
}

func (c *Client) Task(id string) (tracker.Task, error) {
	item, err := c.issue(id)
	return c.toTask(item), err
}

func (c *Client) TaskWithMarkdown(id string) (tracker.Task, error) { return c.Task(id) }

func (c *Client) Subtasks(id string) ([]tracker.Task, error) {
	issues, err := c.search("parent = "+strconv.Quote(id), issueFields)
	if err != nil {
		return nil, err
	}
	tasks := make([]tracker.Task, 0, len(issues))
	for _, item := range issues {
		tasks = append(tasks, c.toTask(item))
	}
	return tasks, nil
}

// label makes a tag a valid Jira label, which cannot contain spaces.
func label(tag string) string { return strings.Join(strings.Fields(tag), "-") }

func (c *Client) CreateTask(id string, task tracker.NewTask) (tracker.Task, error) {
	projectKey, typeID, _ := strings.Cut(id, "/")
	fields := map[string]any{
		"project": map[string]string{"key": projectKey},
		"summary": task.Name,
	}
	if task.Parent != "" {
		// A subtask takes the project's subtask issue type, whatever list was picked.
		p, err := c.project(projectKey)
		if err != nil {
			return tracker.Task{}, err
		}
		typeID = ""
		for _, kind := range p.IssueTypes {
			if kind.Subtask {
				typeID = kind.ID
				break
			}
		}
		if typeID == "" {
			return tracker.Task{}, fmt.Errorf("project %s has no subtask issue type", projectKey)
		}
		fields["parent"] = map[string]string{"id": task.Parent}
		if tracker.IsTaskKey(task.Parent) {
			fields["parent"] = map[string]string{"key": task.Parent}
		}
	}
	if typeID == "" {
		return tracker.Task{}, fmt.Errorf("list %q is not <project>/<issue type ID>", id)
	}
	fields["issuetype"] = map[string]string{"id": typeID}
	description := task.MarkdownContent
	if description == "" {
		description = task.Description
	}
	if description != "" {
		fields["description"] = MarkdownToWiki(description)
	}
	if len(task.Tags) > 0 {
		labels := make([]string, len(task.Tags))
		for index, tag := range task.Tags {
			labels[index] = label(tag)
		}
		fields["labels"] = labels
	}
	if len(task.Assignees) > 0 {
		fields["assignee"] = c.userRef(task.Assignees[0]) // Jira issues have one assignee
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.do(http.MethodPost, "/rest/api/2/issue", nil, map[string]any{"fields": fields}, &created); err != nil {
		return tracker.Task{}, err
	}
	return c.Task(created.Key)
}

// SetStatus moves an issue to a status through the workflow transition that leads there.
func (c *Client) SetStatus(id, wanted string) (tracker.Task, error) {
	var response struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   status `json:"to"`
		} `json:"transitions"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(id) + "/transitions"
	if err := c.do(http.MethodGet, path, nil, nil, &response); err != nil {
		return tracker.Task{}, err
	}
	var reachable []string
	for _, transition := range response.Transitions {
		if strings.EqualFold(transition.To.Name, wanted) || strings.EqualFold(transition.Name, wanted) {
			body := map[string]any{"transition": map[string]string{"id": transition.ID}}
			if err := c.do(http.MethodPost, path, nil, body, nil); err != nil {
				return tracker.Task{}, err
			}
			return c.Task(id)
		}
		reachable = append(reachable, transition.To.Name)
	}
	if task, err := c.Task(id); err == nil && strings.EqualFold(task.Status.Status, wanted) {
		return task, nil
	}
	return tracker.Task{}, fmt.Errorf("%s cannot move to %q from its current status; its workflow allows: %s",
		id, wanted, strings.Join(reachable, ", "))
}

// priorityFor returns the site's priority name for clctl's priority value (1-4).
func (c *Client) priorityFor(value int) (string, error) {
	c.mu.Lock()
	names := c.priorities
	c.mu.Unlock()
	if names == nil {
		var priorities []struct {
			Name string `json:"name"`
		}
		if err := c.do(http.MethodGet, "/rest/api/2/priority", nil, nil, &priorities); err != nil {
			return "", err
		}
		for _, priority := range priorities {
			names = append(names, priority.Name)
		}
		c.mu.Lock()
		c.priorities = names
		c.mu.Unlock()
	}
	wanted := tracker.PriorityNames[value-1]
	for _, candidate := range priorityGroups[wanted] {
		for _, name := range names {
			if strings.EqualFold(name, candidate) {
				return name, nil
			}
		}
	}
	if len(names) == 0 {
		return "", errors.New("this Jira site has no priorities")
	}
	// Unusual names: pick by position, highest first.
	return names[min((value-1)*len(names)/len(tracker.PriorityNames), len(names)-1)], nil
}

func (c *Client) UpdateTask(id string, update tracker.TaskUpdate) (tracker.Task, error) {
	fields := map[string]any{}
	if update.Name != nil {
		fields["summary"] = *update.Name
	}
	if update.MarkdownContent != nil {
		fields["description"] = MarkdownToWiki(*update.MarkdownContent)
	}
	if update.Priority != nil {
		if *update.Priority == 0 {
			fields["priority"] = nil
		} else {
			name, err := c.priorityFor(*update.Priority)
			if err != nil {
				return tracker.Task{}, err
			}
			fields["priority"] = map[string]string{"name": name}
		}
	}
	if update.DueDate != nil {
		fields["duedate"] = nil
		if *update.DueDate != 0 {
			fields["duedate"] = time.UnixMilli(*update.DueDate).Format("2006-01-02")
		}
	}
	if len(update.AddAssignees) > 0 {
		fields["assignee"] = c.userRef(update.AddAssignees[len(update.AddAssignees)-1])
	} else if len(update.RemoveAssignees) > 0 {
		current, err := c.Task(id)
		if err != nil {
			return tracker.Task{}, err
		}
		for _, assignee := range current.Assignees {
			if slices.Contains(update.RemoveAssignees, string(assignee.ID)) {
				fields["assignee"] = nil
			}
		}
	}
	if len(fields) > 0 {
		if err := c.do(http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(id), nil, map[string]any{"fields": fields}, nil); err != nil {
			return tracker.Task{}, err
		}
	}
	if update.Status != nil {
		return c.SetStatus(id, *update.Status)
	}
	return c.Task(id)
}

func (c *Client) changeLabel(id, change, tag string) error {
	body := map[string]any{"update": map[string]any{"labels": []map[string]string{{change: label(tag)}}}}
	return c.do(http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(id), nil, body, nil)
}

func (c *Client) AddTag(id, tag string) error    { return c.changeLabel(id, "add", tag) }
func (c *Client) RemoveTag(id, tag string) error { return c.changeLabel(id, "remove", tag) }

// Time: a local running timer, logged as worklogs.

func (c *Client) CurrentTimer() (*tracker.TimeEntry, error) { return c.timer.Current() }

func (c *Client) StartTimer(taskID, description string, tags []string) error {
	var task *tracker.TimeEntryTask
	if taskID != "" {
		item, err := c.Task(taskID)
		if err != nil {
			return err
		}
		task = &tracker.TimeEntryTask{ID: item.ID, CustomID: item.CustomID, Name: item.Name}
	} else if c.options.MiscIssue == "" {
		return errNoMiscIssue
	}
	if err := c.StopTimer(); err != nil { // one running timer, as in ClickUp
		return err
	}
	return c.timer.Start(task, description, tags)
}

func (c *Client) StopTimer() error {
	running, err := c.timer.Current()
	if err != nil || running == nil {
		return err
	}
	ref := ""
	if running.Task != nil {
		ref = running.Task.Ref()
	}
	if err := c.LogTime(ref, running.StartTime(), time.Now(), running.Description, running.TagNames()); err != nil {
		return err
	}
	return c.timer.Clear()
}

var errNoMiscIssue = errors.New("Jira logs time on issues: pick a task, or set jira_misc_issue to an issue for time without one")

// worklogComment is a worklog's comment: the description, then the labels as #tags.
func worklogComment(description string, tags []string) string {
	comment := description
	for _, tag := range tags {
		comment = strings.TrimSpace(comment + " #" + label(tag))
	}
	return comment
}

// splitWorklogComment reverses worklogComment: trailing #words are the labels.
func splitWorklogComment(comment string) (string, []string) {
	words := strings.Fields(comment)
	end := len(words)
	for end > 0 && strings.HasPrefix(words[end-1], "#") && len(words[end-1]) > 1 {
		end--
	}
	var tags []string
	for _, word := range words[end:] {
		tags = append(tags, word[1:])
	}
	return strings.Join(words[:end], " "), tags
}

func worklogBody(start, end time.Time, comment string) map[string]any {
	// Jira rejects worklogs under a minute.
	seconds := max(int64(end.Sub(start).Round(time.Second).Seconds()), 60)
	body := map[string]any{"started": start.Format(jiraTime), "timeSpentSeconds": seconds}
	if comment != "" {
		body["comment"] = comment
	}
	return body
}

func (c *Client) LogTime(taskID string, start, end time.Time, description string, tags []string) error {
	if taskID == "" {
		if taskID = c.options.MiscIssue; taskID == "" {
			return errNoMiscIssue
		}
	}
	path := "/rest/api/2/issue/" + url.PathEscape(taskID) + "/worklog"
	return c.do(http.MethodPost, path, nil, worklogBody(start, end, worklogComment(description, tags)), nil)
}

type worklog struct {
	ID               string          `json:"id"`
	IssueID          string          `json:"issueId"`
	Author           user            `json:"author"`
	Started          string          `json:"started"`
	TimeSpentSeconds int64           `json:"timeSpentSeconds"`
	Comment          json.RawMessage `json:"comment"`
}

// toEntry turns a worklog on an issue into a time entry; worklogs on the misc issue are
// time without a task.
func (c *Client) toEntry(item issue, log worklog) tracker.TimeEntry {
	started, _ := time.Parse(jiraTime, log.Started)
	var comment string
	_ = json.Unmarshal(log.Comment, &comment)
	description, tags := splitWorklogComment(comment)
	entry := tracker.TimeEntry{ID: item.Key + ":" + log.ID, Description: description,
		Start: tracker.FlexInt(started.UnixMilli()), Duration: tracker.FlexInt(log.TimeSpentSeconds * 1000)}
	entry.End = entry.Start + entry.Duration
	for _, tag := range tags {
		entry.Tags = append(entry.Tags, tracker.TimeEntryTag{Name: tag})
	}
	if !strings.EqualFold(item.Key, c.options.MiscIssue) {
		entry.Task = &tracker.TimeEntryTask{ID: item.ID, CustomID: item.Key, Name: item.Fields.Summary}
	}
	return entry
}

// worklogs returns your worklogs that started between from and to, with their issues.
func (c *Client) worklogs(from, to time.Time) ([]issue, [][]worklog, error) {
	me, err := c.Me()
	if err != nil {
		return nil, nil, err
	}
	jql := fmt.Sprintf(`worklogAuthor = currentUser() AND worklogDate >= %q AND worklogDate <= %q`,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
	issues, err := c.search(jql, "summary")
	if err != nil {
		return nil, nil, err
	}
	logs := make([][]worklog, len(issues))
	for index, item := range issues {
		var response struct {
			Worklogs []worklog `json:"worklogs"`
		}
		query := url.Values{"startedAfter": {strconv.FormatInt(from.UnixMilli()-1, 10)}, "maxResults": {"5000"}}
		if err := c.do(http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(item.Key)+"/worklog", query, nil, &response); err != nil {
			return nil, nil, err
		}
		for _, log := range response.Worklogs {
			started, err := time.Parse(jiraTime, log.Started)
			if err == nil && log.Author.id() == string(me.ID) && !started.Before(from) && !started.After(to) {
				logs[index] = append(logs[index], log)
			}
		}
	}
	return issues, logs, nil
}

func (c *Client) TimeEntries(from, to time.Time) ([]tracker.TimeEntry, error) {
	issues, logs, err := c.worklogs(from, to)
	if err != nil {
		return nil, err
	}
	var entries []tracker.TimeEntry
	for index, item := range issues {
		for _, log := range logs[index] {
			entries = append(entries, c.toEntry(item, log))
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Start < entries[j].Start })
	return entries, nil
}

func (c *Client) RawTimeEntries(from, to time.Time) ([]json.RawMessage, error) {
	issues, logs, err := c.worklogs(from, to)
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	for index, item := range issues {
		for _, log := range logs[index] {
			entry := c.toEntry(item, log)
			data, err := json.Marshal(map[string]any{"id": entry.ID, "start": entry.Start, "end": entry.End,
				"duration": entry.Duration, "description": entry.Description, "task": entry.Task, "tags": entry.Tags,
				"source": "jira worklog", "started": log.Started, "timeSpentSeconds": log.TimeSpentSeconds})
			if err != nil {
				return nil, err
			}
			raw = append(raw, data)
		}
	}
	return raw, nil
}

func splitID(id, kind string) (string, string, error) {
	issueKey, itemID, ok := strings.Cut(id, ":")
	if !ok || issueKey == "" || itemID == "" {
		return "", "", fmt.Errorf("%s ID %q is not <issue key>:<ID>", kind, id)
	}
	return issueKey, itemID, nil
}

func (c *Client) TimeEntry(id string) (tracker.TimeEntry, error) {
	issueKey, worklogID, err := splitID(id, "time entry")
	if err != nil {
		return tracker.TimeEntry{}, err
	}
	item, err := c.issue(issueKey)
	if err != nil {
		return tracker.TimeEntry{}, err
	}
	var log worklog
	if err := c.do(http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(issueKey)+"/worklog/"+url.PathEscape(worklogID), nil, nil, &log); err != nil {
		return tracker.TimeEntry{}, err
	}
	return c.toEntry(item, log), nil
}

func (c *Client) UpdateTimeEntry(id string, start, end time.Time, description string) error {
	entry, err := c.TimeEntry(id) // keeps its labels
	if err != nil {
		return err
	}
	issueKey, worklogID, _ := splitID(id, "time entry")
	path := "/rest/api/2/issue/" + url.PathEscape(issueKey) + "/worklog/" + url.PathEscape(worklogID)
	return c.do(http.MethodPut, path, nil, worklogBody(start, end, worklogComment(description, entry.TagNames())), nil)
}

func (c *Client) DeleteTimeEntry(id string) error {
	issueKey, worklogID, err := splitID(id, "time entry")
	if err != nil {
		return err
	}
	return c.do(http.MethodDelete, "/rest/api/2/issue/"+url.PathEscape(issueKey)+"/worklog/"+url.PathEscape(worklogID), nil, nil, nil)
}

// Comments.

// MarkdownOps turns Markdown into comment ops.
func MarkdownOps(text string) []map[string]any {
	ops, _ := tracker.Ops(markdown.CommentOps(text))
	return ops
}

func (c *Client) Comments(taskID string) ([]tracker.Comment, error) {
	var all []tracker.Comment
	query := url.Values{"maxResults": {"100"}}
	for startAt := 0; ; {
		query.Set("startAt", strconv.Itoa(startAt))
		var page struct {
			Comments []struct {
				ID      string `json:"id"`
				Body    string `json:"body"`
				Author  user   `json:"author"`
				Created string `json:"created"`
			} `json:"comments"`
			Total int `json:"total"`
		}
		if err := c.do(http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(taskID)+"/comment", query, nil, &page); err != nil {
			return nil, err
		}
		for _, comment := range page.Comments {
			text := WikiToMarkdown(comment.Body)
			created, _ := time.Parse(jiraTime, comment.Created)
			all = append(all, tracker.Comment{ID: tracker.FlexString(taskID + ":" + comment.ID), CommentText: text,
				Comment: MarkdownOps(text), Date: tracker.FlexString(strconv.FormatInt(created.UnixMilli(), 10)),
				User: comment.Author.toUser()})
		}
		startAt += len(page.Comments)
		if len(page.Comments) == 0 || startAt >= page.Total {
			break
		}
	}
	// Newest first, like the other backends.
	sort.SliceStable(all, func(i, j int) bool { return string(all[i].Date) > string(all[j].Date) })
	return all, nil
}

func commentBody(ops any) (map[string]string, error) {
	list, err := tracker.Ops(ops)
	if err != nil {
		return nil, err
	}
	return map[string]string{"body": MarkdownToWiki(markdown.ToMarkdown(list))}, nil
}

func (c *Client) PostComment(taskID string, ops any) (string, error) {
	body, err := commentBody(ops)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	err = c.do(http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(taskID)+"/comment", nil, body, &created)
	return taskID + ":" + created.ID, err
}

func (c *Client) UpdateComment(commentID string, ops any) error {
	issueKey, id, err := splitID(commentID, "comment")
	if err != nil {
		return err
	}
	body, err := commentBody(ops)
	if err != nil {
		return err
	}
	return c.do(http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(issueKey)+"/comment/"+url.PathEscape(id), nil, body, nil)
}

func (c *Client) UploadAttachment(taskID, path string) (tracker.Attachment, error) {
	file, err := os.Open(path)
	if err != nil {
		return tracker.Attachment{}, err
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return tracker.Attachment{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return tracker.Attachment{}, err
	}
	if err := writer.Close(); err != nil {
		return tracker.Attachment{}, err
	}
	header := http.Header{}
	header.Set("Content-Type", writer.FormDataContentType())
	header.Set("X-Atlassian-Token", "no-check")
	var attachments []struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := c.send(http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(taskID)+"/attachments", nil, header, body.Bytes(), &attachments); err != nil {
		return tracker.Attachment{}, err
	}
	if len(attachments) == 0 {
		return tracker.Attachment{}, errors.New("Jira returned no attachment")
	}
	return tracker.Attachment{ID: attachments[0].ID, URL: attachments[0].Content, Title: attachments[0].Filename}, nil
}
