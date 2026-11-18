package tasks

import (
	"strings"
	"testing"

	"github.com/JB0t/clctl/internal/tracker"
)

func task(id, name, list, folder string, hidden bool, priority string, parent string) tracker.Task {
	t := tracker.Task{ID: id, CustomID: "T-" + id, Name: name, Parent: parent,
		List: tracker.Ref{ID: list, Name: list}, Folder: tracker.FolderRef{Name: folder, Hidden: hidden},
		Space: tracker.Ref{ID: "s", Name: "Space"}, Status: tracker.Status{Status: "to do", Type: "open"}}
	if priority != "" {
		t.Priority = &tracker.Priority{Priority: priority}
	}
	return t
}

func TestPrintTree(t *testing.T) {
	var out strings.Builder
	PrintTree(&out, []tracker.Task{
		task("1", "low one", "Backlog", "Dev", false, "low", ""),
		task("2", "urgent one", "Backlog", "Dev", false, "urgent", ""),
		task("3", "child", "Backlog", "Dev", false, "", "2"),
		task("4", "loose", "Inbox", "hidden", true, "", ""),
	}, false)
	want := `Space
├── Inbox (1)
│   └── -      T-4         loose  [to do]
└── Dev
    └── Backlog (3)
        ├── urgent T-2         urgent one  [to do]
        │   └── -      T-3         child  [to do]
        └── low    T-1         low one  [to do]
`
	if got := strings.TrimRight(out.String(), "\n") + "\n"; got != want {
		t.Fatalf("tree mismatch:\n%s\nwant:\n%s", got, want)
	}
}

func TestFilters(t *testing.T) {
	done := task("1", "d", "L", "", false, "", "")
	done.Status.Type = "done"
	mine := task("2", "m", "L", "", false, "", "")
	mine.Assignees = []tracker.User{{ID: "7"}}
	if got := Open([]tracker.Task{done, mine}); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("Open = %v", got)
	}
	if got := AssignedTo([]tracker.Task{done, mine}, "7"); len(got) != 1 {
		t.Fatalf("AssignedTo = %v", got)
	}
	if Location(task("1", "", "L", "hidden", true, "", "")) != "Space/L" {
		t.Fatal("hidden folders must not appear in locations")
	}
}
