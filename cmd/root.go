// Package cmd defines clctl's command tree.
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/JB0t/clctl/internal/ui"
)

// Annotation keys on commands.
const (
	// annotationNotes marks commands that need the notes layout; they are removed when
	// notes are disabled in the config.
	annotationNotes = "clctl/notes"
	// annotationTaskAction marks commands offered by `tasks search` once a task is picked.
	annotationTaskAction = "clctl/task-action"
	// annotationMenu marks top-level commands whose actions plain `clctl` offers.
	annotationMenu = "clctl/menu"
)

var version = "dev"

// Execute runs clctl and returns the process exit code.
func Execute() int {
	// The command tree depends on the config (notes commands are removed when notes are
	// off), so --config has to be known before cobra parses flags.
	app := &App{ConfigPath: configFlag(os.Args[1:])}
	root := newRoot(app)
	root.SetArgs(rewriteActionFlags(root, os.Args[1:]))
	err := root.Execute()
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ui.ErrCancelled):
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return 130
	default:
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
}

func newRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "clctl",
		Short:         "Tasks, timers, notes and comments from the terminal (local, ClickUp or Jira)",
		Version:       buildVersion(version),
		SilenceUsage:  true,
		SilenceErrors: true,
		// Plain `clctl` opens a menu of every task, timer, notes and comments action;
		// `clctl -r` only refreshes the task cache.
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ForceRefresh {
				return app.RefreshCache(true)
			}
			return runMenu(app, cmd)
		},
		ValidArgsFunction: completeShortCommands,
	}
	root.PersistentFlags().BoolVarP(&app.ForceRefresh, "refresh", "r", false, "refresh the task cache before running (alone: only refresh it)")
	root.PersistentFlags().BoolVar(&app.IncludeDone, "include-done", false, "also list and offer done and closed tasks (closed ones are fetched from the task backend)")
	configPath := app.ConfigPath // defining the flag resets the field to its default
	root.PersistentFlags().StringVar(&app.ConfigPath, "config", "", "config file (default $XDG_CONFIG_HOME/clctl)")
	app.ConfigPath = configPath

	root.AddCommand(newInitCmd(app), newCacheCmd(app), newTasksCmd(app), newTimersCmd(app), newNotesCmd(app),
		newCommentsCmd(app), newDigestCmd(app), newIndexCmd(app), newSemanticSearchCmd(app), newSessionsCmd())
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newCompletionCmd(root))
	removeDisabled(app, root)
	addShorts(root)
	return root
}

// commandShorts are short names for top-level commands: `clctl t` is `clctl tasks`.
var commandShorts = map[string]string{"tasks": "t", "timers": "ti", "notes": "n", "search": "s"}

// actionShorts are one-letter flags that run a group's actions, tmux style: `clctl t -l`
// is `clctl tasks list`, and `clctl t -l --assignee jo` passes --assignee on to list.
// Actions in noActionShort have none; h, r and v are taken by help, refresh and version.
var actionShorts = map[string]map[string]string{
	"tasks":    {"list": "l", "open": "o", "update": "u", "search": "s", "status": "i", "create": "c", "attach": "a", "export": "e"},
	"timers":   {"start": "s", "stop": "x", "pause": "p", "resume": "u", "switch": "c", "list": "l", "watch": "w", "log": "g", "edit": "e"},
	"notes":    {"open": "o", "workspace": "w", "push": "p", "unpushed": "u", "tldr": "t"},
	"comments": {"add": "a", "list": "l", "fix-tables": "f"},
	"sessions": {"digest": "d", "export": "e", "init": "i"},
}

var noActionShort = []string{"tasks push-todo", "tasks complete", "tasks refresh"}

// addShorts gives top-level commands their short names and groups their action flags.
// The flags are for help and Tab completion; rewriteActionFlags does the dispatch.
func addShorts(root *cobra.Command) {
	for _, child := range root.Commands() {
		if short, ok := commandShorts[child.Name()]; ok {
			child.Aliases = append(child.Aliases, short)
		}
		for _, action := range child.Commands() {
			if letter, ok := actionShorts[child.Name()][action.Name()]; ok && !action.Hidden {
				child.Flags().BoolP(action.Name(), letter, false, action.Short)
			}
		}
	}
}

// rewriteActionFlags turns a group's action flag into the action's name before cobra
// parses the arguments: `t -l --assignee jo` becomes `t list --assignee jo`. Tab
// completion requests go through it too, except for the word being completed.
func rewriteActionFlags(root *cobra.Command, args []string) []string {
	out := append([]string(nil), args...)
	start, end := 0, len(out)
	if len(out) > 0 && (out[0] == cobra.ShellCompRequestCmd || out[0] == cobra.ShellCompNoDescRequestCmd) {
		start, end = 1, len(out)-1
	}
	current := root
	for index := start; index < end; index++ {
		arg := out[index]
		switch {
		case arg == "--":
			return out
		case strings.HasPrefix(arg, "-"):
			if action := actionForFlag(current, arg); action != "" {
				out[index] = action
				return out
			}
			if arg == "--config" {
				index++ // its value
			}
		default:
			next := subcommand(current, arg)
			if next == nil || !next.HasSubCommands() {
				return out // an argument, or a leaf command whose flags are its own
			}
			current = next
		}
	}
	return out
}

