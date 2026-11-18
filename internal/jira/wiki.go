package jira

import (
	"regexp"
	"strings"
)

// Jira's v2 REST API takes and returns descriptions and comments as wiki markup. clctl
// works in Markdown, so these convert the common subset both ways: headings, bold,
// italic, strikethrough, code, links, images, lists, quotes, code blocks, rules and
// tables. Anything else passes through as text.

var (
	mdFence      = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([\\w+#.-]*)\\s*$")
	mdHeading    = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdListItem   = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(?:\[([ xX])\]\s+)?(.*)$`)
	mdQuote      = regexp.MustCompile(`^\s*>\s?(.*)$`)
	mdRule       = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	mdTableSep   = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	mdCode       = regexp.MustCompile("`([^`]+)`")
	mdImage      = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	mdLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdBold       = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	mdItalic     = regexp.MustCompile(`(^|[^\w*])\*(\S(?:.*?\S)?)\*($|[^\w*])|(^|[^\w_])_(\S(?:.*?\S)?)_($|[^\w_])`)
	mdStrike     = regexp.MustCompile(`~~(.+?)~~`)
	wikiFence    = regexp.MustCompile(`^\s*\{(code|noformat)(?::([^}]*))?\}\s*(.*)$`)
	wikiHeading  = regexp.MustCompile(`^\s*h([1-6])\.\s+(.*)$`)
	wikiListItem = regexp.MustCompile(`^\s*([*#-]+)\s+(.*)$`)
	wikiQuote    = regexp.MustCompile(`^\s*bq\.\s+(.*)$`)
	wikiCode     = regexp.MustCompile(`\{\{(.+?)\}\}`)
	wikiImage    = regexp.MustCompile(`!([^!\s|]+)(?:\|[^!]*)?!`)
	wikiLink     = regexp.MustCompile(`\[([^\]|]+)\|([^\]]+)\]|\[((?:https?|mailto):[^\]]+)\]`)
	wikiBold     = regexp.MustCompile(`(^|[^\w*])\*(\S(?:[^*]*?\S)?)\*($|[^\w*])`)
	wikiItalic   = regexp.MustCompile(`(^|[^\w_])_(\S(?:[^_]*?\S)?)_($|[^\w_])`)
	wikiStrike   = regexp.MustCompile(`(^|\s)-(\S(?:[^-]*?\S)?)-($|\s)`)
	wikiColor    = regexp.MustCompile(`\{color(?::[^}]*)?\}`)
)

// placeholders keep code spans out of the other inline rules.
type placeholders []string

func (p *placeholders) hide(text string) string {
	*p = append(*p, text)
	return "\x00" + string(rune('A'+len(*p)-1)) + "\x00"
}

func (p placeholders) restore(text string) string {
	for index, value := range p {
		text = strings.ReplaceAll(text, "\x00"+string(rune('A'+index))+"\x00", value)
	}
	return text
}

// MarkdownToWiki converts Markdown to Jira wiki markup.
func MarkdownToWiki(markdown string) string {
	var out []string
	var table []string
	inCode := false
	flushTable := func() {
		for index, row := range table {
			cells := splitCells(row)
			if index == 0 && len(table) > 1 {
				out = append(out, "||"+strings.Join(mapStrings(cells, mdInline), "||")+"||")
			} else {
				out = append(out, "|"+strings.Join(mapStrings(cells, mdInline), "|")+"|")
			}
		}
		table = nil
	}
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	for _, line := range lines {
		if match := mdFence.FindStringSubmatch(line); match != nil {
			flushTable()
			if inCode {
				out = append(out, "{code}")
			} else if match[2] != "" {
				out = append(out, "{code:"+match[2]+"}")
			} else {
				out = append(out, "{code}")
			}
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") {
			if !mdTableSep.MatchString(trimmed) {
				table = append(table, trimmed)
			}
			continue
		}
		flushTable()
		switch {
		case mdRule.MatchString(line):
			out = append(out, "----")
		case mdHeading.MatchString(line):
			match := mdHeading.FindStringSubmatch(line)
			out = append(out, "h"+string(rune('0'+len(match[1])))+". "+mdInline(match[2]))
		case mdListItem.MatchString(line):
			match := mdListItem.FindStringSubmatch(line)
			level := len(strings.ReplaceAll(match[1], "\t", "  "))/2 + 1
			marker := "*"
			if match[2][0] >= '0' && match[2][0] <= '9' {
				marker = "#"
			}
			text := mdInline(match[4])
			switch match[3] {
			case " ":
				text = "[ ] " + text
			case "x", "X":
				text = "[x] " + text
			}
			out = append(out, strings.Repeat(marker, level)+" "+text)
		case mdQuote.MatchString(line):
			out = append(out, "bq. "+mdInline(mdQuote.FindStringSubmatch(line)[1]))
		default:
			out = append(out, mdInline(line))
		}
	}
	flushTable()
	if inCode {
		out = append(out, "{code}")
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func mdInline(text string) string {
	var hidden placeholders
	text = mdCode.ReplaceAllStringFunc(text, func(match string) string {
		return hidden.hide("{{" + match[1:len(match)-1] + "}}")
	})
	text = mdImage.ReplaceAllStringFunc(text, func(match string) string {
		parts := mdImage.FindStringSubmatch(match)
		return hidden.hide("!" + parts[2] + "!")
	})
	text = mdLink.ReplaceAllStringFunc(text, func(match string) string {
		parts := mdLink.FindStringSubmatch(match)
		return hidden.hide("[" + parts[1] + "|" + parts[2] + "]")
	})
	text = mdBold.ReplaceAllStringFunc(text, func(match string) string {
		parts := mdBold.FindStringSubmatch(match)
		return hidden.hide("*" + parts[1] + parts[2] + "*")
	})
	text = mdItalic.ReplaceAllStringFunc(text, func(match string) string {
		parts := mdItalic.FindStringSubmatch(match)
		if parts[2] != "" {
			return parts[1] + "_" + parts[2] + "_" + parts[3]
		}
		return parts[4] + "_" + parts[5] + "_" + parts[6]
	})
	text = mdStrike.ReplaceAllString(text, "-$1-")
	return hidden.restore(text)
}

// WikiToMarkdown converts Jira wiki markup to Markdown.
func WikiToMarkdown(wiki string) string {
	var out []string
	inCode := ""
	headerRow := false
	lines := strings.Split(strings.ReplaceAll(wiki, "\r\n", "\n"), "\n")
	for _, line := range lines {
		if inCode != "" {
			if index := strings.Index(line, "{"+inCode+"}"); index >= 0 {
				if before := line[:index]; strings.TrimSpace(before) != "" {
					out = append(out, before)
				}
				out = append(out, "```")
				inCode = ""
				continue
			}
			out = append(out, line)
			continue
		}
		if match := wikiFence.FindStringSubmatch(line); match != nil {
			language := ""
			for _, option := range strings.Split(match[2], "|") {
				if option != "" && !strings.Contains(option, "=") {
					language = option
				}
			}
			out = append(out, "```"+language)
			rest := match[3]
			if index := strings.Index(rest, "{"+match[1]+"}"); index >= 0 { // one-line block
				out = append(out, rest[:index], "```")
				continue
			}
			if strings.TrimSpace(rest) != "" {
				out = append(out, rest)
			}
			inCode = match[1]
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "||") {
			cells := strings.Split(strings.Trim(trimmed, "|"), "||")
			out = append(out, "| "+strings.Join(mapStrings(cells, wikiInline), " | ")+" |",
				"|"+strings.Repeat("---|", len(cells)))
			headerRow = true
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			cells := strings.Split(strings.Trim(trimmed, "|"), "|")
			if !headerRow {
				out = append(out, "| "+strings.Repeat(" | ", len(cells)-1)+" |", "|"+strings.Repeat("---|", len(cells)))
				headerRow = true
			}
			out = append(out, "| "+strings.Join(mapStrings(cells, wikiInline), " | ")+" |")
			continue
		}
		headerRow = false
		switch {
		case trimmed == "----":
			out = append(out, "---")
		case wikiHeading.MatchString(line):
			match := wikiHeading.FindStringSubmatch(line)
			out = append(out, strings.Repeat("#", int(match[1][0]-'0'))+" "+wikiInline(match[2]))
		case wikiListItem.MatchString(line):
			match := wikiListItem.FindStringSubmatch(line)
			marker := "- "
			if strings.HasSuffix(match[1], "#") {
				marker = "1. "
			}
			out = append(out, strings.Repeat("  ", len(match[1])-1)+marker+wikiInline(match[2]))
		case wikiQuote.MatchString(line):
			out = append(out, "> "+wikiInline(wikiQuote.FindStringSubmatch(line)[1]))
		default:
			out = append(out, wikiInline(line))
		}
	}
	if inCode != "" {
		out = append(out, "```")
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func wikiInline(text string) string {
	var hidden placeholders
	text = wikiColor.ReplaceAllString(text, "")
	text = wikiCode.ReplaceAllStringFunc(text, func(match string) string {
		return hidden.hide("`" + match[2:len(match)-2] + "`")
	})
	text = wikiLink.ReplaceAllStringFunc(text, func(match string) string {
		parts := wikiLink.FindStringSubmatch(match)
		if parts[3] != "" {
			return hidden.hide("<" + parts[3] + ">")
		}
		return hidden.hide("[" + parts[1] + "](" + parts[2] + ")")
	})
	text = wikiImage.ReplaceAllStringFunc(text, func(match string) string {
		return hidden.hide("![](" + wikiImage.FindStringSubmatch(match)[1] + ")")
	})
	text = wikiBold.ReplaceAllString(text, "$1**$2**$3")
	text = wikiItalic.ReplaceAllString(text, "$1*$2*$3")
	text = wikiStrike.ReplaceAllString(text, "$1~~$2~~$3")
	return hidden.restore(text)
}

func splitCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	cells := strings.Split(row, "|")
	for index := range cells {
		cells[index] = strings.TrimSpace(cells[index])
	}
	return cells
}

func mapStrings(values []string, convert func(string) string) []string {
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = convert(strings.TrimSpace(value))
	}
	return out
}
