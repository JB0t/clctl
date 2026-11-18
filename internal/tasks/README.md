# tasks

<!-- docgen:begin id=internal/tasks:overview scope=internal/tasks hash=5034b8d2e076 -->
Package tasks provides functionality to filter and render ClickUp tasks specifically for terminal-based display. It offers utilities to rank tasks by priority, filter out closed tasks, and restrict assignments to specific users. The package also supports formatting task information for display, including location paths and one-line labels for use in interactive pickers.

The core operations in this package revolve around organizing tasks into a hierarchical tree structure based on their ClickUp space, folder, and list. Tasks are sorted by priority and recency, with subtasks properly nested under their parent tasks. The `PrintTree` function outputs this hierarchy in a visually structured way, using indentation and Unicode tree characters to represent the nesting levels.

Helper functions support the main operations by providing consistent formatting and sorting logic. The `Rank` function assigns priority levels to tasks, while `Sort` ensures tasks are ordered correctly. The `Location` and `Label` functions format task metadata for display, and `PrintTree` uses these along with internal tree-building logic to render a structured output with color support.
<!-- docgen:end id=internal/tasks:overview -->

<!-- docgen:begin id=internal/tasks:reference scope=internal/tasks hash=5034b8d2e076 -->
## Reference

### `tasks.go`

- **`Rank`** (function): Rank orders tasks by priority, unprioritised last.
- **`Open`** (function): Open drops tasks whose status is a done/closed status.
- **`AssignedTo`** (function): AssignedTo keeps tasks assigned to every one of the user IDs.
- **`Location`** (function): Location returns "Space/Folder/List"; lists sitting directly in a space have no folder.
- **`Label`** (function): Label is the one-line form used in pickers.
- **`Sort`** (function): Sort orders tasks by priority, then most recently updated.
- **`PrintTree`** (function): PrintTree writes tasks as a Space > Folder > List tree with subtasks nested under their parents and each level sorted by priority.
<!-- docgen:end id=internal/tasks:reference -->