// actionForFlag returns the action a group's -x or --name flag stands for, or "".
func actionForFlag(group *cobra.Command, flag string) string {
	if !group.HasParent() || group.Parent().HasParent() {
		return ""
	}
	for action, letter := range actionShorts[group.Name()] {
		if flag == "-"+letter || flag == "--"+action {
			if child := subcommand(group, action); child != nil && !child.Hidden {
				return action
			}
		}
	}
	return ""
}

func subcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name || child.HasAlias(name) {
			return child
		}
	}
	return nil
}

// completeShortCommands offers the short command names on Tab, next to the full ones
// cobra offers.
func completeShortCommands(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var shorts []string
	for _, child := range cmd.Commands() {
		short, ok := commandShorts[child.Name()]
		if ok && child.IsAvailableCommand() && strings.HasPrefix(short, toComplete) {
			shorts = append(shorts, short+"\t"+child.Name()+": "+child.Short)
		}
	}
	return shorts, cobra.ShellCompDirectiveNoFileComp
}

// configFlag finds a --config value in raw arguments.
func configFlag(args []string) string {
	for index, arg := range args {
		if arg == "--" {
			break
		}
		if value, ok := strings.CutPrefix(arg, "--config="); ok {
			return value
		}
		if arg == "--config" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

// removeDisabled drops notes-only commands when notes are turned off.
func removeDisabled(app *App, root *cobra.Command) {
	cfg, err := app.Config()
	if err != nil || cfg.NotesEnabled() {
		return
	}
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, child := range parent.Commands() {
			if child.Annotations[annotationNotes] != "" {
				parent.RemoveCommand(child)
				continue
			}
			walk(child)
		}
	}
	walk(root)
}

// menuActions returns the actions a menu offers, with their menu names: a group's own
// actions, or for the root every action of the groups (and commands) marked for the menu.
func menuActions(parent *cobra.Command) ([]*cobra.Command, []string) {
	var actions []*cobra.Command
	var names []string
	add := func(action *cobra.Command, name string) {
		if action.IsAvailableCommand() && action.RunE != nil {
			actions = append(actions, action)
			names = append(names, name)
		}
	}
	for _, child := range parent.Commands() {
		switch {
		case parent.HasParent():
			add(child, child.Name())
		case child.Annotations[annotationMenu] == "" || !child.IsAvailableCommand():
		case child.HasAvailableSubCommands():
			for _, action := range child.Commands() {
				add(action, child.Name()+" "+action.Name())
			}
		default:
			add(child, child.Name())
		}
	}
	return actions, names
}

// runMenu loops over a command's actions with fzf. Esc or "quit" exits clctl; Esc
// inside an action only cancels that action.
func runMenu(app *App, parent *cobra.Command) error {
	if !ui.Interactive() {
		return parent.Help()
	}
	actions, names := menuActions(parent)
	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}
	rows := make([]string, 0, len(actions)+1)
	for index, action := range actions {
		rows = append(rows, fmt.Sprintf("%-*s  %s", width, names[index], action.Short))
	}
	rows = append(rows, fmt.Sprintf("%-*s  %s", width, "quit", "Exit clctl"))
	app.InMenu = true
	defer func() { app.InMenu = false }()
	for {
		index, err := ui.Pick(rows, strings.TrimSpace(parent.CommandPath())+" >")
		if err != nil || index == len(actions) {
			return nil
		}
		action := actions[index]
		if err := action.RunE(action, nil); err != nil {
			if errors.Is(err, ui.ErrCancelled) {
				fmt.Fprintln(os.Stderr, "Cancelled.")
			} else {
				fmt.Fprintln(os.Stderr, "Error:", err)
			}
		}
		resetFlags(action)
	}
}

// resetFlags restores a command's flags (and the variables bound to them) to their
// defaults between menu runs, since prompts write answers into those variables.
func resetFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if slice, ok := flag.Value.(pflag.SliceValue); ok {
			_ = slice.Replace(nil)
		} else {
			_ = flag.Value.Set(flag.DefValue)
		}
		flag.Changed = false
	})
}

// newGroup returns a command grouping related actions; run alone, it opens their menu.
func newGroup(app *App, use, short string, annotations map[string]string, actions ...*cobra.Command) *cobra.Command {
	group := &cobra.Command{
		Use:         use,
		Short:       short + " (run without an action for a menu)",
		Annotations: map[string]string{annotationMenu: "true"},
		RunE:        func(cmd *cobra.Command, args []string) error { return runMenu(app, cmd) },
	}
	for key, value := range annotations {
		group.Annotations[key] = value
	}
	group.AddCommand(actions...)
	return group
}

