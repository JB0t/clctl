# clctl

<!-- docgen:begin id=cli:clctl:usage scope=. hash=9aec45f77495 -->
## Usage

| Command | Description | Flags |
|---|---|---|
| `clctl cache` | Manage the local task cache |  |
| `clctl cache path` | Print the cache file location |  |
| `clctl cache refresh` | Re-download open tasks for the configured spaces | `--quiet` |
| `clctl completion` | Generate the autocompletion script for the specified shell |  |
| `clctl index` | Build or update the local search index (notes, and optionally comments) | `--comments` |
| `clctl init` | Set up clctl: API token, workspace, spaces and notes | `--editor` `--no-notes` `--notes-dir` `--spaces` `--team` `--token` |
| `clctl search` | Search notes and comments by meaning (run 'clctl index' first) | `--comments` `-n` `--limit` `--notes` |
| `clctl tasks` | Work with tasks (run without an action for a menu) |  |
| `clctl tasks add-comment` | Add a comment to a task | `--bulletlist` `--codeblock` `--comment` `--task-id` |
| `clctl tasks attach-file` | Attach files to a task | `--file` `--task-id` |
| `clctl tasks complete` | Mark a task done (stops its timer if running) | `--task-id` |
| `clctl tasks create` | Create a task (prompts for anything not given) | `--assignee` `--assignee-id` `--description` `--list-id` `--name` `--tag` `--yes` |
| `clctl tasks digest` | Write a Markdown digest of recently edited tasks and their comments | `--again` `--assignee` `--comments` `--everyone` `--limit` `--llm-url` `--meeting` `--meeting-pattern` `--model` `-o` `--output` `--since` `--summarize` `--task-id` |
| `clctl tasks export` | Write your open tasks to a dated Markdown file, by priority | `--assignee` `-o` `--output` |
| `clctl tasks fix-tables` | Turn Markdown tables in your existing comments into ClickUp tables | `--dry-run` `--task-id` |
| `clctl tasks get-comments` | Show a task's comments | `--export` `--json` `--task-id` |
| `clctl tasks list` | Tree of open tasks by space/folder/list, then priority | `--assignee` `--everyone` `--include-done` |
| `clctl tasks list-timers` | Show running timers once |  |
| `clctl tasks log-time` | Log a finished time range against a task | `--date` `--description` `--end` `--start` `--task-id` |
| `clctl tasks make-notes` | Open today's task note in a new tmux window | `--start-timer` `--task-id` |
| `clctl tasks make-workspace` | Symlink every day's notes and files for a task into today's workspace | `--task-id` |
| `clctl tasks open` | Open a task in the browser | `--task-id` |
| `clctl tasks push-notes` | Post a task note as comments, one per ## section | `--all` `--date` `--dry-run` `--file` `--force` `--select` `--task-id` |
| `clctl tasks push-todo` | Create tasks from 'new: ' lines in a TODO file | `--dry-run` `--file` `--list-id` `--tag` |
| `clctl tasks search` | Pick any cached task, then pick actions to run on it |  |
| `clctl tasks set-status` | Change a task's status | `--status` `--task-id` |
| `clctl tasks start-timer` | Start a task timer (offers to stop one already running) | `--task-id` |
| `clctl tasks stop-timer` | Stop your running timer |  |
| `clctl tasks watch-timers` | Watch running timers (Ctrl+C to stop) | `--interval` |

Global flags: `--config` `--refresh` `-v` `--version`

### Examples

### `clctl cache path`

Print the cache file location
```bash
# Get the cache file location
clctl cache path
```

### `clctl cache refresh`

Re-download open tasks for the configured spaces to update local cache.
```bash
# Refresh cache with verbose output
clctl cache refresh

# Refresh cache quietly for cron jobs
clctl cache refresh --quiet

# Refresh cache and verify it worked
clctl cache refresh && clctl cache path | xargs ls -la
```

### `clctl index`

Index your notes and open task comments for search functionality.
```bash
# Index notes and comments from open tasks
clctl index --comments

# Index notes only (default behavior)
clctl index

# Index notes and comments with a custom configuration file
clctl index --comments --config "/path/to/custom-config.yaml"
```

### `clctl init`

