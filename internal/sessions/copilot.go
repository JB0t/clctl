package sessions

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SourceCopilot marks entries read from GitHub Copilot Chat (VS Code) sessions. Claude
// Code entries carry no source.
const SourceCopilot = "copilot"

// VS Code keeps each Copilot Chat session as a file in one of these folders, under
// <Code>/User/workspaceStorage/<hash>/ (with the window's folder in workspace.json) or
// <Code>/User/globalStorage/ for windows without a folder.
var copilotDirs = []string{"chatSessions", "emptyWindowChatSessions"}

// IsCopilot reports whether path is a VS Code Copilot Chat session file: <id>.json (the
// whole session) or <id>.jsonl (a log of changes to it) in a chat sessions folder.
func IsCopilot(path string) bool {
	parent := filepath.Base(filepath.Dir(path))
	if parent != copilotDirs[0] && parent != copilotDirs[1] {
		return false
	}
	return strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".jsonl")
}

// inVSCodeStorage reports whether path is under a VS Code storage folder, where only
// Copilot session files are transcripts (extensions keep other *.jsonl files there).
func inVSCodeStorage(path string) bool {
	slashed := filepath.ToSlash(path)
	return strings.Contains(slashed, "/workspaceStorage/") || strings.Contains(slashed, "/globalStorage/")
}

// copilotState is the part of a Copilot Chat session that is kept. Everything else (the
// input box, thinking, tool output, edits' contents) is never decoded into memory.
type copilotState struct {
	SessionID    string           `json:"sessionId"`
	CreationDate int64            `json:"creationDate"`
	CustomTitle  string           `json:"customTitle"`
	Requests     []copilotRequest `json:"requests"`
}

// copilotRequest is one prompt and the reply to it.
type copilotRequest struct {
	Timestamp int64 `json:"timestamp"` // Unix milliseconds
	Message   struct {
		Text string `json:"text"`
	} `json:"message"`
	ElapsedMs int64         `json:"elapsedMs"`
	Result    copilotResult `json:"result"`
	Response  []copilotPart `json:"response"`
}

type copilotResult struct {
	Timings struct {
		TotalElapsed int64 `json:"totalElapsed"`
	} `json:"timings"`
}

// copilotPart is one item of a reply, reduced as it is decoded: markdown text, a
// reference shown inline, a tool call with its file paths, or an edit of a file. Other
// kinds keep only their kind, so the indices change-log entries use still line up.
type copilotPart struct {
	Kind    string
	Text    string   // markdown, or the name of an inline reference
	Tool    string   // tool ID
	Paths   []string // files the tool call or edit was given
	Command string   // a terminal command: only read for workspace repositories
}

