package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JB0t/clctl/internal/cache"
	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/llm"
	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

// ErrNotInitialised is returned when no task backend is configured.
var ErrNotInitialised = errors.New("clctl is not set up yet; run 'clctl init'")

// App lazily loads the config, task backend and task cache shared by every command.
type App struct {
	ConfigPath   string
	CachePath    string
	ForceRefresh bool
	Quiet        bool // no progress output (for cron)
	IncludeDone  bool // pickers and listings also offer done and closed tasks (--include-done)
	InMenu       bool // running an action from a menu, which reads the terminal again afterwards

	config *config.Config
	client tracker.Backend
	cache  *cache.Cache
}

func (a *App) Config() (*config.Config, error) {
	if a.config == nil {
		path := a.ConfigPath
		if path == "" {
			path = config.DefaultPath()
		}
		loaded, err := config.Load(path)
		if err != nil {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		a.config = loaded
	}
	return a.config, nil
}

// Backend returns the configured task backend (local, ClickUp or Jira).
func (a *App) Backend() (tracker.Backend, error) {
	if a.client != nil {
		return a.client, nil
	}
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	if a.client, err = openBackend(cfg); err != nil {
		return nil, err
	}
	return a.client, nil
}

// Cache returns the task cache, refreshing it first when stale or when --refresh was given.
func (a *App) Cache() (*cache.Cache, error) {
	if a.cache != nil {
		return a.cache, nil
	}
	client, err := a.Backend()
	if err != nil {
		return nil, err
	}
	cfg, _ := a.Config()
	path := a.CachePath
	if path == "" {
		path = cache.DefaultPath()
	}
	a.cache = cache.Load(path)
	ttl := time.Duration(cfg.CacheTTLMinutes()) * time.Minute
	if a.ForceRefresh || a.cache.Stale(client.WorkspaceID(), ttl) {
		if err := a.RefreshCache(false); err != nil {
			return nil, err
		}
	}
	return a.cache, nil
}

// CachedOnly returns whatever is cached without touching the network (for completion).
func (a *App) CachedOnly() *cache.Cache {
	path := a.CachePath
	if path == "" {
		path = cache.DefaultPath()
	}
	return cache.Load(path)
}

// RefreshCache re-reads the open tasks of the configured spaces from the backend.
func (a *App) RefreshCache(verbose bool) error {
	client, err := a.Backend()
	if err != nil {
		return err
	}
	cfg, _ := a.Config()
	if a.cache == nil {
		path := a.CachePath
		if path == "" {
			path = cache.DefaultPath()
		}
		a.cache = cache.Load(path)
	}
	if a.Quiet {
		return a.cache.Refresh(client, cfg.SpaceIDs())
	}
	fmt.Fprint(os.Stderr, "Refreshing task cache... ")
	if err := a.cache.Refresh(client, cfg.SpaceIDs()); err != nil {
		fmt.Fprintln(os.Stderr)
		return err
	}
	fmt.Fprintf(os.Stderr, "%d tasks in %d space(s).\n", len(a.cache.Tasks), len(a.cache.Spaces))
	return nil
}

// UserID returns your user ID in the task backend. ClickUp's is looked up once and saved
// (user_id); the other backends answer locally (local) or once per run (Jira).
func (a *App) UserID() (string, error) {
	cfg, err := a.Config()
	if err != nil {
		return "", err
	}
	clickUp := cfg.Backend() == "clickup"
	if id := cfg.Get(config.KeyUserID); id != "" && clickUp {
		return id, nil
	}
	client, err := a.Backend()
	if err != nil {
		return "", err
	}
	me, err := client.Me()
	if err != nil {
		return "", err
	}
	if !clickUp {
		return string(me.ID), nil
	}
	cfg.Set(config.KeyUserID, string(me.ID))
	cfg.Set(config.KeyUserName, me.DisplayName())
	return string(me.ID), cfg.Save()
}

// TaskScope says which cached tasks a picker offers.
type TaskScope struct {
	Everyone    bool // not just tasks assigned to you
	IncludeDone bool // include done/closed statuses
	AllowNone   bool // the picker also offers "(no task)", returned as a task with no ID
}

// SelectTask returns the task with this ID (cache first, then the API) or lets the user
// pick one from the cache.
func (a *App) SelectTask(id string, scope TaskScope) (tracker.Task, error) {
	c, err := a.Cache()
	if err != nil {
		return tracker.Task{}, err
	}
	if id != "" {
		if task, ok := c.Find(id); ok {
			return task, nil
		}
		client, _ := a.Backend()
		task, err := client.Task(id)
		if err != nil {
			return tracker.Task{}, fmt.Errorf("task %s: %w", id, err)
		}
		return a.WithSpaceName(task), nil
	}
	candidates := c.Tasks
	if a.IncludeDone {
		closed, err := a.ClosedTasks(scope.Everyone)
		if err != nil {
			return tracker.Task{}, err
		}
		candidates = append(append([]tracker.Task(nil), candidates...), closed...)
	} else if !scope.IncludeDone {
		candidates = tasks.Open(candidates)
	}
	if !scope.Everyone {
		userID, err := a.UserID()
		if err != nil {
			return tracker.Task{}, err
		}
		candidates = tasks.AssignedTo(candidates, userID)
	}
	if len(candidates) == 0 && !scope.AllowNone {
		return tracker.Task{}, errors.New("no matching tasks in the cache (try --refresh)")
	}
	tasks.Sort(candidates)
	// A picker over everyone's tasks marks yours with @me in a first column (type @me
	// in fzf to keep only those).
	me := ""
	if scope.Everyone {
		me, _ = a.UserID()
	}
	var rows []string
	if scope.AllowNone {
		rows = append(rows, assignedMark(me, false)+"(no task: a description and labels say what it was)")
	}
	for _, task := range candidates {
		row := assignedMark(me, task.AssignedTo(me)) + tasks.Label(task)
		if task.Status.Done() {
			row += "  [" + task.Status.Status + "]"
		}
		rows = append(rows, row)
	}
	index, err := ui.Pick(rows, "Task >")
	if err != nil {
		return tracker.Task{}, err
	}
	if scope.AllowNone {
		if index == 0 {
			return tracker.Task{}, nil
		}
		index--
	}
	return candidates[index], nil
}

// assignedMark is the @me column of a picker over everyone's tasks: "@me " for a task
// assigned to the user, else blanks of the same width; "" when the picker has no column.
func assignedMark(me string, assigned bool) string {
	switch {
	case me == "":
		return ""
	case assigned:
		return "@me "
	}
	return "    "
}

// ClosedTasks fetches closed tasks in the cached spaces, which the cache leaves out:
// yours, or everyone's when everyone is set.
func (a *App) ClosedTasks(everyone bool) ([]tracker.Task, error) {
	c, err := a.Cache()
	if err != nil {
		return nil, err
	}
	client, err := a.Backend()
	if err != nil {
		return nil, err
	}
	filter := tracker.TaskFilter{IncludeClosed: true}
	for _, space := range c.Spaces {
		filter.SpaceIDs = append(filter.SpaceIDs, space.ID)
	}
	if !everyone {
		userID, err := a.UserID()
		if err != nil {
			return nil, err
		}
		filter.AssigneeIDs = []string{userID}
	}
	if !a.Quiet {
		fmt.Fprint(os.Stderr, "Fetching closed tasks... ")
	}
	found, err := client.Tasks(filter)
	if err != nil {
		if !a.Quiet {
			fmt.Fprintln(os.Stderr)
		}
		return nil, err
	}
	var closed []tracker.Task
	for _, task := range found {
		if task.Status.Type == "closed" {
			task.Space.Name = c.SpaceName(task.Space.ID)
			closed = append(closed, task)
		}
	}
	if !a.Quiet {
		fmt.Fprintf(os.Stderr, "%d.\n", len(closed))
	}
	return closed, nil
}

// WithSpaceName fills in a task's space name, which task payloads leave out.
func (a *App) WithSpaceName(task tracker.Task) tracker.Task {
	if task.Space.Name != "" {
		return task
	}
	if c, err := a.Cache(); err == nil {
		task.Space.Name = c.SpaceName(task.Space.ID)
	}
	if task.Space.Name == "" && task.Space.ID != "" {
		if client, err := a.Backend(); err == nil {
			if space, err := client.Space(task.Space.ID); err == nil {
				task.Space.Name = space.Name
			}
		}
	}
	return task
}

// NotesRoot returns the notes folder, or an error when notes are disabled.
func (a *App) NotesRoot() (string, error) {
	cfg, err := a.Config()
	if err != nil {
		return "", err
	}
	root := cfg.NotesDir()
	if root == "" {
		return "", errors.New("notes are disabled; enable them with 'clctl init'")
	}
	return root, nil
}

// modelLauncher says how to start a model server that is down: run start (and stop
// afterwards), or else docker start/stop container. Both empty means it is not started.
type modelLauncher struct {
	start, stop, container string
}

// chatLauncher and embedLauncher read llm_start/llm_stop (llm_embed_start/stop for a
// separate embedding server) and llm_container from the config.
func chatLauncher(cfg *config.Config) modelLauncher {
	return modelLauncher{cfg.Get(config.KeyLLMStart), cfg.Get(config.KeyLLMStop), cfg.Get(config.KeyLLMContainer)}
}

func embedLauncher(cfg *config.Config) modelLauncher {
	if cfg.Get(config.KeyLLMEmbedURL) == "" {
		return chatLauncher(cfg)
	}
	return modelLauncher{cfg.Get(config.KeyLLMEmbedStart), cfg.Get(config.KeyLLMEmbedStop), cfg.Get(config.KeyLLMContainer)}
}

// describe says what a summarising run does when the server is down, or "" for nothing.
func (l modelLauncher) describe() string {
	switch {
	case l.start != "":
		return "runs '" + l.start + "'"
	case l.container != "":
		return "starts docker container " + l.container
	}
	return ""
}

// ensure makes sure the client's server answers, starting it when it is down. The
// returned function stops a server this call started, and is a no-op otherwise.
func (l modelLauncher) ensure(client *llm.Client) (func(), error) {
	noop := func() {}
	_, err := client.Models()
	if err == nil {
		return noop, nil
	}
	var stop func()
	wait := 90 * time.Second
	switch {
	case l.start != "":
		fmt.Fprintf(os.Stderr, "Starting the model server (%s)...\n", l.start)
		start := exec.Command("sh", "-c", l.start)
		start.Stdout, start.Stderr = os.Stderr, os.Stderr
		if err := start.Run(); err != nil {
			return noop, fmt.Errorf("starting the model server with %q: %w", l.start, err)
		}
		stop = func() {
			if l.stop == "" {
				return
			}
			fmt.Fprintf(os.Stderr, "Stopping the model server (%s).\n", l.stop)
			stopCmd := exec.Command("sh", "-c", l.stop)
			stopCmd.Stdout, stopCmd.Stderr = os.Stderr, os.Stderr
			_ = stopCmd.Run()
		}
		// A large model can still be loading when the start command returns.
		wait = 20 * time.Minute
	case l.container != "":
		fmt.Fprintf(os.Stderr, "Starting model container %s...\n", l.container)
		if output, err := exec.Command("docker", "start", l.container).CombinedOutput(); err != nil {
			return noop, fmt.Errorf("docker start %s: %v: %s", l.container, err, strings.TrimSpace(string(output)))
		}
		stop = func() {
			fmt.Fprintf(os.Stderr, "Stopping model container %s.\n", l.container)
			_ = exec.Command("docker", "stop", l.container).Run()
		}
	default:
		return noop, err
	}
	// Stop the server again if the run is interrupted (Ctrl+C) while it is up.
	unregister := onInterrupt(stop)
	started := stop
	stop = func() {
		unregister()
		started()
	}
	for deadline := time.Now().Add(wait); ; time.Sleep(time.Second) {
		if _, err = client.Models(); err == nil {
			return stop, nil
		}
		if time.Now().After(deadline) {
			stop()
			return noop, fmt.Errorf("the model server was started but %s did not answer: %w", client.URL, err)
		}
	}
}

// ModelServer returns a client for the local chat model server (llm_url, llm_model,
// llm_threads, llm_context_tokens, llm_max_tokens), starting it when it is down (llm_start, or the
// llm_container docker container). The returned stop function stops a server this
// call started, and is a no-op otherwise.
func (a *App) ModelServer(model, url string) (*llm.Client, func(), error) {
	noop := func() {}
	cfg, err := a.Config()
	if err != nil {
		return nil, noop, err
	}
	if url == "" {
		url = cfg.Get(config.KeyLLMURL)
	}
	if model == "" {
		model = cfg.Get(config.KeyLLMModel)
	}
	contextTokens, _ := strconv.Atoi(cfg.Get(config.KeyLLMContext))
	client := llm.New(url, model, contextTokens)
	client.Threads, _ = strconv.Atoi(cfg.Get(config.KeyLLMThreads))
	client.MaxTokens, _ = strconv.Atoi(cfg.Get(config.KeyLLMMaxTokens))
	stop, err := chatLauncher(cfg).ensure(client)
	if err != nil {
		return nil, noop, err
	}
	if err := client.ResolveModel(); err != nil {
		stop()
		return nil, noop, err
	}
	return client, stop, nil
}

// EmbedServer returns a client for the embedding server: llm_embed_url (started with
// llm_embed_start) when set, else the chat server. Its model is not resolved, since
// the embedding model is named per request.
func (a *App) EmbedServer() (*llm.Client, func(), error) {
	noop := func() {}
	cfg, err := a.Config()
	if err != nil {
		return nil, noop, err
	}
	url := cfg.Get(config.KeyLLMEmbedURL)
	if url == "" {
		url = cfg.Get(config.KeyLLMURL)
	}
	client := llm.New(url, "", 0)
	stop, err := embedLauncher(cfg).ensure(client)
	if err != nil {
		return nil, noop, err
	}
	return client, stop, nil
}
