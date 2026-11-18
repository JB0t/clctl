package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/sessions"
	"github.com/JB0t/clctl/internal/ui"
)

// defaultExportCron is when the nightly export runs: 02:30, as the README suggests.
const defaultExportCron = "30 2 * * *"

// sessionsInit holds the answers 'sessions init' collects (flags fill them up front).
type sessionsInit struct {
	claudeDir, copilotDir string
	user, server          string
	ncUser, ncPassFile    string
	schedule              string
	noCron                bool
}

func newSessionsInitCmd() *cobra.Command {
	var options sessionsInit
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up which Claude Code and Copilot sessions to digest and export, and the nightly export",
		Long: `Walks through setting up session digests and exports:

  1. which assistants to read: Claude Code, GitHub Copilot (VS Code Copilot Chat) or both;
  2. their folders, offering the ones found on this machine with how many sessions each has;
  3. the Nextcloud URL, your folder name under Shared/Sessions, the account, and the file
     holding its password (it can write that file for you, mode 0600);
  4. a dry run of the export, to show what would upload;
  5. the nightly cron line, added to your crontab only when you say yes, and never twice.

The answers go in the clctl config (sessions_* keys), so 'clctl sessions export' and
'clctl sessions digest' need no arguments afterwards. The password is never written to
the config. Without a terminal, the flags give the answers and the cron line is printed,
not installed.`,
		Example: `  clctl sessions init
  clctl sessions init --claude-dir ~/.claude/projects --copilot-dir ~/.config/Code/User \
    --to https://cloud.example --user jo --nc-user 'Data Bot' --nc-pass-file ~/.config/clctl/nc-data-bot`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := sessionsConfig(cmd)
			if err != nil {
				return err
			}
			return runSessionsInit(cfg, options, ui.Interactive())
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.claudeDir, "claude-dir", "", "Claude Code projects folder (none to stop reading Claude Code)")
	flags.StringVar(&options.copilotDir, "copilot-dir", "", "VS Code user folder with Copilot Chat sessions (none to stop reading Copilot)")
	flags.StringVar(&options.server, "to", "", "Nextcloud URL")
	flags.StringVar(&options.user, "user", "", "your folder name under Shared/Sessions")
	flags.StringVar(&options.ncUser, "nc-user", "", "Nextcloud account that uploads")
	flags.StringVar(&options.ncPassFile, "nc-pass-file", "", "file holding that account's (app) password")
	flags.StringVar(&options.schedule, "cron", defaultExportCron, "cron schedule for the nightly export")
	flags.BoolVar(&options.noCron, "no-cron", false, "do not offer to add the cron line")
	return cmd
}

