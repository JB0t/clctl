// Package config reads and writes clctl's settings.
//
// Settings live in an INI file at $XDG_CONFIG_HOME/clctl (default ~/.config/clctl),
// under a [clickup] section (named for the first task backend, kept so existing configs
// load; it holds every backend's settings). When that path is a directory (the sessions export keeps its
// Nextcloud password file in ~/.config/clctl/, as its help suggests), the settings are the
// file "config" inside it (DirFile). The format is shared with the original Python clctl, so
// both versions can run side by side; keys this version does not know are preserved.
package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const section = "clickup"

// Keys used in the config file.
const (
	// KeyBackend picks the task backend: local, clickup or jira (see Backend).
	KeyBackend = "backend"

	// ClickUp.
	KeyAPIToken = "api_token"
	KeyAPIURL   = "api_url"
	KeyTeamID   = "team_id"
	KeyTeamName = "team_name"

	// Jira Cloud or Data Center. Cloud signs in with jira_email and an API token; Data
	// Center with a personal access token and no email.
	KeyJiraURL       = "jira_url"
	KeyJiraEmail     = "jira_email"
	KeyJiraToken     = "jira_token"
	KeyJiraMiscIssue = "jira_misc_issue" // issue that time without a task is logged on

	// The local backend: clctl's own task store.
	KeyLocalDir = "local_dir"

	// Every backend.
	KeyUserID         = "user_id"
	KeyUserName       = "user_name"
	KeySpaces         = "spaces"
	KeyLegacySpace    = "space_id"
	KeyNotesEnabled   = "notes_enabled"
	KeyNotesDir       = "notes_dir"
	KeyEditor         = "editor"
	KeyCacheTTL       = "cache_ttl_minutes"
	KeyLLMURL         = "llm_url"
	KeyLLMModel       = "llm_model"
	KeyLLMContext     = "llm_context_tokens"
	KeyLLMContainer   = "llm_container"
	KeyLLMBackground  = "llm_background"
	KeyLLMThreads     = "llm_threads"
	KeyLLMMaxTokens   = "llm_max_tokens" // answer cap per OpenAI-style call (default 1536)
	KeyLLMEmbedModel  = "llm_embed_model"
	KeyLLMStart       = "llm_start"       // command that starts the model server when it is down
	KeyLLMStop        = "llm_stop"        // command that stops it again afterwards
	KeyLLMEmbedURL    = "llm_embed_url"   // embedding server, when not llm_url
	KeyLLMEmbedStart  = "llm_embed_start" // and its start/stop commands
	KeyLLMEmbedStop   = "llm_embed_stop"
	KeyMeetingPattern = "meeting_pattern"

	// Session digests and exports (see 'clctl sessions init'). Directories may be
	// comma-separated lists. The Nextcloud password is never stored here, only the path
	// of the file that holds it.
	KeySessionsClaudeDir  = "sessions_claude_dir"   // Claude Code projects folder(s)
	KeySessionsCopilotDir = "sessions_copilot_dir"  // VS Code user folder(s) with Copilot Chat sessions
	KeySessionsUser       = "sessions_user"         // folder name under Shared/Sessions
	KeySessionsTo         = "sessions_to"           // Nextcloud URL
	KeySessionsNCUser     = "sessions_nc_user"      // Nextcloud account (NC_USER overrides)
	KeySessionsNCPassFile = "sessions_nc_pass_file" // its password file (NC_PASS_FILE overrides)
)

// DefaultAPIURL is ClickUp's public API.
const DefaultAPIURL = "https://api.clickup.com/api/v2"

// env maps environment variables that override file values.
var env = map[string]string{
	KeyBackend:   "CLCTL_BACKEND",
	KeyJiraURL:   "JIRA_URL",
	KeyJiraEmail: "JIRA_EMAIL",
	KeyJiraToken: "JIRA_API_TOKEN",
	KeyLocalDir:  "CLCTL_LOCAL_DIR",
	KeyAPIToken:  "CLICKUP_API_TOKEN",
	KeyAPIURL:    "CLICKUP_API_URL",
	KeyTeamID:    "CLICKUP_TEAM_ID",
	KeyNotesDir:  "NOTES_DIR",
}

// Config holds the [clickup] section of the config file plus environment overrides.
type Config struct {
	Path   string
	values map[string]string // as stored in the file
	envs   map[string]string // environment overrides, never written back
}

// DefaultPath returns $XDG_CONFIG_HOME/clctl or ~/.config/clctl.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "clctl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "clctl")
}

// DirFile is the settings file's name inside a config path that is a directory.
const DirFile = "config"

// Resolve is the settings file for path: path itself, or path/DirFile when path is a directory.
func Resolve(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return filepath.Join(path, DirFile)
	}
	return path
}

