# clickup

<!-- docgen:begin id=internal/clickup:overview scope=internal/clickup hash=2e6f873c6e33 -->
Package clickup provides a client for the ClickUp v2 API. It handles authentication, HTTP requests, and response parsing for a subset of ClickUp's functionality used by clctl. The client supports operations on users, teams, spaces, tasks, lists, time entries, comments, and attachments. It includes logic for custom task IDs, flexible JSON unmarshaling, and rate-limit handling.

The client struct holds authentication details and HTTP settings. Methods on the client wrap API endpoints, translating Go types to JSON and back. Functions like TeamTasks fetch multiple pages of results in parallel. The client handles ClickUp's specific data formats, such as numeric IDs stored as strings and time values as Unix timestamps.

Types define the structure of ClickUp's API responses. FlexInt and FlexString handle JSON fields that may be numbers or strings. Task, List, TimeEntry, and other types represent core entities. Methods on these types provide helper functions for common operations like checking if a task matches an ID or determining if a time entry is running.
<!-- docgen:end id=internal/clickup:overview -->

<!-- docgen:begin id=internal/clickup:reference scope=internal/clickup hash=2e6f873c6e33 -->
## Reference

### `api.go`

- **`Client.Me`** (method): Me returns the user who owns the API token.
- **`Client.Teams`** (method): Teams returns the workspaces ("teams" in the API) the token can access.
- **`Client.Spaces`** (method): Spaces returns the workspace's non-archived spaces.
- **`Client.Space`** (method): Space fetches one space.
- **`Client.Members`** (method): Members returns the workspace's members.
- **`TaskFilter`** (struct): TaskFilter narrows a workspace task search.
- **`Client.TeamTasks`** (method): TeamTasks returns every task matching the filter. ClickUp pages 100 tasks at a time without a total count, so pages are fetched in parallel waves until one comes back short.
- **`Client.Task`** (method): Task fetches one task by ID or custom ID.
- **`Client.SetStatus`** (method): SetStatus changes a task's status and returns the updated task.
- **`Client.List`** (method): List fetches a list, including its statuses.
- **`Client.CurrentTimer`** (method): CurrentTimer returns the token owner's running time entry, or nil. ClickUp allows one running timer per user and leaves it out of the time-entry history.
- **`Client.StartTimer`** (method): StartTimer starts the token owner's timer on a task.
- **`Client.StopTimer`** (method): StopTimer stops the token owner's running timer.
- **`Client.LogTime`** (method): LogTime records a finished time entry against a task.
- **`Client.TimeEntries`** (method): TimeEntries returns the token owner's time entries that started between from and to.
- **`Client.TimeEntry`** (method): TimeEntry fetches one time entry.
- **`Client.UpdateTimeEntry`** (method): UpdateTimeEntry changes a finished time entry's range and description.
- **`Client.DeleteTimeEntry`** (method): DeleteTimeEntry removes a time entry.
- **`Comment`** (struct): Comment is a task comment as returned by the API.
- **`Client.Comments`** (method): Comments returns every comment on a task (the API pages 25 at a time, newest first).
- **`Client.PostComment`** (method): PostComment adds a comment built from ops and returns its ID.
- **`Client.UpdateComment`** (method): UpdateComment replaces a comment's content, keeping its formatting.
- **`Attachment`** (struct): Attachment is an uploaded task attachment.
- **`Client.UploadAttachment`** (method): UploadAttachment attaches a local file to a task.
- **`Folder`** (struct): Folder is a space folder with its lists.
- **`Client.SpaceFolders`** (method): SpaceFolders returns a space's non-archived folders, including their lists.
- **`Client.SpaceLists`** (method): SpaceLists returns the lists sitting directly in a space (not in a folder).
- **`NewTask`** (struct): NewTask is the payload for creating a task.
- **`Client.CreateTask`** (method): CreateTask creates a task in a list.

### `client.go`

- **`IsCustomTaskID`** (function): IsCustomTaskID reports whether id looks like a custom task ID rather than a raw one.
- **`APIError`** (struct): APIError is a non-2xx response from ClickUp.
- **`APIError.Error`** (method)
- **`IsNotFound`** (function): IsNotFound reports whether err is a ClickUp 404.
- **`Client`** (struct)
- **`New`** (function)
- **`Client.TaskQuery`** (method): TaskQuery returns query params that let the API accept a custom task ID.
- **`Client.Do`** (method): Do sends a JSON request and decodes the JSON response into out (which may be nil).
- **`Client.Upload`** (method): Upload sends a multipart request built by the caller.

### `types.go`

- **`FlexInt`** (type): FlexInt decodes ClickUp numbers that arrive either as JSON numbers or numeric strings (time-entry start/duration, dates).
- **`FlexInt.UnmarshalJSON`** (method)
- **`FlexString`** (type): FlexString decodes IDs that arrive either as strings or numbers (user and comment IDs).
- **`FlexString.UnmarshalJSON`** (method)
- **`FlexString.MarshalJSON`** (method)
- **`User`** (struct)
- **`User.DisplayName`** (method): DisplayName returns the username, falling back to the email.
- **`Member`** (struct)
- **`Team`** (struct)
- **`Space`** (struct)
- **`Status`** (struct)
- **`Status.Done`** (method): Done reports whether the status is one of ClickUp's done or closed status types.
- **`Priority`** (struct)
- **`Ref`** (struct)
- **`FolderRef`** (struct)
- **`Task`** (struct)
- **`Task.Ref`** (method): Ref returns the ID people type for a task: its custom ID when it has one.
- **`Task.PriorityName`** (method): PriorityName returns the priority ("urgent", "high", ...) or "".
- **`Task.AssignedTo`** (method): AssignedTo reports whether the user ID is among the task's assignees.
- **`Task.Matches`** (method): Matches reports whether id is the task's ID or custom ID (custom IDs ignore case).
- **`List`** (struct)
- **`TimeEntryTask`** (struct)
- **`TimeEntryTask.Ref`** (method): Ref returns the custom ID when present, else the task ID.
- **`TimeEntry`** (struct)
- **`TimeEntry.StartTime`** (method): StartTime returns when the entry started.
- **`TimeEntry.EndTime`** (method): EndTime returns when the entry ended, from start + duration when the end is missing.
- **`TimeEntry.Running`** (method): Running reports whether the entry is a timer that has not been stopped.
<!-- docgen:end id=internal/clickup:reference -->
