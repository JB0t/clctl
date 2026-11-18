# notes

<!-- docgen:begin id=internal/notes:overview scope=internal/notes hash=7ebf5935c69f -->
Package notes handles the layout and operations for dated task notes in a structured filesystem. Notes are organized by day, space, folder, and list, with each task having a markdown file and optional files folder. The package supports creating, finding, and managing these notes with helpers for safe naming and path resolution.

The main concepts are the dated directory structure, workspace symlinks, and note identification by key. Functions ensure notes exist with proper titles, find notes by task ID, list all task notes, and create workspaces that link to daily notes and folders. The package tracks note days and handles symbolic links to maintain consistent keys for migration support.

Functions work together to build and navigate the note hierarchy. `Ensure` creates today's note and folder. `Find` and `TaskNotes` locate notes by ID or across all tasks. `MakeWorkspace` builds symlinks for a task's workspaces. `Key` and `Day` resolve paths to consistent identifiers. `SafePart` cleans names for filesystem use. The `walk` helper skips workspace directories when searching.
<!-- docgen:end id=internal/notes:overview -->

<!-- docgen:begin id=internal/notes:reference scope=internal/notes hash=7ebf5935c69f -->
## Reference

### `notes.go`

- **`WorkspacesDir`** (constant): WorkspacesDir is the per-day folder holding task workspaces; it is skipped when searching.
- **`SafePart`** (function): SafePart makes a name shell-friendly: letters, digits, '_', '.' and '-' only ("Infra & Devops" -> "Infra_Devops").
- **`RelPath`** (function): RelPath returns `<Space>/<Folder>/<List>/<ID>.md` for a task; lists that sit directly in a space have no folder level.
- **`DayDir`** (function): DayDir returns &lt;root>/YYYY/MM/DD for a day.
- **`Ensure`** (function): Ensure creates today's note (with a title) and its files folder if missing.
- **`Day`** (function): Day returns "YYYY-MM-DD" for a path under &lt;root>/YYYY/MM/DD/, or "".
- **`Key`** (function): Key identifies a note in the push-notes state: the day for a task's own &lt;ID>.md, otherwise the path relative to root (an absolute path outside root). Links (such as workspace entries) are resolved first, so every route to a file gets the same key. It also returns older spellings of the key (absolute paths) to migrate from.
- **`ParseDay`** (function): ParseDay accepts YYYY-MM-DD, YYYY/MM/DD, "today" or "yesterday".
- **`Find`** (function): Find returns a task's `<ID>.md` notes, newest day first.
- **`TaskNotes`** (function): TaskNotes returns every dated task note (&lt;root>/YYYY/MM/DD/&lt;Space>/[&lt;Folder>/]&lt;List>/&lt;ID>.md), newest day first. Files directly in a day folder, such as digest.md, are not task notes.
- **`Entries`** (function): Entries returns every dated `<ID>.md` note and `<ID>/` folder, oldest day first.
- **`MakeWorkspace`** (function): MakeWorkspace links every day's note and files folder for a task into &lt;root>/&lt;today>/workspaces/&lt;ID>/&lt;YYYY-MM-DD>/. Only the per-day folders are real; links are relative, and anything else placed in the workspace is left alone. It returns the workspace path, how many links were added, and warnings for paths it had to skip.
<!-- docgen:end id=internal/notes:reference -->
