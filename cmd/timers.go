package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

func newTimersCmd(app *App) *cobra.Command {
	return newGroup(app, "timers", "Start, stop, log and edit time on tasks", nil,
		newStartTimerCmd(app), newStopTimerCmd(app), newPauseTimerCmd(app), newResumeTimerCmd(app), newSwitchTimerCmd(app),
		newListTimersCmd(app), newWatchTimersCmd(app), newLogTimeCmd(app), newEditTimeCmd(app), newInspectTimeCmd(app))
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}
	minutes := int(duration.Minutes())
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}

// timer is a running timer: the backend's own (one per person) or one clctl keeps locally.
type timer struct {
	*tracker.TimeEntry
	local bool
}

// work returns what the timer is for.
func (t timer) work() work { return work{Task: t.Task, Description: t.Description, Tags: t.TagNames()} }

func (t timer) ref() string { return t.work().name() }

// on reports whether the timer runs on the task with this ID or custom ID.
func (t timer) on(id string) bool {
	return t.Task != nil && (t.Task.ID == id || strings.EqualFold(t.Task.Ref(), id))
}

func (t timer) elapsed() time.Duration { return time.Since(t.StartTime()) }

// work is what a timer or time entry is for: a task, or no task (Task nil) with a
// description and labels (time-entry tags) saying what it was.
type work struct {
	Task        *tracker.TimeEntryTask
	Description string
	Tags        []string
}

func taskWork(task tracker.Task) work {
	return work{Task: &tracker.TimeEntryTask{ID: task.ID, CustomID: task.CustomID, Name: task.Name}}
}

// ref is the task ID the API takes, "" for no task.
func (w work) ref() string {
	if w.Task == nil {
		return ""
	}
	return w.Task.Ref()
}

// name is a short name: the task's ID, else the description, else the labels.
func (w work) name() string {
	switch {
	case w.Task != nil:
		return w.Task.Ref()
	case w.Description != "":
		return fmt.Sprintf("%q", w.Description)
	case len(w.Tags) > 0:
		return labelList(w.Tags)
	}
	return "(no task)"
}

// summary is the task's name, or the description, followed by any labels.
func (w work) summary() string {
	text := w.Description
	if w.Task != nil {
		text = w.Task.Name
	}
	if len(w.Tags) > 0 {
		text = strings.TrimSpace(text + "  " + labelList(w.Tags))
	}
	return text
}

func labelList(tags []string) string { return "#" + strings.Join(tags, " #") }

