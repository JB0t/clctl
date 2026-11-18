package cmd

import (
	"fmt"
	"strings"

	"github.com/JB0t/clctl/internal/clickup"
	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/jira"
	"github.com/JB0t/clctl/internal/local"
	"github.com/JB0t/clctl/internal/tracker"
)

// backendNames are the backends' display names.
var backendNames = map[string]string{"local": "clctl's local store", "clickup": "ClickUp", "jira": "Jira"}

// backendName is the configured backend's display name, for messages.
func (a *App) backendName() string {
	if cfg, err := a.Config(); err == nil {
		if name, ok := backendNames[cfg.Backend()]; ok {
			return name
		}
	}
	return "the task backend"
}

// openBackend returns the task backend the config names.
func openBackend(cfg *config.Config) (tracker.Backend, error) {
	switch backend := cfg.Backend(); backend {
	case "clickup":
		if cfg.Get(config.KeyAPIToken) == "" || cfg.Get(config.KeyTeamID) == "" {
			return nil, ErrNotInitialised
		}
		return clickup.New(cfg.Get(config.KeyAPIToken), cfg.APIURL(), cfg.Get(config.KeyTeamID)), nil
	case "jira":
		return jiraBackend(cfg, cfg.Get(config.KeyJiraToken))
	case "local":
		return local.Open(cfg.LocalDir(), cfg.Get(config.KeyUserName), "")
	case "":
		return nil, ErrNotInitialised
	default:
		return nil, fmt.Errorf("unknown backend %q in %s; use %s", backend, cfg.Path, strings.Join(config.Backends, ", "))
	}
}

func jiraBackend(cfg *config.Config, token string) (*jira.Client, error) {
	if cfg.Get(config.KeyJiraURL) == "" || token == "" {
		return nil, ErrNotInitialised
	}
	return jira.New(jira.Options{
		URL:       cfg.Get(config.KeyJiraURL),
		Email:     cfg.Get(config.KeyJiraEmail),
		Token:     token,
		Projects:  cfg.SpaceIDs(),
		MiscIssue: cfg.Get(config.KeyJiraMiscIssue),
		TimerPath: statePath("jira_timer.json"),
	}), nil
}
