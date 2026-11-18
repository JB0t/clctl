// Package notes implements the dated notes layout:
//
//	<root>/YYYY/MM/DD/<Space>/<Folder>/<List>/<TASK-ID>.md
//	<root>/YYYY/MM/DD/<Space>/<Folder>/<List>/<TASK-ID>/
//
// plus per-task workspaces of symlinks at <root>/<today>/workspaces/<TASK-ID>/.
package notes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JB0t/clctl/internal/tracker"
)

// WorkspacesDir is the per-day folder holding task workspaces; it is skipped when searching.
const WorkspacesDir = "workspaces"

var (
	unsafeChars = regexp.MustCompile(`[^\p{L}\p{N}_.-]+`)
	dashRuns    = regexp.MustCompile(`_*-_*`)
	dayDirRE    = regexp.MustCompile(`^\d{4}$`)
	dayNameRE   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// SafePart makes a name shell-friendly: letters, digits, '_', '.' and '-' only
// ("Infra & Devops" -> "Infra_Devops").
func SafePart(value, fallback string) string {
	cleaned := unsafeChars.ReplaceAllString(strings.TrimSpace(value), "_")
	cleaned = strings.Trim(dashRuns.ReplaceAllString(cleaned, "-"), "._-")
	if cleaned == "" {
		return fallback
	}
	return cleaned
}

// RelPath returns `<Space>/<Folder>/<List>/<ID>.md` for a task; lists that sit directly in
// a space have no folder level.
func RelPath(task tracker.Task) string {
	parts := []string{SafePart(task.Space.Name, "Unknown_Space")}
	if task.Folder.Name != "" && !task.Folder.Hidden {
		parts = append(parts, SafePart(task.Folder.Name, "Unknown_Folder"))
	}
	parts = append(parts, SafePart(task.List.Name, "Unknown_List"), task.Ref()+".md")
	return filepath.Join(parts...)
}

// DayDir returns <root>/YYYY/MM/DD for a day.
func DayDir(root string, day time.Time) string {
	return filepath.Join(root, day.Format("2006"), day.Format("01"), day.Format("02"))
}

// Ensure creates today's note (with a title) and its files folder if missing.
func Ensure(root string, task tracker.Task) (string, error) {
	note := filepath.Join(DayDir(root, time.Now()), RelPath(task))
	if err := os.MkdirAll(strings.TrimSuffix(note, ".md"), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(note); errors.Is(err, fs.ErrNotExist) {
		title := strings.TrimRight(task.Ref()+": "+task.Name, ": ")
		if err := os.WriteFile(note, []byte("# "+title+"\n\n"), 0o644); err != nil {
			return "", err
		}
	}
	return note, nil
}

// Day returns "YYYY-MM-DD" for a path under <root>/YYYY/MM/DD/, or "".
func Day(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 4 || !dayDirRE.MatchString(parts[0]) {
		return ""
	}
	day := parts[0] + "-" + parts[1] + "-" + parts[2]
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return ""
	}
	return day
}

// Key identifies a note in the push-notes state: the day for a task's own <ID>.md,
// otherwise the path relative to root (an absolute path outside root). Links (such as
// workspace entries) are resolved first, so every route to a file gets the same key.
// It also returns older spellings of the key (absolute paths) to migrate from.
func Key(root, note, taskID string) (string, []string) {
	abs, err := filepath.Abs(note)
	if err != nil {
		abs = note
	}
	legacy := []string{abs}
	real := abs
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		real = resolved
		if resolved != abs {
			legacy = append(legacy, resolved)
		}
	}
	if root != "" {
		if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
			root = resolvedRoot
		}
		if day := Day(root, real); day != "" && strings.EqualFold(filepath.Base(real), taskID+".md") {
			return day, legacy
		}
		if rel, err := filepath.Rel(root, real); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel), legacy
		}
	}
	return real, legacy
}

// ParseDay accepts YYYY-MM-DD, YYYY/MM/DD, "today" or "yesterday".
func ParseDay(value string) (string, error) {
	switch strings.ToLower(value) {
	case "today":
		return time.Now().Format("2006-01-02"), nil
	case "yesterday":
		return time.Now().AddDate(0, 0, -1).Format("2006-01-02"), nil
	}
	day, err := time.Parse("2006-01-02", strings.ReplaceAll(value, "/", "-"))
	if err != nil {
		return "", fmt.Errorf("invalid date %q; use YYYY-MM-DD, 'today' or 'yesterday'", value)
	}
	return day.Format("2006-01-02"), nil
}

// walk visits real (non-symlink) paths under root, skipping workspace folders.
func walk(root string, visit func(path string, entry fs.DirEntry)) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && entry.Name() == WorkspacesDir {
			return filepath.SkipDir
		}
		if entry.Type()&fs.ModeSymlink == 0 {
			visit(path, entry)
		}
		return nil
	})
}

