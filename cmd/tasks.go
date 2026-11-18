package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

func newTasksCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "tasks",
		Short:       "Find, open, create and update tasks (run without an action for a menu)",
		Annotations: map[string]string{annotationMenu: "true"},
		RunE:        func(cmd *cobra.Command, args []string) error { return runMenu(app, cmd) },
	}
	cmd.AddCommand(
		newListCmd(app),
		newOpenCmd(app),
		newUpdateTaskCmd(app),
		newCompleteCmd(app),
		newSearchCmd(app),
		newStatusCmd(app),
		newCreateCmd(app),
		newPushTodoCmd(app),
		newAttachFileCmd(app),
		newExportCmd(app),
		newRefreshCmd(app),
		// Old spellings from before actions were grouped, kept working for scripts and cron.
		legacy(newStartTimerCmd(app), "start-timer", "timers start"),
		legacy(newStopTimerCmd(app), "stop-timer", "timers stop"),
		legacy(newListTimersCmd(app), "list-timers", "timers list"),
		legacy(newWatchTimersCmd(app), "watch-timers", "timers watch"),
		legacy(newLogTimeCmd(app), "log-time", "timers log"),
		legacy(newEditTimeCmd(app), "edit-time", "timers edit"),
		legacy(newAddCommentCmd(app), "add-comment", "comments add"),
		legacy(newGetCommentsCmd(app), "get-comments", "comments list"),
		legacy(newFixTablesCmd(app), "fix-tables", "comments fix-tables"),
		legacy(newAttachFileCmd(app), "attach-file", "tasks attach"),
		legacy(newMakeNotesCmd(app), "make-notes", "notes open"),
		legacy(newMakeWorkspaceCmd(app), "make-workspace", "notes workspace"),
		legacy(newPushNotesCmd(app), "push-notes", "notes push"),
		legacy(newUnpushedNotesCmd(app), "unpushed-notes", "notes unpushed"),
		legacy(newDigestCmd(app), "digest", "digest"),
		newSetStatusCmd(app),
	)
	return cmd
}

func newListCmd(app *App) *cobra.Command {
	var assignees []string
	var everyone bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"get"},
		Short:   "Tree of open tasks by space/folder/list, then priority",
		Long:    "Shows your open tasks by default. --assignee shows tasks assigned to all of the named people; --include-done adds done and closed tasks.",
		Example: `  clctl tasks list
  clctl tasks list --assignee "Jane Smith" --assignee "John Doe"
  clctl tasks list --everyone
  clctl tasks list --include-done`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.Cache()
			if err != nil {
				return err
			}
			selected := c.Tasks
			if app.IncludeDone {
				// Closed tasks are fetched for everyone here, then filtered like the cached ones.
				closed, err := app.ClosedTasks(everyone || len(assignees) > 0)
				if err != nil {
					return err
				}
				selected = append(append([]tracker.Task(nil), selected...), closed...)
			} else {
				selected = tasks.Open(selected)
			}
			switch {
			case everyone:
			case len(assignees) > 0:
				ids, err := resolveAssignees(app, assignees)
				if err != nil {
					return err
				}
				selected = tasks.AssignedTo(selected, ids...)
			default:
				userID, err := app.UserID()
				if err != nil {
					return err
				}
				selected = tasks.AssignedTo(selected, userID)
			}
			if len(selected) == 0 {
				fmt.Println("No tasks.")
				return nil
			}
			tasks.PrintTree(os.Stdout, selected, ui.Color())
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&assignees, "assignee", nil, "assignee name or email; repeat or comma-separate")
	cmd.Flags().BoolVar(&everyone, "everyone", false, "show tasks for everyone")
	return cmd
}

// resolveAssignees turns names/emails into user IDs using the workspace member list.
func resolveAssignees(app *App, names []string) ([]string, error) {
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	members, err := client.Members()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, raw := range names {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			var matches []tracker.User
			for _, member := range members {
				if strings.EqualFold(member.Username, name) || strings.EqualFold(member.Email, name) {
					matches = []tracker.User{member}
					break
				}
				if strings.Contains(strings.ToLower(member.Username), strings.ToLower(name)) {
					matches = append(matches, member)
				}
			}
			switch len(matches) {
			case 0:
				return nil, fmt.Errorf("no workspace member matches %q", name)
			case 1:
				ids = append(ids, string(matches[0].ID))
			default:
				var found []string
				for _, match := range matches {
					found = append(found, match.Username)
				}
				return nil, fmt.Errorf("%q matches several members: %s", name, strings.Join(found, ", "))
			}
		}
	}
	return ids, nil
}

func newOpenCmd(app *App) *cobra.Command {
	var taskID string
	var tui bool
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open a task: its web page, or clctl's task view",
		Long: `Opens the task's web page in the browser. Tasks without one (the local store's) open
in clctl's task view instead, which shows the task the way a tracker's page does: its
status, priority, assignees, due date, tags and timer, then the description, subtasks,
time logged and comments. Its keys change the status (s) or priority (p), start or stop
a timer (t), add a comment (c), edit the description (e), open a subtask (o) or the
parent (u); ? lists them all. --tui uses the task view for any backend.`,
		Example: `  clctl tasks open
  clctl t -o --task-id TASK-12
  clctl tasks open --task-id OPS-123 --tui`,
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{Everyone: true})
			if err != nil {
				return err
			}
			return openTask(app, task, tui)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().BoolVar(&tui, "tui", false, "show the task in clctl's task view, even when it has a web page")
	return cmd
}