func (p *copilotPart) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind              *string         `json:"kind"`
		Value             json.RawMessage `json:"value"`
		Content           json.RawMessage `json:"content"`
		InlineReference   json.RawMessage `json:"inlineReference"`
		Name              string          `json:"name"`
		ToolID            string          `json:"toolId"`
		InvocationMessage json.RawMessage `json:"invocationMessage"`
		ToolSpecificData  json.RawMessage `json:"toolSpecificData"`
		URI               json.RawMessage `json:"uri"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = copilotPart{}
	if raw.Kind != nil {
		p.Kind = *raw.Kind
	}
	switch p.Kind {
	case "", "markdownContent":
		p.Kind = "markdown"
		var text string
		if json.Unmarshal(raw.Value, &text) != nil {
			var content struct {
				Value string `json:"value"`
			}
			_ = json.Unmarshal(raw.Content, &content)
			text = content.Value
		}
		p.Text = text
	case "inlineReference":
		p.Text = referenceName(raw.Name, raw.InlineReference)
	case "toolInvocationSerialized":
		p.Tool = raw.ToolID
		var message struct {
			URIs map[string]json.RawMessage `json:"uris"`
		}
		if len(raw.InvocationMessage) > 0 && raw.InvocationMessage[0] == '{' {
			_ = json.Unmarshal(raw.InvocationMessage, &message)
		}
		for uri := range message.URIs {
			if file := uriPath(uri); file != "" {
				p.Paths = append(p.Paths, file)
			}
		}
		var specific struct {
			CommandLine struct {
				Original string `json:"original"`
			} `json:"commandLine"`
		}
		_ = json.Unmarshal(raw.ToolSpecificData, &specific)
		p.Command = specific.CommandLine.Original
	case "textEditGroup":
		if file := uriObjectPath(raw.URI); file != "" {
			p.Paths = []string{file}
		}
	}
	return nil
}

// referenceName names an inline reference: a symbol's name, else a file's base name.
func referenceName(name string, reference json.RawMessage) string {
	if name != "" {
		return name
	}
	var ref struct {
		Name     string          `json:"name"`
		Path     string          `json:"path"`
		URI      json.RawMessage `json:"uri"`
		Location struct {
			URI json.RawMessage `json:"uri"`
		} `json:"location"`
	}
	_ = json.Unmarshal(reference, &ref)
	switch {
	case ref.Name != "":
		return ref.Name
	case ref.Path != "":
		return path.Base(ref.Path)
	}
	for _, uri := range []json.RawMessage{ref.URI, ref.Location.URI} {
		if file := uriObjectPath(uri); file != "" {
			return path.Base(file)
		}
	}
	return ""
}

// uriObjectPath reads the path from a serialized VS Code URI ({"path": ...}) or a URI string.
func uriObjectPath(raw json.RawMessage) string {
	var uri struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &uri) == nil && uri.Path != "" {
		return uri.Path
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return uriPath(text)
	}
	return ""
}

// uriPath is the file path of a file:// or vscode-remote:// URI, without its #fragment.
func uriPath(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Path == "" {
		return ""
	}
	return parsed.Path
}

// change is one line of a session's change log. Kinds: 0 the whole state, 1 set the
// value at path k, 2 push values onto the array at k (cut to index i first when i is
// given), 3 delete the value at k.
type change struct {
	Kind  int               `json:"kind"`
	Path  []json.RawMessage `json:"k"`
	Value json.RawMessage   `json:"v"`
	Index *int              `json:"i"`
}

// replay applies a change log, keeping only the session fields copilotState holds.
// Lines that do not decode, and changes to anything else, are skipped.
func replay(r io.Reader) (*copilotState, error) {
	state := &copilotState{}
	err := eachLine(r, func(line []byte) {
		var c change
		if json.Unmarshal(line, &c) != nil {
			return
		}
		state.apply(c)
	})
	return state, err
}

func (s *copilotState) apply(c change) {
	if c.Kind == 0 {
		var initial copilotState
		if json.Unmarshal(c.Value, &initial) == nil {
			*s = initial
		}
		return
	}
	keys := make([]string, len(c.Path))
	for index, raw := range c.Path {
		var key any
		_ = json.Unmarshal(raw, &key)
		switch value := key.(type) {
		case string:
			keys[index] = value
		case float64:
			keys[index] = strconv.Itoa(int(value))
		}
	}
	switch {
	case len(keys) == 1:
		s.applyTop(c, keys[0])
	case len(keys) >= 2 && keys[0] == "requests":
		number, err := strconv.Atoi(keys[1])
		if err != nil || number < 0 || number >= len(s.Requests) {
			return
		}
		if len(keys) == 2 {
			if c.Kind == 1 {
				var request copilotRequest
				if json.Unmarshal(c.Value, &request) == nil {
					s.Requests[number] = request
				}
			} else if c.Kind == 3 {
				s.Requests = append(s.Requests[:number], s.Requests[number+1:]...)
			}
			return
		}
		if len(keys) == 3 {
			s.Requests[number].apply(c, keys[2])
		}
	}
}

func (s *copilotState) applyTop(c change, key string) {
	switch key {
	case "customTitle":
		s.CustomTitle = ""
		if c.Kind == 1 {
			_ = json.Unmarshal(c.Value, &s.CustomTitle)
		}
	case "sessionId":
		if c.Kind == 1 {
			_ = json.Unmarshal(c.Value, &s.SessionID)
		}
	case "requests":
		var requests []copilotRequest
		if c.Kind != 3 && json.Unmarshal(c.Value, &requests) != nil {
			return
		}
		switch c.Kind {
		case 1:
			s.Requests = requests
		case 2:
			s.Requests = append(cut(s.Requests, c.Index), requests...)
		case 3:
			s.Requests = nil
		}
	}
}

func (r *copilotRequest) apply(c change, key string) {
	if c.Kind == 3 {
		c.Value = []byte("null")
	}
	switch key {
	case "response":
		var parts []copilotPart
		if json.Unmarshal(c.Value, &parts) != nil {
			return
		}
		if c.Kind == 2 {
			r.Response = append(cut(r.Response, c.Index), parts...)
		} else {
			r.Response = parts
		}
	case "timestamp":
		_ = json.Unmarshal(c.Value, &r.Timestamp)
	case "message":
		_ = json.Unmarshal(c.Value, &r.Message)
	case "elapsedMs":
		_ = json.Unmarshal(c.Value, &r.ElapsedMs)
	case "result":
		r.Result = copilotResult{}
		_ = json.Unmarshal(c.Value, &r.Result)
	}
}

// cut shortens list to index when one is given and in range.
func cut[T any](list []T, index *int) []T {
	if index != nil && *index >= 0 && *index < len(list) {
		return list[:*index]
	}
	return list
}

// readCopilot reads a Copilot Chat session file: the whole state (*.json) or its change
// log (*.jsonl).
func readCopilot(name string, r io.Reader) (*copilotState, error) {
	if strings.HasSuffix(name, ".jsonl") {
		return replay(r)
	}
	state := &copilotState{}
	if err := json.NewDecoder(r).Decode(state); err != nil {
		return nil, err
	}
	return state, nil
}

// copilotFolder is the folder of the VS Code window a session belongs to, from
// workspace.json next to its chatSessions folder; "" for windows without a folder.
func copilotFolder(sessionPath string) string {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(sessionPath)), "workspace.json"))
	if err != nil {
		return ""
	}
	var workspace struct {
		Folder    string `json:"folder"`
		Workspace string `json:"workspace"`
	}
	if json.Unmarshal(data, &workspace) != nil {
		return ""
	}
	if workspace.Folder != "" {
		return uriPath(workspace.Folder)
	}
	return path.Dir(uriPath(workspace.Workspace))
}

// copilotLines turns a Copilot Chat session into transcript lines in Claude Code's
// shape (unreduced), so Reduce redacts and keeps them like any transcript: per request
// the prompt as a user message, and the reply's text and tool calls as an assistant
// message, plus the session's title.
func copilotLines(sessionPath string, r io.Reader) ([][]byte, error) {
	state, err := readCopilot(sessionPath, r)
	if err != nil {
		return nil, err
	}
	id := state.SessionID
	if id == "" {
		id = SessionName(sessionPath)
	}
	cwd := copilotFolder(sessionPath)
	var lines [][]byte
	add := func(value any) {
		line, _ := json.Marshal(value)
		lines = append(lines, line)
	}
	type block struct {
		Type  string         `json:"type"`
		Text  string         `json:"text,omitempty"`
		Name  string         `json:"name,omitempty"`
		Input map[string]any `json:"input,omitempty"`
	}
	type line struct {
		Type      string `json:"type"`
		Source    string `json:"source"`
		SessionID string `json:"sessionId"`
		Timestamp string `json:"timestamp,omitempty"`
		Cwd       string `json:"cwd,omitempty"`
		AITitle   string `json:"aiTitle,omitempty"`
		Message   any    `json:"message,omitempty"`
	}
	for _, request := range state.Requests {
		asked := time.UnixMilli(request.Timestamp).UTC()
		if request.Timestamp == 0 {
			asked = time.UnixMilli(state.CreationDate).UTC()
		}
		add(line{Type: "user", Source: SourceCopilot, SessionID: id, Timestamp: asked.Format(time.RFC3339Nano), Cwd: cwd,
			Message: map[string]any{"role": "user", "content": request.Message.Text}})

		var blocks []block
		var text strings.Builder
		for _, part := range request.Response {
			switch part.Kind {
			case "markdown":
				text.WriteString(part.Text)
			case "inlineReference":
				if part.Text != "" {
					text.WriteString("`" + part.Text + "`")
				}
			case "toolInvocationSerialized":
				input := map[string]any{}
				if len(part.Paths) == 1 {
					input["file_path"] = part.Paths[0]
				} else if len(part.Paths) > 1 {
					input["paths"] = part.Paths
				}
				if part.Command != "" {
					input["command"] = part.Command // Reduce keeps only the repositories it names
				}
				blocks = append(blocks, block{Type: "tool_use", Name: strings.TrimPrefix(part.Tool, "copilot_"), Input: input})
			case "textEditGroup":
				if len(part.Paths) > 0 {
					blocks = append(blocks, block{Type: "tool_use", Name: "Edit", Input: map[string]any{"file_path": part.Paths[0]}})
				}
			}
		}
		if reply := strings.TrimSpace(text.String()); reply != "" {
			blocks = append([]block{{Type: "text", Text: reply}}, blocks...)
		}
		if len(blocks) == 0 {
			continue
		}
		elapsed := request.ElapsedMs
		if elapsed == 0 {
			elapsed = request.Result.Timings.TotalElapsed
		}
		replied := asked.Add(time.Duration(elapsed) * time.Millisecond)
		add(line{Type: "assistant", Source: SourceCopilot, SessionID: id, Timestamp: replied.Format(time.RFC3339Nano), Cwd: cwd,
			Message: map[string]any{"role": "assistant", "content": blocks}})
	}
	if title := strings.TrimSpace(state.CustomTitle); title != "" {
		add(line{Type: "ai-title", Source: SourceCopilot, SessionID: id, AITitle: title})
	}
	return lines, nil
}

// eachTranscriptLine calls fn with every line of a transcript: a Claude Code transcript
// as it is, a Copilot Chat session converted by copilotLines.
func eachTranscriptLine(sessionPath string, r io.Reader, fn func([]byte)) error {
	if !IsCopilot(sessionPath) {
		return eachLine(r, fn)
	}
	lines, err := copilotLines(sessionPath, r)
	for _, line := range lines {
		fn(line)
	}
	return err
}

// ReducedName is the file name sessions export uploads a transcript as: <session-id>.jsonl.gz,
// with a copilot- prefix for Copilot Chat sessions.
func ReducedName(sessionPath string) string {
	name := SessionName(sessionPath) + ".jsonl.gz"
	if IsCopilot(sessionPath) {
		return "copilot-" + name
	}
	return name
}
