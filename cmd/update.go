package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

// priorities are clctl's priority names; a name's index + 1 is its TaskUpdate value.
var priorities = tracker.PriorityNames

// updateFields are what `tasks update` can change, in the order it asks about them.
var updateFields = []string{"status", "name", "description", "priority", "due date", "tags", "assignees"}

type updateOptions struct {
	taskID, status, name, description, priority, due string
	tags, addTags, removeTags                        []string
	assignees, unassign                              []string
	yes                                              bool
	// set names the fields given, by flag or by picking them: a given empty
	// description or tag list clears it.
	set map[string]bool
}

func newUpdateTaskCmd(app *App) *cobra.Command {
	var o updateOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Change a task's status, name, description, priority, due date, tags or assignees",
		Long: `With flags, changes only what they name. Without any, picks the fields to change and asks
for each, with the current value as the default; the description opens in your editor.

--tag sets the whole tag list; --add-tag and --remove-tag change single tags. --assignee
adds people and --unassign removes them (names or emails). --priority none and --due none
clear those fields.`,
		Example: `  clctl tasks update
  clctl t -u --task-id TECH-12 --status "in progress"
  clctl tasks update --task-id TECH-12 --add-tag infra --priority high --due 2026-10-31
  clctl tasks update --task-id TECH-12 --description "$(cat notes.md)" --yes`,
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := app.SelectTask(o.taskID, TaskScope{IncludeDone: true})
			if err != nil {
				return err
			}
			o.set = map[string]bool{}
			cmd.Flags().Visit(func(flag *pflag.Flag) { o.set[flag.Name] = true })
			return updateTask(app, task, o)
		},
	}
	addTaskIDFlag(app, cmd, &o.taskID)
	flags := cmd.Flags()
	flags.StringVar(&o.status, "status", "", "new status (one of the list's statuses)")
	flags.StringVar(&o.name, "name", "", "new name")
	flags.StringVar(&o.description, "description", "", "new description (Markdown); replaces the old one")
	flags.StringVar(&o.priority, "priority", "", "urgent, high, normal, low or none")
	flags.StringVar(&o.due, "due", "", "due date: YYYY-MM-DD, today, tomorrow, +3d or none")
	flags.StringArrayVar(&o.tags, "tag", nil, "set the tag list; repeat or comma-separate (--tag \"\" removes all)")
	flags.StringArrayVar(&o.addTags, "add-tag", nil, "add a tag; repeat or comma-separate")
	flags.StringArrayVar(&o.removeTags, "remove-tag", nil, "remove a tag; repeat or comma-separate")
	flags.StringArrayVar(&o.assignees, "assignee", nil, "add an assignee by name or email; repeat for several")
	flags.StringArrayVar(&o.unassign, "unassign", nil, "remove an assignee by name or email; repeat for several")
	flags.BoolVar(&o.yes, "yes", false, "apply without confirmation")
	_ = cmd.RegisterFlagCompletionFunc("priority", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return append(slices.Clone(priorities), "none"), cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// taskChange is a planned update: the API fields, tag changes and a line per change.
type taskChange struct {
	update            tracker.TaskUpdate
	addTags, dropTags []string
	lines             []string
}

func updateTask(app *App, task tracker.Task, o updateOptions) error {
	client, err := app.Backend()
	if err != nil {
		return err
	}
	// The cached copy has no Markdown description, and tags may have changed since.
	if fresh, err := client.TaskWithMarkdown(task.Ref()); err == nil {
		task = fresh
	}
	anyField := false
	for _, name := range []string{"status", "name", "description", "priority", "due", "tag", "add-tag", "remove-tag", "assignee", "unassign"} {
		anyField = anyField || o.set[name]
	}
	if !anyField {
		if !ui.Interactive() {
			return errors.New("nothing to change: give --status, --name, --description, --priority, --due, --tag, --add-tag, --remove-tag, --assignee or --unassign")
		}
		if o, err = askUpdate(app, task, o); err != nil {
			return err
		}
	}
	change, err := planUpdate(app, task, o)
	if err != nil {
		return err
	}
	if change.update.Empty() && len(change.addTags) == 0 && len(change.dropTags) == 0 {
		fmt.Println("Nothing to change.")
		return nil
	}
	fmt.Printf("%s: %s\n", task.Ref(), task.Name)
	for _, line := range change.lines {
		fmt.Println("  " + line)
	}
	if !o.yes && ui.Interactive() {
		ok, err := ui.Confirm("Apply?", true)
		if err != nil || !ok {
			return ui.ErrCancelled
		}
	}
	if !change.update.Empty() {
		if _, err := client.UpdateTask(task.Ref(), change.update); err != nil {
			return err
		}
	}
	for _, tag := range change.addTags {
		if err := client.AddTag(task.Ref(), tag); err != nil {
			return fmt.Errorf("add tag %q: %w", tag, err)
		}
	}
	for _, tag := range change.dropTags {
		if err := client.RemoveTag(task.Ref(), tag); err != nil {
			return fmt.Errorf("remove tag %q: %w", tag, err)
		}
	}
	if updated, err := client.Task(task.Ref()); err == nil {
		if c, err := app.Cache(); err == nil {
			_ = c.Upsert(app.WithSpaceName(updated))
		}
	}
	fmt.Println("Updated", task.Ref())
	return nil
}

// askUpdate picks the fields to change and asks for each, starting from the current value.
func askUpdate(app *App, task tracker.Task, o updateOptions) (updateOptions, error) {
	picked, err := ui.PickMany(updateFields, task.Ref()+" change >")
	if err != nil {
		return o, err
	}
	for _, index := range picked {
		switch updateFields[index] {
		case "status":
			if o.status, err = pickStatus(app, task); err != nil {
				return o, err
			}
		case "name":
			if o.name, err = ui.Ask("Name", task.Name); err != nil {
				return o, err
			}
		case "description":
			o.set["description"] = true
			if o.description, err = editText(app, task.Ref()+"-description.md", taskDescription(task)); err != nil {
				return o, err
			}
		case "priority":
			rows := append(slices.Clone(priorities), "none")
			for index, name := range rows {
				if name == task.PriorityName() || name == "none" && task.PriorityName() == "" {
					rows[index] += "  (current)"
				}
			}
			choice, err := ui.Pick(rows, "Priority >")
			if err != nil {
				return o, err
			}
			o.priority = strings.Fields(rows[choice])[0]
		case "due date":
			current := "none"
			if task.DueDate != 0 {
				current = time.UnixMilli(int64(task.DueDate)).Format("2006-01-02")
			}
			if o.due, err = ui.Ask("Due date (YYYY-MM-DD, today, tomorrow, +3d or none)", current); err != nil {
				return o, err
			}
		case "tags":
			answer, err := ui.Ask("Tags (comma-separated; empty removes all)", strings.Join(task.TagNames(), ", "))
			if err != nil {
				return o, err
			}
			o.tags, o.set["tag"] = []string{answer}, true
		case "assignees":
			ids, err := pickAssignees(app)
			if err != nil {
				return o, err
			}
			// Picked people are the new list: add the new ones, remove the rest.
			keep := map[string]bool{}
			for _, id := range ids {
				keep[id] = true
				o.assignees = append(o.assignees, "id:"+id)
			}
			for _, assignee := range task.Assignees {
				if !keep[string(assignee.ID)] {
					o.unassign = append(o.unassign, "id:"+string(assignee.ID))
				}
			}
		}
	}
	return o, nil
}

// planUpdate turns the options into API changes, checking each against the task.
func planUpdate(app *App, task tracker.Task, o updateOptions) (taskChange, error) {
	var change taskChange
	note := func(format string, args ...any) { change.lines = append(change.lines, fmt.Sprintf(format, args...)) }
	if o.status != "" && !strings.EqualFold(o.status, task.Status.Status) {
		status, err := matchStatus(app, task, o.status)
		if err != nil {
			return change, err
		}
		change.update.Status = &status
		note("status: %s -> %s", task.Status.Status, status)
	}
	if o.name = strings.TrimSpace(o.name); o.name != "" && o.name != task.Name {
		change.update.Name = &o.name
		note("name: %s", o.name)
	}
	if o.set["description"] && strings.TrimSpace(o.description) != strings.TrimSpace(taskDescription(task)) {
		change.update.MarkdownContent = &o.description
		note("description: %d lines", strings.Count(strings.TrimRight(o.description, "\n"), "\n")+1)
	}
	if o.priority != "" {
		value, err := priorityValue(o.priority)
		if err != nil {
			return change, err
		}
		if current, _ := priorityValue(task.PriorityName()); current != value {
			change.update.Priority = &value
			note("priority: %s -> %s", orNone(task.PriorityName()), strings.ToLower(o.priority))
		}
	}
	if o.due != "" {
		due, err := parseDue(o.due, time.Now())
		if err != nil {
			return change, err
		}
		if !sameDay(due, int64(task.DueDate)) {
			change.update.DueDate = &due
			label := "none"
			if due != 0 {
				label = time.UnixMilli(due).Format("2006-01-02")
			}
			note("due: %s", label)
		}
	}
	current := task.TagNames()
	if o.set["tag"] {
		wanted := lowerAll(splitList(o.tags))
		for _, tag := range wanted {
			if !containsFold(current, tag) {
				change.addTags = append(change.addTags, tag)
			}
		}
		for _, tag := range current {
			if !containsFold(wanted, tag) {
				change.dropTags = append(change.dropTags, tag)
			}
		}
	}
	for _, tag := range lowerAll(splitList(o.addTags)) {
		if !containsFold(current, tag) && !containsFold(change.addTags, tag) {
			change.addTags = append(change.addTags, tag)
		}
	}
	for _, tag := range splitList(o.removeTags) {
		if containsFold(current, tag) && !containsFold(change.dropTags, tag) {
			change.dropTags = append(change.dropTags, tag)
		}
	}
	if len(change.addTags) > 0 {
		note("add tags: %s", strings.Join(change.addTags, ", "))
	}
	if len(change.dropTags) > 0 {
		note("remove tags: %s", strings.Join(change.dropTags, ", "))
	}
	add, err := assigneeIDs(app, o.assignees)
	if err != nil {
		return change, err
	}
	remove, err := assigneeIDs(app, o.unassign)
	if err != nil {
		return change, err
	}
	for _, id := range add {
		if !assigned(task, id) {
			change.update.AddAssignees = append(change.update.AddAssignees, id)
		}
	}
	for _, id := range remove {
		if assigned(task, id) {
			change.update.RemoveAssignees = append(change.update.RemoveAssignees, id)
		}
	}
	if count := len(change.update.AddAssignees); count > 0 {
		note("assign: %d", count)
	}
	if count := len(change.update.RemoveAssignees); count > 0 {
		note("unassign: %d", count)
	}
	return change, nil
}

// pickStatus offers the task's list statuses, marking the current one.
func pickStatus(app *App, task tracker.Task) (string, error) {
	names, _, err := listStatuses(app, task)
	if err != nil {
		return "", err
	}
	rows := make([]string, len(names))
	for index, name := range names {
		rows[index] = name
		if strings.EqualFold(name, task.Status.Status) {
			rows[index] += "  (current)"
		}
	}
	index, err := ui.Pick(rows, task.Ref()+" status >")
	if err != nil {
		return "", err
	}
	return names[index], nil
}

// matchStatus finds status among the task's list statuses, ignoring case.
func matchStatus(app *App, task tracker.Task, status string) (string, error) {
	names, list, err := listStatuses(app, task)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		if strings.EqualFold(name, status) {
			return name, nil
		}
	}
	return "", fmt.Errorf("%q is not a status of list %s; choose from: %s", status, list, strings.Join(names, ", "))
}

