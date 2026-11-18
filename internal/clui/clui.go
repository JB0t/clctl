// Package clui presents one task in the terminal the way a task tracker's web page does:
// a header with its key, status, priority, assignees, due date, tags and timer, then its
// description, subtasks, time logged and comments, with keys to change status or
// priority, comment, edit the description, start or stop a timer and move between
// subtasks and their parent.
//
// It works with any tracker.Backend; `clctl tasks open` uses it for the local store,
// whose tasks have no web page, and for any backend with --tui. Timers go through
// clctl's own timer logic (Actions), so a task timed here shows up in `clctl timers`.
package clui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/JB0t/clctl/internal/markdown"
	"github.com/JB0t/clctl/internal/tracker"
)

// TimerState says what clctl's timers are doing, as far as one task is concerned.
type TimerState struct {
	Running bool      // a timer runs on this task
	Since   time.Time // when it started
	Local   bool      // it is one of clctl's local timers, not the backend's own
	Other   string    // what the backend's own timer runs on instead; "" when it is free
}

// Actions connect the view to the backend and to clctl's timers and cache.
type Actions struct {
	Backend tracker.Backend
	// Timer reports the timers for a task.
	Timer func(tracker.Task) TimerState
	// StartTimer starts a timer on the task; keepOther keeps the backend's timer running
	// on something else and times this task with a local timer instead.
	StartTimer func(task tracker.Task, keepOther bool) (string, error)
	// StopTimer stops every timer on the task, logging the time.
	StopTimer func(tracker.Task) (string, error)
	// Saved is told about every task the view changes (to update the cache).
	Saved func(tracker.Task)
	// OpenURL opens a web page; nil hides the action.
	OpenURL func(string) error
	// Editor is the command that edits the description ("nvim", "code -w", ...).
	Editor string
	// Style is the Markdown style: "dark", "light" or "notty"; "" asks the terminal.
	Style string
	Now   func() time.Time
}

// Run shows the task with this ID until the user quits.
func Run(actions Actions, taskID string) error {
	if actions.Style == "" {
		actions.Style = "dark"
		switch {
		case os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb":
			actions.Style = "notty"
		case !lipgloss.HasDarkBackground():
			actions.Style = "light"
		}
	}
	model := New(actions, taskID)
	_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err == nil && model.fatal != nil {
		err = *model.fatal
	}
	return err
}

// history is how far back the time-logged section looks.
const history = 90 * 24 * time.Hour

type mode int

const (
	modeView mode = iota
	modePick
	modeComment
	modeHelp
)

// picker is a small list overlay.
type picker struct {
	title  string
	rows   []string
	cursor int
	choose func(index int) tea.Cmd
}

// Model is the task view.
type Model struct {
	actions Actions
	id      string   // the task shown
	back    []string // tasks to go back to (Backspace)

	loaded   bool
	task     tracker.Task
	subtasks []tracker.Task
	comments []tracker.Comment // oldest first
	entries  []tracker.TimeEntry
	timer    TimerState
	warnings []string

	width, height int
	viewport      viewport.Model
	input         textarea.Model
	mode          mode
	picker        picker
	message       string
	busy          bool
	renderer      *glamour.TermRenderer
	fatal         *error // a load error to report after quitting
}

// New returns the view of a task, before it has loaded.
func New(actions Actions, taskID string) *Model {
	if actions.Now == nil {
		actions.Now = time.Now
	}
	if actions.Style == "" {
		actions.Style = "notty"
	}
	vp := viewport.New(80, 20)
	// u and d are this view's keys (parent, ...); half pages stay on Ctrl+U and Ctrl+D.
	vp.KeyMap.HalfPageUp = key.NewBinding(key.WithKeys("ctrl+u"))
	vp.KeyMap.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"))
	vp.MouseWheelEnabled = true
	input := textarea.New()
	input.Placeholder = "Comment (Markdown). Ctrl+S posts, Esc cancels."
	input.ShowLineNumbers = false
	input.SetHeight(8)
	input.CharLimit = 0
	return &Model{actions: actions, id: taskID, viewport: vp, input: input}
}

// Messages.

type loadedMsg struct {
	task     tracker.Task
	subtasks []tracker.Task
	comments []tracker.Comment
	entries  []tracker.TimeEntry
	timer    TimerState
	warnings []string
	err      error
}

