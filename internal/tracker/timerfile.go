package tracker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// TimerFile keeps a backend's running timer in a local file, for backends whose API has
// no running timer of its own (Jira). The backend logs the range when it is stopped.
type TimerFile struct {
	Path string
}

type timerState struct {
	Task        *TimeEntryTask `json:"task,omitempty"`
	Description string         `json:"description,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Start       int64          `json:"start"` // Unix milliseconds
}

// Current returns the running timer, or nil.
func (f TimerFile) Current() (*TimeEntry, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state timerState
	if err := json.Unmarshal(data, &state); err != nil || state.Start == 0 {
		return nil, err
	}
	entry := &TimeEntry{Task: state.Task, Description: state.Description, Start: FlexInt(state.Start), Duration: -1}
	for _, tag := range state.Tags {
		entry.Tags = append(entry.Tags, TimeEntryTag{Name: tag})
	}
	return entry, nil
}

// Start records a timer started now, replacing any running one.
func (f TimerFile) Start(task *TimeEntryTask, description string, tags []string) error {
	data, err := json.MarshalIndent(timerState{Task: task, Description: description, Tags: tags, Start: time.Now().UnixMilli()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	temp := f.Path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, f.Path)
}

// Clear forgets the running timer.
func (f TimerFile) Clear() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
