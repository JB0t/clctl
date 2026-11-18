# cache

<!-- docgen:begin id=internal/cache:overview scope=internal/cache hash=003ed0c99a67 -->
The `cache` package maintains a local JSON file copy of open tasks from a ClickUp workspace to support fast access for pickers, listings, and shell completions. It provides functionality to load, refresh, and save this cache, ensuring that task data is readily available without repeated API calls.

The main type `Cache` holds fetched tasks and associated metadata like the team ID and space information. It includes methods to check if the cache is stale, find specific tasks by ID, and update or add tasks while maintaining consistency. The cache is saved atomically to prevent corruption.

The package determines the cache file path using the `XDG_CACHE_HOME` environment variable or defaults to `~/.cache/clctl/tasks.json`. It supports loading an existing cache file or creating a new one, and can refresh the cache contents from the ClickUp API for specified spaces or all spaces.
<!-- docgen:end id=internal/cache:overview -->

<!-- docgen:begin id=internal/cache:reference scope=internal/cache hash=003ed0c99a67 -->
## Reference

### `cache.go`

- **`Cache`** (struct)
- **`DefaultPath`** (function): DefaultPath returns $XDG_CACHE_HOME/clctl/tasks.json or ~/.cache/clctl/tasks.json.
- **`Load`** (function): Load reads the cache; a missing or unreadable file yields an empty cache.
- **`Cache.Path`** (method): Path returns the cache file location.
- **`Cache.Stale`** (method): Stale reports whether the cache is empty, for another workspace, or older than ttl.
- **`Cache.Refresh`** (method): Refresh replaces the cache with the open tasks of the given spaces (nil = all spaces).
- **`Cache.SpaceName`** (method): SpaceName returns a cached space's name, or "" when unknown.
- **`Cache.Find`** (method): Find returns the cached task with this ID or custom ID.
- **`Cache.Upsert`** (method): Upsert adds or replaces a task (dropping it once closed) and saves the cache.
- **`Cache.Save`** (method): Save writes the cache atomically.
<!-- docgen:end id=internal/cache:reference -->