// openBrowser opens a URL with the platform's opener (the Windows browser under WSL).
func openBrowser(url string) error {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"open", url}}
	case "windows":
		candidates = [][]string{{"rundll32", "url.dll,FileProtocolHandler", url}}
	default:
		if version, err := os.ReadFile("/proc/version"); err == nil && strings.Contains(strings.ToLower(string(version)), "microsoft") {
			candidates = append(candidates, []string{"wslview", url}, []string{"powershell.exe", "-NoProfile", "-Command", "Start-Process", "'" + url + "'"})
		}
		candidates = append(candidates, []string{"xdg-open", url})
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		if err := exec.Command(candidate[0], candidate[1:]...).Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("could not open a browser; the URL is %s", url)
}

// newSetStatusCmd is the old spelling of `tasks update --status`, kept for scripts.
func newSetStatusCmd(app *App) *cobra.Command {
	var taskID, status string
	cmd := &cobra.Command{
		Use:        "set-status",
		Short:      "Change a task's status",
		Hidden:     true,
		Deprecated: "use 'clctl tasks update --status'",
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{IncludeDone: true})
			if err != nil {
				return err
			}
			return setStatus(app, task, status)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().StringVar(&status, "status", "", "new status (pick from the list's statuses when omitted)")
	return cmd
}

// setStatus moves a task to a status of its list, picking one when status is empty.
func setStatus(app *App, task tracker.Task, status string) error {
	client, err := app.Backend()
	if err != nil {
		return err
	}
	if status == "" {
		status, err = pickStatus(app, task)
	} else {
		status, err = matchStatus(app, task, status)
	}
	if err != nil {
		return err
	}
	updated, err := client.SetStatus(task.Ref(), status)
	if err != nil {
		return err
	}
	if c, err := app.Cache(); err == nil {
		_ = c.Upsert(updated)
	}
	fmt.Printf("%s -> %s\n", task.Ref(), status)
	return nil
}

func newCompleteCmd(app *App) *cobra.Command {
	var taskID string
	cmd := &cobra.Command{
		Use:         "complete",
		Short:       "Mark a task done (stops its timers if running)",
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(taskID, TaskScope{})
			if err != nil {
				return err
			}
			client, err := app.Backend()
			if err != nil {
				return err
			}
			list, err := client.List(task.List.ID)
			if err != nil {
				return err
			}
			done := doneStatus(list.Statuses)
			if done == "" {
				return fmt.Errorf("list %s has no done or closed status", list.Name)
			}
			if err := stopTimersOn(app, task); err != nil {
				return err
			}
			return setStatus(app, task, done)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	return cmd
}

// doneStatus prefers a status named done/complete(d), then the closed status, then any done type.
func doneStatus(statuses []tracker.Status) string {
	for _, status := range statuses {
		switch strings.ToLower(status.Status) {
		case "done", "complete", "completed":
			return status.Status
		}
	}
	for _, wanted := range []string{"closed", "done"} {
		for _, status := range statuses {
			if status.Type == wanted {
				return status.Status
			}
		}
	}
	return ""
}

func newSearchCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "search",
		Short: "Pick any cached task, then pick actions to run on it",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Every command marked as a task action is offered, so new ones appear here automatically.
			var actions []*cobra.Command
			var rows []string
			for _, group := range cmd.Root().Commands() {
				for _, action := range group.Commands() {
					if action.Annotations[annotationTaskAction] != "" && action.IsAvailableCommand() && group.IsAvailableCommand() {
						actions = append(actions, action)
						rows = append(rows, fmt.Sprintf("%-20s %s", group.Name()+" "+action.Name(), action.Short))
					}
				}
			}
			app.InMenu = true
			defer func() { app.InMenu = false }()
			var task tracker.Task
			pickTask := func() (err error) {
				task, err = app.SelectTask("", TaskScope{Everyone: true})
				return err
			}
			// Esc in the action list goes back to the task picker; Esc there leaves.
			runActions := func() error {
				for {
					index, err := ui.Pick(rows, task.Ref()+" >")
					if err != nil {
						return err
					}
					action := actions[index]
					_ = action.Flags().Set("task-id", task.Ref())
					err = action.RunE(action, nil)
					resetFlags(action)
					if err != nil && !errors.Is(err, ui.ErrCancelled) {
						fmt.Fprintln(os.Stderr, "Error:", err)
					}
				}
			}
			if err := ui.Steps(pickTask, runActions); err != nil && !errors.Is(err, ui.ErrCancelled) {
				return err
			}
			return nil
		},
	}
}

func newRefreshCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Re-download open tasks for the configured spaces into the local cache",
		Long:  "Every command refreshes the cache when it is older than cache_ttl_minutes; -r/--refresh on any command forces it first.",
	}
	cmd.Flags().BoolVar(&app.Quiet, "quiet", false, "print nothing unless there is an error (for cron)")
	cmd.Flags().Bool("path", false, "print the cache file location instead of refreshing")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if path, _ := cmd.Flags().GetBool("path"); path {
			fmt.Println(app.CachedOnly().Path())
			return nil
		}
		return app.RefreshCache(true)
	}
	return cmd
}

// newCacheCmd is the old home of `tasks refresh`, kept hidden (and quiet: no deprecation
// notice) so existing cron entries keep working.
func newCacheCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "cache", Short: "Manage the local task cache", Hidden: true}
	cmd.AddCommand(newRefreshCmd(app), &cobra.Command{
		Use:   "path",
		Short: "Print the cache file location",
		Run:   func(*cobra.Command, []string) { fmt.Println(app.CachedOnly().Path()) },
	})
	return cmd
}
