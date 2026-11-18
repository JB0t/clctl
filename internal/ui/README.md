# ui

<!-- docgen:begin id=internal/ui:overview scope=internal/ui hash=87c5852ef418 -->
Package ui holds the interactive bits: fzf pickers and terminal prompts. It provides functions for user input, terminal detection, and tmux integration. The package supports fzf-based selection when available, falling back to numbered lists. It handles text prompts, secret input, and confirmation questions. Tmux functions switch windows, create workspaces, and send commands to tmux panes. The package detects if the terminal is interactive and whether ANSI colors should be used. It also provides shell quoting utilities. The exported symbols include functions for picking rows, asking questions, and managing tmux sessions. Error handling covers user cancellation and system errors. The package uses `fzf` for enhanced selection when available, with fallbacks for basic terminal input. Environment variables like `NO_COLOR` and `TMUX` affect behavior. The `ErrCancelled` error signals user aborts. Functions like `Pick` and `PickMany` support single or multiple selection from lists. `Ask` and `AskSecret` prompt for text input. `Confirm` handles yes/no questions. Tmux functions manage windows and workspaces. `ShellQuote` escapes strings for shell use. The package reads from stdin and writes to stdout and stderr. It checks for terminal capabilities and fzf availability. The `FzfMultiArgs` variable configures fzf's multi-select behavior. The `Interactive` and `Color` functions report terminal properties. The `HasFzf` function checks for fzf on PATH. The `TmuxCurrentWindow` function returns the current tmux window name. The `TmuxType` function sends input to tmux. The `TmuxWorkspace` function creates a workspace layout. The `pick` function implements fzf selection. The `pickNumbered` function implements numbered list selection. The `inTmux` function checks if running inside tmux. The `selectTmuxWindow` function switches to an existing tmux window. The `stdin` variable is a buffered reader for stdin. The `ErrCancelled` error is returned when the user cancels an operation. The `Interactive` function checks if stdin and stdout are terminals. The `Color` function checks if stdout should use ANSI colors. The `HasFzf` function checks if fzf is available. The `FzfMultiArgs` variable configures fzf's multi-select mode. The `Pick` function lets the user choose one row. The `PickMany` function lets the user choose multiple rows. The `Ask` function prompts for text input. The `AskSecret` function prompts for secret input. The `Confirm` function asks a yes/no question. The `TmuxWindow` function switches to a tmux window. The `TmuxCurrentWindow` function returns the current tmux window name. The `TmuxType` function sends input to tmux. The `TmuxWorkspace` function creates a tmux workspace. The `ShellQuote` function quotes strings for shell use. The `pick` function implements fzf selection. The `pickNumbered` function implements numbered list selection. The `inTmux` function checks if running inside tmux. The `selectTmuxWindow` function switches to an existing tmux window. The `stdin` variable is a buffered reader for stdin. The `ErrCancelled` error is returned when the user cancels an operation.
<!-- docgen:end id=internal/ui:overview -->

<!-- docgen:begin id=internal/ui:reference scope=internal/ui hash=87c5852ef418 -->
## Reference

### `ui.go`

- **`ErrCancelled`** (variable): ErrCancelled means the user backed out of a picker or prompt (Esc / Ctrl+C / empty).
- **`Interactive`** (function): Interactive reports whether stdin and stdout are terminals.
- **`Color`** (function): Color reports whether stdout should get ANSI colours.
- **`HasFzf`** (function): HasFzf reports whether fzf is on PATH.
- **`FzfMultiArgs`** (variable): FzfMultiArgs turns on fzf's multi-select, with keys to mark or clear every row.
- **`Pick`** (function): Pick lets the user choose one row and returns its index.
- **`PickMany`** (function): PickMany lets the user choose rows and returns their indexes in row order. In fzf, Tab marks a row, Ctrl+A marks every row and Ctrl+D clears the marks; in the numbered list, "all" picks every row.
- **`Ask`** (function): Ask prompts for a line of text, returning fallback when the answer is empty.
- **`AskSecret`** (function): AskSecret prompts without echoing input.
- **`Confirm`** (function): Confirm asks a yes/no question.
- **`TmuxWindow`** (function): TmuxWindow switches to tmux window `name`, creating it in dir (running command, if any) when it does not exist. It returns false when not running inside tmux.
- **`TmuxCurrentWindow`** (function): TmuxCurrentWindow returns the name of the tmux window clctl runs in, or "" outside tmux.
- **`TmuxType`** (function): TmuxType types a command line into clctl's own tmux pane. The shell that started clctl reads it once clctl exits, so this is how clctl can change that shell's directory.
- **`TmuxWorkspace`** (function): TmuxWorkspace switches to tmux window `name`, or creates it in dir laid out as:
- **`ShellQuote`** (function): ShellQuote quotes a string for a POSIX shell.
<!-- docgen:end id=internal/ui:reference -->