func listStatuses(app *App, task tracker.Task) ([]string, string, error) {
	client, err := app.Backend()
	if err != nil {
		return nil, "", err
	}
	list, err := client.List(task.List.ID)
	if err != nil {
		return nil, "", err
	}
	var names []string
	for _, candidate := range list.Statuses {
		names = append(names, candidate.Status)
	}
	return names, list.Name, nil
}

func taskDescription(task tracker.Task) string {
	if task.MarkdownDescription != "" {
		return task.MarkdownDescription
	}
	return task.Description
}

// editText opens text in the configured editor and returns what was saved.
func editText(app *App, name, text string) (string, error) {
	cfg, err := app.Config()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "clctl-edit-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := dir + string(os.PathSeparator) + name
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	editor := exec.Command("sh", "-c", cfg.Editor()+" "+ui.ShellQuote(path))
	editor.Stdin, editor.Stdout, editor.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := editor.Run(); err != nil {
		return "", fmt.Errorf("editor: %w", err)
	}
	edited, err := os.ReadFile(path)
	return string(edited), err
}

func priorityValue(name string) (int, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "none" {
		return 0, nil
	}
	if index := slices.Index(priorities, name); index >= 0 {
		return index + 1, nil
	}
	return 0, fmt.Errorf("priority %q: use urgent, high, normal, low or none", name)
}

