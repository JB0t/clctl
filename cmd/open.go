package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/JB0t/clctl/internal/clui"
	"github.com/JB0t/clctl/internal/tasks"
	"github.com/JB0t/clctl/internal/tracker"
	"github.com/JB0t/clctl/internal/ui"
)

// openInTUI shows a task in clui, clctl's task view, with clctl's own timers, cache and
// editor behind its keys.
func openInTUI(app *App, task tracker.Task) error {
	if !ui.Interactive() {
		return errors.New(task.Ref() + " has no web page; run tasks open in a terminal to view it")
	}
	client, err := app.Backend()
	if err != nil {
		return err
	}
	cfg, err := app.Config()
	if err != nil {
		return err
	}
	// Nothing may print while the view owns the screen.
	quiet := app.Quiet
	app.Quiet = true
	defer func() { app.Quiet = quiet }()
	_, _ = app.Cache()
	actions := clui.Actions{
		Backend: client,
		Timer: func(task tracker.Task) clui.TimerState {
			var state clui.TimerState
			timers, err := runningTimers(app)
			if err != nil {
				return state
			}
			for _, running := range timers {
				switch {
				case running.on(task.ID) || running.on(task.Ref()):
					state.Running, state.Since, state.Local = true, running.StartTime(), running.local
				case !running.local:
					state.Other = running.ref()
				}
			}
			return state
		},
		StartTimer: func(task tracker.Task, keepOther bool) (string, error) {
			return startWorkQuietly(app, taskWork(task), task.Ref()+" "+task.Name, keepOther)
		},
		StopTimer: func(task tracker.Task) (string, error) {
			timers, err := runningTimers(app)
			if err != nil {
				return "", err
			}
			var messages []string
			for _, running := range timers {
				if running.on(task.ID) || running.on(task.Ref()) {
					message, err := stopTimerQuietly(app, running)
					if err != nil {
						return "", err
					}
					messages = append(messages, message)
				}
			}
			return strings.Join(messages, " "), nil
		},
		Saved: func(task tracker.Task) {
			if c, err := app.Cache(); err == nil {
				_ = c.Upsert(app.WithSpaceName(task))
			}
		},
		OpenURL: openBrowser,
		Editor:  cfg.Editor(),
	}
	return clui.Run(actions, task.Ref())
}

// openTask shows a task in the browser when it has a web page, else in clui; tui
// always uses clui.
func openTask(app *App, task tracker.Task, tui bool) error {
	if tui || task.URL == "" {
		return openInTUI(app, task)
	}
	if err := openBrowser(task.URL); err != nil {
		return err
	}
	fmt.Println("Opened", tasks.Label(task))
	return nil
}
