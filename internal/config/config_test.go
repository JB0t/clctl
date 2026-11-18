package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSaveKeepsUnknownKeysAndSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clctl")
	original := "[clickup]\napi_token = pk_1\nspace_id = 42\ndefault_assignee_name = Someone\n\n[other]\nkeep = me\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLICKUP_API_TOKEN", "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SpaceIDs(); len(got) != 1 || got[0] != "42" {
		t.Fatalf("legacy space_id not used: %v", got)
	}
	cfg.Set(KeyUserID, "7")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"default_assignee_name = Someone", "user_id = 7", "[other]", "keep = me"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("saved config lacks %q:\n%s", want, data)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestEnvironmentOverridesAreNotSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clctl")
	t.Setenv("CLICKUP_API_TOKEN", "pk_env")
	cfg, _ := Load(path)
	cfg.Set(KeyTeamID, "1")
	if cfg.Get(KeyAPIToken) != "pk_env" {
		t.Fatal("environment override ignored")
	}
	_ = cfg.Save()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "pk_env") {
		t.Fatal("environment token was written to disk")
	}
}

func TestSpacesAllAndNotesFlag(t *testing.T) {
	t.Setenv("NOTES_DIR", "")
	cfg, _ := Load(filepath.Join(t.TempDir(), "missing"))
	cfg.Set(KeySpaces, "all")
	cfg.Set(KeyLegacySpace, "42")
	if ids := cfg.SpaceIDs(); ids != nil {
		t.Fatalf("spaces=all should mean every space, got %v", ids)
	}
	cfg.Set(KeyNotesDir, "~/n")
	if !cfg.NotesEnabled() {
		t.Fatal("a notes dir without a flag should enable notes (Python-era config)")
	}
	cfg.Set(KeyNotesEnabled, "false")
	if cfg.NotesEnabled() || cfg.NotesDir() != "" {
		t.Fatal("notes_enabled=false should win")
	}
}

// ~/.config/clctl may be a directory holding the sessions export's password file, as the
// export's help suggests: the settings are then the file "config" in it (2026-10-27: every
// nightly export failed "read ~/.config/clctl: is a directory" from 10-17).
func TestConfigPathThatIsADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clctl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nc-data-bot"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("a directory with no settings file: %v", err)
	}
	if cfg.Path != filepath.Join(dir, DirFile) || cfg.Get(KeySessionsUser) != "" {
		t.Fatalf("path %q, user %q", cfg.Path, cfg.Get(KeySessionsUser))
	}
	cfg.Set(KeySessionsUser, "alex")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil || again.Get(KeySessionsUser) != "alex" {
		t.Fatalf("saved into the directory and read back: %v %q", err, again.Get(KeySessionsUser))
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "nc-data-bot")); string(data) != "secret" {
		t.Errorf("the password file was touched: %q", data)
	}
}
