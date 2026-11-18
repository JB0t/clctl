// Package cache keeps a local copy of the task backend's open tasks for fast pickers,
// listings and shell completion.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/JB0t/clctl/internal/tracker"
)

type Cache struct {
	FetchedAt time.Time       `json:"fetched_at"`
	TeamID    string          `json:"team_id"` // the backend's WorkspaceID
	Spaces    []tracker.Space `json:"spaces"`
	Tasks     []tracker.Task  `json:"tasks"`

	path string
}

// DefaultPath returns $XDG_CACHE_HOME/clctl/tasks.json or ~/.cache/clctl/tasks.json.
func DefaultPath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "clctl", "tasks.json")
}

// Load reads the cache; a missing or unreadable file yields an empty cache.
func Load(path string) *Cache {
	c := &Cache{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, c)
	}
	c.path = path
	return c
}

// Path returns the cache file location.
func (c *Cache) Path() string { return c.path }

// Stale reports whether the cache is empty, for another workspace or backend, or older
// than ttl.
func (c *Cache) Stale(workspaceID string, ttl time.Duration) bool {
	return c.FetchedAt.IsZero() || c.TeamID != workspaceID || time.Since(c.FetchedAt) > ttl
}

// Refresh replaces the cache with the open tasks of the given spaces (nil = all spaces).
func (c *Cache) Refresh(client tracker.Backend, spaceIDs []string) error {
	spaces, err := client.Spaces()
	if err != nil {
		return err
	}
	if len(spaceIDs) > 0 {
		wanted := map[string]bool{}
		for _, id := range spaceIDs {
			wanted[id] = true
		}
		kept := spaces[:0]
		for _, space := range spaces {
			if wanted[space.ID] {
				kept = append(kept, space)
			}
		}
		spaces = kept
	}
	ids := make([]string, len(spaces))
	for index, space := range spaces {
		ids[index] = space.ID
	}
	tasks, err := client.Tasks(tracker.TaskFilter{SpaceIDs: ids})
	if err != nil {
		return err
	}
	c.FetchedAt, c.TeamID, c.Spaces, c.Tasks = time.Now(), client.WorkspaceID(), spaces, nil
	for _, task := range tasks {
		c.Tasks = append(c.Tasks, c.withSpaceName(task))
	}
	return c.Save()
}

// withSpaceName fills in the space name, which task payloads leave out.
func (c *Cache) withSpaceName(task tracker.Task) tracker.Task {
	if task.Space.Name == "" {
		task.Space.Name = c.SpaceName(task.Space.ID)
	}
	return task
}

// SpaceName returns a cached space's name, or "" when unknown.
func (c *Cache) SpaceName(id string) string {
	for _, space := range c.Spaces {
		if space.ID == id {
			return space.Name
		}
	}
	return ""
}

// Find returns the cached task with this ID or custom ID.
func (c *Cache) Find(id string) (tracker.Task, bool) {
	for _, task := range c.Tasks {
		if task.Matches(id) {
			return task, true
		}
	}
	return tracker.Task{}, false
}

// Upsert adds or replaces a task (dropping it once closed) and saves the cache.
func (c *Cache) Upsert(task tracker.Task) error {
	task = c.withSpaceName(task)
	kept := c.Tasks[:0]
	for _, cached := range c.Tasks {
		if cached.ID != task.ID {
			kept = append(kept, cached)
		}
	}
	c.Tasks = kept
	if task.Status.Type != "closed" {
		c.Tasks = append(c.Tasks, task)
	}
	return c.Save()
}

// Save writes the cache atomically.
func (c *Cache) Save() error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	temp := c.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, c.path)
}
