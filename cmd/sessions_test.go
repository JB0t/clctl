package cmd

import "testing"

func TestNormalizeHeadings(t *testing.T) {
	input := "Here is the digest.\n# Session digest\n## Repositories:\n- svc-mp: deploy fixed\n\n\n**Decisions**\n#### Decisions\n- keep sqlite\n### Dates\n- 2026-09-17 deploy"
	want := "Here is the digest.\n\n### Repositories and services touched\n\n- svc-mp: deploy fixed\n\n**Decisions**\n\n### Decisions\n\n- keep sqlite\n\n### Dates\n\n- 2026-09-17 deploy"
	if got := normalizeHeadings(input); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