type resultMsg struct {
	text   string
	err    error
	reload bool
}

type editedMsg struct {
	original, text string
	err            error
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Init() tea.Cmd { return tea.Batch(m.load(), tick()) }

// load fetches the task and everything shown with it.
func (m *Model) load() tea.Cmd {
	id, actions := m.id, m.actions
	return func() tea.Msg {
		b := actions.Backend
		task, err := b.TaskWithMarkdown(id)
		if err != nil {
			return loadedMsg{err: err}
		}
		msg := loadedMsg{task: task}
		warn := func(what string, err error) {
			if err != nil && !errors.Is(err, tracker.ErrUnsupported) {
				msg.warnings = append(msg.warnings, what+": "+err.Error())
			}
		}
		msg.subtasks, err = b.Subtasks(task.ID)
		warn("subtasks", err)
		comments, err := b.Comments(task.Ref())
		warn("comments", err)
		for index := len(comments) - 1; index >= 0; index-- { // oldest first, like a conversation
			msg.comments = append(msg.comments, comments[index])
		}
		now := actions.Now()
		entries, err := b.TimeEntries(now.Add(-history), now)
		warn("time entries", err)
		for _, entry := range entries {
			if entry.Task != nil && (entry.Task.ID == task.ID || strings.EqualFold(entry.Task.Ref(), task.Ref())) && !entry.Running() {
				msg.entries = append(msg.entries, entry)
			}
		}
		if actions.Timer != nil {
			msg.timer = actions.Timer(task)
		}
		return msg
	}
}

// do runs a change in the background and reports its result.
func (m *Model) do(change func() (string, error)) tea.Cmd {
	m.busy = true
	return func() tea.Msg {
		text, err := change()
		return resultMsg{text: text, err: err, reload: err == nil}
	}
}

func (m *Model) saved(task tracker.Task) {
	if m.actions.Saved != nil {
		m.actions.Saved(task)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.renderer = nil
		m.layout()
		return m, nil
	case loadedMsg:
		m.busy = false
		if msg.err != nil {
			if !m.loaded {
				err := fmt.Errorf("task %s: %w", m.id, msg.err)
				m.fatal = &err
				return m, tea.Quit
			}
			m.message = "Error: " + msg.err.Error()
			return m, nil
		}
		m.loaded = true
		m.task, m.subtasks, m.comments, m.entries, m.timer, m.warnings = msg.task, msg.subtasks, msg.comments, msg.entries, msg.timer, msg.warnings
		m.refreshContent()
		return m, nil
	case resultMsg:
		m.busy = false
		if msg.err != nil {
			m.message = "Error: " + msg.err.Error()
			return m, nil
		}
		m.message = msg.text
		if msg.reload {
			return m, m.load()
		}
		return m, nil
	case editedMsg:
		if msg.err != nil {
			m.message = "Error: " + msg.err.Error()
			return m, nil
		}
		if strings.TrimSpace(msg.text) == strings.TrimSpace(msg.original) {
			m.message = "Description unchanged."
			return m, nil
		}
		task, text := m.task, msg.text
		return m, m.do(func() (string, error) {
			updated, err := m.actions.Backend.UpdateTask(task.Ref(), tracker.TaskUpdate{MarkdownContent: &text})
			if err != nil {
				return "", err
			}
			m.saved(updated)
			return "Description saved.", nil
		})
	case tickMsg:
		if m.loaded && m.actions.Timer != nil {
			m.timer = m.actions.Timer(m.task)
		}
		return m, tick()
	case tea.KeyMsg:
		return m.key(msg)
	case tea.MouseMsg:
		if m.mode == modeView {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modePick:
		return m.pickKey(msg)
	case modeComment:
		return m.commentKey(msg)
	case modeHelp:
		m.mode = modeView
		return m, nil
	}
	if !m.loaded || m.busy {
		if msg.String() == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
	case "r":
		m.message = "Refreshing..."
		return m, m.load()
	case "s":
		return m, m.pickStatus()
	case "p":
		m.pickPriority()
	case "t":
		return m, m.toggleTimer()
	case "c":
		m.mode = modeComment
		m.input.Reset()
		m.layout()
		return m, m.input.Focus()
	case "e":
		return m, m.editDescription()
	case "o":
		m.pickSubtask()
	case "u":
		if m.task.Parent == "" {
			m.message = m.task.Ref() + " has no parent task."
			return m, nil
		}
		return m, m.open(m.task.Parent)
	case "backspace", "esc":
		if len(m.back) == 0 {
			if msg.String() == "esc" {
				return m, tea.Quit
			}
			return m, nil
		}
		m.id, m.back = m.back[len(m.back)-1], m.back[:len(m.back)-1]
		m.loaded = true
		m.message = ""
		return m, m.load()
	case "w":
		if m.actions.OpenURL == nil || m.task.URL == "" {
			m.message = m.task.Ref() + " has no web page."
			return m, nil
		}
		if err := m.actions.OpenURL(m.task.URL); err != nil {
			m.message = "Error: " + err.Error()
		}
	default:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

// open shows another task, remembering this one for Backspace.
func (m *Model) open(id string) tea.Cmd {
	m.back = append(m.back, m.task.Ref())
	m.id = id
	m.message = ""
	m.viewport.GotoTop()
	return m.load()
}

func (m *Model) showPicker(title string, rows []string, cursor int, choose func(int) tea.Cmd) {
	m.picker = picker{title: title, rows: rows, cursor: max(cursor, 0), choose: choose}
	m.mode = modePick
}

func (m *Model) pickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode = modeView
	case "up", "k":
		if m.picker.cursor > 0 {
			m.picker.cursor--
		}
	case "down", "j":
		if m.picker.cursor < len(m.picker.rows)-1 {
			m.picker.cursor++
		}
	case "enter":
		m.mode = modeView
		return m, m.picker.choose(m.picker.cursor)
	}
	return m, nil
}

func (m *Model) pickStatus() tea.Cmd {
	task := m.task
	list, err := m.actions.Backend.List(task.List.ID)
	if err != nil {
		m.message = "Error: " + err.Error()
		return nil
	}
	var rows []string
	cursor := 0
	for index, status := range list.Statuses {
		rows = append(rows, status.Status)
		if strings.EqualFold(status.Status, task.Status.Status) {
			cursor = index
			rows[index] += "  (current)"
		}
	}
	m.showPicker(task.Ref()+" status", rows, cursor, func(index int) tea.Cmd {
		status := list.Statuses[index]
		if strings.EqualFold(status.Status, task.Status.Status) {
			return nil
		}
		timing := m.timer.Running
		return m.do(func() (string, error) {
			updated, err := m.actions.Backend.SetStatus(task.Ref(), status.Status)
			if err != nil {
				return "", err
			}
			m.saved(updated)
			text := fmt.Sprintf("%s -> %s", task.Ref(), status.Status)
			if status.Done() && timing && m.actions.StopTimer != nil {
				stopped, err := m.actions.StopTimer(task)
				if err != nil {
					return "", err
				}
				text += ". " + stopped
			}
			return text, nil
		})
	})
	return nil
}

func (m *Model) pickPriority() {
	task := m.task
	rows := append(slices.Clone(tracker.PriorityNames), "none")
	cursor := len(rows) - 1
	for index, name := range rows {
		if name == task.PriorityName() {
			cursor = index
		}
	}
	rows[cursor] += "  (current)"
	m.showPicker(task.Ref()+" priority", rows, cursor, func(index int) tea.Cmd {
		value := index + 1
		if index == len(tracker.PriorityNames) {
			value = 0
		}
		return m.do(func() (string, error) {
			updated, err := m.actions.Backend.UpdateTask(task.Ref(), tracker.TaskUpdate{Priority: &value})
			if err != nil {
				return "", err
			}
			m.saved(updated)
			return fmt.Sprintf("%s priority: %s", task.Ref(), strings.Fields(rows[index])[0]), nil
		})
	})
}

func (m *Model) pickSubtask() {
	if len(m.subtasks) == 0 {
		m.message = m.task.Ref() + " has no subtasks."
		return
	}
	rows := make([]string, len(m.subtasks))
	for index, sub := range m.subtasks {
		rows[index] = fmt.Sprintf("%s %-10s %s", statusGlyph(sub.Status), sub.Ref(), sub.Name)
	}
	m.showPicker("Open subtask", rows, 0, func(index int) tea.Cmd { return m.open(m.subtasks[index].Ref()) })
}

func (m *Model) toggleTimer() tea.Cmd {
	task := m.task
	switch {
	case m.actions.StartTimer == nil || m.actions.StopTimer == nil:
		m.message = "Timers are not available here."
		return nil
	case m.timer.Running:
		return m.do(func() (string, error) { return m.actions.StopTimer(task) })
	case m.timer.Other != "":
		m.showPicker("A timer is running on "+m.timer.Other, []string{
			"Stop it, then start the timer on " + task.Ref(),
			"Keep it running and time " + task.Ref() + " too (a local timer)",
			"Cancel",
		}, 0, func(index int) tea.Cmd {
			if index == 2 {
				return nil
			}
			return m.do(func() (string, error) { return m.actions.StartTimer(task, index == 1) })
		})
		return nil
	}
	return m.do(func() (string, error) { return m.actions.StartTimer(task, false) })
}

func (m *Model) commentKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeView
		m.input.Blur()
		m.layout()
		return m, nil
	case "ctrl+s":
		text := strings.TrimSpace(m.input.Value())
		m.mode = modeView
		m.input.Blur()
		m.layout()
		if text == "" {
			m.message = "Empty comment not posted."
			return m, nil
		}
		task := m.task
		return m, m.do(func() (string, error) {
			if _, err := m.actions.Backend.PostComment(task.Ref(), markdown.CommentOps(text)); err != nil {
				return "", err
			}
			return "Comment added to " + task.Ref() + ".", nil
		})
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// editDescription opens the description in the editor, suspending the view meanwhile.
func (m *Model) editDescription() tea.Cmd {
	original := m.task.MarkdownDescription
	if original == "" {
		original = m.task.Description
	}
	dir, err := os.MkdirTemp("", "clctl-edit-")
	if err != nil {
		m.message = "Error: " + err.Error()
		return nil
	}
	path := filepath.Join(dir, m.task.Ref()+"-description.md")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		m.message = "Error: " + err.Error()
		return nil
	}
	editor := m.actions.Editor
	if editor == "" {
		editor = "vi"
	}
	command := exec.Command("sh", "-c", editor+" '"+strings.ReplaceAll(path, "'", `'\''`)+"'")
	return tea.ExecProcess(command, func(err error) tea.Msg {
		defer os.RemoveAll(dir)
		if err != nil {
			return editedMsg{err: fmt.Errorf("editor: %w", err)}
		}
		data, err := os.ReadFile(path)
		return editedMsg{original: original, text: string(data), err: err}
	})
}

// Layout and rendering.

const (
	headerLines = 3
	footerLines = 3
)

func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	m.viewport.Width = m.width
	m.viewport.Height = max(m.height-headerLines-footerLines, 3)
	m.input.SetWidth(max(m.width-2, 10))
	m.refreshContent()
}

