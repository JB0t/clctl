package ui

import (
	"errors"
	"testing"
)

// TestStepsGoBack checks that Esc returns to the nearest earlier step that asked
// something, that Esc at the first prompt cancels, and that ErrAbort never goes back.
func TestStepsGoBack(t *testing.T) {
	var trace []string
	answers := map[string][]error{
		"task":     {nil, nil},
		"note":     {nil, nil},
		"sections": {ErrCancelled, nil},
	}
	step := func(name string, asks bool) func() error {
		return func() error {
			trace = append(trace, name)
			if asks {
				prompts++
			}
			err := answers[name][0]
			answers[name] = answers[name][1:]
			return err
		}
	}
	// The note step asks nothing (a single note), so Esc at sections goes back to task.
	if err := Steps(step("task", true), step("note", false), step("sections", true)); err != nil {
		t.Fatal(err)
	}
	if got := joined(trace); got != "task,note,sections,task,note,sections" {
		t.Errorf("trace = %s", got)
	}

	first := func() error { prompts++; return ErrCancelled }
	if err := Steps(first, func() error { t.Fatal("ran past a cancelled first step"); return nil }); err != ErrCancelled {
		t.Errorf("Esc at the first prompt: %v", err)
	}
	if err := Steps(func() error { return nil }, first); err != ErrCancelled {
		t.Errorf("Esc with no earlier prompt: %v", err)
	}

	runs := 0
	abort := func() error { prompts++; runs++; return ErrAbort }
	err := Steps(func() error { prompts++; return nil }, abort)
	if !errors.Is(err, ErrCancelled) || runs != 1 {
		t.Errorf("ErrAbort: err %v, runs %d", err, runs)
	}
	other := errors.New("boom")
	if err := Steps(func() error { prompts++; return nil }, func() error { return other }); err != other {
		t.Errorf("other error: %v", err)
	}
}

func joined(items []string) string {
	out := ""
	for index, item := range items {
		if index > 0 {
			out += ","
		}
		out += item
	}
	return out
}