// legacy renames a moved command back to its old spelling, hidden and marked deprecated,
// so scripts and cron entries using that spelling keep working.
func legacy(cmd *cobra.Command, oldName, newPath string) *cobra.Command {
	_, args, _ := strings.Cut(cmd.Use, " ")
	cmd.Use = strings.TrimSpace(oldName + " " + args)
	cmd.Aliases = nil
	cmd.Deprecated = "use 'clctl " + newPath + "'"
	return cmd
}

// addTaskIDFlag adds --task-id with completion from the cache.
func addTaskIDFlag(app *App, cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "task-id", "", "task ID or custom ID (pick with fzf when omitted)")
	_ = cmd.RegisterFlagCompletionFunc("task-id", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return completeTaskIDs(app), cobra.ShellCompDirectiveNoFileComp
	})
}

// completeTaskIDs lists cached open task IDs with their names, never calling the API.
func completeTaskIDs(app *App) []string {
	cached := app.CachedOnly()
	var ids []string
	for _, task := range cached.Tasks {
		if !task.Status.Done() {
			ids = append(ids, task.Ref()+"\t"+strings.ReplaceAll(task.Name, "\t", " "))
		}
	}
	return ids
}

// newCompletionCmd is cobra's completion command with --alias added, so a shell alias
// such as cl=clctl gets the same Tab completion as clctl itself.
func newCompletionCmd(root *cobra.Command) *cobra.Command {
	var aliases []string
	var noDescriptions bool
	cmd := &cobra.Command{
		Use:   "completion bash|zsh|fish|powershell",
		Short: "Generate the shell completion script",
		Long: `Prints the completion script for a shell; scripts/install.sh installs it for you.
With --alias the script also completes a shell alias of clctl, for example after
"alias cl=clctl" in your shell profile: for bash and zsh the alias is registered next to
clctl, for fish the alias wraps clctl's completion, for PowerShell both names are
registered.`,
		Example: `  clctl completion bash --alias cl > ~/.local/share/bash-completion/completions/clctl
  clctl completion zsh --alias cl > ~/.local/share/zsh/site-functions/_clctl
  clctl completion fish --alias cl > ~/.config/fish/completions/clctl.fish`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var script bytes.Buffer
			var err error
			switch args[0] {
			case "bash":
				err = root.GenBashCompletionV2(&script, !noDescriptions)
			case "zsh":
				if noDescriptions {
					err = root.GenZshCompletionNoDesc(&script)
				} else {
					err = root.GenZshCompletion(&script)
				}
			case "fish":
				err = root.GenFishCompletion(&script, !noDescriptions)
			case "powershell":
				if noDescriptions {
					err = root.GenPowerShellCompletion(&script)
				} else {
					err = root.GenPowerShellCompletionWithDesc(&script)
				}
			default:
				return fmt.Errorf("unknown shell %q (bash, zsh, fish or powershell)", args[0])
			}
			if err != nil {
				return err
			}
			_, err = os.Stdout.WriteString(completionWithAliases(args[0], root.Name(), script.String(), aliases))
			return err
		},
	}
	cmd.Flags().StringSliceVar(&aliases, "alias", nil, "shell alias of clctl to complete too (e.g. cl); repeat or comma-separate")
	cmd.Flags().BoolVar(&noDescriptions, "no-descriptions", false, "leave the descriptions out of the completions")
	return cmd
}

// completionWithAliases registers the aliases in a shell's completion script for name.
func completionWithAliases(shell, name, script string, aliases []string) string {
	var cleaned []string
	for _, alias := range aliases {
		if alias = strings.TrimSpace(alias); alias != "" && alias != name {
			cleaned = append(cleaned, alias)
		}
	}
	if len(cleaned) == 0 {
		return script
	}
	switch shell {
	case "bash":
		for _, alias := range cleaned {
			script += fmt.Sprintf("complete -o default -F __start_%s %s\n", name, alias)
		}
	case "zsh":
		// The #compdef line is what autoloading reads; the compdef call is for sourcing.
		names := name + " " + strings.Join(cleaned, " ")
		script = strings.Replace(script, "#compdef "+name+"\n", "#compdef "+names+"\n", 1)
		script = strings.Replace(script, "compdef _"+name+" "+name+"\n", "compdef _"+name+" "+names+"\n", 1)
	case "fish":
		for _, alias := range cleaned {
			script += fmt.Sprintf("complete -c %s -w %s\n", alias, name)
		}
	case "powershell":
		quoted := []string{"'" + name + "'"}
		for _, alias := range cleaned {
			quoted = append(quoted, "'"+alias+"'")
		}
		script = strings.Replace(script, "-CommandName '"+name+"'", "-CommandName "+strings.Join(quoted, ","), 1)
	}
	return script
}