func runSessionsInit(cfg *config.Config, o sessionsInit, interactive bool) error {
	home, _ := os.UserHomeDir()
	claudeFound, copilotFound := sessionCandidates(home, wslCodeDirs())

	// 1-2. Assistants and their folders. Without a terminal, a folder not given keeps
	// its configured value.
	if !interactive {
		if o.claudeDir == "" {
			o.claudeDir = cfg.Get(config.KeySessionsClaudeDir)
		}
		if o.copilotDir == "" {
			o.copilotDir = cfg.Get(config.KeySessionsCopilotDir)
		}
	}
	wantClaude, wantCopilot := o.claudeDir != "", o.copilotDir != ""
	if interactive && !wantClaude && !wantCopilot {
		choices := []string{"Claude Code and GitHub Copilot", "Claude Code only", "GitHub Copilot only (VS Code Copilot Chat)"}
		index, err := ui.Pick(choices, "Which sessions? >")
		if err != nil {
			return err
		}
		wantClaude, wantCopilot = index != 2, index != 1
	}
	var err error
	if wantClaude && interactive && o.claudeDir == "" {
		if o.claudeDir, err = pickSessionDir("Claude Code projects folder", claudeFound, filepath.Join(home, ".claude", "projects")); err != nil {
			return err
		}
	}
	if wantCopilot && interactive && o.copilotDir == "" {
		if o.copilotDir, err = pickSessionDir("VS Code user folder", copilotFound, filepath.Join(home, ".config", "Code", "User")); err != nil {
			return err
		}
	}
	if !isSessionDir(o.claudeDir) && !isSessionDir(o.copilotDir) {
		return errors.New("no session folders: give --claude-dir and/or --copilot-dir")
	}
	setDir := func(key, dir string) {
		if dir == "none" {
			dir = ""
		}
		cfg.Set(key, dir)
	}
	setDir(config.KeySessionsClaudeDir, o.claudeDir)
	setDir(config.KeySessionsCopilotDir, o.copilotDir)

	// 3. Where the export goes, and the account that uploads it.
	ask := func(value *string, label, key, fallback string) error {
		if *value == "" {
			*value = cfg.Get(key)
		}
		if *value == "" {
			*value = fallback
		}
		if interactive {
			answer, err := ui.Ask(label, *value)
			if err != nil {
				return err
			}
			*value = answer
		}
		cfg.Set(key, *value)
		return nil
	}
	if err := ask(&o.server, "Nextcloud URL", config.KeySessionsTo, ""); err != nil {
		return err
	}
	if err := ask(&o.user, "Your folder name under Shared/Sessions", config.KeySessionsUser, os.Getenv("USER")); err != nil {
		return err
	}
	if err := ask(&o.ncUser, "Nextcloud account that uploads", config.KeySessionsNCUser, os.Getenv("NC_USER")); err != nil {
		return err
	}
	if err := ask(&o.ncPassFile, "File holding that account's password", config.KeySessionsNCPassFile, "~/.config/clctl/nc-sessions"); err != nil {
		return err
	}
	if err := ensurePassFile(config.ExpandHome(o.ncPassFile), interactive); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("Saved the session settings to %s.\n", cfg.Path)

	// 4. What the export would upload.
	var dirs []string
	for _, dir := range []string{o.claudeDir, o.copilotDir} {
		if isSessionDir(dir) {
			dirs = append(dirs, config.ExpandHome(dir))
		}
	}
	since, _ := parseSince("30d")
	var out bytes.Buffer
	if err := runSessionsExport(exportOptions{dirs: dirs, user: o.user, server: o.server, since: since, dryRun: true,
		statePath: statePath("sessions-export.json"), getenv: exportEnv(cfg, os.Getenv), out: &out}); err != nil {
		fmt.Fprintln(os.Stderr, "Dry run failed:", err)
	} else {
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		fmt.Printf("Dry run (last 30 days): %s\n", lines[len(lines)-1])
	}

	// 5. The nightly export.
	if o.noCron {
		return nil
	}
	line, err := exportCronLine(o.schedule, cfg.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath("sessions-export.log")), 0o755); err != nil {
		return err
	}
	current, err := readCrontab()
	if err != nil {
		return err
	}
	if cronHasExport(current) {
		fmt.Println("Your crontab already runs 'clctl sessions export'; left it as it is.")
		return nil
	}
	fmt.Printf("Nightly export cron line:\n  %s\n", line)
	if !interactive {
		fmt.Println("Add it with 'crontab -e', or run 'clctl sessions init' in a terminal.")
		return nil
	}
	add, err := ui.Confirm("Add it to your crontab?", true)
	if err != nil || !add {
		return err
	}
	if err := writeCrontab(appendCronLine(current, line)); err != nil {
		return err
	}
	fmt.Println("Added. The log goes to", statePath("sessions-export.log"))
	return nil
}

// isSessionDir reports whether a folder answer names a folder ("none" turns one off).
func isSessionDir(dir string) bool { return dir != "" && dir != "none" }

