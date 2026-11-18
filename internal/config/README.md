# config

<!-- docgen:begin id=internal/config:overview scope=internal/config hash=a44a3a960629 -->
Package config reads and writes clctl's settings from an INI file. Settings live in a [clickup] section of the file. The file path defaults to `$XDG_CONFIG_HOME/clctl` or `~/.config/clctl`. The package supports environment variable overrides for some keys.

The Config type holds settings from the file and environment. It reads values with Get, which prefers environment overrides. Stored returns the raw file value. Set updates the file value. Bool parses boolean strings. APIURL returns the ClickUp API URL with a default. SpaceIDs returns a list of space IDs or nil. NotesDir and NotesEnabled handle the notes feature. Editor returns an editor command. CacheTTLMinutes returns cache TTL in minutes. Save writes the config file with mode 0600. ExpandHome replaces a leading ~ with the home directory.

The package loads config with Load, which reads the file and sets environment overrides. It preserves other sections of the file when saving. Keys are stored in lowercase. The package handles missing files gracefully. It supports legacy space_id key for backward compatibility.
<!-- docgen:end id=internal/config:overview -->

<!-- docgen:begin id=internal/config:reference scope=internal/config hash=a44a3a960629 -->
## Reference

### `config.go`

- **`KeyAPIToken`** (constant): Keys used in the config file.
- **`KeyAPIURL`** (constant): Keys used in the config file.
- **`KeyTeamID`** (constant): Keys used in the config file.
- **`KeyTeamName`** (constant): Keys used in the config file.
- **`KeyUserID`** (constant): Keys used in the config file.
- **`KeyUserName`** (constant): Keys used in the config file.
- **`KeySpaces`** (constant): Keys used in the config file.
- **`KeyLegacySpace`** (constant): Keys used in the config file.
- **`KeyNotesEnabled`** (constant): Keys used in the config file.
- **`KeyNotesDir`** (constant): Keys used in the config file.
- **`KeyEditor`** (constant): Keys used in the config file.
- **`KeyCacheTTL`** (constant): Keys used in the config file.
- **`KeyLLMURL`** (constant): Keys used in the config file.
- **`KeyLLMModel`** (constant): Keys used in the config file.
- **`KeyLLMContext`** (constant): Keys used in the config file.
- **`KeyLLMContainer`** (constant): Keys used in the config file.
- **`KeyLLMBackground`** (constant): Keys used in the config file.
- **`KeyLLMThreads`** (constant): Keys used in the config file.
- **`KeyLLMEmbedModel`** (constant): Keys used in the config file.
- **`KeyLLMStart`** (constant): Keys used in the config file.
- **`KeyLLMStop`** (constant): Keys used in the config file.
- **`KeyLLMEmbedURL`** (constant): Keys used in the config file.
- **`KeyLLMEmbedStart`** (constant): Keys used in the config file.
- **`KeyLLMEmbedStop`** (constant): Keys used in the config file.
- **`KeyMeetingPattern`** (constant): Keys used in the config file.
- **`DefaultAPIURL`** (constant): DefaultAPIURL is ClickUp's public API.
- **`Config`** (struct): Config holds the [clickup] section of the config file plus environment overrides.
- **`DefaultPath`** (function): DefaultPath returns $XDG_CONFIG_HOME/clctl or ~/.config/clctl.
- **`Load`** (function): Load reads the config file at path; a missing file yields an empty config.
- **`Config.Get`** (method): Get returns a value, preferring environment overrides.
- **`Config.Stored`** (method): Stored returns the value in the file, ignoring environment overrides.
- **`Config.Set`** (method): Set stores a value in the file section; an empty value removes the key.
- **`Config.Bool`** (method): Bool parses a boolean value; missing or invalid values are false.
- **`Config.APIURL`** (method): APIURL returns the configured API URL or ClickUp's default.
- **`Config.SpaceIDs`** (method): SpaceIDs returns the spaces clctl works with; nil means every space in the workspace. Configs written by the Python version only have a single `space_id`.
- **`Config.NotesDir`** (method): NotesDir returns the notes root with ~ expanded, or "" when notes are disabled.
- **`Config.NotesEnabled`** (method): NotesEnabled reports whether the dated notes layout features are turned on. Python-era configs have no flag, so fall back to whether $NOTES_DIR is set.
- **`Config.Editor`** (method): Editor returns the editor command: the configured one, else $VISUAL, $EDITOR, or the first of nvim/vim/vi/nano found on PATH.
- **`Config.CacheTTLMinutes`** (method): CacheTTLMinutes returns how old the task cache may get before it is refreshed.
- **`Config.Save`** (method): Save writes the [clickup] section (other sections are rewritten verbatim) with mode 0600.
- **`ExpandHome`** (function): ExpandHome replaces a leading ~ with the home directory.
<!-- docgen:end id=internal/config:reference -->
