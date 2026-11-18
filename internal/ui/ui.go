// Package ui holds the interactive bits: fzf pickers and terminal prompts.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// ErrCancelled means the user backed out of a picker or prompt (Esc / Ctrl+C / empty).
// In Steps it goes back to the previous prompt.
var ErrCancelled = errors.New("cancelled")

// ErrAbort is a cancellation that Steps does not go back on: an explicit "Cancel" row.
// It still counts as cancelled (errors.Is(err, ErrCancelled)).
var ErrAbort = fmt.Errorf("%w: aborted", ErrCancelled)

var stdin = bufio.NewReader(os.Stdin)

// prompts counts every picker and question asked, so Steps knows which steps asked anything.
var prompts int

// Steps runs prompting steps in order, each usually storing its answer in a variable the
// later steps read. Esc (ErrCancelled) in a step goes back to the nearest earlier step that
// asked something, so a wrong pick is undone one step at a time; Esc at the first prompt,
// or ErrAbort anywhere, cancels the whole thing. Steps that asked nothing (a task given by
// --task-id, a single note) are skipped on the way back.
func Steps(steps ...func() error) error {
	asked := make([]bool, len(steps))
	for index := 0; index < len(steps); {
		before := prompts
		err := steps[index]()
		asked[index] = prompts != before
		if err == nil {
			index++
			continue
		}
		if err != ErrCancelled { // wrapped cancellations (ErrAbort) and other errors
			return err
		}
		back := index - 1
		for back >= 0 && !asked[back] {
			back--
		}
		if back < 0 {
			return err
		}
		index = back
	}
	return nil
}

// Interactive reports whether stdin and stdout are terminals.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Color reports whether stdout should get ANSI colours.
func Color() bool {
	return os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))
}

// HasFzf reports whether fzf is on PATH.
func HasFzf() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

// FzfMultiArgs turns on fzf's multi-select, with keys to mark or clear every row.
var FzfMultiArgs = []string{"--multi", "--bind=ctrl-a:select-all,ctrl-d:deselect-all",
	"--header=Tab: mark  Ctrl+A: mark all  Ctrl+D: clear marks"}

// Pick lets the user choose one row and returns its index.
func Pick(rows []string, prompt string) (int, error) {
	picked, err := pick(rows, prompt, false)
	if err != nil {
		return -1, err
	}
	return picked[0], nil
}

// PickMany lets the user choose rows and returns their indexes in row order. In fzf, Tab
// marks a row, Ctrl+A marks every row and Ctrl+D clears the marks; in the numbered list,
// "all" picks every row.
func PickMany(rows []string, prompt string) ([]int, error) {
	return pick(rows, prompt, true)
}

func pick(rows []string, prompt string, multi bool) ([]int, error) {
	if len(rows) == 0 {
		return nil, errors.New("nothing to choose from")
	}
	prompts++
	if !HasFzf() {
		return pickNumbered(rows, prompt, multi)
	}
	// Prefix each row with its index so duplicate labels stay distinguishable.
	var input strings.Builder
	for index, row := range rows {
		fmt.Fprintf(&input, "%d\t%s\n", index, row)
	}
	args := []string{"--height=80%", "--layout=reverse", "--border", "--prompt=" + prompt + " ",
		"--delimiter=\t", "--with-nth=2..", "--tiebreak=index"}
	if multi {
		args = append(args, FzfMultiArgs...)
	}
	command := exec.Command("fzf", args...)
	command.Stdin = strings.NewReader(input.String())
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		return nil, ErrCancelled
	}
	var picked []int
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		index, _, _ := strings.Cut(line, "\t")
		if value, err := strconv.Atoi(index); err == nil {
			picked = append(picked, value)
		}
	}
	if len(picked) == 0 {
		return nil, ErrCancelled
	}
	return picked, nil
}

func pickNumbered(rows []string, prompt string, multi bool) ([]int, error) {
	for index, row := range rows {
		fmt.Printf("%3d  %s\n", index+1, row)
	}
	hint := "number"
	if multi {
		hint = "numbers, comma-separated, or all"
	}
	answer, err := Ask(fmt.Sprintf("%s (%s)", strings.TrimSpace(prompt), hint), "")
	if err != nil || answer == "" {
		return nil, ErrCancelled
	}
	var picked []int
	if multi && strings.EqualFold(answer, "all") {
		for index := range rows {
			picked = append(picked, index)
		}
		return picked, nil
	}
	for _, part := range strings.Split(answer, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 1 || value > len(rows) {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		picked = append(picked, value-1)
		if !multi {
			break
		}
	}
	return picked, nil
}

// Ask prompts for a line of text, returning fallback when the answer is empty.
func Ask(label, fallback string) (string, error) {
	prompts++
	if fallback != "" {
		fmt.Printf("%s [%s]: ", label, fallback)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", ErrCancelled
	}
	if line = strings.TrimSpace(line); line == "" {
		return fallback, nil
	}
	return line, nil
}

// AskSecret prompts without echoing input.
func AskSecret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return Ask("", "")
	}
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", ErrCancelled
	}
	return strings.TrimSpace(string(secret)), nil
}

// Confirm asks a yes/no question.
func Confirm(label string, fallback bool) (bool, error) {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}
	answer, err := Ask(fmt.Sprintf("%s [%s]", label, hint), "")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "":
		return fallback, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// TmuxWindow switches to tmux window `name`, creating it in dir (running command, if any)