func (m *Model) markdownRenderer() *glamour.TermRenderer {
	if m.renderer == nil {
		renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle(m.actions.Style), glamour.WithWordWrap(max(m.width-6, 20)))
		if err == nil {
			m.renderer = renderer
		}
	}
	return m.renderer
}

// renderMarkdown renders Markdown for the body, indented under its section title.
func (m *Model) renderMarkdown(text string) string {
	if renderer := m.markdownRenderer(); renderer != nil {
		if rendered, err := renderer.Render(text); err == nil {
			return strings.Trim(rendered, "\n")
		}
	}
	return indent(text, "  ")
}

var (
	accent   = lipgloss.Color("39")
	dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	bold     = lipgloss.NewStyle().Bold(true)
	keyStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	title    = lipgloss.NewStyle().Bold(true).Foreground(accent).MarginTop(1)
	pill     = lipgloss.NewStyle().Padding(0, 1)
	selected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(accent)
)

var statusColors = map[string]lipgloss.Color{"open": "240", "custom": "33", "done": "35", "closed": "34"}

var priorityColors = map[string]lipgloss.Color{"urgent": "196", "high": "214", "normal": "39", "low": "244"}

func statusGlyph(status tracker.Status) string {
	switch {
	case status.Done():
		return "✓"
	case status.Type == "custom":
		return "◐"
	}
	return "○"
}

