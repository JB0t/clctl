package notes

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JB0t/clctl/internal/tracker"
)

func TestSafePartAndRelPath(t *testing.T) {
	for in, want := range map[string]string{"Infra & Devops": "Infra_Devops", "A - B": "A-B", "R&D/Ops (v2)": "R_D_Ops_v2", "": "fb"} {
		if got := SafePart(in, "fb"); got != want {
			t.Errorf("SafePart(%q) = %q, want %q", in, got, want)
		}
	}
	task := tracker.Task{CustomID: "T-1", Space: tracker.Ref{Name: "Team Space"}, Folder: tracker.FolderRef{Name: "hidden", Hidden: true}, List: tracker.Ref{Name: "Inbox"}}
	if got := RelPath(task); got != filepath.Join("Team_Space", "Inbox", "T-1.md") {
		t.Errorf("RelPath = %q", got)
	}
}

func TestWorkspaceLinksEveryDay(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "2026", "09", "21", "S", "L")
	if err := os.MkdirAll(filepath.Join(old, "T-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(old, "T-1.md"), []byte("# old"), 0o644)
	task := tracker.Task{CustomID: "T-1", Space: tracker.Ref{Name: "S"}, List: tracker.Ref{Name: "L"}}
	if _, err := Ensure(root, task); err != nil {
		t.Fatal(err)
	}
	workspace, added, carried, _, err := MakeWorkspace(root, "T-1")
	if err != nil || added != 4 || carried != 0 {
		t.Fatalf("added %d, carried %d, err %v", added, carried, err)
	}
	today := time.Now().Format("2006-01-02")
	for _, rel := range []string{"2026-09-21/T-1.md", "2026-09-21/T-1", today + "/T-1.md", today + "/T-1"} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			t.Errorf("missing link %s: %v", rel, err)
		}
	}
	if _, added, carried, _, _ := MakeWorkspace(root, "T-1"); added != 0 || carried != 0 {
		t.Errorf("re-run added %d links, carried %d", added, carried)
	}
	if found := Find(root, "T-1"); len(found) != 2 || Day(root, found[0]) != today {
		t.Errorf("Find should skip workspace links and sort newest first: %v", found)
	}
}

func TestWorkspaceCarriesOwnLinks(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0o755)
	task := tracker.Task{CustomID: "T-2", Space: tracker.Ref{Name: "S"}, List: tracker.Ref{Name: "L"}}
	if _, err := Ensure(root, task); err != nil {
		t.Fatal(err)
	}
	// Yesterday's workspace: the day links, plus a relative and an absolute link of the user's own.
	yesterday := filepath.Join(DayDir(root, time.Now().AddDate(0, 0, -1)), WorkspacesDir, "T-2")
	_ = os.MkdirAll(filepath.Join(yesterday, "2026-09-21"), 0o755)
	_ = os.Symlink(filepath.Join("..", "..", "..", "..", "..", "repo"), filepath.Join(yesterday, "repo"))
	_ = os.Symlink(repo, filepath.Join(yesterday, "repo-abs"))
	_ = os.WriteFile(filepath.Join(yesterday, "scratch.txt"), []byte("not a link"), 0o644)
	workspace, _, carried, _, err := MakeWorkspace(root, "T-2")
	if err != nil || carried != 2 {
		t.Fatalf("carried %d, err %v", carried, err)
	}
	for _, name := range []string{"repo", "repo-abs"} {
		resolved, err := filepath.EvalSymlinks(filepath.Join(workspace, name))
		if want, _ := filepath.EvalSymlinks(repo); err != nil || resolved != want {
			t.Errorf("%s resolves to %q (%v), want %q", name, resolved, err, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(workspace, "2026-09-21")); err == nil {
		t.Error("an old day folder was carried over")
	}
	if _, err := os.Lstat(filepath.Join(workspace, "scratch.txt")); err == nil {
		t.Error("a real file was carried over")
	}
	if _, _, carried, _, _ := MakeWorkspace(root, "T-2"); carried != 0 {
		t.Errorf("re-run carried %d links", carried)
	}
}

func TestKey(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "09", "30", "S", "L")
	_ = os.MkdirAll(filepath.Join(dir, "T-1"), 0o755)
	main := filepath.Join(dir, "T-1.md")
	extra := filepath.Join(dir, "T-1", "proposal.md")
	_ = os.WriteFile(main, nil, 0o644)
	_ = os.WriteFile(extra, nil, 0o644)
	if key, _ := Key(root, main, "T-1"); key != "2026-09-30" {
		t.Errorf("main note key = %q", key)
	}
	key, legacy := Key(root, extra, "T-1")
	if key != "2026/09/30/S/L/T-1/proposal.md" || len(legacy) == 0 || legacy[0] != extra {
		t.Errorf("extra note key = %q, legacy %v", key, legacy)
	}
	link := filepath.Join(root, "link.md")
	_ = os.Symlink(extra, link)
	if key, _ := Key(root, link, "T-1"); key != "2026/09/30/S/L/T-1/proposal.md" {
		t.Errorf("linked note key = %q", key)
	}
}