// sessionCandidates lists the Claude Code and VS Code folders found under home (and the
// Windows user folders WSL sees) that exist.
func sessionCandidates(home string, extraCopilot []string) (claude, copilot []string) {
	exists := func(dir string) bool {
		info, err := os.Stat(dir)
		return err == nil && info.IsDir()
	}
	if dir := filepath.Join(home, ".claude", "projects"); exists(dir) {
		claude = append(claude, dir)
	}
	for _, dir := range append([]string{
		filepath.Join(home, ".config", "Code", "User"),
		filepath.Join(home, ".config", "Code - Insiders", "User"),
		filepath.Join(home, "Library", "Application Support", "Code", "User"),
		filepath.Join(home, "Library", "Application Support", "Code - Insiders", "User"),
		filepath.Join(home, ".vscode-server", "data", "User"),
	}, extraCopilot...) {
		if exists(dir) {
			copilot = append(copilot, dir)
		}
	}
	return claude, copilot
}

// wslCodeDirs are the Windows VS Code user folders, seen from WSL.
func wslCodeDirs() []string {
	dirs, _ := filepath.Glob("/mnt/c/Users/*/AppData/Roaming/Code/User")
	return dirs
}

// pickSessionDir offers the folders found, with their session counts, or another one.
func pickSessionDir(label string, found []string, fallback string) (string, error) {
	if len(found) == 0 {
		return ui.Ask(label+" (none found)", fallback)
	}
	rows := make([]string, 0, len(found)+1)
	for _, dir := range found {
		paths, _ := sessions.Transcripts(dir)
		rows = append(rows, fmt.Sprintf("%s  (%d sessions)", dir, len(paths)))
	}
	rows = append(rows, "another folder...")
	index, err := ui.Pick(rows, label+" >")
	if err != nil {
		return "", err
	}
	if index < len(found) {
		return found[index], nil
	}
	return ui.Ask(label, fallback)
}

// ensurePassFile offers to write the password file when it is missing.
func ensurePassFile(path string, interactive bool) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if !interactive {
		fmt.Printf("Note: %s does not exist yet; the export needs it.\n", path)
		return nil
	}
	write, err := ui.Confirm(fmt.Sprintf("%s does not exist. Write the password to it now (mode 0600)?", path), true)
	if err != nil {
		return err
	}
	if !write {
		fmt.Printf("Create %s (mode 0600) before the first export.\n", path)
		return nil
	}
	password, err := ui.AskSecret("Nextcloud (app) password")
	if err != nil {
		return err
	}
	if password == "" {
		return errors.New("no password given")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(password+"\n"), 0o600)
}

// exportCronLine runs the configured export with this binary and config, logging to the
// state folder.
func exportCronLine(schedule, configPath string) (string, error) {
	if len(strings.Fields(schedule)) != 5 {
		return "", fmt.Errorf("--cron %q: want five cron fields, like %q", schedule, defaultExportCron)
	}
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	if absolute, err := filepath.Abs(configPath); err == nil {
		configPath = absolute
	}
	return fmt.Sprintf("%s %s --config %s sessions export >> %s 2>&1", schedule, ui.ShellQuote(binary),
		ui.ShellQuote(configPath), ui.ShellQuote(statePath("sessions-export.log"))), nil
}

// cronHasExport reports whether a crontab already runs 'clctl sessions export' (lines
// that are commented out do not count).
func cronHasExport(crontab string) bool {
	for _, line := range strings.Split(crontab, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") && strings.Contains(line, "clctl") && strings.Contains(line, "sessions export") {
			return true
		}
	}
	return false
}

func appendCronLine(crontab, line string) string {
	if crontab != "" && !strings.HasSuffix(crontab, "\n") {
		crontab += "\n"
	}
	return crontab + line + "\n"
}

// readCrontab returns the user's crontab, "" when there is none.
func readCrontab() (string, error) {
	if _, err := exec.LookPath("crontab"); err != nil {
		return "", errors.New("crontab is not installed; add the export to your scheduler by hand")
	}
	var stderr bytes.Buffer
	command := exec.Command("crontab", "-l")
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func writeCrontab(text string) error {
	command := exec.Command("crontab", "-")
	command.Stdin = strings.NewReader(text)
	command.Stderr = os.Stderr
	return command.Run()
}