func statusPill(status tracker.Status) string {
	color, ok := statusColors[status.Type]
	if !ok {
		color = "240"
	}
	return pill.Foreground(lipgloss.Color("15")).Background(color).Render(strings.ToUpper(status.Status))
}

func formatDuration(duration time.Duration) string {
	minutes := int(duration.Round(time.Minute).Minutes())
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}

func location(task tracker.Task) string {
	parts := []string{task.Space.Name}
	if task.Folder.Name != "" && !task.Folder.Hidden {
		parts = append(parts, task.Folder.Name)
	}
	return strings.Join(append(parts, task.List.Name), " / ")
}

func (m *Model) header() string {
	task := m.task
	right := dim.Render(location(task))
	name := task.Name
	room := m.width - lipgloss.Width(task.Ref()) - lipgloss.Width(right) - 5
	if room > 0 && lipgloss.Width(name) > room {
		name = string([]rune(name)[:max(room-1, 1)]) + "…"
	}
	left := " " + keyStyle.Render(task.Ref()) + "  " + bold.Render(name)
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(right)-1, 1)
	first := left + strings.Repeat(" ", gap) + right

	fields := []string{statusPill(task.Status)}
	if m.timer.Running { // next to the status, so a narrow terminal still shows it
		label := "⏱ " + formatDuration(m.actions.Now().Sub(m.timer.Since))
		if m.timer.Local {
			label += " (local)"
		}
		fields = append(fields, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")).Render(label))
	}
	if priority := task.PriorityName(); priority != "" {
		fields = append(fields, lipgloss.NewStyle().Foreground(priorityColors[priority]).Render("▲ "+priority))
	} else {
		fields = append(fields, dim.Render("no priority"))
	}
	var people []string
	for _, assignee := range task.Assignees {
		people = append(people, assignee.DisplayName())
	}
	if len(people) > 0 {
		fields = append(fields, "@"+strings.Join(people, ", @"))
	} else {
		fields = append(fields, dim.Render("unassigned"))
	}
	if task.DueDate != 0 {
		due := time.UnixMilli(int64(task.DueDate))
		style := lipgloss.NewStyle()
		if due.Before(m.actions.Now()) && !task.Status.Done() {
			style = style.Foreground(lipgloss.Color("196"))
		}
		fields = append(fields, style.Render("due "+due.Format("Mon 2 Jan")))
	}
	if tags := task.TagNames(); len(tags) > 0 {
		fields = append(fields, lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Render("#"+strings.Join(tags, " #")))
	}
	clip := lipgloss.NewStyle().MaxWidth(max(m.width, 1))
	second := " " + strings.Join(fields, "   ")
	return clip.Render(first) + "\n" + clip.Render(second) + "\n" + dim.Render(strings.Repeat("─", max(m.width, 1)))
}

