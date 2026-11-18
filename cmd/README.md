# cmd

<!-- docgen:begin id=cmd:overview scope=cmd hash=435fa57710b5 -->
Package cmd implements the command-line interface for clctl. It defines the main command tree and all subcommands for interacting with ClickUp tasks, timers, notes, and comments. The root command handles configuration loading, menu navigation, and flag parsing. Commands are organized into groups and can be invoked directly or through a menu system.

The App type provides shared resources across commands including configuration, ClickUp API client, task cache, and model servers. It manages lazy loading of these components and offers methods to access them. The cache is refreshed automatically when needed, and commands can force a refresh with the --refresh flag. Task selection supports both direct ID lookup and interactive picking from cached tasks.

Commands are structured around core functionality: tasks, timers, notes, comments, and search. The tasks command offers listing, creating, and managing tasks. Timers track time spent on tasks. Notes handles markdown files in a configured directory. Comments manages task comments. Search uses local embedding models to find relevant content across notes and comments. The system uses XDG directories for configuration and cache storage.
<!-- docgen:end id=cmd:overview -->

<!-- docgen:begin id=cmd:reference scope=cmd hash=435fa57710b5 -->
## Reference

### `app.go`

- **`ErrNotInitialised`** (variable): ErrNotInitialised is returned when there is no token or workspace configured.
- **`App`** (struct): App lazily loads the config, API client and task cache shared by every command.
- **`App.Config`** (method)
- **`App.Client`** (method)
- **`App.Cache`** (method): Cache returns the task cache, refreshing it first when stale or when --refresh was given.
- **`App.CachedOnly`** (method): CachedOnly returns whatever is cached without touching the network (for completion).
- **`App.RefreshCache`** (method): RefreshCache re-downloads the open tasks of the configured spaces.
- **`App.UserID`** (method): UserID returns the token owner's ClickUp user ID, looking it up once and saving it.
- **`TaskScope`** (struct): TaskScope says which cached tasks a picker offers.
- **`App.SelectTask`** (method): SelectTask returns the task with this ID (cache first, then the API) or lets the user pick one from the cache.
- **`App.ClosedTasks`** (method): ClosedTasks fetches closed tasks in the cached spaces, which the cache leaves out: the token owner's, or everyone's when everyone is set.
- **`App.WithSpaceName`** (method): WithSpaceName fills in a task's space name, which task payloads leave out.
- **`App.NotesRoot`** (method): NotesRoot returns the notes folder, or an error when notes are disabled.
- **`App.ModelServer`** (method): ModelServer returns a client for the local chat model server (llm_url, llm_model, llm_threads, llm_context_tokens), starting it when it is down (llm_start, or the llm_container docker container). The returned stop function stops a server this call started, and is a no-op otherwise.
- **`App.EmbedServer`** (method): EmbedServer returns a client for the embedding server: llm_embed_url (started with llm_embed_start) when set, else the chat server. Its model is not resolved, since the embedding model is named per request.

### `root.go`

- **`Execute`** (function): Execute runs clctl and returns the process exit code.

### `summarize.go`

- **`summarizer.Close`** (method): Close stops the model container if this run started it.
- **`summarizer.Summarize`** (method): Summarize summarises each comment in the window (cached per comment), then merges those into one briefing for the task.
<!-- docgen:end id=cmd:reference -->