// workFields is how local and paused timers store their work.
type workFields struct {
	TaskID      string   `json:"task_id"` // "" for a timer without a task
	CustomID    string   `json:"custom_id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

func fieldsOf(w work) workFields {
	fields := workFields{Description: w.Description, Tags: w.Tags}
	if w.Task != nil {
		fields.TaskID, fields.CustomID, fields.Name = w.Task.ID, w.Task.CustomID, w.Task.Name
	}
	return fields
}

func (f workFields) work() work {
	w := work{Description: f.Description, Tags: f.Tags}
	if f.TaskID != "" {
		w.Task = &tracker.TimeEntryTask{ID: f.TaskID, CustomID: f.CustomID, Name: f.Name}
	}
	return w
}

// localTimer is a timer clctl runs itself, for work timed while the backend's single
// running timer is on something else. Stopping it logs the range as a time entry.
type localTimer struct {
	workFields
	Start int64 `json:"start"` // Unix milliseconds
}

func localTimersPath() string { return statePath("timers.json") }

func loadLocalTimers() []localTimer { return loadStateList[localTimer](localTimersPath()) }

func saveLocalTimers(timers []localTimer) error { return saveStateList(localTimersPath(), timers) }

// pausedTimer is a timer `timers pause` stopped, kept so `timers resume` can start it
// again. The list is a stack: the last one was paused most recently.
type pausedTimer struct {
	workFields
	Paused int64 `json:"paused"` // Unix milliseconds
}

func pausedTimersPath() string { return statePath("paused_timers.json") }

func loadPausedTimers() []pausedTimer { return loadStateList[pausedTimer](pausedTimersPath()) }

func savePausedTimers(timers []pausedTimer) error { return saveStateList(pausedTimersPath(), timers) }

func loadStateList[T any](path string) []T {
	var items []T
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &items)
	}
	return items
}

func saveStateList[T any](path string, items []T) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// runningTimers returns the backend's running timer (first, when there is one) and the local ones.
func runningTimers(app *App) ([]timer, error) {
	client, err := app.Backend()
	if err != nil {
		return nil, err
	}
	running, err := client.CurrentTimer()
	if err != nil {
		return nil, err
	}
	var timers []timer
	if running != nil {
		if running.Task != nil && running.Task.ID == "" {
			running.Task = nil // a timer without a task
		}
		timers = append(timers, timer{TimeEntry: running})
	}
	for _, local := range loadLocalTimers() {
		w := local.work()
		entry := &tracker.TimeEntry{Task: w.Task, Description: w.Description, Start: tracker.FlexInt(local.Start), Duration: -1}
		for _, tag := range w.Tags {
			entry.Tags = append(entry.Tags, tracker.TimeEntryTag{Name: tag})
		}
		timers = append(timers, timer{TimeEntry: entry, local: true})
	}
	return timers, nil
}

// timerQueue returns the running timers oldest first, so the last one is the most
// recently started: the one `timers pause` stops. The backend is read every time, so timers
// started or stopped in its UI count.
func timerQueue(app *App) ([]timer, error) {
	timers, err := runningTimers(app)
	sort.SliceStable(timers, func(i, j int) bool { return timers[i].Start < timers[j].Start })
	return timers, err
}

// workLine is a timer's ID column, then the time shown, then what it is for, adding the
// task's location when it is cached.
func workLine(app *App, w work, shown string) string {
	ref := w.ref()
	if ref == "" {
		ref = "(no task)"
	}
	line := fmt.Sprintf("%-12s %8s  %s", ref, shown, w.summary())
	if w.Task != nil {
		if cached, ok := app.CachedOnly().Find(w.Task.ID); ok {
			line += "  (" + tasks.Location(cached) + ")"
		}
	}
	return line
}

// timerLine describes a running timer.
func timerLine(app *App, running timer) string {
	line := workLine(app, running.work(), formatDuration(running.elapsed()))
	if running.local {
		line += "  [local]"
	}
	return line
}

// stopTimer stops the backend's timer, or logs a local timer's range and forgets it.
func stopTimer(app *App, running timer) error {
	message, err := stopTimerQuietly(app, running)
	if err == nil {
		fmt.Println(message)
	}
	return err
}

// stopTimerQuietly is stopTimer returning what it did instead of printing it.
func stopTimerQuietly(app *App, running timer) (string, error) {
	client, err := app.Backend()
	if err != nil {
		return "", err
	}
	if !running.local {
		if err := client.StopTimer(); err != nil {
			return "", err
		}
		return fmt.Sprintf("Stopped timer on %s (%s).", running.ref(), formatDuration(running.elapsed())), nil
	}
	w := running.work()
	if err := client.LogTime(w.ref(), running.StartTime(), time.Now(), w.Description, w.Tags); err != nil {
		return "", err
	}
	kept := []localTimer{}
	for _, local := range loadLocalTimers() {
		if local.TaskID != fieldsOf(w).TaskID || local.Start != int64(running.Start) {
			kept = append(kept, local)
		}
	}
	if err := saveLocalTimers(kept); err != nil {
		return "", err
	}
	return fmt.Sprintf("Stopped local timer on %s and logged %s.", running.ref(), formatDuration(running.elapsed())), nil
}

// startWorkQuietly starts a timer without asking: when the backend's timer runs on
// something else, keep says whether to leave it running and time w with a local timer,
// or stop it first. It returns what it did.
func startWorkQuietly(app *App, w work, label string, keep bool) (string, error) {
	timers, err := runningTimers(app)
	if err != nil {
		return "", err
	}
	for _, running := range timers {
		if w.Task != nil && running.on(w.Task.ID) {
			return "Timer already running on " + w.name() + ".", nil
		}
	}
	var messages []string
	if len(timers) > 0 && !timers[0].local {
		if keep {
			local := localTimer{workFields: fieldsOf(w), Start: time.Now().UnixMilli()}
			if err := saveLocalTimers(append(loadLocalTimers(), local)); err != nil {
				return "", err
			}
			return "Started local timer: " + label, nil
		}
		message, err := stopTimerQuietly(app, timers[0])
		if err != nil {
			return "", err
		}
		messages = append(messages, message)
	}
	client, err := app.Backend()
	if err != nil {
		return "", err
	}
	if err := client.StartTimer(w.ref(), w.Description, w.Tags); err != nil {
		return "", err
	}
	return strings.Join(append(messages, "Started timer: "+label), " "), nil
}

// stopTimersOn stops every timer running on a task.
func stopTimersOn(app *App, task tracker.Task) error {
	timers, err := runningTimers(app)
	if err != nil {
		return err
	}
	for _, running := range timers {
		if running.on(task.ID) || running.on(task.Ref()) {
			if err := stopTimer(app, running); err != nil {
				return err
			}
		}
	}
	return nil
}

// timerRunningOn reports whether any timer runs on the task.
func timerRunningOn(app *App, task tracker.Task) bool {
	timers, err := runningTimers(app)
	if err != nil {
		return false
	}
	for _, running := range timers {
		if running.on(task.ID) || running.on(task.Ref()) {
			return true
		}
	}
	return false
}

// startTimer starts a timer on a task. When the backend's timer is running on another task
// it offers to stop that one, or to keep it and time this task with a local timer.
func startTimer(app *App, task tracker.Task) error {
	return startWork(app, taskWork(task), tasks.Label(task))
}

// startWork starts a timer on a task or, with no task, on its description and labels.
// label describes it in the messages.
func startWork(app *App, w work, label string) error {
	timers, err := runningTimers(app)
	if err != nil {
		return err
	}
	for _, running := range timers {
		if w.Task != nil && running.on(w.Task.ID) {
			fmt.Println("Timer already running:", timerLine(app, running))
			return nil
		}
	}
	if len(timers) > 0 && !timers[0].local {
		fmt.Println("A timer is already running:", timerLine(app, timers[0]))
		choice, err := ui.Pick([]string{
			"Stop it, then start the new timer",
			"Keep it running and time this task too (a local timer, logged when stopped)",
			"Cancel",
		}, "Running timer >")
		if err != nil {
			return err // Esc: back to the task picker
		}
		if choice == 2 {
			return ui.ErrAbort
		}
		if choice == 1 {
			local := localTimer{workFields: fieldsOf(w), Start: time.Now().UnixMilli()}
			if err := saveLocalTimers(append(loadLocalTimers(), local)); err != nil {
				return err
			}
			fmt.Println("Started local timer:", label)
			return nil
		}
		if err := stopTimer(app, timers[0]); err != nil {
			return err
		}
	}
	client, err := app.Backend()
	if err != nil {
		return err
	}
	if err := client.StartTimer(w.ref(), w.Description, w.Tags); err != nil {
		return err
	}
	fmt.Println("Started timer:", label)
	return nil
}

func newStartTimerCmd(app *App) *cobra.Command {
	var taskID string
	var what workFlags
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a timer (offers to stop or keep one already running)",
		Long: `Starts the task backend's timer on a task, or on no task (--no-task, or "(no task)" in
the picker) with a description and labels saying what it is for. Each backend runs one
timer per person (ClickUp's own; Jira's and the local store's are kept by clctl), so
when it is already running you can stop it, or keep it and time this with a local
timer that clctl logs as a time entry when you stop it. On Jira, time without a task
is logged on jira_misc_issue.`,
		Example: `  clctl timers start --task-id TECH-1234
  clctl timers start --no-task --description "Sales call" --label meetings`,
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var w work
			var label string
			return ui.Steps(
				func() (err error) { w, label, err = selectWork(app, taskID, what); return err },
				func() error { return startWork(app, w, label) },
			)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	what.add(cmd)
	return cmd
}

func newStopTimerCmd(app *App) *cobra.Command {
	var taskID string
	var stopAll bool
	cmd := &cobra.Command{
		Use:         "stop",
		Short:       "Stop running timers (pick when several run)",
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			timers, err := runningTimers(app)
			if err != nil {
				return err
			}
			if len(timers) == 0 {
				fmt.Println("No active timers.")
				return nil
			}
			var chosen []timer
			switch {
			case stopAll:
				chosen = timers
			case taskID != "":
				for _, running := range timers {
					if running.on(taskID) {
						chosen = append(chosen, running)
					}
				}
				if len(chosen) == 0 {
					fmt.Println("No timer running on", taskID+".")
					return nil
				}
			case len(timers) == 1:
				chosen = timers
			default:
				rows := make([]string, len(timers))
				for index, running := range timers {
					rows[index] = timerLine(app, running)
				}
				picked, err := ui.PickMany(rows, "Stop timers >")
				if err != nil {
					return err
				}
				for _, index := range picked {
					chosen = append(chosen, timers[index])
				}
			}
			for _, running := range chosen {
				if err := stopTimer(app, running); err != nil {
					return err
				}
			}
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	cmd.Flags().BoolVar(&stopAll, "all", false, "stop every running timer")
	cmd.MarkFlagsMutuallyExclusive("all", "task-id")
	return cmd
}

// pauseTimer stops a running timer and pushes its task onto the paused stack.
func pauseTimer(app *App, running timer) error {
	if err := stopTimer(app, running); err != nil {
		return err
	}
	paused := append(loadPausedTimers(), pausedTimer{workFields: fieldsOf(running.work()), Paused: time.Now().UnixMilli()})
	if err := savePausedTimers(paused); err != nil {
		return err
	}
	fmt.Printf("Paused %s (%d paused).\n", running.ref(), len(paused))
	return nil
}

// resumeTimer pops the most recently paused task and starts its timer: the backend's when it
// is free, else a local one. It returns false when nothing was paused.
func resumeTimer(app *App) (bool, error) {
	paused := loadPausedTimers()
	if len(paused) == 0 {
		return false, nil
	}
	w := paused[len(paused)-1].work()
	timers, err := runningTimers(app)
	if err != nil {
		return false, err
	}
	clickUpBusy := false
	alreadyRunning := false
	for _, running := range timers {
		clickUpBusy = clickUpBusy || !running.local
		alreadyRunning = alreadyRunning || w.Task != nil && running.on(w.Task.ID)
	}
	switch {
	case alreadyRunning:
		// Started again elsewhere, such as in ClickUp's UI.
		fmt.Println("Timer already running on", w.name()+"; dropped it from the paused list.")
	case !clickUpBusy:
		client, err := app.Backend()
		if err != nil {
			return false, err
		}
		if err := client.StartTimer(w.ref(), w.Description, w.Tags); err != nil {
			return false, err
		}
		fmt.Printf("Resumed timer: %s  %s\n", w.name(), w.summary())
	default:
		local := localTimer{workFields: fieldsOf(w), Start: time.Now().UnixMilli()}
		if err := saveLocalTimers(append(loadLocalTimers(), local)); err != nil {
			return false, err
		}
		fmt.Printf("Resumed local timer: %s  %s\n", w.name(), w.summary())
	}
	return true, savePausedTimers(paused[:len(paused)-1])
}

func newPauseTimerCmd(app *App) *cobra.Command {
	var pauseAll bool
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause the most recently started timer (resume brings it back)",
		Long: `Stops the most recently started running timer, logging its time as stop does, and
puts its task on a paused stack ($XDG_STATE_HOME/clctl/paused_timers.json). Running
timers queue in the order they started, so pausing again stops the next newest.
With --all every running timer is paused, newest first, so resume --all restarts them
in their original order.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			timers, err := timerQueue(app)
			if err != nil {
				return err
			}
			if len(timers) == 0 {
				fmt.Println("No active timers.")
				return nil
			}
			if !pauseAll {
				timers = timers[len(timers)-1:]
			}
			for index := len(timers) - 1; index >= 0; index-- {
				if err := pauseTimer(app, timers[index]); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&pauseAll, "all", false, "pause every running timer")
	return cmd
}