func (m *Model) body() string {
	var b strings.Builder
	task := m.task
	b.WriteString(title.Render(" Description") + "\n")
	description := task.MarkdownDescription
	if description == "" {
		description = task.Description
	}
	if strings.TrimSpace(description) == "" {
		b.WriteString(dim.Render("   No description. Press e to write one.") + "\n")
	} else {
		b.WriteString(m.renderMarkdown(description) + "\n")
	}

	if len(m.subtasks) > 0 {
		b.WriteString(title.Render(fmt.Sprintf(" Subtasks (%d)", len(m.subtasks))) + "\n")
		for _, sub := range m.subtasks {
			color := statusColors[sub.Status.Type]
			b.WriteString(fmt.Sprintf("   %s %s  %s  %s\n", lipgloss.NewStyle().Foreground(color).Render(statusGlyph(sub.Status)),
				keyStyle.Render(fmt.Sprintf("%-10s", sub.Ref())), sub.Name, dim.Render("["+sub.Status.Status+"]")))
		}
	}

	var total time.Duration
	for _, entry := range m.entries {
		total += entry.EndTime().Sub(entry.StartTime())
	}
	b.WriteString(title.Render(fmt.Sprintf(" Time logged · last %d days · %s", int(history.Hours()/24), formatDuration(total))) + "\n")
	if len(m.entries) == 0 {
		b.WriteString(dim.Render("   Nothing logged. Press t to start a timer.") + "\n")
	}
	shown := m.entries
	if len(shown) > 10 {
		shown = shown[len(shown)-10:]
		b.WriteString(dim.Render(fmt.Sprintf("   ... %d earlier entries", len(m.entries)-10)) + "\n")
	}
	for _, entry := range shown {
		start, end := entry.StartTime(), entry.EndTime()
		line := fmt.Sprintf("   %s  %s-%s  %8s", start.Format("Mon 02 Jan"), start.Format("15:04"), end.Format("15:04"), formatDuration(end.Sub(start)))
		if entry.Description != "" {
			line += "  " + entry.Description
		}
		if tags := entry.TagNames(); len(tags) > 0 {
			line += "  " + dim.Render("#"+strings.Join(tags, " #"))
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(title.Render(fmt.Sprintf(" Comments (%d)", len(m.comments))) + "\n")
	if len(m.comments) == 0 {
		b.WriteString(dim.Render("   No comments. Press c to add one.") + "\n")
	}
	for _, comment := range m.comments {
		when := ""
		if millis, err := strconv.ParseInt(string(comment.Date), 10, 64); err == nil {
			when = time.UnixMilli(millis).Format("Mon 2 Jan 15:04")
		}
		b.WriteString("\n   " + bold.Render(comment.User.DisplayName()) + dim.Render("  "+when) + "\n")
		text := markdown.ToMarkdown(comment.Comment)
		if strings.TrimSpace(text) == "" {
			text = comment.CommentText
		}
		b.WriteString(m.renderMarkdown(text) + "\n")
	}
	for _, warning := range m.warnings {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(" ! "+warning) + "\n")
	}
	return b.String()
}

func (m *Model) refreshContent() {
	if !m.loaded || m.width == 0 {
		return
	}
	m.viewport.SetContent(m.body())
}

const helpText = `
  Keys

  s          change status            p    change priority
  t          start or stop a timer    c    add a comment (Ctrl+S posts)
  e          edit the description     o    open a subtask
  u          open the parent task     ⌫    back to the previous task
  w          open the web page        r    refresh
  ↑ ↓ j k    scroll                   PgUp PgDn Space b   page
  ?          this help                q Esc   quit

  Press any key to go back.`

func (m *Model) footer() string {
	keys := "s status · p priority · t timer · c comment · e edit · o subtask · u parent · r refresh · ? help · q quit"
	if len(m.back) > 0 {
		keys = "⌫ back · " + keys
	}
	if m.mode == modeComment {
		keys = "Ctrl+S post · Esc cancel"
	}
	if m.mode == modePick {
		keys = "↑↓ choose · Enter select · Esc cancel"
	}
	message := m.message
	if m.busy {
		message = "Working..."
	}
	style := dim
	if strings.HasPrefix(message, "Error:") {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	}
	return dim.Render(strings.Repeat("─", max(m.width, 1))) + "\n" +
		lipgloss.NewStyle().MaxWidth(m.width).Render(" "+dim.Render(keys)) + "\n" +
		lipgloss.NewStyle().MaxWidth(m.width).Render(" "+style.Render(message))
}

func (m *Model) pickerView() string {
	var b strings.Builder
	b.WriteString("\n " + bold.Render(m.picker.title) + "\n\n")
	for index, row := range m.picker.rows {
		if index == m.picker.cursor {
			b.WriteString("  " + selected.Render(" "+row+" ") + "\n")
		} else {
			b.WriteString("   " + row + "\n")
		}
	}
	return b.String()
}

func (m *Model) View() string {
	if m.width == 0 || !m.loaded {
		return "\n  Loading " + m.id + "..."
	}
	var body string
	height := max(m.height-headerLines-footerLines, 3)
	switch m.mode {
	case modePick:
		body = m.pickerView()
	case modeComment:
		body = "\n " + bold.Render("New comment on "+m.task.Ref()) + "\n\n" + m.input.View()
	case modeHelp:
		body = helpText
	default:
		body = m.viewport.View()
	}
	body = lipgloss.NewStyle().Height(height).MaxHeight(height).Render(body)
	return m.header() + "\n" + body + "\n" + m.footer()
}

func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}
