// Package sessions reads Claude Code session transcripts (*.jsonl, one JSON object per
// line) and GitHub Copilot Chat sessions from VS Code (see copilot.go) for a digest: only what the user and the assistant wrote, plus the names of the
// tools the assistant called and the file paths it gave them. Tool results, attachments,
// thinking and bookkeeping entries are dropped, since they hold file contents the
// repositories already cover. Everything kept is redacted (see Redact) before it can
// reach a model.
package sessions

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JB0t/clctl/internal/compress"
)

// Session is one transcript's conversation, redacted.
type Session struct {
	ID          string
	Source      string // "" for Claude Code, SourceCopilot for GitHub Copilot Chat
	Title       string // the last ai-title entry, "" when there is none
	First, Last time.Time
	Turns       []Turn
	// Repos are the workspace/<name> directories the session worked in or touched.
	Repos []string
}

// Turn is one user or assistant message.
type Turn struct {
	Role  string // user or assistant
	Time  time.Time
	Text  string    // what was written, redacted and compressed
	Tools []string  // "Read ~/workspace/x/main.go", "Bash (List files)"
	last  time.Time // the latest entry merged into this message
}

// maxLine bounds one transcript line; tool results can be megabytes.
const maxLine = 256 << 20

// ReadDir parses every transcript under dir (*.jsonl, and *.jsonl.gz as sessions export
// writes them, in subdirectories too, plus Copilot Chat sessions when dir is a VS Code
// user folder) and returns the sessions, newest (by last message) first.
// Several dirs are read as one; a session in more than one of them is merged.
func ReadDir(dirs ...string) ([]*Session, error) {
	var paths []string
	for _, dir := range dirs {
		found, err := transcripts(dir, true)
		if err != nil {
			return nil, err
		}
		paths = append(paths, found...)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no transcripts (*.jsonl, *.jsonl.gz or Copilot Chat sessions) in %s", strings.Join(dirs, ", "))
	}
	byID := map[string]*Session{}
	for _, path := range paths {
		if err := parseFile(path, byID); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return finish(byID), nil
}

// Transcripts returns the transcripts under dir, sorted: Claude Code *.jsonl files and
// Copilot Chat session files.
func Transcripts(dir string) ([]string, error) { return transcripts(dir, false) }

func transcripts(dir string, gzipped bool) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		switch {
		case entry.IsDir():
		case IsCopilot(path):
			// VS Code moved sessions from <id>.json to an <id>.jsonl log that starts with the
			// same state; read only the log when both are there.
			if _, err := os.Stat(path + "l"); err != nil || !strings.HasSuffix(path, ".json") {
				paths = append(paths, path)
			}
		case inVSCodeStorage(path):
			// extensions keep other *.jsonl files here; they are not transcripts
		case strings.HasSuffix(name, ".jsonl") || gzipped && strings.HasSuffix(name, ".jsonl.gz"):
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// SessionName is a transcript's file name without .jsonl, .jsonl.gz or (a Copilot Chat
// session) .json: its session ID.
func SessionName(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".gz")
	if trimmed := strings.TrimSuffix(name, ".jsonl"); trimmed != name {
		return trimmed
	}
	return strings.TrimSuffix(name, ".json")
}

func parseFile(path string, byID map[string]*Session) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var r io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		unzipped, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer unzipped.Close()
		r = unzipped
	}
	return parseLines(func(fn func([]byte)) error { return eachTranscriptLine(path, r, fn) }, SessionName(path), byID)
}

// Parse reads one transcript. Lines that are not JSON are skipped.
func Parse(r io.Reader, fallbackID string) ([]*Session, error) {
	byID := map[string]*Session{}
	if err := parse(r, fallbackID, byID); err != nil {
		return nil, err
	}
	return finish(byID), nil
}

// finish drops sessions without messages and sorts the rest, newest first.
func finish(byID map[string]*Session) []*Session {
	var sessions []*Session
	for _, session := range byID {
		if len(session.Turns) > 0 {
			sort.Strings(session.Repos)
			session.Repos = slices.Compact(session.Repos)
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Last.After(sessions[j].Last) })
	return sessions
}

// eachLine calls fn with every non-empty line of r, however long.
func eachLine(r io.Reader, fn func([]byte)) error {
	reader := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A long line (a large tool result): read the rest of it. ReadSlice's bytes are
			// only valid until the next read, so copy them first.
			start := append([]byte(nil), line...)
			rest, restErr := reader.ReadBytes('\n')
			line = append(start, rest...)
			err = restErr
			if len(line) > maxLine {
				line = nil
			}
		}
		if len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func parse(r io.Reader, fallbackID string, byID map[string]*Session) error {
	return parseLines(func(fn func([]byte)) error { return eachLine(r, fn) }, fallbackID, byID)
}

// parseLines adds the entries of the lines each yields.
func parseLines(each func(func([]byte)) error, fallbackID string, byID map[string]*Session) error {
	repos := map[*Session]map[string]bool{}
	err := each(func(line []byte) {
		if e, ok := Reduce(line); ok {
			addEntry(e, fallbackID, byID, repos)
		}
	})
	for session, names := range repos {
		for name := range names {
			session.Repos = append(session.Repos, name)
		}
	}
	return err
}