// Load reads the config file at path (Resolve: inside it when path is a directory); a missing
// file yields an empty config.
func Load(path string) (*Config, error) {
	path = Resolve(path)
	c := &Config{Path: path, values: map[string]string{}, envs: map[string]string{}}
	for key, name := range env {
		if value := os.Getenv(name); value != "" {
			c.envs[key] = value
		}
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	current := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			current = strings.TrimSpace(line[1 : len(line)-1])
		case current == section || current == "DEFAULT":
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				key, value, ok = strings.Cut(line, ":")
			}
			if ok {
				c.values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
			}
		}
	}
	return c, scanner.Err()
}

// Get returns a value, preferring environment overrides.
func (c *Config) Get(key string) string {
	if value, ok := c.envs[key]; ok {
		return value
	}
	return c.values[key]
}

// Stored returns the value in the file, ignoring environment overrides.
func (c *Config) Stored(key string) string { return c.values[key] }

// Set stores a value in the file section; an empty value removes the key.
func (c *Config) Set(key, value string) {
	if value == "" {
		delete(c.values, key)
		return
	}
	c.values[key] = value
}

// Bool parses a boolean value; missing or invalid values are false.
func (c *Config) Bool(key string) bool {
	value, _ := strconv.ParseBool(c.Get(key))
	return value
}

// APIURL returns the configured API URL or ClickUp's default.
func (c *Config) APIURL() string {
	if url := c.Get(KeyAPIURL); url != "" {
		return strings.TrimRight(url, "/")
	}
	return DefaultAPIURL
}

// Backends are the task backends clctl supports.
var Backends = []string{"local", "clickup", "jira"}

// Backend returns the configured task backend. Configs from before there was a choice
// have only ClickUp settings, so a ClickUp token means clickup; "" means not set up.
func (c *Config) Backend() string {
	if backend := strings.ToLower(c.Get(KeyBackend)); backend != "" {
		return backend
	}
	if c.Get(KeyAPIToken) != "" {
		return "clickup"
	}
	return ""
}

// LocalDir returns the local backend's folder: local_dir, else
// $XDG_DATA_HOME/clctl/tasks (default ~/.local/share/clctl/tasks).
func (c *Config) LocalDir() string {
	if dir := c.Get(KeyLocalDir); dir != "" {
		return ExpandHome(dir)
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "clctl", "tasks")
}

// SpaceIDs returns the spaces clctl works with (Jira: project keys); nil means every space in the workspace.
// Configs written by the Python version only have a single `space_id`.
func (c *Config) SpaceIDs() []string {
	raw := c.Get(KeySpaces)
	if raw == "" {
		raw = c.Get(KeyLegacySpace)
	}
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" && id != "all" {
			ids = append(ids, id)
		}
	}
	return ids
}

// NotesDir returns the notes root with ~ expanded, or "" when notes are disabled.
func (c *Config) NotesDir() string {
	if !c.NotesEnabled() {
		return ""
	}
	return ExpandHome(c.Get(KeyNotesDir))
}

// NotesEnabled reports whether the dated notes layout features are turned on.
// Python-era configs have no flag, so fall back to whether $NOTES_DIR is set.
func (c *Config) NotesEnabled() bool {
	if _, ok := c.values[KeyNotesEnabled]; ok {
		return c.Bool(KeyNotesEnabled)
	}
	return c.Get(KeyNotesDir) != ""
}

// Editor returns the editor command: the configured one, else $VISUAL, $EDITOR, or the
// first of nvim/vim/vi/nano found on PATH.
func (c *Config) Editor() string {
	for _, candidate := range []string{c.Get(KeyEditor), os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if candidate != "" {
			return candidate
		}
	}
	for _, candidate := range []string{"nvim", "vim", "vi", "nano"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return "vi"
}

// CacheTTLMinutes returns how old the task cache may get before it is refreshed.
func (c *Config) CacheTTLMinutes() int {
	if minutes, err := strconv.Atoi(c.Get(KeyCacheTTL)); err == nil && minutes >= 0 {
		return minutes
	}
	return 60
}

// Save writes the [clickup] section (other sections are rewritten verbatim) with mode 0600.
func (c *Config) Save() error {
	others, err := otherSections(c.Path)
	if err != nil {
		return err
	}
	var builder strings.Builder
	builder.WriteString("[" + section + "]\n")
	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&builder, "%s = %s\n", key, c.values[key])
	}
	builder.WriteString(others)

	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	temp := c.Path + ".tmp"
	if err := os.WriteFile(temp, []byte(builder.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, c.Path)
}

// otherSections returns the text of every section except [clickup] and [DEFAULT].
func otherSections(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	keep := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			keep = name != section && name != "DEFAULT"
			if keep {
				builder.WriteString("\n")
			}
		}
		if keep {
			builder.WriteString(line + "\n")
		}
	}
	return builder.String(), nil
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}