Run guided setup to configure clctl with your workspace and preferences.
```bash
# Interactive setup
clctl init

# Scripted setup with token and team
clctl init --token pk_1234567890abcdef --team "My Workspace" --spaces all

# Setup without notes features
clctl init --token pk_1234567890abcdef --team "My Workspace" --no-notes
```

### `clctl search`

Search notes and comments by meaning using semantic search.
```bash
# Search for postgres connection timeout
clctl search "postgres connection timeout in the dask cluster"

# Search only comments
clctl search "urgent task" --comments

# Search with limited results
clctl search "database migration" --limit 5
```

### `clctl tasks`

Work with tasks (run without an action for a menu).

```bash
clctl tasks
```

### `clctl tasks add-comment`

Add a comment to a task, useful for updating progress or adding notes during development.

```bash
# Add a simple comment to a task
clctl tasks add-comment --task-id TECH-1234 --comment "Starting implementation of user authentication"

# Add a comment with code block formatting
clctl tasks add-comment --task-id BUG-456 --codeblock --comment "function validateEmail(email) {\n  return email.includes('@');\n}"

# Add a comment with bullet points
clctl tasks add-comment --task-id FEATURE-789 --bulletlist --comment "• Updated documentation\n• Added unit tests\n• Fixed minor UI issues"
```

### `clctl tasks attach-file`

Attach files to a task for sharing documents or code snippets.
```bash
# Attach a single file to a task
clctl tasks attach-file --task-id TECH-1234 --file "project-plan.pdf"

# Attach multiple files to a task
clctl tasks attach-file --task-id TECH-1234 --file "design-doc.md" --file "wireframes.zip"

# Attach a file with spaces in the name
clctl tasks attach-file --task-id TECH-1234 --file "final report v2.1.pdf"
```

### `clctl tasks complete`

Mark a task as complete when you've finished working on it.
```bash
# Complete a task by ID
clctl tasks complete --task-id TECH-1234

# Complete a task with a specific task ID
clctl tasks complete --task-id "custom-task-001"

# Complete a task without specifying the ID (will prompt)
clctl tasks complete
```

### `clctl tasks create`

Create a new task with a name, description, and optional tags or assignees.
```bash
# Create a task with minimal information
clctl tasks create --name "Fix the login bug"

# Create a task with a description and tags
clctl tasks create --name "Update documentation" --description "Update the API documentation with new endpoints" --tag docs --tag api

# Create a task with an assignee and list ID
clctl tasks create --list-id 901200000001 --name "Deploy to production" --assignee "john.doe@example.com"
```

### `clctl tasks digest`

Generate a digest of recent task activity for review or meeting notes.
```bash
# Generate a digest for the last 7 days
clctl tasks digest

# Generate a digest for a specific date range
clctl tasks digest --since "2026-09-21"

# Generate a digest with AI summaries
clctl tasks digest --summarize --since "2w"
```

### `clctl tasks export`

Export your open tasks to a Markdown file for review or planning.
```bash
# Export tasks to a default filename
clctl tasks export

# Export tasks to a specific file
clctl tasks export --output "my-tasks-2026-09-21.md"

# Export tasks assigned to a specific person
clctl tasks export --assignee "jane.smith@example.com"
```

### `clctl tasks fix-tables`

Use this command to automatically convert plain text Markdown tables in your task comments to proper ClickUp tables.

```bash
# Fix tables in a specific task
clctl tasks fix-tables --task-id TECH-1234

# Preview which comments would be changed without making modifications
clctl tasks fix-tables --dry-run --task-id BUG-5678

# Fix tables across all your open tasks
clctl tasks fix-tables
```

### `clctl tasks get-comments`

Use this command to view or export the comments on a specific task for review or backup.
```bash
# Display comments for a task in the terminal
clctl tasks get-comments --task-id TECH-1234

# Export task comments to a JSON file
clctl tasks get-comments --export --task-id TECH-5678

# Print raw JSON of task comments
clctl tasks get-comments --json --task-id TECH-9012
```

### `clctl tasks list`

Use this command to view your open tasks or filter tasks by assignee.
```bash
# Show all your open tasks
clctl tasks list

# Show tasks assigned to specific people
clctl tasks list --assignee "Jane Smith" --assignee "John Doe"

# Show tasks assigned to everyone in the workspace
clctl tasks list --everyone
```

