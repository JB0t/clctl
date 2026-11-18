package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

type listChoice struct {
	ID    string
	Label string // Space / Folder / List
}

// listChoices returns every list in the configured spaces, discovered live.
func listChoices(app *App) ([]listChoice, error) {
	c, err := app.Cache()
	if err != nil {
		return nil, err
	}
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	results := make([][]listChoice, len(c.Spaces))
	errs := make([]error, len(c.Spaces))
	var group sync.WaitGroup
	for index, space := range c.Spaces {
		group.Add(1)
		go func() {
			defer group.Done()
			lists, err := client.SpaceLists(space.ID)
			if err != nil {
				errs[index] = err
				return
			}
			for _, list := range lists {
				results[index] = append(results[index], listChoice{list.ID, space.Name + " / " + list.Name})
			}
			folders, err := client.SpaceFolders(space.ID)
			if err != nil {
				errs[index] = err
				return
			}
			for _, folder := range folders {
				for _, list := range folder.Lists {
					label := space.Name + " / " + folder.Name + " / " + list.Name
					if folder.Hidden {
						label = space.Name + " / " + list.Name
					}
					results[index] = append(results[index], listChoice{list.ID, label})
				}
			}
		}()
	}
	group.Wait()
	var choices []listChoice
	for index := range results {
		if errs[index] != nil {
			return nil, errs[index]
		}
		choices = append(choices, results[index]...)
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].Label < choices[j].Label })
	return choices, nil
}

func pickList(app *App, prompt string) (string, error) {
	choices, err := listChoices(app)
	if err != nil {
		return "", err
	}
	if len(choices) == 0 {
		return "", errors.New("no lists found in the configured spaces")
	}
	rows := make([]string, len(choices))
	for index, choice := range choices {
		rows[index] = choice.Label
	}
	index, err := ui.Pick(rows, prompt)
	if err != nil {
		return "", err
	}
	return choices[index].ID, nil
}

// pickAssignees lets the user choose workspace members, with themselves listed first.
func pickAssignees(app *App) ([]string, error) {
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	members, err := client.Members()
	if err != nil {
		return nil, err
	}
	me, _ := app.UserID()
	sort.SliceStable(members, func(i, j int) bool {
		if (string(members[i].ID) == me) != (string(members[j].ID) == me) {
			return string(members[i].ID) == me
		}
		return strings.ToLower(members[i].DisplayName()) < strings.ToLower(members[j].DisplayName())
	})
	rows := []string{"(nobody)"}
	for _, member := range members {
		label := member.DisplayName()
		if string(member.ID) == me {
			label += "  (you)"
		}
		rows = append(rows, label)
	}
	picked, err := ui.PickMany(rows, "Assignees >")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, index := range picked {
		if index > 0 {
			ids = append(ids, string(members[index-1].ID))
		}
	}
	return ids, nil
}

func splitList(values []string) []string {
	var items []string
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
	}
	return items
}

