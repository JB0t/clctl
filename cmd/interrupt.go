package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Cleanups that must run even when a run is interrupted (Ctrl+C, or SIGTERM from cron
// or a timeout): removing the model lock and stopping a model server the run started.
// Deferred calls do not run when the process is killed by a signal, so a pending
// cleanup also catches the signal while it is registered.
var interruptCleanups struct {
	sync.Mutex
	next    int
	funcs   map[int]func()
	order   []int
	signals chan os.Signal
}

// onInterrupt registers cleanup to run if the process is interrupted, newest first,
// before it exits with code 130. The returned function unregisters it; call it once
// the cleanup has run normally.
func onInterrupt(cleanup func()) (unregister func()) {
	state := &interruptCleanups
	state.Lock()
	defer state.Unlock()
	if state.funcs == nil {
		state.funcs = map[int]func(){}
	}
	id := state.next
	state.next++
	state.funcs[id] = cleanup
	state.order = append(state.order, id)
	if state.signals == nil {
		state.signals = make(chan os.Signal, 1)
		signal.Notify(state.signals, os.Interrupt, syscall.SIGTERM)
		go runInterruptCleanups(state.signals)
	}
	return func() {
		state.Lock()
		defer state.Unlock()
		delete(state.funcs, id)
		if len(state.funcs) == 0 && state.signals != nil {
			// Nothing left to clean up: give the signal back to its default (and to
			// commands such as watch-timers that handle it themselves).
			signal.Stop(state.signals)
			close(state.signals)
			state.signals = nil
			state.order = nil
		}
	}
}

func runInterruptCleanups(signals chan os.Signal) {
	received, ok := <-signals
	if !ok {
		return
	}
	state := &interruptCleanups
	state.Lock()
	var pending []func()
	for index := len(state.order) - 1; index >= 0; index-- {
		if cleanup, ok := state.funcs[state.order[index]]; ok {
			pending = append(pending, cleanup)
		}
	}
	state.Unlock()
	fmt.Fprintf(os.Stderr, "\nInterrupted (%v): cleaning up...\n", received)
	for _, cleanup := range pending {
		cleanup()
	}
	os.Exit(130)
}
