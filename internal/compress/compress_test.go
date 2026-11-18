package compress

import (
	"fmt"
	"strings"
	"testing"
)

func TestTextMarksPlansAndKeepsMeaning(t *testing.T) {
	input := strings.Join([]string{
		"## Overview",
		"Need a dev storage account to test against",
		"Provision via terraform - the set of storage accounts",
		"## Creation",
		"We just created the account with the `az storage account create` command.",
		"It could potentially use the **new** module, not the old one.",
		"See https://github.com/example-org/systematic/pull/670?tab=files for details.",
		"```bash",
		"az storage account create --name the-account",
		"```",
	}, "\n")
	got, stats := Text(input)
	want := strings.Join([]string{
		"## Overview",
		"(planned) Need dev storage account to test against",
		"(planned) Provision via terraform - set of storage accounts",
		"## Creation",
		"We created account with `az storage account create` command.",
		"It could potentially use new module, not old one.",
		"See github.com/example-org/systematic/pull/670 for details.",
		"```bash",
		"az storage account create --name the-account",
		"```",
	}, "\n")
	if got != want {
		t.Fatalf("Text =\n%s\nwant\n%s", got, want)
	}
	if stats.After >= stats.Before {
		t.Errorf("no saving: %+v", stats)
	}
}

func TestPlanningSectionEndsAtSameLevelHeading(t *testing.T) {
	got, _ := Text("## Next steps\n### Detail\n- do x\n## Done\n- did y")
	if got != "## Next steps\n### Detail\n- (planned) do x\n## Done\n- did y" {
		t.Fatalf("got %q", got)
	}
}

func TestCleanSummary(t *testing.T) {
	got := CleanSummary("Here is the briefing in Markdown bullet points:\n\n- Did X.\n- No further details provided about Y.\n- Will do Z.")
	if got != "- Did X.\n- Will do Z." {
		t.Fatalf("got %q", got)
	}
}

func TestTextShortensProsePhrasesButKeepsHedges(t *testing.T) {
	got, _ := Text("So we restarted the pods in order to clear the cache. However, it might not have fixed it.\n- Okay, make sure to check the logs, for example the ingress ones.")
	want := "we restarted pods to clear cache. it might not have fixed it.\n- check logs, e.g. ingress ones."
	if got != want {
		t.Fatalf("Text =\n%s\nwant\n%s", got, want)
	}
}

func TestTextCompressesPastedLogKeepingErrors(t *testing.T) {
	var lines []string
	lines = append(lines, "Nightly batch output:", "")
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("2026-10-07T09:%02d:11Z INFO worker-%d processed batch id=%d rows=500", i, i%4, 1000+i))
	}
	lines = append(lines, "2026-10-07T10:01:02Z ERROR worker-2 FATAL: sorry, too many clients already")
	input := strings.Join(lines, "\n")
	got, stats := Text(input)
	if !strings.Contains(got, "FATAL: sorry, too many clients already") || !strings.Contains(got, "Nightly batch output:") {
		t.Fatalf("lost the error or the prose:\n%s", got)
	}
	if strings.Contains(got, "caveman_retrieve") || strings.Contains(got, "(caveman)") {
		t.Fatalf("engine notes for agents left in:\n%s", got)
	}
	if stats.After*3 > stats.Before {
		t.Fatalf("expected at least 3x smaller, got %d -> %d:\n%s", stats.Before, stats.After, got)
	}
}

func TestMachineCompressesAlignedTablesNotProseOrCode(t *testing.T) {
	rows := []string{"NAME                         READY   STATUS             RESTARTS   AGE"}
	for i := 0; i < 20; i++ {
		status := "Running"
		if i == 13 {
			status = "CrashLoopBackOff"
		}
		rows = append(rows, fmt.Sprintf("dask-worker-7f9c6d%02d-x%02dzk   1/1     %-18s 0          3d", i, i, status))
	}
	got, ok := Machine(strings.Join(rows, "\n"))
	if !ok || !strings.Contains(got, "CrashLoopBackOff") || strings.Count(got, "\n") >= 20 {
		t.Fatalf("table not compressed with its anomaly kept (ok=%v):\n%s", ok, got)
	}
	prose := strings.Repeat("We moved the workers to the new node pool and watched the queue drain overnight. ", 30)
	if _, ok := Machine(prose); ok {
		t.Fatal("prose went through the engine")
	}
	code := "package main\n\nimport \"fmt\"\n\nfunc main() {\n" + strings.Repeat("\tfmt.Println(\"x\")\n", 30) + "}\n"
	if _, ok := Machine(code); ok {
		t.Fatal("source code went through the engine")
	}
}
