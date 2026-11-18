package sessions

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Entry is a transcript line reduced to what leaves the machine or reaches a model: the
// user's and the assistant's text, tool names and the file paths given to tools, all
// redacted. It keeps the shape of Claude Code's own lines, so a reduced file (what
// sessions export uploads) reads like any transcript.
type Entry struct {
	Type      string   `json:"type"`             // user, assistant or ai-title
	Source    string   `json:"source,omitempty"` // "" for Claude Code, SourceCopilot for Copilot Chat
	SessionID string   `json:"sessionId,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	AITitle   string   `json:"aiTitle,omitempty"`
	Message   *Message `json:"message,omitempty"`
}

// Message is a reduced message: text and tool_use blocks only.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Block is a text block, or a tool call with only its paths, a Bash call's description
// and the workspace repositories its other input named.
type Block struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// rawEntry is the part of a transcript line Reduce reads.
type rawEntry struct {
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	SessionID string          `json:"sessionId"`
	Timestamp string          `json:"timestamp"`
	IsMeta    bool            `json:"isMeta"`
	Cwd       string          `json:"cwd"`
	AITitle   string          `json:"aiTitle"`
	Message   json.RawMessage `json:"message"`
}

// rawBlock is one item of a message's content list; tool_result bodies are never decoded.
type rawBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

const (
	// pathsKey holds a tool's list of paths; workspaceKey the workspace/<name>
	// repositories named in input that is not kept (a Bash command).
	pathsKey     = "paths"
	workspaceKey = "workspace_repos"
)

// pathKeys are tool input fields that hold one file path.
var pathKeys = []string{"file_path", "notebook_path", "path"}

// Reduce turns one transcript line into its kept, redacted form. It reports false for
// lines with nothing to keep: attachments, system and bookkeeping entries, tool results,
// thinking, harness-injected user messages, and lines that are not JSON. Reducing a
// reduced line gives the same line.
func Reduce(line []byte) (Entry, bool) {
	var raw rawEntry
	if json.Unmarshal(line, &raw) != nil {
		return Entry{}, false
	}
	switch raw.Type {
	case "ai-title":
		title := strings.TrimSpace(Redact(raw.AITitle))
		return Entry{Type: raw.Type, Source: raw.Source, SessionID: raw.SessionID, AITitle: title}, title != ""
	case "user", "assistant":
	default:
		return Entry{}, false // attachment, system, file-history-*, queue-operation, ...
	}
	if raw.IsMeta || len(raw.Message) == 0 {
		return Entry{}, false
	}
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw.Message, &message) != nil || len(message.Content) == 0 {
		return Entry{}, false
	}
	var blocks []rawBlock
	if message.Content[0] == '"' {
		var text string
		_ = json.Unmarshal(message.Content, &text)
		blocks = []rawBlock{{Type: "text", Text: text}}
	} else {
		_ = json.Unmarshal(message.Content, &blocks)
	}
	var kept []Block
	for _, item := range blocks {
		switch item.Type {
		case "text":
			if text := strings.TrimSpace(Redact(cleanUserText(item.Text))); text != "" {
				kept = append(kept, Block{Type: "text", Text: text})
			}
		case "tool_use":
			if raw.Type == "assistant" {
				kept = append(kept, Block{Type: "tool_use", Name: item.Name, Input: reduceInput(item.Name, item.Input)})
			}
		}
		// tool_result, thinking, image: dropped
	}
	if len(kept) == 0 {
		return Entry{}, false
	}
	return Entry{Type: raw.Type, Source: raw.Source, SessionID: raw.SessionID, Timestamp: raw.Timestamp, Cwd: Redact(raw.Cwd),
		Message: &Message{Role: raw.Type, Content: kept}}, true
}

// reduceInput keeps a tool call's paths, a Bash call's description and the workspace
// repositories named anywhere else in its input; file contents, edits and commands go.
func reduceInput(tool string, input map[string]any) map[string]any {
	kept := map[string]any{}
	repos := stringList(input[workspaceKey])
	for key, value := range input {
		text, ok := value.(string)
		switch {
		case !ok:
		case slices.Contains(pathKeys, key):
			if text != "" {
				kept[key] = Redact(text)
			}
		case key == "description" && tool == "Bash":
			if text = strings.TrimSpace(text); text != "" {
				kept[key] = Redact(text)
			}
		default:
			repos = append(repos, workspaceRepos(text)...)
		}
	}
	if paths := stringList(input[pathsKey]); len(paths) > 0 {
		for index := range paths {
			paths[index] = Redact(paths[index])
		}
		kept[pathsKey] = paths
	}
	if len(repos) > 0 {
		kept[workspaceKey] = uniqueSorted(repos)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// paths returns the file paths a reduced tool call was given.
func (b Block) paths() []string {
	var paths []string
	for _, key := range pathKeys {
		if path, ok := b.Input[key].(string); ok && path != "" {
			paths = append(paths, path)
		}
	}
	return append(paths, stringList(b.Input[pathsKey])...)
}

// stringList reads a list of strings from decoded JSON ([]any) or from Reduce ([]string).
func stringList(value any) []string {
	switch list := value.(type) {
	case []string:
		return append([]string(nil), list...)
	case []any:
		var out []string
		for _, item := range list {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

var (
	// Context the client adds to a prompt, which the person did not write.
	injectedRE = regexp.MustCompile(`(?s)<(system-reminder|local-command-caveat|local-command-stdout|local-command-stderr)>.*?</(?:system-reminder|local-command-caveat|local-command-stdout|local-command-stderr)>`)
	// Slash-command wrappers: their contents (/rename my-session) are kept, the tags dropped.
	commandTagRE = regexp.MustCompile(`</?(?:command-name|command-message|command-args)>`)
)

func cleanUserText(text string) string {
	text = injectedRE.ReplaceAllString(text, "")
	return commandTagRE.ReplaceAllString(text, " ")
}

// WriteReduced reads a Claude Code transcript and writes its reduced lines as gzipped
// JSON lines, returning how many it kept. Nothing else from the transcript reaches w.
func WriteReduced(r io.Reader, w io.Writer) (int, error) {
	return writeReduced(func(fn func([]byte)) error { return eachLine(r, fn) }, w)
}

// WriteReducedFile is WriteReduced for the transcript at path, which may also be a
// Copilot Chat session (read from r); its lines come out in Claude Code's shape.
func WriteReducedFile(path string, r io.Reader, w io.Writer) (int, error) {
	return writeReduced(func(fn func([]byte)) error { return eachTranscriptLine(path, r, fn) }, w)
}

func writeReduced(each func(func([]byte)) error, w io.Writer) (int, error) {
	zipped := gzip.NewWriter(w)
	encoder := json.NewEncoder(zipped)
	kept := 0
	var writeErr error
	err := each(func(line []byte) {
		if writeErr != nil {
			return
		}
		if e, ok := Reduce(line); ok {
			writeErr = encoder.Encode(e)
			kept++
		}
	})
	if err == nil {
		err = writeErr
	}
	if closeErr := zipped.Close(); err == nil {
		err = closeErr
	}
	return kept, err
}

func uniqueSorted(list []string) []string {
	slices.Sort(list)
	return slices.Compact(list)
}