func newResumeTimerCmd(app *App) *cobra.Command {
	var resumeAll bool
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Restart the most recently paused timer",
		Long: `Takes the most recently paused task off the paused stack and starts its timer,
which becomes the newest running timer. The backend's timer is used when it is free;
otherwise it runs as a local timer. With --all every paused timer is resumed, last
paused first, which restores the order pause --all stopped them in.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resumed := false
			for {
				ok, err := resumeTimer(app)
				if err != nil {
					return err
				}
				if !ok {
					break
				}
				resumed = true
				if !resumeAll {
					break
				}
			}
			if !resumed {
				fmt.Println("No paused timers.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&resumeAll, "all", false, "resume every paused timer")
	return cmd
}

// sameWork reports whether two timers are for the same thing: the same task, or with no
// task, the same description and labels.
func sameWork(a, b work) bool {
	if a.Task != nil || b.Task != nil {
		return a.Task != nil && b.Task != nil && (a.Task.ID == b.Task.ID || strings.EqualFold(a.Task.Ref(), b.Task.Ref()))
	}
	return a.Description == b.Description && strings.Join(a.Tags, ",") == strings.Join(b.Tags, ",")
}

func newSwitchTimerCmd(app *App) *cobra.Command {
	var taskID string
	var what workFlags
	cmd := &cobra.Command{
		Use:   "switch",
		Short: "Pause every running timer and start or resume another task's",
		Long: `Picks a paused timer, or a task (or no task) with no timer running, then pauses every
running timer (newest first, as pause --all does) and starts the picked one on
the backend's timer. A picked paused timer leaves the paused stack; the ones just paused
go on top of it, so resume brings them back. --task-id on a task already running
pauses the others and leaves it running.`,
		Example: `  clctl timers switch
  clctl ti -c --task-id TECH-1234
  clctl timers switch --no-task --description "Support call" --label support`,
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			running, err := timerQueue(app)
			if err != nil {
				return err
			}
			paused := loadPausedTimers()
			target, label, pausedIndex, err := pickSwitchTarget(app, running, paused, taskID, what)
			if err != nil {
				return err
			}
			return switchTo(app, running, target, label, pausedIndex)
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	what.add(cmd)
	return cmd
}

// pickSwitchTarget returns what to switch to and, when it is a paused timer, its index
// in the paused stack (else -1): from the flags, or picked from the paused timers, then
// "(no task)", then your open tasks without a running or paused timer.
func pickSwitchTarget(app *App, running []timer, paused []pausedTimer, taskID string, what workFlags) (work, string, int, error) {
	findPaused := func(w work) int {
		for index := len(paused) - 1; index >= 0; index-- {
			if sameWork(paused[index].work(), w) {
				return index
			}
		}
		return -1
	}
	if taskID != "" || what.noTask {
		w, label, err := selectWork(app, taskID, what)
		if index := findPaused(w); err == nil && index >= 0 {
			// Resume it as it was paused, with its description and labels.
			return paused[index].work(), label, index, nil
		}
		return w, label, -1, err
	}
	c, err := app.Cache()
	if err != nil {
		return work{}, "", -1, err
	}
	userID, err := app.UserID()
	if err != nil {
		return work{}, "", -1, err
	}
	isRunning := func(w work) bool {
		for _, timer := range running {
			if sameWork(timer.work(), w) {
				return true
			}
		}
		return false
	}
	var rows []string
	var pausedRows []int
	for index := len(paused) - 1; index >= 0; index-- { // most recently paused first, once each
		if w := paused[index].work(); !isRunning(w) && findPaused(w) == index {
			rows = append(rows, pausedLine(app, paused[index]))
			pausedRows = append(pausedRows, index)
		}
	}
	rows = append(rows, "(no task: a description and labels say what it was)")
	candidates := tasks.AssignedTo(tasks.Open(c.Tasks), userID)
	tasks.Sort(candidates)
	var free []tracker.Task
	for _, task := range candidates {
		if w := taskWork(task); !isTLDRSubtask(task) && !isRunning(w) && findPaused(w) < 0 {
			free = append(free, task)
			rows = append(rows, tasks.Label(task))
		}
	}
	index, err := ui.Pick(rows, "Switch to >")
	if err != nil {
		return work{}, "", -1, err
	}
	switch {
	case index < len(pausedRows):
		w := paused[pausedRows[index]].work()
		return w, w.name() + "  " + w.summary(), pausedRows[index], nil
	case index == len(pausedRows):
		w, label, err := selectWork(app, "", workFlags{noTask: true, description: what.description, labels: what.labels})
		return w, label, -1, err
	default:
		task := free[index-len(pausedRows)-1]
		return taskWork(task), tasks.Label(task), -1, nil
	}
}

// switchTo pauses every running timer except one on target, then starts target on
// the backend's timer, taking it off the paused stack when it came from there.
func switchTo(app *App, running []timer, target work, label string, pausedIndex int) error {
	alreadyRunning := false
	for index := len(running) - 1; index >= 0; index-- { // newest first, like pause --all
		if sameWork(running[index].work(), target) {
			alreadyRunning = true
			continue
		}
		if err := pauseTimer(app, running[index]); err != nil {
			return err
		}
	}
	if pausedIndex >= 0 {
		// Pausing only appended to the stack, so the index still points at it.
		paused := loadPausedTimers()
		if pausedIndex < len(paused) && sameWork(paused[pausedIndex].work(), target) {
			if err := savePausedTimers(append(paused[:pausedIndex:pausedIndex], paused[pausedIndex+1:]...)); err != nil {
				return err
			}
		}
	}
	if alreadyRunning {
		fmt.Println("Still running:", label)
		return nil
	}
	client, err := app.Backend()
	if err != nil {
		return err
	}
	if err := client.StartTimer(target.ref(), target.Description, target.Tags); err != nil {
		return err
	}
	if pausedIndex >= 0 {
		fmt.Println("Resumed timer:", label)
	} else {
		fmt.Println("Started timer:", label)
	}
	return nil
}

// pausedLine describes a paused timer.
func pausedLine(app *App, paused pausedTimer) string {
	return workLine(app, paused.work(), "paused") + "  [since " + time.UnixMilli(paused.Paused).Format("Mon 15:04") + "]"
}

func renderTimers(app *App) (string, error) {
	timers, err := timerQueue(app)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, running := range timers {
		lines = append(lines, timerLine(app, running))
	}
	if len(lines) == 0 {
		lines = append(lines, "No active timers.")
	}
	paused := loadPausedTimers()
	if len(paused) > 0 {
		lines = append(lines, "", "Paused (resume takes the last):")
		for _, entry := range paused {
			lines = append(lines, pausedLine(app, entry))
		}
	}
	return strings.Join(lines, "\n"), nil
}

func newListTimersCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show running timers once",
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := renderTimers(app)
			if err == nil {
				fmt.Println(text)
			}
			return err
		},
	}
}

func newWatchTimersCmd(app *App) *cobra.Command {
	var interval int
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch running timers (Ctrl+C to stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			interrupt := make(chan os.Signal, 1)
			signal.Notify(interrupt, os.Interrupt)
			defer signal.Stop(interrupt)
			ticker := time.NewTicker(time.Duration(max(interval, 5)) * time.Second)
			defer ticker.Stop()
			for {
				text, err := renderTimers(app)
				if err != nil {
					return err
				}
				fmt.Printf("\033[2J\033[HActive timers (%s, %s)\n\n%s\n", app.backendName(), time.Now().Format("2006-01-02 15:04:05"), text)
				select {
				case <-interrupt:
					fmt.Println("\nStopped watching active timers.")
					return nil
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().IntVar(&interval, "interval", 60, "polling interval in seconds (minimum 5)")
	return cmd
}

func newLogTimeCmd(app *App) *cobra.Command {
	var taskID, date, start, end string
	var what workFlags
	cmd := &cobra.Command{
		Use:         "log",
		Short:       "Log a finished time range against a task, or with no task",
		Annotations: map[string]string{annotationTaskAction: "true"},
		Long: `Logs a finished time range against a task, or against no task (--no-task, or "(no
task)" in the picker) with a description and labels saying what it was. The day
defaults to today. Start or end left blank at its prompt, or given as "now", is the
current minute when you press Enter; they cannot both be now.`,
		Example: `  clctl timers log --task-id TECH-1234 --date 2026-09-14 --start 09:00 --end 10:30
  clctl timers log --task-id TECH-1234 --start 09:00 --end now
  clctl timers log --no-task --description "Team standup" --label meetings --start 09:30 --end 09:45`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w, label, err := selectWork(app, taskID, what)
			if err != nil {
				return err
			}
			day := time.Now()
			if date != "" {
				if day, err = time.ParseInLocation("2006-01-02", date, time.Local); err != nil {
					return fmt.Errorf("invalid --date %q (use YYYY-MM-DD)", date)
				}
			}
			// A blank answer is now, read as Enter is pressed. Start and end cannot both
			// be now, so then the start is asked again.
			startNow, endNow := false, false
			if start == "" {
				if start, startNow, err = askClock("Start time (HH:MM, Enter for now)", "", true); err != nil {
					return err
				}
			}
			if end == "" {
				if end, endNow, err = askClock("End time (HH:MM, Enter for now)", "", true); err != nil {
					return err
				}
			}
			for startNow && endNow {
				fmt.Println("Start and end cannot both be now.")
				if start, startNow, err = askClock("Start time (HH:MM)", "", false); err != nil {
					return err
				}
				if start == "" {
					startNow = true
				}
			}
			startTime, err := clockOn(day, start)
			if err != nil {
				return err
			}
			endTime, err := clockOn(day, end)
			if err != nil {
				return err
			}
			if !endTime.After(startTime) {
				return fmt.Errorf("end time must be after start time")
			}
			client, err := app.Backend()
			if err != nil {
				return err
			}
			if err := client.LogTime(w.ref(), startTime, endTime, w.Description, w.Tags); err != nil {
				return err
			}
			fmt.Printf("Logged %s: %s\n", formatDuration(endTime.Sub(startTime)), label)
			return nil
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	what.add(cmd)
	cmd.Flags().StringVar(&date, "date", "", "day to log (YYYY-MM-DD, default today)")
	cmd.Flags().StringVar(&start, "start", "", "start time (HH:MM or now)")
	cmd.Flags().StringVar(&end, "end", "", "end time (HH:MM or now)")
	return cmd
}

// workFlags are the flags that say what timers start and timers log time is for, besides
// --task-id: --no-task, --description and --label.
type workFlags struct {
	noTask      bool
	description string
	labels      []string
}

func (f *workFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.noTask, "no-task", false, "no task: the description and labels say what it was")
	cmd.Flags().StringVar(&f.description, "description", "", "time-entry description")
	cmd.Flags().StringSliceVar(&f.labels, "label", nil, "time-entry label (a ClickUp tag, a #tag in a Jira worklog); repeat or comma-separate for several")
	cmd.MarkFlagsMutuallyExclusive("no-task", "task-id")
}

// selectWork returns what the time is for, and how to describe it: the task from
// --task-id or the picker, or no task (--no-task, or the picker's "(no task)" row) with
// its description and labels, asked for when neither flag gave one.
func selectWork(app *App, taskID string, flags workFlags) (work, string, error) {
	if !flags.noTask {
		task, err := app.SelectTask(taskID, TaskScope{AllowNone: true})
		if err != nil {
			return work{}, "", err
		}
		if task.ID != "" {
			w := taskWork(task)
			w.Description, w.Tags = flags.description, flags.labels
			return w, tasks.Label(task), nil
		}
	}
	w := work{Description: flags.description, Tags: flags.labels}
	if w.Description == "" && len(w.Tags) == 0 && ui.Interactive() {
		description, err := ui.Ask("Description", "")
		if err != nil {
			return work{}, "", err
		}
		labels, err := ui.Ask("Labels (comma-separated)", "")
		if err != nil {
			return work{}, "", err
		}
		w.Description = description
		for _, label := range strings.Split(labels, ",") {
			if label = strings.TrimSpace(label); label != "" {
				w.Tags = append(w.Tags, label)
			}
		}
	}
	if w.Description == "" && len(w.Tags) == 0 {
		return work{}, "", errors.New("time without a task needs a --description or --label")
	}
	return w, "(no task) " + w.summary(), nil
}

func newEditTimeCmd(app *App) *cobra.Command {
	var taskID, entryID, since, date, start, end, description string
	var remove bool
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Change or delete a logged time entry",
		Long: `Lists your finished time entries since --since (only the task's with --task-id),
newest first, and lets you pick one. Then change its day, start, end and description
(Enter keeps each value, "now" is the current minute, "-" clears the description) or
delete it. With --date, --start, --end, --description or --delete there are no prompts.`,
		Example: `  clctl timers edit
  clctl timers edit --task-id TECH-1234 --since 2026-10-01
  clctl timers edit --task-id TECH-1234 --start 09:15 --end 10:45
  clctl timers edit --entry-id 4212345678901234567 --delete`,
		Annotations: map[string]string{annotationTaskAction: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := app.Backend()
			if err != nil {
				return err
			}
			var entry tracker.TimeEntry
			pickEntry := func() (err error) {
				if entryID != "" {
					entry, err = client.TimeEntry(entryID)
					return err
				}
				from, err := parseSince(since)
				if err != nil {
					return err
				}
				entry, err = pickTimeEntry(client, taskID, from)
				return err
			}
			edits := false
			for _, name := range []string{"date", "start", "end", "description"} {
				edits = edits || cmd.Flags().Changed(name)
			}
			// Esc at "Edit, Delete, Cancel" goes back to the entry picker.
			return ui.Steps(pickEntry, func() error { return editEntry(cmd, client, entry, edits, remove, date, start, end, description) })
		},
	}
	addTaskIDFlag(app, cmd, &taskID)
	flags := cmd.Flags()
	flags.StringVar(&entryID, "entry-id", "", "time-entry ID (pick from recent entries when omitted)")
	flags.StringVar(&since, "since", "14d", "how far back to list entries (7d, 2w or YYYY-MM-DD)")
	flags.StringVar(&date, "date", "", "move the entry to this day (YYYY-MM-DD)")
	flags.StringVar(&start, "start", "", "new start time (HH:MM or now)")
	flags.StringVar(&end, "end", "", "new end time (HH:MM or now)")
	flags.StringVar(&description, "description", "", "new description")
	flags.BoolVar(&remove, "delete", false, "delete the entry")
	cmd.MarkFlagsMutuallyExclusive("entry-id", "task-id")
	return cmd
}

// editEntry changes or deletes a finished time entry: from the flags when edits or
// remove say so, else by asking.
func editEntry(cmd *cobra.Command, client tracker.Backend, entry tracker.TimeEntry, edits, remove bool, date, start, end, description string) error {
	if entry.Running() {
		return errors.New("that timer is still running; stop it first")
	}
	if !edits && !remove {
		if !ui.Interactive() {
			return errors.New("non-interactive timers edit needs --date, --start, --end, --description or --delete")
		}
		fmt.Println(timeEntryLine(entry))
		choice, err := ui.Pick([]string{"Edit", "Delete", "Cancel"}, "Time entry >")
		if err != nil {
			return err
		}
		if choice == 2 {
			return ui.ErrAbort
		}
		remove = choice == 1
	}
	if remove {
		if err := client.DeleteTimeEntry(entry.ID); err != nil {
			return err
		}
		fmt.Println("Deleted:", timeEntryLine(entry))
		return nil
	}
	oldDay, oldStart, oldEnd := entry.StartTime().Format("2006-01-02"), entry.StartTime().Format("15:04"), entry.EndTime().Format("15:04")
	if !cmd.Flags().Changed("date") {
		date = oldDay
	}
	if !cmd.Flags().Changed("start") {
		start = oldStart
	}
	if !cmd.Flags().Changed("end") {
		end = oldEnd
	}
	if !cmd.Flags().Changed("description") {
		description = entry.Description
	}
	if !edits {
		var err error
		if date, err = ui.Ask("Day (YYYY-MM-DD)", date); err != nil {
			return err
		}
		if start, _, err = askClock("Start time (HH:MM or now)", start, false); err != nil {
			return err
		}
		if end, _, err = askClock("End time (HH:MM or now)", end, false); err != nil {
			return err
		}
		if description, err = ui.Ask("Description", description); err != nil {
			return err
		}
		if description == "-" {
			description = ""
		}
	}
	startTime, endTime, err := editedRange(entry, date, start, end)
	if err != nil {
		return err
	}
	if startTime.Equal(entry.StartTime()) && endTime.Equal(entry.EndTime()) && description == entry.Description {
		fmt.Println("No changes.")
		return nil
	}
	if err := client.UpdateTimeEntry(entry.ID, startTime, endTime, description); err != nil {
		return err
	}
	entry.Start, entry.End = tracker.FlexInt(startTime.UnixMilli()), tracker.FlexInt(endTime.UnixMilli())
	entry.Duration, entry.Description = tracker.FlexInt(endTime.Sub(startTime).Milliseconds()), description
	fmt.Println("Updated:", timeEntryLine(entry))
	return nil
}

// newInspectTimeCmd prints time entries with the fields the backend stores, to find ones
// its Time entries view shows as "Invalid date".
func newInspectTimeCmd(app *App) *cobra.Command {
	var since string
	var asJSON bool
	cmd := &cobra.Command{
		Use:    "inspect",
		Short:  "Print your time entries' stored fields, flagging inconsistent ones",
		Hidden: true,
		Long: `Lists your finished and running time entries since --since as the backend stores them:
start, end, duration, when the entry was added and its source, and flags an entry whose
end is missing, not start + duration, or stored as a different type: the shapes that
ClickUp's Time entries view shows as "Invalid date". --json prints them as the
backend returns them.`,
		Example: `  clctl timers inspect --since 30d
  clctl timers inspect --since 2026-09-01 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := app.Backend()
			if err != nil {
				return err
			}
			from, err := parseSince(since)
			if err != nil {
				return err
			}
			raw, err := client.RawTimeEntries(from, time.Now())
			if err != nil {
				return err
			}
			if asJSON {
				data, _ := json.MarshalIndent(raw, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			flagged := 0
			for _, item := range raw {
				var entry tracker.TimeEntry
				var fields map[string]any
				if err := json.Unmarshal(item, &entry); err != nil {
					return err
				}
				_ = json.Unmarshal(item, &fields)
				line := inspectLine(entry, fields)
				if strings.Contains(line, "  !") {
					flagged++
				}
				fmt.Println(line)
			}
			fmt.Printf("%d entries, %d flagged.\n", len(raw), flagged)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "30d", "how far back to list entries (7d, 2w or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the entries as the backend returns them")
	return cmd
}

// inspectLine is a time entry with its stored start, end, duration, at and source, and
// "  !reason" for each inconsistency found.
func inspectLine(entry tracker.TimeEntry, fields map[string]any) string {
	format := func(key string) string {
		value, ok := fields[key]
		if !ok || value == nil {
			return "-"
		}
		var ms tracker.FlexInt
		text, _ := json.Marshal(value)
		if err := json.Unmarshal(text, &ms); err != nil || ms == 0 {
			return fmt.Sprint(value)
		}
		return time.UnixMilli(int64(ms)).Format("2006-01-02 15:04:05")
	}
	line := fmt.Sprintf("%-20s %s  start=%s end=%s duration=%v at=%s source=%v", entry.ID, timeEntryLine(entry),
		format("start"), format("end"), fields["duration"], format("at"), fields["source"])
	switch {
	case entry.Running():
		line += "  (running)"
	case fields["end"] == nil || fields["end"] == "":
		line += "  !no end"
	case entry.End != entry.Start+entry.Duration:
		line += "  !end is not start + duration"
	}
	for _, key := range []string{"start", "end", "duration"} {
		if value, ok := fields[key]; ok && value != nil {
			if _, isNumber := value.(float64); !isNumber {
				line += fmt.Sprintf("  !%s stored as %T", key, value)
			}
		}
	}
	return line
}

// pickTimeEntry lets the user choose one of their finished time entries since from,
// only the task's when taskID is set.
func pickTimeEntry(client tracker.Backend, taskID string, from time.Time) (tracker.TimeEntry, error) {
	entries, err := client.TimeEntries(from, time.Now())
	if err != nil {
		return tracker.TimeEntry{}, err
	}
	var kept []tracker.TimeEntry
	for _, entry := range entries {
		if entry.Running() || (taskID != "" && !(timer{TimeEntry: &entry}).on(taskID)) {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		return tracker.TimeEntry{}, fmt.Errorf("no finished time entries since %s", from.Format("2006-01-02"))
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Start > kept[j].Start })
	rows := make([]string, len(kept))
	for index, entry := range kept {
		rows[index] = timeEntryLine(entry)
	}
	index, err := ui.Pick(rows, "Time entry >")
	if err != nil {
		return tracker.TimeEntry{}, err
	}
	return kept[index], nil
}

// timeEntryLine is the one-line form of a finished time entry.
func timeEntryLine(entry tracker.TimeEntry) string {
	start, end := entry.StartTime(), entry.EndTime()
	task := &tracker.TimeEntryTask{Name: "(no task)"}
	if entry.Task != nil {
		task = entry.Task
	}
	line := fmt.Sprintf("%s  %s-%s  %8s  %-12s %s", start.Format("Mon 2006-01-02"), start.Format("15:04"),
		end.Format("15:04"), formatDuration(end.Sub(start)), task.Ref(), task.Name)
	if entry.Description != "" {
		line += "  - " + entry.Description
	}
	if len(entry.Tags) > 0 {
		line += "  " + labelList(entry.TagNames())
	}
	return line
}

// editedRange applies a day and HH:MM times to an entry. Values that did not change keep
// the entry's exact times (seconds included).
func editedRange(entry tracker.TimeEntry, date, start, end string) (time.Time, time.Time, error) {
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q (use YYYY-MM-DD)", date)
	}
	sameDay := day.Format("2006-01-02") == entry.StartTime().Format("2006-01-02")
	startTime, endTime := entry.StartTime(), entry.EndTime()
	if !sameDay || start != startTime.Format("15:04") {
		if startTime, err = clockOn(day, start); err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if !sameDay || end != endTime.Format("15:04") {
		if endTime, err = clockOn(day, end); err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if !endTime.After(startTime) {
		return time.Time{}, time.Time{}, errors.New("end time must be after start time")
	}
	return startTime, endTime, nil
}

// askClock asks for an HH:MM time. "now", or a blank answer when blankIsNow, becomes the
// current minute at the moment Enter is pressed; isNow reports that it did. Otherwise a
// blank answer returns fallback.
func askClock(label, fallback string, blankIsNow bool) (clock string, isNow bool, err error) {
	answer, err := ui.Ask(label, fallback)
	if err != nil {
		return "", false, err
	}
	answer = strings.TrimSpace(answer)
	if strings.EqualFold(answer, "now") || (blankIsNow && answer == "") {
		return time.Now().Format("15:04"), true, nil
	}
	return answer, false, nil
}

// clockOn returns HH:MM on day; "now" is the current minute.
func clockOn(day time.Time, clock string) (time.Time, error) {
	clock = strings.TrimSpace(clock)
	if strings.EqualFold(clock, "now") {
		clock = time.Now().Format("15:04")
	}
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q (use HH:MM)", clock)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.Local), nil
}
