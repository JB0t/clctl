# clctl

[![CI](https://github.com/JB0t/clctl/actions/workflows/ci.yml/badge.svg?branch=dev)](https://github.com/JB0t/clctl/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/JB0t/clctl)](https://github.com/JB0t/clctl/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/JB0t/clctl.svg)](https://pkg.go.dev/github.com/JB0t/clctl)

Tasks, timers, notes and comments from the terminal. A single binary for Linux, macOS
and Windows. Task management is pluggable: clctl works standalone with its own local
task store, or against ClickUp or Jira (see [Task backends](#task-backends)), and every
command behaves the same on each. Your notes are separate from all of them.

## Install

A release binary for your platform is on the
[releases page](https://github.com/JB0t/clctl/releases) (Linux, macOS and Windows; amd64
and arm64), with `checksums.txt` next to it. With Go installed:

```bash
go install github.com/JB0t/clctl@latest
# or, from a checkout: build with the release version stamped, and install completion
scripts/install.sh              # optional arg: install dir (default ~/.local/bin)
```

`scripts/install.sh` also installs tab completion for bash (and zsh or fish when present).
Completion asks the binary for commands, flags and cached task IDs on every Tab, so it
always matches the installed version. If Tab still offers old commands, `complete -p
clctl` should print `complete -o default -F __start_clctl clctl`; anything else comes
from your shell profile (an old `source <(...)` line) and should be removed. The `notes`
group only completes when notes are enabled in the config.

A shell alias gets the same completion with `--alias`: put `alias cl=clctl` in your
shell profile and install with `scripts/install.sh --alias cl` (or
`CLCTL_ALIASES=cl scripts/install.sh`, so re-installs after a release keep it). By
hand, `clctl completion bash --alias cl` prints the script with the alias registered
(zsh and fish likewise).

### Versions

Releases are git tags `vX.Y.Z` (semver) with a
[GitHub release](https://github.com/JB0t/clctl/releases) each; `CHANGELOG.md` lists what
changed. `clctl --version` (or `-v`) prints the version it was built from:

- `v1.2.0`: a release binary, `go install ...@v1.2.0`, or `scripts/install.sh` at that tag.
- `v1.2.0-3-gabc1234`: `scripts/install.sh` three commits after it (`-dirty` with
  uncommitted changes).
- `v1.2.0-rc.1`: a release candidate, built by CI on a `release/1.2.0` branch.
- `dev-abc1234`: `scripts/install.sh` before the first tag.
- `v0.0.0-20261016080119-1fe5fe1ad6fd`: a plain `go build` from a checkout; Go's
  pseudo-version, the commit's time and hash (`+dirty` with uncommitted changes).

How changes, releases and branches work is in [CONTRIBUTING.md](CONTRIBUTING.md).

Optional tools: [fzf](https://github.com/junegunn/fzf) for fuzzy pickers (numbered
lists otherwise) and tmux for opening notes in their own windows.

## Set up

```bash
clctl init
```

`init` walks through:

1. **Task backend**: `local` (clctl's own store; no account needed), `clickup` or
   `jira`. See [Task backends](#task-backends).
2. **The backend's settings.**
   - local: the store's folder, your name, and the task key prefix (`TASK-1`).
   - ClickUp: API token (avatar > Settings > Apps > API Token; checked straight away,
     and your user ID is stored so "my tasks" never depends on matching names), the
     workspace, and the spaces ("All spaces" picks up new ones automatically).
   - Jira: site URL, then an account email and API token (Cloud) or a personal access
     token (Data Center), the projects, and optionally an issue for time without a task.

   Folders, lists and statuses are always discovered, never configured.
3. **Notes.** Turn on the dated notes layout (see below) in an existing or new
   folder, or turn it off, which hides the notes commands.

Every step has a flag for scripted setups:

```bash
clctl init --backend local --prefix TASK --no-notes
clctl init --backend clickup --token pk_... --team "My Workspace" --spaces all --no-notes
clctl init --backend jira --jira-url https://example.atlassian.net --jira-email me@example.com \
  --jira-token ... --spaces OPS,WEB --jira-misc-issue OPS-1
```

Settings live in `$XDG_CONFIG_HOME/clctl` (INI, mode 0600, in a `[clickup]` section for
compatibility with older configs; it holds every backend's settings). These environment
variables override the file: `CLCTL_BACKEND`, `CLCTL_LOCAL_DIR`, `JIRA_URL`,
`JIRA_EMAIL`, `JIRA_API_TOKEN`, `CLICKUP_API_TOKEN`, `CLICKUP_TEAM_ID`, `CLICKUP_API_URL`
and `NOTES_DIR`. A config with a ClickUp token and no `backend` key is a ClickUp config,
so existing setups keep working unchanged. Open tasks are cached in
`$XDG_CACHE_HOME/clctl/tasks.json` and refreshed when older than `cache_ttl_minutes`
(default 60), with `-r`/`--refresh`, or when the backend changes.

## Task backends

clctl's commands talk to a task backend through one interface
(`internal/tracker`), so `clctl ti -s`, `tasks update`, `comments add`, `notes push`
and the digests work the same whichever one holds your tasks. Switch with `clctl init`
(or `CLCTL_BACKEND` for one command).

| | local | ClickUp | Jira |
|---|---|---|---|
| Space | a space in the store | space | project (`spaces` = project keys) |
| List | a list in the store | list (optionally in a folder) | issue type (`OPS/10001`) |
| Task key | `TASK-12` | custom ID, else task ID | issue key `OPS-123` |
| Statuses | per list | per list | the issue type's workflow, changed through its transitions |
| Tags | tags | tags | labels (spaces become `-`) |
| Assignees | you | several | one |
| Running timer | in the store | ClickUp's own | kept by clctl in `$XDG_STATE_HOME/clctl/jira_timer.json` |
| Time entries | in the store | time entries | worklogs (labels as `#tags` in the comment) |
| Comments | stored as rich text | rich text | wiki markup, converted to and from Markdown |

Status types follow ClickUp's: `open` (not started), `custom` (in progress), `done` and
`closed`. Jira's To Do, In Progress and Done categories map to `open`, `custom` and
`closed`, so done issues leave the cache and `tasks complete` picks the Done status.

**local** keeps everything in `<local_dir>/tasks.json` (default
`~/.local/share/clctl/tasks`), written atomically under a lock file, with attachments
copied to `<local_dir>/attachments/<TASK-ID>/`. Put the folder somewhere synced or in git
to have it on several machines. A new store has one space, `Personal`, with one list,
`Inbox`, and the statuses `to do`, `in progress`, `blocked` and `done`. To add spaces or
lists, edit `tasks.json` while clctl is not running:

```json
"spaces": [
  {"id": "personal", "name": "Personal", "prefix": "TASK", "lists": [
    {"id": "inbox", "name": "Inbox", "statuses": [{"status": "to do", "type": "open"}, ...]},
    {"id": "reading", "name": "Reading", "statuses": [...]}
  ]},
  {"id": "home", "name": "Home", "prefix": "HOME", "lists": [...]}
]
```

**Jira** works with Jira Cloud (account email plus an API token from
id.atlassian.com > Security > API tokens) and Data Center (a personal access token, no
email). Jira has no running timer, so the backend's timer is kept by clctl and logged
as a worklog when it stops (Jira's minimum is one minute). A worklog needs an issue:
set `jira_misc_issue` (for example a "Meetings and admin" issue) to log time without a
task on it; without it, `--no-task` timers are refused. Time-entry and comment IDs are
`<issue key>:<ID>`.

### Notes stay separate

The backend holds what you share; your notes folder holds what you depend on yourself,
task-related or not. Nothing in the notes reaches a backend until you push it
(`clctl notes push`), so there is always a deliberate step between the two, and
changing backend does not touch your notes.

## Use

Commands are grouped: `tasks`, `timers`, `notes` and `comments`, plus `digest`.
Plain `clctl` opens a menu of every action, and each group run alone (`clctl timers`)
opens a menu of its own actions.

```bash
clctl                                # menu of every action
clctl tasks list                     # your open tasks: space > folder > list, by priority
clctl tasks list --assignee "Jane"   # tasks assigned to someone else (name or email)
clctl tasks list --everyone
clctl tasks open                     # pick a task: its web page, or clctl's task view (local tasks, or --tui)
clctl tasks update                   # pick a task, then what to change: status, name,
                                     #   description (in your editor), priority, due, tags, assignees
clctl tasks update --task-id TECH-1 --status "in progress" --add-tag infra --due +3d
clctl tasks complete --task-id TECH-1234
clctl tasks search                   # pick any task, then actions to run on it
clctl tasks create                   # pick a list, then prompts; offers notes/timer after
clctl tasks push-todo                # 'new: ' lines in <notes>/TODO.md become tasks
clctl tasks attach | export
clctl --include-done tasks open      # pickers also offer done and closed tasks
clctl tasks refresh                  # re-download the task cache (clctl -r does the same)
```

### Task view

`tasks open` shows a task that has no web page (the local store's tasks) in clctl's
task view, `clui`; `--tui` uses it for ClickUp and Jira tasks too. It lays the task out
the way a tracker's page does: key, name and location, then the status, timer,
priority, assignees, due date (red when overdue) and tags, then the description and
comments rendered as Markdown, the subtasks, and the time logged on it in the last 90
days.

| Key | Does |
|---|---|
| `s` / `p` | change the status / priority (done stops its timer) |
| `t` | start or stop a timer; with the backend's timer on another task, stop it or time this one with a local timer |
| `c` | add a comment in Markdown (Ctrl+S posts) |
| `e` | edit the description in your editor |
| `o` / `u` / Backspace | open a subtask / the parent / go back |
| `w` | open the web page, when there is one |
| `r`, `?`, `q` | refresh, help, quit |

Timers started here are clctl's ordinary timers, so `clctl timers list` shows them, and
every change updates the task cache.

`tasks update` changes only what its flags name: `--status`, `--name`,
`--description` (Markdown, replaces the old one), `--priority` (urgent, high, normal,
low or none), `--due` (YYYY-MM-DD, today, tomorrow, +3d or none), `--tag` (sets the whole
list), `--add-tag`, `--remove-tag`, `--assignee` and `--unassign` (names or emails). It
shows the changes and asks before applying them; `--yes` skips that. It replaces
`tasks set-status`, which still works.

### Short forms

Like tmux, clctl takes short names for its commands, and one-letter flags for a group's
actions. The action's own flags follow it:

```bash
clctl t -l --assignee Jane           # clctl tasks list --assignee Jane
clctl t -u --task-id TECH-1          # clctl tasks update --task-id TECH-1
clctl ti -s                          # clctl timers start
clctl n -p                           # clctl notes push
clctl s "cert rotation"              # clctl search "cert rotation"
clctl -r                             # refresh the task cache, nothing else
clctl -v                             # version; -h is help everywhere
```

| Short | Command | Actions |
|---|---|---|
| `t` | `tasks` | `-l` list, `-o` open, `-u` update, `-s` search, `-i` status, `-c` create, `-a` attach, `-e` export (`complete`, `push-todo` and `refresh` have none) |
| `ti` | `timers` | `-s` start, `-x` stop, `-p` pause, `-u` resume, `-c` switch, `-l` list, `-w` watch, `-g` log, `-e` edit |
| `n` | `notes` | `-o` open, `-w` workspace, `-p` push, `-u` unpushed, `-t` tldr |
| `s` | `search` | |
|  | `comments` | `-a` add, `-l` list, `-f` fix-tables |
|  | `sessions` | `-d` digest, `-e` export, `-i` init |

The long form of each action flag works too (`clctl tasks --update`). Tab completes the
short names and, after a `-`, the action flags with what they do.

Commands that take a task accept `--task-id` (ID or custom ID, tab-completed from
the cache) or let you pick with fzf. In multi-select pickers Tab marks a row, Ctrl+A
marks every row and Ctrl+D clears the marks (without fzf, answer `all`). Pickers over
everyone's tasks (`tasks search`, `tasks open`) mark the tasks assigned to you with
`@me` in the first column; type `@me` in fzf to keep only those.

Esc goes back one step: in a command with several pickers (`tasks search`, `notes push`,
`timers start`, `timers edit`) Esc in a later picker returns to the one before it, and
Esc in the first picker leaves the command (back to the menu it was started from, if
any). A "Cancel" row leaves the command straight away.

`--include-done` works with every command: pickers and `tasks list` then include done
and closed tasks. The cache holds open tasks only, so closed ones are fetched from
the task backend each time (yours, or everyone's for `--everyone`/`--assignee`).

The commands' earlier names (`clctl tasks start-timer`, `tasks push-notes`,
`tasks digest`...) still work, hidden, with a note naming the new spelling, so
existing scripts and cron entries keep running.

Shell completion: installed by `scripts/install.sh` (see Install), or by hand with
`clctl completion bash|zsh|fish|powershell --help`.

## Timers

```bash
clctl timers start                   # offers to stop, or keep, a timer running elsewhere
clctl timers start --no-task --description "Sales call" --label meetings
clctl timers stop [--all]            # pick which to stop when several run
clctl timers pause [--all]           # stop the most recently started timer, keep it to resume
clctl timers resume [--all]          # restart the most recently paused timer
clctl timers switch                  # pick a paused timer or a task: pause all running, start that one
clctl timers list | watch
clctl timers log --date 2026-09-14 --start 09:00 --end 10:30   # blank or "now" = current minute
clctl timers edit                    # pick a recent time entry, change its range or delete it
clctl timers edit --task-id TECH-1234 --start 09:15 --end 10:45
clctl timers inspect --since 30d     # stored start/end/duration per entry, inconsistent ones flagged
```

Time can also go on no task: `timers start` and `timers log` take `--no-task` (or pick
"(no task)" at the top of the task picker) with `--description` and/or `--label`
(time-entry tags; on Jira, `#tags` in the worklog; repeat or comma-separate for several), and ask for them when
neither is given. Such timers stop, pause, resume and run locally like task timers, and
`timers list` shows them as `(no task)` with their labels.

Each backend runs one timer per person: ClickUp's own, or for the local store and Jira
one clctl keeps. When `timers start` finds it running on another task you can stop that one, or keep it and time the new task with a local timer:
clctl keeps it in `$XDG_STATE_HOME/clctl/timers.json`, shows it in `timers list` as
`[local]`, and logs the range as a time entry when you stop it.

Running timers queue in the order they started. `timers pause` stops the newest one
(logging its time, as `stop` does) and pushes its task onto a paused stack in
`$XDG_STATE_HOME/clctl/paused_timers.json`; `timers resume` pops the last paused task and
starts it again as the newest timer, on the backend's timer when that is free, else as a
local one. Both are last in, first out, and `--all` keeps that order: `pause --all` then
`resume --all` restarts every timer in its original order. The backend is read on every
call, so timers started or stopped in ClickUp's UI count, and a paused task already running again is
dropped instead of started twice. `timers list` shows the paused stack below the running
timers.

`timers inspect` (hidden from the menu) prints each entry with the fields the backend stores
(start, end, duration, when it was added, source) and flags one whose end is missing, not
start + duration, or stored as a string: the shapes ClickUp's Time entries view shows as
"Invalid date". `--json` prints the entries as ClickUp returns them. Entries clctl logs or
edits are sent with start, end and duration in Unix milliseconds, as the API asks.

`timers switch` (`clctl ti -c`) moves to other work in one step: pick a paused timer,
"(no task)" or one of your open tasks without a timer, and it pauses every running
timer (newest first, as `pause --all` does) and starts the picked one on the backend's
timer. A paused timer picked keeps its description and labels and leaves the stack; the
ones just paused go on top, so `resume` brings them back. `--task-id` skips the picker
(on a task already running, it only pauses the others).

## Notes

```bash
clctl notes open                     # today's note in a tmux window; asks to start the timer
clctl notes workspace TECH-1234      # symlink every day's note + files into today's workspace
clctl notes unpushed --since 7d      # notes with sections not pushed yet, or edited since
clctl notes push                     # pick task, note (by day) and ## sections
clctl notes push --task-id TECH-1234 --date yesterday --all
clctl notes push --task-id TECH-1234 --select 1,3-4 --dry-run
clctl notes tldr                     # daily tl;dr of each task with freshly pushed notes
```

Inside tmux, `notes workspace` opens a window named `<ID>-ws`: today's note in your
editor above a shell (70/30) on the left, your editor on the workspace folder on the
right. Run it again on a later day, from inside that window or anywhere else, and it
switches to the window and changes every shell pane in it to today's workspace (a pane
still running an editor is left alone and named; quit the editor for a shell there).
From the menu, your own pane is left alone too and the `cd` is printed instead.

Links you add to a workspace yourself (a repo, a shared folder) carry over: the next
day's workspace gets the same links from the task's previous workspace, re-pointed from
the new folder, so they only need adding once.

`notes push` with several days of notes for a task shows each day's progress next to it
(`pushed`, `2 pushed, 1 new`, `1 edited`, `empty`), so the days already posted stand out.

`notes push` posts one comment per `##` section, skips sections already posted and
updates edited ones. Headings become headings of the same level (in ClickUp 5 and 6 show
as 4), and Markdown tables become tables (ClickUp tables; Jira wiki tables) (header row bold, `:---:` alignment
kept). `--force` re-renders sections that were already pushed, for example after a
formatting change. Pushed sections are tracked in
`$XDG_STATE_HOME/clctl/pushed_notes.json`, the same file and format the Python
version used.

### Daily tl;dr

`notes tldr` keeps a running tl;dr of each task in a subtask named `summary`, one
comment per note day. It looks at the note days in `--since` (default `2d`: today, and
the two days before) for tasks whose pushed sections are new or changed since its last
run. For each one, the local model (the digest's `llm_*` settings) gets that day's
pushed sections and the latest three earlier tl;dr comments, and writes where the task
stands plus up to 6 caveman bullets. The comment is headed `tl;dr YYYY-MM-DD`; pushing
more notes for that day later updates it in place. The `summary` subtask is created in
the task's list on the first run, unassigned, and digests leave it out. Sections
edited but not pushed again are left out until they are, so nothing reaches the backend
through the tl;dr that `notes push` has not posted. What was written is tracked in
`$XDG_STATE_HOME/clctl/tldr.json`. It takes the digest's lock, so it never runs at the
same time as a summarising digest. Run it from cron after the day's pushes:

```cron
30 22 * * 1-5 $HOME/.local/bin/clctl notes tldr >> $HOME/.local/state/clctl-tldr.log 2>&1
```

`--dry-run` prints the tl;drs without posting, `--again` rewrites ones whose notes
have not changed, and `--task-id` limits it to one task. `--since 2026-09-01` backfills
older days, oldest first.

`--gentle` is for a machine that must stay usable, such as a WSL VM: same model and
prompts, so the tl;drs are just as good, but less at once. A server clctl starts gets
`LLM_PROFILE=low` (at most 4 threads, small batches, 8k context), it waits `--pause`
(default 30s) between tl;drs, and it writes at most `--limit` (default 5) per run,
oldest first, leaving the rest to the next run. For a backfill, run it from cron every
hour or so until it reports nothing to do:

```cron
15 * * * * $HOME/.local/bin/clctl notes tldr --gentle --since 2026-09-01 >> $HOME/.local/state/clctl-tldr.log 2>&1
```

### Notes layout

When enabled, task notes live at

```
<notes dir>/YYYY/MM/DD/<Space>/<Folder>/<List>/<TASK-ID>.md
<notes dir>/YYYY/MM/DD/<Space>/<Folder>/<List>/<TASK-ID>/
```

Lists directly in a space have no folder level. Names keep only letters, digits,
`_`, `.` and `-`.

## Comments

```bash
clctl comments add | list
clctl comments fix-tables [--task-id ID] [--dry-run]
```

`comments fix-tables` converts Markdown tables typed into your existing comments (on
one task, or all your open tasks) into real tables (ClickUp tables; the local store and
Jira render them as tables too), keeping the formatting inside cells.

## Digest

```bash
clctl digest                         # tasks active in the last 7 days + comment summaries
clctl digest --since 2w --everyone -o -
clctl digest --summarize             # plus AI bullet points per task
clctl digest --meeting               # on a meeting day only; see below
clctl digest --check                 # check the meeting-digest set-up, change nothing
```

`digest` writes `<notes>/<today>/digest.md` (re-running the same day overwrites it):
tasks edited or commented on in the window, newest first, each recent comment cut to
its headings and opening sentences, with lists, code, tables and images as markers.

### AI summaries

`digest --summarize` adds bullet points per task from a local model server: an
OpenAI-compatible one such as llama.cpp's `llama-server` when `llm_url` ends in
`/v1`, otherwise Ollama (`llm_url` default `http://localhost:11434`). `llm_model`
defaults to the server's first model. Each comment is summarised on its own, with the
task's description, parent, neighbouring tasks and your notes on it as background,
then the summaries are merged per task (more comments allow more bullets, up to 12).
The previous `digest.md`'s summary of each task is passed in too, so the briefing
focuses on what changed. Everything is cached in `$XDG_CACHE_HOME/clctl/summaries.json`,
so re-runs only summarise new or edited comments (changing the model starts the cache
afresh). When the server is down, clctl runs `llm_start` (or starts the docker
container `llm_container`) and afterwards runs `llm_stop` (or stops the container).

Tasks with a `summary` subtask (see [Daily tl;dr](#daily-tldr)) are summarised from
its tl;drs instead of their comments, when it has any from the digest window: one
tl;dr is used as it is, several are merged by the model into one briefing (the newest
wins where they disagree). The digest marks those summaries, e.g.
`**Summary** (from 3 tl;drs, 2026-10-12 to 2026-10-14)`. Tasks without the subtask,
or without a tl;dr in the window, are summarised from their comments as before. This
is much less model work, so a meeting digest over tasks with tl;drs runs far faster.

Before comments reach the model they are compressed, as the model's prompt processing
is the slowest part on a CPU:

- Prose loses articles, filler, sentence openers ("So", "Okay"), connectives
  ("however", "additionally") and wordy phrases ("in order to" becomes "to"), but
  keeps hedging words such as "could", "potentially" and "proposed", negations,
  numbers, names, commands and code.
- Pasted machine output (logs, terminal output such as `kubectl get pods`, JSON,
  tables, diffs, config, test reports), in a code block or as 8+ lines of plain text,
  goes through the [Caveman](https://github.com/JuliusBrussee/caveman) engine. It keeps
  errors, warnings, anomalies and the lines around them, and replaces repetitive lines
  with what they had in common ("… 57 lines elided: all rows=500; duration
  200ms..206ms …"). A 40-line batch log becomes a few lines with its warning intact.
- Long scripts are reduced to their first lines and the commands they run.
- Lines under planning headings (Overview, Goals, Plan, Next steps, Design…) are
  marked "(planned)" so they are not reported as done work.

The digest run reports how much shorter the comments got. The model is told to write
caveman style too: terse bullets of at most 15 words with names, numbers, commands and
negations exact, which also cuts the time it spends writing. With Ollama,
`llm_threads` sets the CPU threads per request: on CPUs with performance and efficiency
cores fewer threads than cores is often faster (14 of 20 doubled output speed on a Core
Ultra 7 265). llama.cpp servers take their threads when they start (`llama-server --threads`).

With an OpenAI-style server (`llm_url` ending in `/v1`), each answer is capped at
`llm_max_tokens` tokens (default 1536) so a model stuck repeating itself stops. A
long task, or a thinking model whose reasoning counts towards the cap, can run past
it ("the answer ran past 1536 tokens"); raise it, e.g. `llm_max_tokens = 4096`, keeping
it below the server's context size. Ollama has no answer cap.

Only one summarising run happens at a time: a second one exits (`--meeting`) or fails
(otherwise) while `$XDG_STATE_HOME/clctl/digest.lock` exists. A lock older than 3 hours
is treated as left over from a killed run.

### Meeting digest

`digest --meeting` runs only when today's notes folder has a meeting note (a file
name containing the words weekly, it and meeting, or `meeting_pattern` as a regex).
It summarises everything since the previous meeting note, writes `digest.md` next to
the meeting note and links it under the note's title, once (`--again` redoes it).

### Setting up the meeting digest

The meeting digest joins four pieces: meeting notes in the notes folder, a model
server, the clctl config, and cron. Check each step with `clctl digest --check`,
which reports every problem it finds and asks the model for one word.

**1. Meeting notes.** The meeting note must be a file directly in the day folder,
`<notes dir>/YYYY/MM/DD/<name>.md`, not in a task's subfolder, so a meeting called
"Weekly IT Meeting" becomes `weekly-it-meeting.md`. For a meeting with another name, set
`meeting_pattern` (a regex on the file name, e.g. `meeting_pattern = ^infra-sync`). It must
use the same notes folder as clctl.

**2. Model server.** Any OpenAI-compatible server works. A good fit on a CPU is
Qwen3-30B-A3B-Instruct-2507 at Q8_0 (32.5 GB) served by llama.cpp's `llama-server`: a
general instruction model that answers without thinking blocks and has only 3B active
parameters, so it is fast without a GPU. Dense models of similar quality (Gemma 3 27B,
Mistral Small 24B) are many times slower without one. Give it at least an 8k context
(clctl's prompts are sized for 8k); 16k leaves room. For `clctl index` and `search`, serve
an embedding model such as nomic-embed-text as well. For a smaller footprint (18.6 GB,
somewhat lower quality), use the Q4_K_M build.

```bash
llama-server -m ~/models/qwen3-30b-a3b-instruct-2507-q8_0.gguf --alias qwen3-instruct \
  --port 8082 --ctx-size 16384
llama-server -m ~/models/nomic-embed-text-v1.5.gguf --embeddings --port 8083
```

**3. Config.** Add the keys to the `[clickup]` section of `~/.config/clctl` (or, when
`~/.config/clctl` is a directory, as it is once it holds the sessions export's password file,
of `~/.config/clctl/config`):

```ini
[clickup]
notes_enabled = true
notes_dir = ~/notes
llm_url = http://127.0.0.1:8082/v1
llm_model = qwen3-instruct
; optional: commands that start and stop the server around each run
llm_start = ~/bin/start-digest-model
llm_stop = ~/bin/stop-digest-model
; for clctl index / search:
llm_embed_url = http://127.0.0.1:8083/v1
; llm_embed_start = ...
; llm_embed_stop = ...
; meeting_pattern = ^infra-sync
```

`llm_start` and `llm_stop` are optional: clctl runs `llm_start` when the server is down and
`llm_stop` afterwards, so a model that needs most of the machine's memory only runs for the
digest. Give the commands by their full path: cron's `PATH` is only `/usr/bin:/bin`, and
exports in your shell profile do not reach cron.

If you switch an existing search index from Ollama to the `embed` preset, rebuild it
(`rm ~/.cache/clctl/index.json && clctl index --comments`): the model name is the same,
but the vectors differ slightly.

To use Ollama instead, run it in a docker container and point clctl at it without
`/v1`:

```bash
docker run -d --name clctl-llm -p 127.0.0.1:11434:11434 -v clctl-ollama:/root/.ollama ollama/ollama
docker exec clctl-llm ollama pull qwen3:30b-a3b-instruct-2507-q8_0
docker exec clctl-llm ollama pull nomic-embed-text
docker stop clctl-llm                 # clctl starts it when it needs it
```

with `llm_url = http://localhost:11434`, `llm_model` set to the pulled model and
`llm_container = clctl-llm` in place of the `llm_start`/`llm_stop` and embed keys. Your
user must be able to run `docker` without sudo, because cron runs clctl as you.

Put `notes_dir` in the file even if `NOTES_DIR` is in your shell profile: cron does
not read the profile, so without it the cron run finds no notes folder. For the same
reason the config must be at `~/.config/clctl`, unless the crontab sets
`XDG_CONFIG_HOME`.

**4. Try it by hand.** On a day with a meeting note:

```bash
clctl digest --check
clctl digest --meeting --again        # --again in case a run already linked it
```

Loading the model takes a few minutes; the run prints
`Summarising TECH-1234 (3/12)...` per task, and the server's own log is
`~/.local/state/llm-server-digest.log` (`llm-server log digest`).

**5. Cron.** `clctl digest --init` does steps 1, 2 and 5 for you, asking before each
change. It saves `notes_dir` in the config when it only came from `$NOTES_DIR`, lists
the notes of the last 120 days so you can pick which ones are the meeting's (stored as
`meeting_pattern`), and adds the cron line below, or replaces an existing meeting digest
line (one marked `# clctl-managed meeting digest`, or any `clctl digest --meeting`). It
ends with `--check`.

By hand: run it every 20 minutes on weekday mornings; it does nothing on days
without a meeting note and once the digest is linked. Make the log directory once with
`mkdir -p ~/.local/state`:

```cron
*/20 6-11 * * 1-5 $HOME/.local/bin/clctl digest --meeting >> $HOME/.local/state/clctl-meeting-digest.log 2>&1
```

If something else writes the meeting notes from cron, run it before this, so the
meeting note exists by the time the digest looks. Under WSL, cron only runs while the
distro is running.

A nightly run can do the slow part in advance: `clctl index --comments` keeps search
current, and `clctl digest --summarize --since last-meeting -o - > /dev/null` fills
the summary cache over the same window the meeting run will use, so on the morning
only comments added overnight still need the model.

**When it doesn't work**, check the log, then `clctl digest --check`:

| Log line | Cause |
| --- | --- |
| `No meeting note today; nothing to do.` | No matching file directly in today's day folder. `--check` lists today's notes. To digest now anyway: `clctl digest --meeting-note <name>` (links that note) or `clctl digest --meeting --force` (links nothing). To match it from now on: `clctl digest --init`. |
| `Command "digest" is deprecated` | The cron line, or a script it runs, uses the old `clctl tasks digest`; `--check` names it. `clctl digest --init` replaces the line. |
| `notes are disabled` | `notes_dir` is missing from the config (cron has no `NOTES_DIR`). |
| `no model server at ...` | The server is down and neither `llm_start` nor `llm_container` is set. |
| `starting the model server with ...` | `llm-server` failed: see the lines above it (model not downloaded, the coding model running, `llama-server` not found). |
| `serves the OpenAI API` | `llm_url` points at `llm-server` without `/v1` at the end. |
| `docker start clctl-llm: ...` | The container is missing, or your user cannot run docker. |
| `model "..." is not on ...` | `llm_model` does not match the served name (`qwen3-instruct` for `llm-server`), or the model is not pulled into Ollama. |
| `the answer ran past 1536 tokens` | The model repeated itself on one comment; re-running retries it. |
| `another summarising digest is running` | A run is still going; the next cron run tries again. |

### Where a task stands

```bash
clctl tasks status TECH-1234                    # print the task's current state
clctl tasks status TECH-1234 --related --save   # with related notes; save to status.md
clctl tasks status TECH-1234 --since 2026-09-01 -o state.md
```

`tasks status` reads everything about one task: every comment, every day's note in the
notes layout and the description, compressed as above. The model tags what each one says
(done, doing, blocked, next, decision, fact), then writes the current state in four
sections, Done, In progress, Blocked / open questions and Next, each bullet dated by its
latest evidence. Later notes override earlier ones, so a fix replaces the bug report and
finished work replaces the plan. Each comment and note is tagged once and cached, so a
re-run only reads what is new; a long history is reduced in parts, oldest first, to stay
within the model's context. If the model fails on one item (repeating itself), that
item's own text is used instead and the run carries on.

`--related` adds the closest matches from other tasks' notes and comments in the search
index (`clctl index --comments`), for work recorded somewhere else; the model only uses
them when they clearly concern the task and names their source. `--save` writes
`status.md` into today's folder for the task. It uses the digest's model server
settings, and refuses to start while a summarising digest is running (they share its
lock, so neither stops the model server under the other).

### Redoing digests written without AI summaries

Digests from runs that had no model (plain `digest`, or before the model server was set
up) have only the shortened comments. `--redo` rewrites them with summaries:

```bash
clctl digest --redo --dry-run       # list the digests without AI summaries, and their windows
clctl digest --redo                 # rewrite them, oldest first
clctl digest --redo=all --since 2026-09-01   # every digest from that day on, summarised or not
```

Each digest keeps its own window, read from its title line (`# Task digest: FROM to
TO`; older digests say `ClickUp digest`): it starts at FROM and ends when the file was written (or at the end of TO when the
file has been touched since). Only comments from that window count, including comments
on tasks closed since then, and each digest gets the one before it as context, as if it
had been summarised on the day. The model server is started once for the whole run. The
files are overwritten in place, so commit your notes first if you want the old versions.
A task whose only activity in the window was an edit (no comment) can be missing from a
redone digest, as the backends keep only a task's latest edit time.

## Session digests

```bash
clctl sessions init                     # set up once: which assistants, folders, Nextcloud, cron
clctl sessions digest --out sessions.md # the folders init saved
clctl sessions digest ~/.claude/projects/-home-me-workspace --out sessions.md
clctl sessions digest ~/.config/Code/User --out copilot.md          # GitHub Copilot Chat
clctl sessions digest ./transcripts --dry-run --out redacted.txt   # what the model would read
```

`sessions digest` summarises Claude Code transcripts and GitHub Copilot Chat sessions into
one Markdown file with a section per session, newest first: which assistant, session ID,
first and last message time, the session's title, and bullets on repositories and
services touched (from `workspace/<name>` paths), decisions, designs chosen and rejected,
plans and open items, and dates. It needs no task-backend set-up.

Each DIR is one of:

- a Claude Code projects folder (`~/.claude/projects`): `*.jsonl` transcripts, or the
  `*.jsonl.gz` that `sessions export` uploads, in subfolders too;
- a VS Code user folder, for Copilot Chat: `~/.config/Code/User` (Linux),
  `~/Library/Application Support/Code/User` (macOS), `.../Code - Insiders/User`, or from
  WSL the Windows one, `/mnt/c/Users/<you>/AppData/Roaming/Code/User`. VS Code keeps each
  chat in `workspaceStorage/<hash>/chatSessions/` (or `globalStorage/emptyWindowChatSessions/`
  for windows without a folder), as `<id>.json` or, in newer versions, an `<id>.jsonl` log
  of changes, which is replayed; when both exist only the log is read. The window's folder
  (from `workspace.json`) counts as the session's working directory. Chats from VS Code
  remote windows (SSH, WSL, containers) are kept by the VS Code you run locally, so point
  at that machine's user folder. Other files in VS Code's storage are never read.

Without a DIR, the folders `sessions init` saved are read.

Only the user's and the assistant's text, tool names and the file paths given to tools
are read; tool results, attachments, thinking, terminal commands, edit contents and (for
Copilot) the input box and confirmations are dropped, since they hold file contents the
repositories already cover. Secrets become `[REDACTED:<kind>]` before
anything reaches the model (private keys, `Authorization` and `PRIVATE-TOKEN` values,
password/secret/token/api_key assignments, URL userinfo, JWTs, hex or base64 runs of 32+
characters). Each session is cut into chunks of about 6k tokens (`--chunk-chars`),
summarised by the llm-server code preset (`--llm-url`, default
`http://127.0.0.1:8081/v1`, `--model qwen3-coder`), and merged. Copilot sessions get the
same prompts, naming Copilot instead of Claude Code. Answers are cached in
`$XDG_CACHE_HOME/clctl/session-summaries.json`, so a re-run only summarises what changed.

### Setting up: `sessions init`

`clctl sessions init` walks you through it:

1. Claude Code, GitHub Copilot or both.
2. Their folders: it lists the ones it finds on this machine, with how many sessions
   each holds, or takes one you type.
3. The Nextcloud URL, your folder name under `Shared/Sessions`, the account that uploads,
   and the file holding its password. If that file is missing it offers to write it
   (mode 0600), asking for the password without echoing it.
4. A dry run of the export over the last 30 days, showing how many sessions would upload.
5. The nightly cron line (02:30; `--cron` changes it). It is added to your crontab only
   when you say yes, and not at all when your crontab already runs `clctl sessions export`.
   It creates `~/.local/state/clctl/` for the log, since cron cannot.

The answers go in the config's `[clickup]` section:

| Key | What |
|---|---|
| `sessions_claude_dir` | Claude Code projects folder(s), comma-separated |
| `sessions_copilot_dir` | VS Code user folder(s), comma-separated |
| `sessions_to` | Nextcloud URL |
| `sessions_user` | your folder under `Shared/Sessions` |
| `sessions_nc_user` | the uploading account (`NC_USER` overrides) |
| `sessions_nc_pass_file` | the file holding its password (`NC_PASS_FILE` overrides) |

The password itself is never written to the config. Without a terminal, init takes the
answers from flags (`--claude-dir`, `--copilot-dir`, `--to`, `--user`, `--nc-user`,
`--nc-pass-file`; `none` turns a source off), keeps configured values for flags not
given, and prints the cron line instead of adding it.

### Exporting your sessions

```bash
clctl sessions export --dry-run          # the folders and account init saved
clctl sessions export ~/.claude/projects --user jo --to https://cloud.example --dry-run
```

`sessions export` uploads a redacted `<session-id>.jsonl.gz` of each transcript changed in
the last `--since` (default 30d) to `Shared/Sessions/<NAME>/` in a Nextcloud account over
WebDAV, creating the folder when it is missing. Copilot Chat sessions go up as
`copilot-<session-id>.jsonl.gz`, converted to the same reduced shape as Claude Code's (each
line marked `"source":"copilot"`), so whatever digests the uploads reads both. It keeps
exactly what `sessions digest` reads (your and the assistant's text, tool names, file
paths), through the same redactor; tool results, attachments and thinking never leave the
machine. A transcript already uploaded unchanged is skipped (size and sha256 in
`~/.local/state/clctl/sessions-export.json`); one that grew goes up again, whole. Without
a DIR it exports the folders `sessions init` saved, and `--user` and `--to` default to its
answers. The account comes from the environment, `NC_USER` and `NC_PASS_FILE` (the path
of a file holding its app password), else from the config. `--dry-run` lists what would
upload, with sizes, and needs no account. A large Copilot log is read whole: the biggest
here (200 MB) took about 450 MB of memory.

Opt-in only: a person's directory is only ever uploaded by that person's own cron on
their own machine. Never export someone else's `~/.claude` or VS Code folder, and never
from a shared host. `sessions init` adds the cron line for you; by hand it is (02:30
nightly):

```cron
30 2 * * * $HOME/.local/bin/clctl sessions export >> $HOME/.local/state/clctl/sessions-export.log 2>&1
```

or, without `sessions init`, with everything on the line:

```cron
30 2 * * * NC_USER='Data Bot' NC_PASS_FILE=$HOME/.config/clctl/nc-data-bot $HOME/.local/bin/clctl sessions export $HOME/.claude/projects --user <you> --to https://<nextcloud> >> $HOME/.local/state/clctl/sessions-export.log 2>&1
```

## Search

```bash
clctl index              # embed your notes (only new or changed parts)
clctl index --comments   # ... and the comments on your open tasks
clctl index --rebuild --comments   # embed everything again, e.g. after changing embedding server
clctl search "postgres connection problems in the dask cluster"
clctl search --notes -n 5 "how did I create the storage accounts"
```

Notes and comments are split by heading, embedded with a local embedding model
(`llm_embed_model`, default `nomic-embed-text`, served by `llm_embed_url` or else the
same model server; see the meeting digest set-up for `llm-server start embed`) and
kept in `$XDG_CACHE_HOME/clctl/index.json` with a compressed copy of each chunk.

## Cron

`clctl tasks refresh --quiet` refreshes the cache without output, e.g. hourly:

```
0 * * * * $HOME/.local/bin/clctl tasks refresh --quiet
```

Entries using the old spelling, `clctl cache refresh --quiet`, keep working.

The meeting digest's cron entry is under [Setting up the meeting digest](#setting-up-the-meeting-digest).

## Develop

```bash
go test ./...
go vet ./...
```

## Contributing

Pull requests go into `dev`; [CONTRIBUTING.md](CONTRIBUTING.md) explains the branches, the
checks and how releases are cut.

## License

[MIT](LICENSE)
