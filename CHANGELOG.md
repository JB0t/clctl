# Changelog

All notable changes to clctl. Each release's section is written by `scripts/release.sh`
(see CONTRIBUTING.md); a `## Unreleased` section at the top, when present, becomes the
next release's notes.

## Unreleased

### Features
- feat: pluggable task backends: a local task store, ClickUp or Jira, with the same commands on each, and a task view (`tasks open`)
- feat: `timers switch` pauses every running timer and starts or resumes another
- feat: `digest` summarises tasks from their summary subtask's tl;drs
- feat: `digest --force`, `--meeting-note` and `--init`; `--check` reads the crontab
- feat: a config path that is a directory holds the settings in `config`

### Other
- Standalone repository: module path `github.com/JB0t/clctl`, release binaries for Linux, macOS and Windows (amd64 and arm64)

## v1.0.0 - 2026-10-16

First stable release, made before clctl moved to its own repository.
