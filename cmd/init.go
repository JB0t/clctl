package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/clickup"
	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/local"
	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

const notesLayout = `clctl can keep task notes in a dated folder layout:

    <notes dir>/YYYY/MM/DD/<Space>/<Folder>/<List>/<TASK-ID>.md
    <notes dir>/YYYY/MM/DD/<Space>/<Folder>/<List>/<TASK-ID>/     (files for the task)

That layout powers notes open, notes workspace and notes push (by day, with change
tracking). Without it those commands are hidden; everything else works the same.`

type initOptions struct {
	backend, token, team, spaces, notesDir, editor string
	jiraURL, jiraEmail, jiraToken, jiraMisc        string
	localDir, prefix, name                         string
	noNotes                                        bool
}

func newInitCmd(app *App) *cobra.Command {
	var options initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up clctl: task backend, spaces and notes",
		Long: `Guided one-time setup. Re-run it any time to change settings; current values are
offered as defaults. Every prompt can be answered with a flag for scripted setups.

The task backend is where tasks, timers and comments live:
  local    clctl's own task store, a JSON file on this machine (no account needed)
  clickup  ClickUp, with a personal API token
  jira     Jira Cloud (email + API token) or Data Center (personal access token)
Notes are separate from all of them: they stay in your notes folder, and only what
you push with 'clctl notes push' reaches the backend.`,
		Example: `  clctl init
  clctl init --backend local --prefix TASK --no-notes
  clctl init --backend clickup --token pk_... --team "My Workspace" --spaces all
  clctl init --backend jira --jira-url https://example.atlassian.net --jira-email me@example.com --spaces OPS,WEB`,
		RunE: func(cmd *cobra.Command, args []string) error { return runInit(app, options) },
	}
	flags := cmd.Flags()
	flags.StringVar(&options.backend, "backend", "", "task backend: local, clickup or jira")
	flags.StringVar(&options.token, "token", "", "ClickUp personal API token (pk_...)")
	flags.StringVar(&options.team, "team", "", "ClickUp workspace ID or name")
	flags.StringVar(&options.spaces, "spaces", "", `comma-separated space IDs or names (Jira: project keys), or "all"`)
	flags.StringVar(&options.jiraURL, "jira-url", "", "Jira site URL")
	flags.StringVar(&options.jiraEmail, "jira-email", "", `Jira Cloud account email ("none" for a Data Center token)`)
	flags.StringVar(&options.jiraToken, "jira-token", "", "Jira API token (Cloud) or personal access token (Data Center)")
	flags.StringVar(&options.jiraMisc, "jira-misc-issue", "", `issue key for time logged without a task ("none" to unset)`)
	flags.StringVar(&options.localDir, "local-dir", "", "folder for the local task store")
	flags.StringVar(&options.prefix, "prefix", "", "task key prefix for a new local store (default TASK)")
	flags.StringVar(&options.name, "name", "", "your name in the local store")
	flags.StringVar(&options.notesDir, "notes-dir", "", "enable the notes layout in this directory")
	flags.BoolVar(&options.noNotes, "no-notes", false, "disable notes features")
	flags.StringVar(&options.editor, "editor", "", "editor for notes (default $VISUAL, $EDITOR, nvim, vim...)")
	return cmd
}

// initSteps numbers the setup steps as they are printed.
type initSteps int

func (s *initSteps) next(title string) {
	*s++
	fmt.Printf("%d. %s\n", *s, title)
}

func runInit(app *App, options initOptions) error {
	cfg, err := app.Config()
	if err != nil {
		return err
	}
	fmt.Printf("Setting up clctl. Settings are saved to %s\n\n", cfg.Path)
	var steps initSteps

	steps.next("Task backend")
	backend, err := chooseBackend(options.backend, cfg.Backend())
	if err != nil {
		return err
	}
	fmt.Printf("   %s\n\n", backendNames[backend])
	cfg.Set(config.KeyBackend, backend)
	switch backend {
	case "clickup":
		err = setupClickUp(cfg, options, &steps)
	case "jira":
		err = setupJira(cfg, options, &steps)
	case "local":
		err = setupLocal(cfg, options, &steps)
	}
	if err != nil {
		return err
	}

	steps.next("Notes")
	if err := chooseNotes(cfg, options); err != nil {
		return err
	}

	steps.next("Tools")
	for _, tool := range []struct{ name, purpose string }{
		{"fzf", "fuzzy pickers (falls back to numbered lists)"},
		{"tmux", "opens notes and workspaces in their own windows"},
	} {
		if _, err := exec.LookPath(tool.name); err == nil {
			fmt.Printf("   %-5s found\n", tool.name)
		} else {
			fmt.Printf("   %-5s not found: %s\n", tool.name, tool.purpose)
		}
	}
	fmt.Println()

	if err := cfg.Save(); err != nil {
		return err
	}
	app.client = nil
	if err := app.RefreshCache(true); err != nil {
		return err
	}
	fmt.Printf(`
Saved %s.

Next:
  clctl tasks list                 your open tasks
  clctl                            menu of every action
  clctl completion --help          shell completion (bash, zsh, fish, powershell)
`, cfg.Path)
	return nil
}