func addEntry(e Entry, fallbackID string, byID map[string]*Session, repos map[*Session]map[string]bool) {
	id := e.SessionID
	if id == "" {
		id = fallbackID
	}
	session := byID[id]
	if session == nil {
		session = &Session{ID: id}
		byID[id] = session
	}
	if repos[session] == nil {
		repos[session] = map[string]bool{}
	}
	if e.Source != "" {
		session.Source = e.Source
	}
	if e.Type == "ai-title" {
		session.Title = e.AITitle
		return
	}
	when, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	for _, name := range workspaceRepos(e.Cwd) {
		repos[session][name] = true
	}
	turn := Turn{Role: e.Type, Time: when}
	var texts []string
	for _, item := range e.Message.Content {
		switch item.Type {
		case "text":
			texts = append(texts, item.Text)
		case "tool_use":
			turn.Tools = append(turn.Tools, toolLine(item))
			for _, path := range item.paths() {
				for _, name := range workspaceRepos(path) {
					repos[session][name] = true
				}
			}
			for _, name := range stringList(item.Input[workspaceKey]) {
				repos[session][name] = true
			}
		}
	}
	text := strings.Join(texts, "\n\n")
	if text != "" {
		text, _ = compress.Text(text)
	}
	turn.Text = text
	if turn.Text == "" && len(turn.Tools) == 0 {
		return
	}
	if !when.IsZero() {
		if session.First.IsZero() || when.Before(session.First) {
			session.First = when
		}
		if when.After(session.Last) {
			session.Last = when
		}
	}
	// One assistant reply arrives as several entries (text, then each tool call): keep
	// them as one message while they follow each other closely.
	if count := len(session.Turns); count > 0 {
		previous := &session.Turns[count-1]
		if previous.Role == turn.Role && turn.Role == "assistant" && turn.Time.Sub(previous.last) < mergeGap {
			if turn.Text != "" {
				previous.Text = strings.TrimSpace(previous.Text + "\n\n" + turn.Text)
			}
			previous.Tools = append(previous.Tools, turn.Tools...)
			previous.last = turn.Time
			return
		}
	}
	turn.last = when
	session.Turns = append(session.Turns, turn)
}

// mergeGap is how far apart assistant entries can be and still count as one message.
const mergeGap = 2 * time.Minute

// toolLine names a tool call and the paths it was given. A Bash call keeps its short
// description.
func toolLine(item Block) string {
	var paths []string
	for _, path := range item.paths() {
		paths = append(paths, shortenHome(path))
	}
	line := item.Name
	if len(paths) > 0 {
		line += " " + strings.Join(paths, ", ")
	}
	if description, ok := item.Input["description"].(string); ok && description != "" {
		line += " (" + description + ")"
	}
	return line
}

var homeRE = regexp.MustCompile(`^/(?:home|Users)/[^/]+/`)

func shortenHome(path string) string { return homeRE.ReplaceAllString(path, "~/") }

var (
	// A slash before "workspace" keeps out Claude's own project folders
	// (~/.claude/projects/-home-x-workspace/memory).
	workspaceRE = regexp.MustCompile(`/workspace/([A-Za-z0-9_][A-Za-z0-9_.-]*)`)
	fileNameRE  = regexp.MustCompile(`\.[A-Za-z0-9]{1,5}$`)
	uuidRE      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-`)
)

// workspaceRepos returns the <name> of each workspace/<name> path in text, leaving out
// files directly in the workspace (SOUL.md) and per-session folders.
func workspaceRepos(text string) []string {
	var names []string
	for _, match := range workspaceRE.FindAllStringSubmatch(text, -1) {
		name := match[1]
		if fileNameRE.MatchString(name) || uuidRE.MatchString(name) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// Render writes turns as the model reads them: a short header per message, its text,
// and its tool calls on one line.
func Render(turn Turn) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[%s %s]\n", turn.Time.UTC().Format("2006-01-02 15:04"), turn.Role)
	if turn.Text != "" {
		builder.WriteString(turn.Text)
		builder.WriteString("\n")
	}
	if len(turn.Tools) > 0 {
		builder.WriteString("tools: " + strings.Join(turn.Tools, "; ") + "\n")
	}
	return builder.String()
}

// Chunks splits a session into pieces of at most budget characters, on message
// boundaries; a message longer than the budget is cut on line boundaries.
func Chunks(session *Session, budget int) []string {
	var chunks []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	for _, turn := range session.Turns {
		text := Render(turn) + "\n"
		if current.Len()+len(text) > budget {
			flush()
		}
		for len(text) > budget {
			cut := strings.LastIndex(text[:budget], "\n")
			if cut < budget/2 {
				cut = budget
				for cut > 0 && !utf8.RuneStart(text[cut]) {
					cut--
				}
			}
			current.WriteString(text[:cut])
			flush()
			text = text[cut:]
		}
		current.WriteString(text)
	}
	flush()
	return chunks
}
