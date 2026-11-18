package markdown

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	sections := Sections(strings.Split(`## Plan
### Goals
First sentence here. Second one! Third is cut.
* a
* b
`+"```bash\nls\npwd\n```"+`
| x | y |
|---|---|
| 1 | 2 |

Another paragraph.`, "\n"), nil, true)
	data, _ := json.Marshal(sections[0].Comment)
	var ops []map[string]any
	_ = json.Unmarshal(data, &ops)
	got := Summarize(ops, 400)
	want := "**Plan** · **Goals** · First sentence here. Second one! … · (2 list items) · [bash code, 2 lines] · [table 2×2] (+1 more paragraphs)"
	if got != want {
		t.Fatalf("Summarize =\n%s\nwant\n%s", got, want)
	}
	if long := Summarize(ops, 20); len([]rune(strings.Split(long, " (+")[0])) != 20 {
		t.Errorf("not truncated to 20 runes: %q", long)
	}
}

func TestPlainTextKeepsTables(t *testing.T) {
	sections := Sections(strings.Split("## State\n| | Azure | AWS |\n|---|---|---|\n| Store | `blob` | S3 |\n\n* item\n    * nested\n```\ncode\n```", "\n"), nil, true)
	data, _ := json.Marshal(sections[0].Comment)
	var ops []map[string]any
	_ = json.Unmarshal(data, &ops)
	want := "## State\n|  | Azure | AWS |\n|---|---|---|\n| Store | blob | S3 |\n\n- item\n  - nested\n```\ncode\n```"
	if got := PlainText(ops); got != want {
		t.Fatalf("PlainText =\n%s\nwant\n%s", got, want)
	}
}

func TestCondenseCode(t *testing.T) {
	lines := []string{"# Get subscription ID", `sub_id=$(az account show --query id -o tsv)`, "if [ -z x ]; then", "  exit 1", "fi",
		`az group create --name "$g"`, "az storage account create \\", "  --name x", "echo done", "a", "b", "c", "d",
		`az role assignment create --role "Storage Blob Data Reader"`}
	got := condenseCode(lines, 12)
	want := "… (14 lines in total; commands run: az account show, az group create, az storage account create, az role assignment create)"
	if len(got) != 4 || got[3] != want {
		t.Fatalf("condenseCode = %q", got)
	}
	if short := condenseCode(lines[:5], 12); len(short) != 5 {
		t.Fatal("short code should be kept as is")
	}
}

func TestCondenseCodeKeepsErrorsInPastedLogs(t *testing.T) {
	var log []string
	for i := 0; i < 30; i++ {
		log = append(log, fmt.Sprintf("2026-10-07T09:%02d:11Z INFO worker processed batch id=%d", i, i))
	}
	log = append(log, "2026-10-07T10:01:02Z ERROR worker FATAL: too many clients")
	got := strings.Join(condenseCode(log, 12), "\n")
	if !strings.Contains(got, "FATAL: too many clients") {
		t.Fatalf("the error at the end of the log was lost:\n%s", got)
	}
	script := []string{"#!/bin/bash", "set -e"}
	for i := 0; i < 20; i++ {
		script = append(script, fmt.Sprintf("kubectl rollout restart deployment/app-%d", i))
	}
	if got := condenseCode(script, 12); len(got) != 4 || !strings.Contains(got[3], "commands run: kubectl rollout restart") {
		t.Fatalf("scripts should still be cut to their first lines and commands: %q", got)
	}
}