// chooseBackend returns the backend named by the flag, else picked (current first).
func chooseBackend(wanted, current string) (string, error) {
	if wanted != "" {
		wanted = strings.ToLower(wanted)
		if !slices.Contains(config.Backends, wanted) {
			return "", fmt.Errorf("unknown backend %q; use %s", wanted, strings.Join(config.Backends, ", "))
		}
		return wanted, nil
	}
	descriptions := map[string]string{
		"local":   "local    clctl's own task store on this machine; no account needed",
		"clickup": "clickup  ClickUp, with a personal API token",
		"jira":    "jira     Jira Cloud or Data Center",
	}
	choices := slices.Clone(config.Backends)
	if index := slices.Index(choices, current); index > 0 {
		choices = append([]string{current}, slices.Delete(choices, index, index+1)...)
	}
	rows := make([]string, len(choices))
	for index, choice := range choices {
		rows[index] = descriptions[choice]
		if choice == current {
			rows[index] += "  (current)"
		}
	}
	index, err := ui.Pick(rows, "Task backend >")
	if err != nil {
		return "", err
	}
	return choices[index], nil
}

// askSecret returns flag, else asks (Enter keeps current); without a terminal it keeps
// current.
func askSecret(flag, current, label string) (string, error) {
	if flag != "" || !ui.Interactive() {
		return firstNonEmpty(flag, current), nil
	}
	if current != "" {
		label += " (Enter keeps the current one)"
	}
	answer, err := ui.AskSecret(label)
	if err != nil || answer != "" {
		return answer, err
	}
	return current, nil
}

// askValue returns flag, else asks with current as the default; without a terminal it
// takes current.
func askValue(flag, current, label string) (string, error) {
	if flag != "" || !ui.Interactive() {
		return firstNonEmpty(flag, current), nil
	}
	return ui.Ask(label, current)
}

func setupClickUp(cfg *config.Config, options initOptions, steps *initSteps) error {
	steps.next("ClickUp API token")
	if options.token == "" {
		fmt.Println("   In ClickUp: avatar (bottom left) > Settings > Apps > API Token > Generate.")
		fmt.Println("   It starts with pk_ and acts as you, so keep it private.")
	}
	token, err := askSecret(options.token, cfg.Get(config.KeyAPIToken), "   Paste token")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("an API token is required")
	}
	client := clickup.New(token, cfg.APIURL(), "")
	me, err := client.Me()
	if err != nil {
		return fmt.Errorf("checking the token: %w", err)
	}
	fmt.Printf("   Signed in as %s (%s).\n\n", me.DisplayName(), me.Email)
	cfg.Set(config.KeyAPIToken, token)
	cfg.Set(config.KeyUserID, string(me.ID))
	cfg.Set(config.KeyUserName, me.DisplayName())

	teams, err := client.Teams()
	if err != nil {
		return err
	}
	team, err := chooseTeam(teams, options.team, cfg.Get(config.KeyTeamID))
	if err != nil {
		return err
	}
	steps.next("Workspace: " + team.Name)
	fmt.Println()
	client.TeamID = team.ID
	cfg.Set(config.KeyTeamID, team.ID)
	cfg.Set(config.KeyTeamName, team.Name)

	spaces, err := client.Spaces()
	if err != nil {
		return err
	}
	return saveSpaces(cfg, spaces, options.spaces, "Spaces", steps)
}

// saveSpaces picks the spaces (Jira: projects) clctl works with and saves them.
func saveSpaces(cfg *config.Config, spaces []tracker.Space, wanted, title string, steps *initSteps) error {
	spaceIDs, summary, err := chooseSpaces(spaces, wanted)
	if err != nil {
		return err
	}
	steps.next(title + ": " + summary)
	fmt.Println()
	cfg.Set(config.KeySpaces, strings.Join(spaceIDs, ","))
	if spaceIDs == nil {
		cfg.Set(config.KeySpaces, "all")
	}
	cfg.Set(config.KeyLegacySpace, "")
	return nil
}

