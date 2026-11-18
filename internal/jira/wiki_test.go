package jira

import "testing"

func TestMarkdownToWiki(t *testing.T) {
	cases := map[string]string{
		"# Title":                             "h1. Title",
		"Some **bold**, *it* and `a*b*c`":     "Some *bold*, _it_ and {{a*b*c}}",
		"See [docs](https://x.io/a_b_c) now":  "See [docs|https://x.io/a_b_c] now",
		"~~gone~~ snake_case_name":            "-gone- snake_case_name",
		"- one\n  - two\n1. first":            "* one\n** two\n# first",
		"- [x] done":                          "* [x] done",
		"> quoted":                            "bq. quoted",
		"```go\nfmt.Println(\"*x*\")\n```":    "{code:go}\nfmt.Println(\"*x*\")\n{code}",
		"| a | b |\n|---|---|\n| 1 | **2** |": "||a||b||\n|1|*2*|",
		"---":                                 "----",
	}
	for markdown, want := range cases {
		if got := MarkdownToWiki(markdown); got != want {
			t.Errorf("MarkdownToWiki(%q)\n got %q\nwant %q", markdown, got, want)
		}
	}
}

func TestWikiToMarkdown(t *testing.T) {
	cases := map[string]string{
		"h2. Plan":                                  "## Plan",
		"Some *bold*, _it_ and {{x_y}}":             "Some **bold**, *it* and `x_y`",
		"[docs|https://x.io] and [https://y.io]":    "[docs](https://x.io) and <https://y.io>",
		"* one\n** two\n# first":                    "- one\n  - two\n1. first",
		"bq. quoted":                                "> quoted",
		"{code:bash}\necho *hi*\n{code}":            "```bash\necho *hi*\n```",
		"{noformat}raw{noformat}":                   "```\nraw\n```",
		"||a||b||\n|1|2|":                           "| a | b |\n|---|---|\n| 1 | 2 |",
		"{color:red}red{color} -struck- well-known": "red ~~struck~~ well-known",
		"*bold line*":                               "**bold line**",
	}
	for wiki, want := range cases {
		if got := WikiToMarkdown(wiki); got != want {
			t.Errorf("WikiToMarkdown(%q)\n got %q\nwant %q", wiki, got, want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	markdown := "## Notes\n\n- **Fix** the `cfg` [link](https://x.io)\n  - nested\n\n```sh\nmake *all*\n```\n\n| k | v |\n|---|---|\n| a | b |"
	if got := WikiToMarkdown(MarkdownToWiki(markdown)); got != markdown {
		t.Errorf("round trip\n got %q\nwant %q", got, markdown)
	}
}