// when it does not exist. It returns false when not running inside tmux.
func TmuxWindow(name, dir, command string) (bool, error) {
	if !inTmux() {
		return false, nil
	}
	if switched, err := selectTmuxWindow(name); switched || err != nil {
		return true, err
	}
	args := []string{"new-window", "-n", name, "-c", dir}
	if command != "" {
		args = append(args, command)
	}
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return true, fmt.Errorf("opening tmux window: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Opened tmux window '%s' in %s.\n", name, dir)
	return true, nil
}

// inTmux reports whether clctl runs inside tmux with the tmux command available.
func inTmux() bool {
	if os.Getenv("TMUX") == "" {
		return false
	}
	_, err := exec.LookPath("tmux")
	return err == nil
}

// TmuxCurrentWindow returns the name of the tmux window clctl runs in, or "" outside tmux.
func TmuxCurrentWindow() string {
	if !inTmux() {
		return ""
	}
	args := []string{"display-message", "-p"}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = append(args, "-t", pane)
	}
	output, err := exec.Command("tmux", append(args, "#W")...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// TmuxType types a command line into clctl's own tmux pane. The shell that started
// clctl reads it once clctl exits, so this is how clctl can change that shell's directory.
func TmuxType(line string) error {
	target := []string{}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		target = []string{"-t", pane}
	}
	if err := exec.Command("tmux", append(append([]string{"send-keys"}, target...), "-l", line)...).Run(); err != nil {
		return err
	}
	return exec.Command("tmux", append(append([]string{"send-keys"}, target...), "Enter")...).Run()
}

// TmuxWorkspace switches to tmux window `name`, or creates it in dir laid out as:
//
//	+----------------+----------------+
//	| noteCommand    |                |
//	|     (70%)      | browseCommand  |
//	+----------------+     (50%)      |
//	| shell (30%)    |                |
//	+----------------+----------------+
//
// Each command leaves a shell behind when it exits. It returns false outside tmux.
func TmuxWorkspace(name, dir, noteCommand, browseCommand string) (bool, error) {
	if !inTmux() {
		return false, nil
	}
	if switched, err := selectTmuxWindow(name); switched || err != nil {
		return true, err
	}
	keepShell := `; exec "${SHELL:-sh}"`
	run := func(args ...string) (string, error) {
		output, err := exec.Command("tmux", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	}
	note, err := run("new-window", "-P", "-F", "#{pane_id}", "-n", name, "-c", dir, noteCommand+keepShell)
	if err != nil {
		return true, err
	}
	if _, err := run("split-window", "-h", "-l", "50%", "-t", note, "-c", dir, browseCommand+keepShell); err != nil {
		return true, err
	}
	if _, err := run("split-window", "-v", "-l", "30%", "-t", note, "-c", dir); err != nil {
		return true, err
	}
	if _, err := run("select-pane", "-t", note); err != nil {
		return true, err
	}
	fmt.Fprintf(os.Stderr, "Opened tmux window '%s' in %s.\n", name, dir)
	return true, nil
}

// TmuxRefreshWindow changes every shell pane of tmux window `name` to dir, by typing
// `cd` into it, and switches to the window. Panes running something else (an editor on
// an older day's note) are left alone and named in skipped. clctl's own pane counts as
// a shell, since the shell that started clctl reads the cd when clctl exits; with
// ownPane false it is left alone too (a menu reads the terminal again afterwards).
// It returns found false when the window does not exist or clctl is not inside tmux.
func TmuxRefreshWindow(name, dir string, ownPane bool) (found bool, changed int, skipped []string, err error) {
	if !inTmux() {
		return false, 0, nil, nil
	}
	output, err := exec.Command("tmux", "list-panes", "-t", "="+name, "-F", "#{pane_id}\t#{pane_current_command}").Output()
	if err != nil {
		return false, 0, nil, nil // no such window
	}
	own := os.Getenv("TMUX_PANE")
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		pane, command, _ := strings.Cut(line, "\t")
		if pane == "" {
			continue
		}
		isOwn := pane == own
		if isOwn && !ownPane {
			continue
		}
		if !isOwn && !isShell(command) {
			skipped = append(skipped, pane+" ("+command+")")
			continue
		}
		line := "cd " + ShellQuote(dir)
		if err := exec.Command("tmux", "send-keys", "-t", pane, "-l", line).Run(); err != nil {
			return true, changed, skipped, fmt.Errorf("tmux send-keys: %w", err)
		}
		if err := exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run(); err != nil {
			return true, changed, skipped, fmt.Errorf("tmux send-keys: %w", err)
		}
		changed++
	}
	if err := exec.Command("tmux", "select-window", "-t", "="+name).Run(); err != nil {
		return true, changed, skipped, fmt.Errorf("tmux select-window: %w", err)
	}
	return true, changed, skipped, nil
}

// isShell reports whether a pane's current command is an interactive shell waiting for input.
func isShell(command string) bool {
	switch strings.TrimPrefix(command, "-") {
	case "bash", "zsh", "sh", "fish", "dash", "ksh", "tcsh", "csh":
		return true
	}
	return false
}

// selectTmuxWindow switches to window `name` in the current session when it exists.
func selectTmuxWindow(name string) (bool, error) {
	output, _ := exec.Command("tmux", "list-windows", "-F", "#W").Output()
	for _, window := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if window == name {
			fmt.Fprintf(os.Stderr, "Switched to existing tmux window '%s'.\n", name)
			return true, exec.Command("tmux", "select-window", "-t", "="+name).Run()
		}
	}
	return false, nil
}

// ShellQuote quotes a string for a POSIX shell.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