func setupJira(cfg *config.Config, options initOptions, steps *initSteps) error {
	steps.next("Jira site")
	site, err := askValue(options.jiraURL, cfg.Get(config.KeyJiraURL), "   Site URL (https://<name>.atlassian.net or your Data Center URL)")
	if err != nil {
		return err
	}
	site = strings.TrimRight(strings.TrimSpace(site), "/")
	if site == "" {
		return errors.New("a Jira site URL is required")
	}
	if !strings.HasPrefix(site, "http://") && !strings.HasPrefix(site, "https://") {
		site = "https://" + site
	}
	email := options.jiraEmail
	if email == "" {
		fmt.Println("   Jira Cloud signs in with your account email and an API token")
		fmt.Println("   (id.atlassian.com > Security > API tokens). For Data Center, leave the")
		fmt.Println("   email empty and use a personal access token (Profile > Personal Access Tokens).")
		if email, err = askValue("", cfg.Get(config.KeyJiraEmail), "   Account email (empty for Data Center)"); err != nil {
			return err
		}
	}
	if strings.EqualFold(email, "none") {
		email = ""
	}
	token, err := askSecret(options.jiraToken, cfg.Get(config.KeyJiraToken), "   Paste token")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("a Jira token is required")
	}
	cfg.Set(config.KeyJiraURL, site)
	cfg.Set(config.KeyJiraEmail, email)
	client, err := jiraBackend(cfg, token)
	if err != nil {
		return err
	}
	me, err := client.Me()
	if err != nil {
		return fmt.Errorf("checking the token: %w", err)
	}
	fmt.Printf("   Signed in as %s.\n\n", me.DisplayName())
	cfg.Set(config.KeyJiraToken, token)
	cfg.Set(config.KeyUserName, me.DisplayName())

	projects, err := client.Spaces()
	if err != nil {
		return err
	}
	if err := saveSpaces(cfg, projects, options.spaces, "Projects", steps); err != nil {
		return err
	}

	steps.next("Time without a task")
	misc := options.jiraMisc
	if misc == "" {
		fmt.Println("   Jira logs time on issues. Name an issue (e.g. OPS-1 \"Meetings and admin\") to")
		fmt.Println("   log time without a task on, or leave it empty to always pick a task.")
		if misc, err = askValue("", cfg.Get(config.KeyJiraMiscIssue), "   Issue key"); err != nil {
			return err
		}
	}
	if strings.EqualFold(misc, "none") {
		misc = ""
	}
	misc = strings.ToUpper(strings.TrimSpace(misc))
	if misc != "" {
		if _, err := client.Task(misc); err != nil {
			return fmt.Errorf("issue %s: %w", misc, err)
		}
		fmt.Printf("   Time without a task goes on %s.\n\n", misc)
	} else {
		fmt.Print("   None: time is always logged on a task.\n\n")
	}
	cfg.Set(config.KeyJiraMiscIssue, misc)
	return nil
}