// Find returns a task's `<ID>.md` notes, newest day first.
func Find(root, taskID string) []string {
	var found []string
	walk(root, func(path string, entry fs.DirEntry) {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), taskID+".md") {
			found = append(found, path)
		}
	})
	sort.SliceStable(found, func(i, j int) bool {
		if Day(root, found[i]) != Day(root, found[j]) {
			return Day(root, found[i]) > Day(root, found[j])
		}
		return modTime(found[i]).After(modTime(found[j]))
	})
	return found
}

// TaskNotes returns every dated task note (<root>/YYYY/MM/DD/<Space>/[<Folder>/]<List>/<ID>.md),
// newest day first. Files directly in a day folder, such as digest.md, are not task notes.
func TaskNotes(root string) []string {
	var found []string
	walk(root, func(path string, entry fs.DirEntry) {
		if entry.IsDir() || filepath.Ext(path) != ".md" || Day(root, path) == "" {
			return
		}
		if rel, err := filepath.Rel(root, path); err == nil && len(strings.Split(rel, string(filepath.Separator))) >= 6 {
			found = append(found, path)
		}
	})
	sort.SliceStable(found, func(i, j int) bool { return Day(root, found[i]) > Day(root, found[j]) })
	return found
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// Entries returns every dated `<ID>.md` note and `<ID>/` folder, oldest day first.
func Entries(root, taskID string) []string {
	var found []string
	walk(root, func(path string, entry fs.DirEntry) {
		matches := (!entry.IsDir() && entry.Name() == taskID+".md") || (entry.IsDir() && entry.Name() == taskID)
		if matches && Day(root, path) != "" {
			found = append(found, path)
		}
	})
	sort.SliceStable(found, func(i, j int) bool { return Day(root, found[i]) < Day(root, found[j]) })
	return found
}

// MakeWorkspace links every day's note and files folder for a task into
// <root>/<today>/workspaces/<ID>/<YYYY-MM-DD>/. Only the per-day folders are real; links
// are relative, and anything else placed in the workspace is left alone. Links you added
// to the task's previous workspace yourself (a repo, a shared folder) are carried over
// to the new one. It returns the workspace path, how many day links were added, how many
// of your links were carried over, and warnings for paths it had to skip.
func MakeWorkspace(root, taskID string) (workspace string, added, carried int, warnings []string, err error) {
	workspace = filepath.Join(DayDir(root, time.Now()), WorkspacesDir, taskID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", 0, 0, nil, err
	}
	for _, source := range Entries(root, taskID) {
		link := filepath.Join(workspace, Day(root, source), filepath.Base(source))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return "", added, 0, warnings, err
		}
		target, err := filepath.Rel(filepath.Dir(link), source)
		if err != nil {
			return "", added, 0, warnings, err
		}
		if info, err := os.Lstat(link); err == nil {
			if info.Mode()&fs.ModeSymlink == 0 {
				warnings = append(warnings, fmt.Sprintf("skipped %s: a real file is in the way", link))
				continue
			}
			if current, _ := os.Readlink(link); current == target {
				continue
			}
			_ = os.Remove(link)
		}
		if err := os.Symlink(target, link); err != nil {
			return "", added, 0, warnings, err
		}
		added++
	}
	carried, err = carryLinks(root, taskID, workspace)
	return workspace, added, carried, warnings, err
}

// carryLinks recreates in workspace the symlinks the task's most recent earlier
// workspace holds besides its per-day folders: folders you linked in yourself follow the
// task to the new day. Relative targets are re-pointed from the new folder. Names already
// present in workspace are left alone. It returns how many links it added.
func carryLinks(root, taskID, workspace string) (int, error) {
	previous := previousWorkspace(root, taskID, workspace)
	if previous == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(previous)
	if err != nil {
		return 0, nil
	}
	carried := 0
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 || dayNameRE.MatchString(entry.Name()) {
			continue
		}
		link := filepath.Join(workspace, entry.Name())
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		target, err := os.Readlink(filepath.Join(previous, entry.Name()))
		if err != nil {
			continue
		}
		if !filepath.IsAbs(target) {
			abs := filepath.Join(previous, target)
			if rel, err := filepath.Rel(workspace, abs); err == nil {
				target = rel
			} else {
				target = abs
			}
		}
		if err := os.Symlink(target, link); err != nil {
			return carried, err
		}
		carried++
	}
	return carried, nil
}

// previousWorkspace returns the task's newest workspace folder from a day before
// workspace's, or "". Day folders sort by date, so the paths compare as days.
func previousWorkspace(root, taskID, workspace string) string {
	matches, _ := filepath.Glob(filepath.Join(root, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]", WorkspacesDir, taskID))
	sort.Strings(matches)
	previous := ""
	for _, match := range matches {
		if match < workspace {
			previous = match
		}
	}
	return previous
}