func newCreateCmd(app *App) *cobra.Command {
	var listID, name, description string
	var assigneeIDs, assignees, tags []string
	var yes bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a task (prompts for anything not given)",
		Example: `  clctl tasks create
  clctl tasks create --name "Rotate certs" --tag infra
  clctl tasks create --list-id 901200000001 --name "Rotate certs" --description "" --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if listID == "" {
				if listID, err = pickList(app, "List >"); err != nil {
					return err
				}
			}
			if name == "" {
				if name, err = ui.Ask("Name", ""); err != nil {
					return err
				}
				if name == "" {
					return errors.New("a task name is required")
				}
			}
			if !cmd.Flags().Changed("description") && ui.Interactive() {
				if description, err = ui.Ask("Description (Markdown, optional)", ""); err != nil {
					return err
				}
			}
			ids := assigneeIDs
			if len(assignees) > 0 {
				resolved, err := resolveAssignees(app, assignees)
				if err != nil {
					return err
				}
				ids = append(ids, resolved...)
			}
			if !cmd.Flags().Changed("assignee-id") && len(assignees) == 0 && ui.Interactive() {
				if ids, err = pickAssignees(app); err != nil {
					return err
				}
			}
			tagList := splitList(tags)
			if !cmd.Flags().Changed("tag") && ui.Interactive() && !yes {
				answer, err := ui.Ask("Tags (comma-separated, optional)", "")
				if err != nil {
					return err
				}
				tagList = splitList([]string{answer})
			}
			payload := tracker.NewTask{Name: name, MarkdownContent: description, Assignees: ids, Tags: tagList}
			if !yes {
				fmt.Printf("\nName:        %s\nDescription: %s\nAssignees:   %d\nTags:        %s\n", name, description, len(ids), strings.Join(tagList, ", "))
				ok, err := ui.Confirm("Create this task?", true)
				if err != nil || !ok {
					return ui.ErrCancelled
				}
			}
			created, err := createTask(app, listID, payload)
			if err != nil {
				return err
			}
			if !yes && ui.Interactive() {
				return postCreateActions(app, created)
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&listID, "list-id", "", "list ID (pick when omitted)")
	flags.StringVar(&name, "name", "", "task name")
	flags.StringVar(&description, "description", "", "task description (Markdown)")
	flags.StringSliceVar(&assigneeIDs, "assignee-id", nil, "assignee user ID; repeat for several")
	flags.StringArrayVar(&assignees, "assignee", nil, "assignee name or email; repeat for several")
	flags.StringArrayVar(&tags, "tag", nil, "tag; repeat or comma-separate")
	flags.BoolVar(&yes, "yes", false, "create without confirmation or follow-up prompts")
	return cmd
}

// createTask creates a task and adds it to the cache.
func createTask(app *App, listID string, payload tracker.NewTask) (tracker.Task, error) {
	client, err := app.Backend()
	if err != nil {
		return tracker.Task{}, err
	}
	created, err := client.CreateTask(listID, payload)
	if err != nil {
		return tracker.Task{}, err
	}
	// ClickUp assigns the custom ID (e.g. TECH-123) just after creation, so the
	// create response often lacks it: re-fetch briefly until it appears.
	for attempt := 0; created.CustomID == "" && client.Kind() == "clickup" && attempt < 5; attempt++ {
		time.Sleep(500 * time.Millisecond)
		if fetched, err := client.Task(created.ID); err == nil {
			created = fetched
		}
	}
	created = app.WithSpaceName(created)
	if c, err := app.Cache(); err == nil {
		_ = c.Upsert(created)
	}
	fmt.Printf("Created %s: %s\n", created.Ref(), created.Name)
	if created.URL != "" {
		fmt.Println(created.URL)
	}
	return created, nil
}

// postCreateActions offers to start notes and/or a timer on a new task.
func postCreateActions(app *App, task tracker.Task) error {
	cfg, _ := app.Config()
	choices := []string{"Start timer", "Nothing"}
	if cfg.NotesEnabled() {
		choices = []string{"Make notes", "Start timer", "Make notes + start timer", "Nothing"}
	}
	index, err := ui.Pick(choices, task.Ref()+" created >")
	if err != nil {
		return nil
	}
	switch choices[index] {
	case "Start timer":
		return startTimer(app, task)
	case "Make notes":
		no := false
		return openTaskNote(app, task, &no)
	case "Make notes + start timer":
		yes := true
		return openTaskNote(app, task, &yes)
	}
	return nil
}

const todoPrefix = "new: "

type todoEntry struct {
	title, description string
	start, end         int
}

// parseTodo returns `new: ` entries: the title line plus the lines up to the next entry.
func parseTodo(lines []string) []todoEntry {
	var entries []todoEntry
	for index, line := range lines {
		if !strings.HasPrefix(line, todoPrefix) {
			continue
		}
		end := index + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], todoPrefix) {
			end++
		}
		body := append([]string(nil), lines[index+1:end]...)
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		entries = append(entries, todoEntry{
			title:       strings.TrimSpace(strings.TrimPrefix(line, todoPrefix)),
			description: strings.TrimSpace(dedent(body)),
			start:       index,
			end:         end,
		})
	}
	return entries
}

// dedent removes the common leading whitespace of non-blank lines.
func dedent(lines []string) string {
	common := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if common < 0 || indent < common {
			common = indent
		}
	}
	out := make([]string, len(lines))
	for index, line := range lines {
		if len(line) >= common && common > 0 {
			line = line[common:]
		}
		out[index] = line
	}
	return strings.Join(out, "\n")
}

func newPushTodoCmd(app *App) *cobra.Command {
	var file, listID string
	var tags []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "push-todo",
		Short: "Create tasks from 'new: ' lines in a TODO file",
		Long: `Each line starting with "new: " becomes a task named after the rest of the line; the
indented lines below it (up to the next entry) become its Markdown description. Tasks are
assigned to you, and pushed entries are removed from the file as they are created.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				root, err := app.NotesRoot()
				if err != nil {
					return errors.New("pass --file; notes are disabled so there is no default TODO.md")
				}
				file = filepath.Join(root, "TODO.md")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			entries := parseTodo(lines)
			if len(entries) == 0 {
				fmt.Printf("No '%s' entries in %s.\n", todoPrefix, file)
				return nil
			}
			var assignees []string
			if !dryRun {
				me, err := app.UserID()
				if err != nil {
					return err
				}
				assignees = []string{me}
			}
			pushed := map[int]bool{}
			count := 0
			for _, entry := range entries {
				fmt.Println("\n---", entry.title)
				if dryRun {
					if entry.description != "" {
						fmt.Println(entry.description)
					}
					continue
				}
				target := listID
				if target == "" {
					label := entry.title
					if len(label) > 40 {
						label = label[:40]
					}
					if target, err = pickList(app, label+" | List >"); err != nil {
						fmt.Println("Selection cancelled; stopping.")
						break
					}
				}
				payload := tracker.NewTask{Name: entry.title, MarkdownContent: entry.description, Assignees: assignees, Tags: splitList(tags)}
				if _, err := createTask(app, target, payload); err != nil {
					fmt.Fprintf(os.Stderr, "Leaving '%s' in %s: %v\n", entry.title, file, err)
					continue
				}
				for line := entry.start; line < entry.end; line++ {
					pushed[line] = true
				}
				count++
				if err := writeRemaining(file, lines, pushed); err != nil {
					return err
				}
			}
			if !dryRun {
				fmt.Printf("\nPushed %d of %d TODO entries.\n", count, len(entries))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "TODO file (default <notes dir>/TODO.md)")
	cmd.Flags().StringVar(&listID, "list-id", "", "list for every entry (pick per entry when omitted)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag for every created task; repeat for several")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the parsed entries without creating tasks")
	return cmd
}

func writeRemaining(file string, lines []string, pushed map[int]bool) error {
	var remaining []string
	for index, line := range lines {
		if !pushed[index] {
			remaining = append(remaining, line)
		}
	}
	return os.WriteFile(file, []byte(strings.TrimRight(strings.Join(remaining, "\n"), "\n")+"\n"), 0o644)
}