func setupLocal(cfg *config.Config, options initOptions, steps *initSteps) error {
	steps.next("Local task store")
	fmt.Println("   Tasks, timers and comments are kept in tasks.json in this folder. It is separate")
	fmt.Println("   from your notes; put it somewhere synced or backed up if you want it elsewhere too.")
	dir, err := askValue(options.localDir, cfg.LocalDir(), "   Folder")
	if err != nil {
		return err
	}
	dir = config.ExpandHome(strings.TrimSpace(dir))
	cfg.Set(config.KeyLocalDir, dir)
	name, err := askValue(options.name, firstNonEmpty(cfg.Get(config.KeyUserName), os.Getenv("USER")), "   Your name")
	if err != nil {
		return err
	}
	cfg.Set(config.KeyUserName, name)
	prefix := options.prefix
	_, statErr := os.Stat(filepath.Join(dir, "tasks.json"))
	if errors.Is(statErr, os.ErrNotExist) && prefix == "" && ui.Interactive() {
		if prefix, err = ui.Ask("   Task key prefix (keys look like TASK-1)", "TASK"); err != nil {
			return err
		}
	}
	if prefix != "" && !tracker.IsTaskKey(prefix+"-1") {
		return fmt.Errorf("prefix %q: use letters and digits, starting with a letter", prefix)
	}
	store, err := local.Open(dir, name, prefix)
	if err != nil {
		return err
	}
	spaces, err := store.Spaces()
	if err != nil {
		return err
	}
	fmt.Printf("   %s (%d space(s))\n\n", filepath.Join(dir, "tasks.json"), len(spaces))
	cfg.Set(config.KeyUserID, "")
	cfg.Set(config.KeySpaces, "all")
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func chooseTeam(teams []clickup.Team, wanted, current string) (clickup.Team, error) {
	if len(teams) == 0 {
		return clickup.Team{}, errors.New("this token has no workspaces")
	}
	if wanted != "" {
		for _, team := range teams {
			if team.ID == wanted || strings.EqualFold(team.Name, wanted) {
				return team, nil
			}
		}
		return clickup.Team{}, fmt.Errorf("no workspace matches %q", wanted)
	}
	if len(teams) == 1 {
		return teams[0], nil
	}
	rows := make([]string, len(teams))
	for index, team := range teams {
		rows[index] = team.Name
		if team.ID == current {
			rows[index] += "  (current)"
		}
	}
	index, err := ui.Pick(rows, "Workspace >")
	if err != nil {
		return clickup.Team{}, err
	}
	return teams[index], nil
}

// chooseSpaces returns the selected space IDs, or nil for all spaces.
func chooseSpaces(spaces []tracker.Space, wanted string) ([]string, string, error) {
	if len(spaces) == 0 {
		return nil, "none found", nil
	}
	if wanted == "" && len(spaces) == 1 {
		return nil, spaces[0].Name + " (the only space; new spaces are picked up automatically)", nil
	}
	if wanted == "" {
		rows := []string{"All spaces (new spaces are picked up automatically)"}
		for _, space := range spaces {
			rows = append(rows, space.Name)
		}
		picked, err := ui.PickMany(rows, "Spaces >")
		if err != nil {
			return nil, "", err
		}
		var names []string
		for _, index := range picked {
			if index == 0 {
				return nil, "all", nil
			}
			names = append(names, spaces[index-1].ID)
		}
		wanted = strings.Join(names, ",")
	}
	if strings.EqualFold(wanted, "all") {
		return nil, "all", nil
	}
	var ids, names []string
	for _, part := range strings.Split(wanted, ",") {
		part = strings.TrimSpace(part)
		found := false
		for _, space := range spaces {
			if space.ID == part || strings.EqualFold(space.Name, part) {
				ids, names, found = append(ids, space.ID), append(names, space.Name), true
				break
			}
		}
		if !found {
			return nil, "", fmt.Errorf("no space matches %q", part)
		}
	}
	return ids, strings.Join(names, ", "), nil
}

func chooseNotes(cfg *config.Config, options initOptions) error {
	if options.editor != "" {
		cfg.Set(config.KeyEditor, options.editor)
	}
	switch {
	case options.noNotes:
		cfg.Set(config.KeyNotesEnabled, "false")
		fmt.Print("   Notes: disabled\n\n")
		return nil
	case options.notesDir != "":
		err := enableNotes(cfg, options.notesDir)
		fmt.Println()
		return err
	}

	fmt.Println(indent(notesLayout, "   "))
	fmt.Println()
	dir := cfg.Get(config.KeyNotesDir)
	if dir == "" {
		dir = "~/notes"
	}
	status := "does not exist yet; it will be created"
	if hasDatedLayout(config.ExpandHome(dir)) {
		status = "already uses this layout"
	} else if info, err := os.Stat(config.ExpandHome(dir)); err == nil && info.IsDir() {
		status = "exists; the layout will be added alongside what is there"
	}
	rows := []string{
		fmt.Sprintf("Use %s (%s)", dir, status),
		"Use a different folder",
		"Disable notes features",
	}
	index, err := ui.Pick(rows, "Notes >")
	if err != nil {
		return err
	}
	switch index {
	case 1:
		if dir, err = ui.Ask("   Notes folder", dir); err != nil {
			return err
		}
	case 2:
		cfg.Set(config.KeyNotesEnabled, "false")
		fmt.Print("   Notes: disabled\n\n")
		return nil
	}
	if err := enableNotes(cfg, dir); err != nil {
		return err
	}
	if options.editor == "" {
		editor, err := ui.Ask("   Editor for notes", cfg.Editor())
		if err != nil {
			return err
		}
		cfg.Set(config.KeyEditor, editor)
	}
	fmt.Println()
	return nil
}

func enableNotes(cfg *config.Config, dir string) error {
	if err := os.MkdirAll(config.ExpandHome(dir), 0o755); err != nil {
		return fmt.Errorf("creating notes folder: %w", err)
	}
	cfg.Set(config.KeyNotesDir, dir)
	cfg.Set(config.KeyNotesEnabled, "true")
	fmt.Printf("   Notes: %s\n", dir)
	return nil
}

// hasDatedLayout reports whether dir already contains YYYY/MM/DD folders.
func hasDatedLayout(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]"))
	return len(matches) > 0
}

func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}