// parseDue reads a due date as Unix milliseconds (local midnight); "none" gives 0.
func parseDue(text string, now time.Time) (int64, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case text == "none":
		return 0, nil
	case text == "today":
		return today.UnixMilli(), nil
	case text == "tomorrow":
		return today.AddDate(0, 0, 1).UnixMilli(), nil
	case strings.HasPrefix(text, "+") && strings.HasSuffix(text, "d"):
		days, err := strconv.Atoi(strings.TrimSuffix(text[1:], "d"))
		if err == nil {
			return today.AddDate(0, 0, days).UnixMilli(), nil
		}
	}
	day, err := time.ParseInLocation("2006-01-02", text, now.Location())
	if err != nil {
		return 0, fmt.Errorf("due date %q: use YYYY-MM-DD, today, tomorrow, +3d or none", text)
	}
	return day.UnixMilli(), nil
}

func sameDay(a, b int64) bool {
	if a == 0 || b == 0 {
		return a == b
	}
	return time.UnixMilli(a).Format("2006-01-02") == time.UnixMilli(b).Format("2006-01-02")
}

// assigneeIDs resolves names or emails to user IDs; "id:<ID>" entries are IDs already.
func assigneeIDs(app *App, names []string) ([]string, error) {
	var ids []string
	var lookup []string
	for _, name := range names {
		if id, ok := strings.CutPrefix(name, "id:"); ok {
			ids = append(ids, id)
			continue
		}
		lookup = append(lookup, name)
	}
	if len(lookup) == 0 {
		return ids, nil
	}
	resolved, err := resolveAssignees(app, lookup)
	if err != nil {
		return nil, err
	}
	return append(ids, resolved...), nil
}

func assigned(task tracker.Task, id string) bool { return task.AssignedTo(id) }

func containsFold(list []string, value string) bool {
	return slices.ContainsFunc(list, func(item string) bool { return strings.EqualFold(item, value) })
}

// lowerAll lowercases tags, as ClickUp and the local store keep them.
func lowerAll(list []string) []string {
	for index := range list {
		list[index] = strings.ToLower(list[index])
	}
	return list
}

func orNone(text string) string {
	if text == "" {
		return "none"
	}
	return text
}