### `clctl tasks list-timers`

Use this command to check which timers are currently running across your tasks.
```bash
# Show all running timers
clctl tasks list-timers
```

### `clctl tasks log-time`

Use this command to log completed work time against a specific task.
```bash
# Log time for a task on a specific date
clctl tasks log-time --task-id TECH-1234 --date 2026-09-14 --start 09:00 --end 10:30

# Log time with a description
clctl tasks log-time --task-id TECH-5678 --start 14:00 --end 15:30 --description "Code review"

# Log time for today with default start and end times
clctl tasks log-time --task-id TECH-9012 --start 10:00 --end 11:00
```

### `clctl tasks make-notes`

Use this command to create a new note file for a task and optionally start its timer.
```bash
# Create a note file for a task and open it
clctl tasks make-notes --task-id TECH-1234

# Create a note file and start the timer immediately
clctl tasks make-notes --task-id TECH-5678 --start-timer

# Create a note file for a task with a custom ID
clctl tasks make-notes --task-id "CUSTOM-123"
```

### `clctl tasks make-workspace`

Create a new workspace directory structure for a task with daily note folders and symlinks.
```bash
# Create a workspace for a task with auto-generated ID
clctl tasks make-workspace

# Create a workspace for a specific task ID
clctl tasks make-workspace --task-id TECH-1234

# Create a workspace with a custom task ID
clctl tasks make-workspace --task-id "my-custom-id"
```

### `clctl tasks open`

Open a task in your default web browser for quick access.
```bash
# Open a task using fzf to select from cached tasks
clctl tasks open

# Open a specific task by ID
clctl tasks open --task-id TECH-1234

# Open a task with a custom ID
clctl tasks open --task-id "custom-task-123"
```

### `clctl tasks push-notes`

Post note sections to a task as comments, handling local images automatically.
```bash
# Push all new or edited sections from a note file
clctl tasks push-notes notes/TECH-1234.md --all

# Push sections from yesterday's note
clctl tasks push-notes --task-id TECH-1234 --date yesterday --all

# Push specific sections using fzf selection
clctl tasks push-notes --task-id TECH-5678 --select
```

### `clctl tasks push-todo`

Convert TODO entries into actual tasks, assigning them to you automatically.
```bash
# Push tasks from the default TODO file
clctl tasks push-todo

# Push tasks with a custom file
clctl tasks push-todo --file /path/to/custom-todo.md

# Push tasks with a specific list ID and tags
clctl tasks push-todo --list-id "my-list" --tag "important" --tag "work"
```

### `clctl tasks search`

Search for cached tasks to run actions on, useful when you need to find and operate on previously stored task data.

```bash
# Search for tasks with a specific configuration file
clctl tasks search --config /path/to/config.yaml

# Search for tasks and refresh the cache before searching
clctl tasks search --refresh

# Search for tasks with both config file and refresh enabled
clctl tasks search --config "config with spaces.yaml" --refresh
```

### `clctl tasks set-status`

Change the status of a task to a new value.
```bash
# Set status for a task using fzf to select
clctl tasks set-status

# Set status for a specific task ID
clctl tasks set-status --task-id TECH-1234 --status "in-progress"

# Set status for a task with a custom ID
clctl tasks set-status --task-id "custom-task-123" --status "completed"
```

### `clctl tasks start-timer`

Start a timer for a specific task when you begin working on it.

```bash
# Start a timer for a task with a custom ID
clctl tasks start-timer --task-id "DEV-5678"

# Start a timer for a task with a standard ID
clctl tasks start-timer --task-id "TECH-1234"

# Start a timer without specifying a task ID to let the tool pick one
clctl tasks start-timer
```

### `clctl tasks stop-timer`

Stop the currently running timer when you finish working on a task.
```bash
# Stop the running timer
clctl tasks stop-timer
```

### `clctl tasks watch-timers`

Monitor all active timers to track work progress across multiple tasks.
```bash
# Watch timers with default 60-second interval
clctl tasks watch-timers

# Watch timers with custom 30-second interval
clctl tasks watch-timers --interval 30

# Watch timers with minimum allowed interval
clctl tasks watch-timers --interval 5
```
<!-- docgen:end id=cli:clctl:usage -->
