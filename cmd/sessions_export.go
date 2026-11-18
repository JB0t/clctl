package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JB0t/clctl/internal/config"
	"github.com/JB0t/clctl/internal/sessions"
	"github.com/JB0t/clctl/internal/webdav"
)

// sessionsShare is where exports go in the uploading account's Nextcloud files.
const sessionsShare = "Shared/Sessions"

func newSessionsExportCmd() *cobra.Command {
	var user, server, since string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "export [DIR...]",
		Short: "Upload redacted copies of your Claude Code and Copilot sessions to Nextcloud",
		Long: `Walks each DIR for transcripts changed within --since, and uploads each as a redacted
<session-id>.jsonl.gz to <URL>/remote.php/dav/files/<account>/Shared/Sessions/<NAME>/
over WebDAV, creating the folder when it is missing.

A DIR is a Claude Code projects directory (*.jsonl transcripts), or a VS Code user folder
such as ~/.config/Code/User, whose GitHub Copilot Chat sessions are uploaded as
copilot-<session-id>.jsonl.gz, in the same reduced shape as Claude Code's, so whatever
digests the uploads reads both. Without a DIR, the folders set by 'clctl sessions init'
are exported, and --user and --to default to the ones it saved.

Only what 'sessions digest' reads is uploaded: the user's and the assistant's text, tool
names and the file paths given to tools, through the same redactor. Tool results,
attachments, thinking and everything else never leave the machine.

A transcript already uploaded with the same size and sha256 is skipped; one that grew is
uploaded again, whole. That record is kept in ~/.local/state/clctl/sessions-export.json.

The Nextcloud account never comes from flags: NC_USER, and NC_PASS_FILE, the path of a
file holding its (app) password, from the environment, else from the config
(sessions_nc_user, sessions_nc_pass_file) that 'clctl sessions init' writes. The password
itself is never in the config.

Upload only your own sessions, from your own machine: a person's directory is only ever
exported by that person's own cron.`,
		Example: `  clctl sessions export ~/.claude/projects --user jo --to https://cloud.example --dry-run
  NC_USER='Data Bot' NC_PASS_FILE=~/.config/clctl/nc-data-bot clctl sessions export ~/.claude/projects --user jo --to https://cloud.example
  clctl sessions export          # what 'clctl sessions init' set up`,
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := parseSince(since)
			if err != nil {
				return err
			}
			dirs, err := sessionDirs(cmd, args)
			if err != nil {
				return err
			}
			cfg, err := sessionsConfig(cmd)
			if err != nil {
				return err
			}
			if user == "" {
				user = cfg.Get(config.KeySessionsUser)
			}
			if server == "" {
				server = cfg.Get(config.KeySessionsTo)
			}
			if user == "" || server == "" {
				return fmt.Errorf("set --user and --to, or run 'clctl sessions init'")
			}
			return runSessionsExport(exportOptions{dirs: dirs, user: user, server: server, since: from, dryRun: dryRun,
				statePath: statePath("sessions-export.json"), getenv: exportEnv(cfg, os.Getenv), out: os.Stdout})
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&user, "user", "", "your name: the folder under Shared/Sessions (default: from 'sessions init')")
	flags.StringVar(&server, "to", "", "Nextcloud URL, e.g. https://cloud.example (default: from 'sessions init')")
	flags.StringVar(&since, "since", "30d", "only transcripts changed since: 30d, 36h, 2w or YYYY-MM-DD")
	flags.BoolVar(&dryRun, "dry-run", false, "list what would upload, with sizes; upload nothing")
	return cmd
}

// exportEnv reads NC_USER and NC_PASS_FILE from the environment, else from the config.
func exportEnv(cfg *config.Config, getenv func(string) string) func(string) string {
	fallback := map[string]string{"NC_USER": cfg.Get(config.KeySessionsNCUser), "NC_PASS_FILE": cfg.Get(config.KeySessionsNCPassFile)}
	return func(key string) string {
		if value := getenv(key); value != "" {
			return value
		}
		return fallback[key]
	}
}

type exportOptions struct {
	dirs         []string
	user, server string
	since        time.Time
	dryRun       bool
	statePath    string
	getenv       func(string) string
	out          io.Writer
}

// exportRecord is what was last uploaded for one transcript file.
type exportRecord struct {
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Uploaded string `json:"uploaded"`
}

func runSessionsExport(o exportOptions) error {
	if o.user == "" || o.user == "." || o.user == ".." || strings.ContainsAny(o.user, `/\`) {
		return fmt.Errorf("--user %q must be a plain folder name", o.user)
	}
	folder := sessionsShare + "/" + o.user
	var paths []string
	for _, dir := range o.dirs {
		found, err := sessions.Transcripts(dir)
		if err != nil {
			return err
		}
		paths = append(paths, found...)
	}
	state := map[string]exportRecord{}
	if data, err := os.ReadFile(o.statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("%s: %w", o.statePath, err)
		}
	}

	var client *webdav.Client // set up at the first upload, so a run with nothing new needs no credentials
	var older, unchanged, empty, uploaded int
	var failures []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.ModTime().Before(o.since) {
			older++
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		key := filepath.Base(path)
		if sessions.IsCopilot(path) {
			key = "copilot:" + key
		}
		record := exportRecord{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
		if previous, ok := state[key]; ok && previous.Size == record.Size && previous.SHA256 == record.SHA256 {
			unchanged++
			continue
		}
		var zipped bytes.Buffer
		kept, err := sessions.WriteReducedFile(path, bytes.NewReader(data), &zipped)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if kept == 0 {
			empty++
			continue
		}
		target := folder + "/" + sessions.ReducedName(path)
		if o.dryRun {
			fmt.Fprintf(o.out, "would upload %s: %d bytes -> %d bytes redacted (%d entries) to %s\n", key, len(data), zipped.Len(), kept, target)
			uploaded++
			continue
		}
		if client == nil {
			if client, err = exportClient(o, folder); err != nil {
				return err
			}
		}
		if err := client.Put(target, zipped.Bytes(), "application/gzip"); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		record.Uploaded = time.Now().UTC().Format(time.RFC3339)
		state[key] = record
		if err := saveExportState(o.statePath, state); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "uploaded %s: %d bytes -> %d bytes redacted (%d entries)\n", key, len(data), zipped.Len(), kept)
		uploaded++
	}
	verb := "uploaded"
	if o.dryRun {
		verb = "would upload"
	}
	fmt.Fprintf(o.out, "%d %s, %d unchanged, %d older than --since, %d with nothing to keep, %d failed\n",
		uploaded, verb, unchanged, older, empty, len(failures))
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "\n"))
	}
	return nil
}

// exportClient reads the account from NC_USER and NC_PASS_FILE and makes sure the
// upload folder exists.
func exportClient(o exportOptions, folder string) (*webdav.Client, error) {
	user, passFile := o.getenv("NC_USER"), o.getenv("NC_PASS_FILE")
	if user == "" || passFile == "" {
		return nil, errors.New("set NC_USER and NC_PASS_FILE (the path of a file holding the password) to upload")
	}
	password, err := os.ReadFile(config.ExpandHome(passFile))
	if err != nil {
		return nil, fmt.Errorf("NC_PASS_FILE: %w", err)
	}
	client := webdav.Nextcloud(o.server, user, strings.TrimSpace(string(password)))
	if err := client.MkdirAll(folder); err != nil {
		return nil, err
	}
	return client, nil
}

func saveExportState(path string, state map[string]exportRecord) error {
	data, _ := json.MarshalIndent(state, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
