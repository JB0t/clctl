package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/config"
)

func TestCronHasExport(t *testing.T) {
	cases := map[string]bool{
		"":                                   false,
		"# 30 2 * * * clctl sessions export": false,
		"30 2 * * * clctl notes tldr\n":      false,
		"MAILTO=me\n30 2 * * * /x/clctl --config /c sessions export >> log 2>&1\n": true,
	}
	for crontab, want := range cases {
		if got := cronHasExport(crontab); got != want {
			t.Errorf("cronHasExport(%q) = %v", crontab, got)
		}
	}
	if got := appendCronLine("a b", "c"); got != "a b\nc\n" {
		t.Errorf("appendCronLine = %q", got)
	}
	if got := appendCronLine("", "c"); got != "c\n" {
		t.Errorf("appendCronLine = %q", got)
	}
}

func TestExportCronLine(t *testing.T) {
	if _, err := exportCronLine("nightly", "/c"); err == nil {
		t.Error("a bad schedule was accepted")
	}
	line, err := exportCronLine("30 2 * * *", "/home/jo/.config/clctl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "30 2 * * * ") || !strings.Contains(line, "--config '/home/jo/.config/clctl' sessions export >> ") ||
		!strings.HasSuffix(line, "sessions-export.log' 2>&1") || !cronHasExport(line) {
		t.Errorf("line = %s", line)
	}
}

func TestSessionCandidates(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".claude/projects", ".config/Code/User", ".vscode-server/data/User"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	windows := filepath.Join(home, "mnt", "Code", "User")
	_ = os.MkdirAll(windows, 0o755)
	claude, copilot := sessionCandidates(home, []string{windows, filepath.Join(home, "missing")})
	if len(claude) != 1 || claude[0] != filepath.Join(home, ".claude", "projects") {
		t.Errorf("claude = %v", claude)
	}
	want := []string{filepath.Join(home, ".config", "Code", "User"), filepath.Join(home, ".vscode-server", "data", "User"), windows}
	if strings.Join(copilot, ",") != strings.Join(want, ",") {
		t.Errorf("copilot = %v", copilot)
	}
}

// sessionsCmd is a command with --config set, as the root gives every command.
func sessionsCmd(t *testing.T, path string) *cobra.Command {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("config", "", "")
	if err := cmd.Flags().Set("config", path); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestSessionsInitWithoutTerminal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, "clctl")
	if err := os.WriteFile(path, []byte("[clickup]\napi_token = tok\nsessions_claude_dir = ~/.claude/projects\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	copilot := filepath.Join(dir, "Code", "User")
	_ = os.MkdirAll(copilot, 0o755)
	err := runSessionsInit(cfg, sessionsInit{copilotDir: copilot, server: "https://cloud.example", user: "jo",
		ncUser: "Data Bot", ncPassFile: filepath.Join(dir, "pass"), noCron: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	for _, want := range []string{"api_token = tok", "sessions_claude_dir = ~/.claude/projects", "sessions_copilot_dir = " + copilot,
		"sessions_to = https://cloud.example", "sessions_user = jo", "sessions_nc_user = Data Bot", "sessions_nc_pass_file = "} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("config lacks %q:\n%s", want, saved)
		}
	}

	// Without DIR arguments the configured folders are read, with ~ expanded.
	dirs, err := sessionDirs(sessionsCmd(t, path), nil)
	home, _ := os.UserHomeDir()
	if err != nil || strings.Join(dirs, ",") != filepath.Join(home, ".claude", "projects")+","+copilot {
		t.Errorf("dirs = %v, %v", dirs, err)
	}
	if dirs, _ := sessionDirs(sessionsCmd(t, path), []string{"/given"}); strings.Join(dirs, ",") != "/given" {
		t.Errorf("arguments not preferred: %v", dirs)
	}

	// The account comes from the environment first, then the config.
	cfg, _ = config.Load(path)
	getenv := exportEnv(cfg, func(key string) string { return map[string]string{"NC_USER": "env-user"}[key] })
	if getenv("NC_USER") != "env-user" || getenv("NC_PASS_FILE") != filepath.Join(dir, "pass") {
		t.Errorf("NC_USER %q, NC_PASS_FILE %q", getenv("NC_USER"), getenv("NC_PASS_FILE"))
	}
}

func TestSessionDirsNeedsSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clctl")
	if _, err := sessionDirs(sessionsCmd(t, path), nil); err == nil || !strings.Contains(err.Error(), "sessions init") {
		t.Errorf("err = %v", err)
	}
}
