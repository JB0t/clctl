package clickup

import "github.com/JB0t/clctl/internal/tracker"

// The ClickUp API's payloads are clctl's own task model (package tracker), which started
// out as ClickUp's wire format. These aliases keep this package's code readable.
type (
	FlexInt       = tracker.FlexInt
	FlexString    = tracker.FlexString
	User          = tracker.User
	Space         = tracker.Space
	Status        = tracker.Status
	Priority      = tracker.Priority
	Ref           = tracker.Ref
	FolderRef     = tracker.FolderRef
	Task          = tracker.Task
	TaskTag       = tracker.TaskTag
	List          = tracker.List
	Folder        = tracker.Folder
	TimeEntry     = tracker.TimeEntry
	TimeEntryTask = tracker.TimeEntryTask
	TimeEntryTag  = tracker.TimeEntryTag
	Comment       = tracker.Comment
	Attachment    = tracker.Attachment
	TaskFilter    = tracker.TaskFilter
	TaskUpdate    = tracker.TaskUpdate
	NewTask       = tracker.NewTask
)

// Member is a workspace member.
type Member struct {
	User User `json:"user"`
}

// Team is a ClickUp workspace ("team" in the API).
type Team struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []Member `json:"members"`
}
