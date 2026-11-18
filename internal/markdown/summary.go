package markdown

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/JB0t/clctl/internal/compress"
)

var sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)

// Summarize condenses an existing comment without AI: headings are kept, the first
// paragraph is cut to two sentences, and lists, code blocks, tables and images are
// reduced to one-line markers. The result is Markdown, at most maxChars long.
func Summarize(ops []map[string]any, maxChars int) string {
	var parts []string
	paragraphs := 0
	listItems, codeLines := 0, 0
	codeLang := ""
	flushList := func() {
		if listItems > 0 {
			label := "list items"
			if listItems == 1 {
				label = "list item"
			}
			parts = append(parts, fmt.Sprintf("(%d %s)", listItems, label))
			listItems = 0
		}
	}
	flushCode := func() {
		if codeLines > 0 {
			parts = append(parts, fmt.Sprintf("[%s code, %d lines]", codeLang, codeLines))
			codeLines = 0
		}
	}
	for _, line := range splitLines(ops) {
		attributes, _ := line.attributes.(map[string]any)
		var text strings.Builder
		for _, op := range line.ops {
			switch kind, _ := op["type"].(string); kind {
			case "table-embed":
				flushList()
				flushCode()
				parts = append(parts, tableMarker(op))
			case "image":
				text.WriteString("[image]")
			case "tag":
				value, _ := op["text"].(string)
				text.WriteString(value)
			default:
				value, _ := op["text"].(string)
				text.WriteString(value)
			}
		}
		content := strings.TrimSpace(text.String())
		if block, ok := attributes["code-block"].(map[string]any); ok {
			flushList()
			if codeLines == 0 {
				codeLang, _ = block["code-block"].(string)
			}
			codeLines++
			continue
		}
		flushCode()
		if _, ok := attributes["list"]; ok {
			listItems++
			continue
		}
		flushList()
		if content == "" {
			continue
		}
		if _, ok := attributes["header"]; ok {
			parts = append(parts, fmt.Sprintf("**%s**", content))
			continue
		}
		if bold, _ := firstBool(line.ops, "bold"); bold && len(line.ops) == 1 {
			parts = append(parts, fmt.Sprintf("**%s**", content)) // old-style bold headings
			continue
		}
		if paragraphs == 0 {
			parts = append(parts, firstSentences(content, 2))
		}
		paragraphs++
	}
	flushList()
	flushCode()
	summary := strings.Join(parts, " · ")
	if len([]rune(summary)) > maxChars {
		summary = string([]rune(summary)[:maxChars-1]) + "…"
	}
	if paragraphs > 1 {
		summary += fmt.Sprintf(" (+%d more paragraphs)", paragraphs-1)
	}
	return summary
}

func firstBool(ops []map[string]any, key string) (bool, bool) {
	if len(ops) == 0 {
		return false, false
	}
	attributes, _ := ops[0]["attributes"].(map[string]any)
	value, ok := attributes[key].(bool)
	return value, ok
}

func firstSentences(text string, count int) string {
	end := 0
	for index := 0; index < count; index++ {
		match := sentenceEnd.FindStringIndex(text[end:])
		if match == nil {
			return text
		}
		end += match[0] + 1
	}
	if end < len(text) {
		return strings.TrimSpace(text[:end]) + " …"
	}
	return text
}

func tableMarker(op map[string]any) string {
	embed, _ := op["table-embed"].(map[string]any)
	rows, _ := embed["rows"].([]any)
	columns, _ := embed["columns"].([]any)
	return fmt.Sprintf("[table %d×%d]", len(rows), len(columns))
}

// PlainText renders an existing comment as light Markdown: headings as #, list items
// as -, code blocks fenced, and tables as | rows (ClickUp's own comment_text drops
// tables).
func PlainText(ops []map[string]any) string { return plainText(ops, 0, false) }

// PlainTextForModel is PlainText with long code blocks condensed to their first lines
// plus the commands they run, which a small model reads far better than raw scripts.
func PlainTextForModel(ops []map[string]any) string { return plainText(ops, 12, false) }

// ToMarkdown renders a comment as Markdown, as PlainText does but keeping inline
// formatting (bold, italic, strikethrough, code and links), for backends that store
// comments as markup rather than ops.
func ToMarkdown(ops []map[string]any) string { return plainText(ops, 0, true) }

// inlineMarkdown wraps one text op in the Markdown for its inline attributes.
func inlineMarkdown(text string, attributes map[string]any) string {
	if text == "" || attributes == nil {
		return text
	}
	if attributes["code"] == true {
		text = "`" + text + "`"
	}
	if attributes["strike"] == true {
		text = "~~" + text + "~~"
	}
	if attributes["italic"] == true {
		text = "*" + text + "*"
	}
	if attributes["bold"] == true {
		text = "**" + text + "**"
	}
	if link, ok := attributes["link"].(string); ok && link != "" {
		text = "[" + text + "](" + link + ")"
	}
	return text
}

func plainText(ops []map[string]any, maxCodeLines int, inline bool) string {
	var out []string
	var code []string
	inCode := false
	flushCode := func() {
		out = append(out, condenseCode(code, maxCodeLines)...)
		code = nil
	}
	for _, line := range splitLines(ops) {
		attributes, _ := line.attributes.(map[string]any)
		_, isCode := attributes["code-block"]
		if isCode != inCode {
			if inCode {
				flushCode()
			}
			out = append(out, "```")
			inCode = isCode
		}
		var text strings.Builder
		embedOnly := len(line.ops) > 0
		for _, op := range line.ops {
			switch kind, _ := op["type"].(string); kind {
			case "table-embed":
				out = append(out, tableRows(op)...)
				continue
			case "image":
				text.WriteString("[image]")
			default:
				value, _ := op["text"].(string)
				if inline && !isCode {
					attributes, _ := op["attributes"].(map[string]any)
					value = inlineMarkdown(value, attributes)
				}
				text.WriteString(value)
			}
			embedOnly = false
		}
		if embedOnly {
			continue
		}
		content := text.String()
		if isCode {
			code = append(code, content)
			continue
		}
		switch {
		case attributes["header"] != nil:
			level := 2
			if number, ok := attributes["header"].(float64); ok {
				level = int(number)
			}
			content = strings.Repeat("#", level) + " " + content
		case attributes["list"] != nil:
			indent := 0
			if number, ok := attributes["indent"].(float64); ok {
				indent = int(number)
			}
			marker := "- "
			if inline && attributes["list"] == "ordered" {
				marker = "1. "
			}
			if list, ok := attributes["list"].(map[string]any); ok && inline && list["list"] == "ordered" {
				marker = "1. "
			}
			content = strings.Repeat("  ", indent) + marker + content
		}
		out = append(out, content)
	}
	if inCode {
		flushCode()
		out = append(out, "```")
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// tableRows renders a table embed as Markdown rows.
func tableRows(op map[string]any) []string {
	embed, _ := op["table-embed"].(map[string]any)
	cells, _ := embed["cells"].(map[string]any)
	rows, _ := embed["rows"].([]any)
	columns, _ := embed["columns"].([]any)
	var out []string
	for row := 1; row <= len(rows); row++ {
		var values []string
		for column := 1; column <= len(columns); column++ {
			cell, _ := cells[fmt.Sprintf("%d:%d", row, column)].(map[string]any)
			content, _ := cell["content"].([]any)
			var text strings.Builder
			for _, part := range content {
				delta, _ := part.(map[string]any)
				value, _ := delta["insert"].(string)
				text.WriteString(strings.ReplaceAll(value, "\n", " "))
			}
			values = append(values, strings.TrimSpace(text.String()))
		}
		out = append(out, "| "+strings.Join(values, " | ")+" |")
		if row == 1 {
			out = append(out, "|"+strings.Repeat("---|", len(columns)))
		}
	}
	return out
}

// commandTools are CLIs whose subcommands say what a script does ("az storage account create").
var commandTools = map[string]bool{
	"az": true, "aws": true, "gcloud": true, "kubectl": true, "helm": true, "terraform": true,
	"tofu": true, "docker": true, "git": true, "gh": true, "psql": true, "sqlcmd": true,
	"ansible-playbook": true, "flux": true, "argocd": true, "uv": true, "pip": true,
}

var commandSeparators = regexp.MustCompile(`\$\(|&&|\|\||[|;]`)

// condenseCode keeps short code as is; longer code becomes its first lines plus the
// distinct CLI commands it runs.
func condenseCode(lines []string, maxLines int) []string {
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	// Logs, terminal output, JSON and the like pasted in a code block: the Caveman
	// engine keeps the errors and anomalies, which the first lines would miss.
	if compressed, ok := compress.Machine(strings.Join(lines, "\n")); ok {
		return strings.Split(compressed, "\n")
	}
	var commands []string
	seen := map[string]bool{}
	for _, line := range lines {
		for _, segment := range commandSeparators.Split(line, -1) {
			words := strings.Fields(segment)
			for len(words) > 0 && strings.Contains(words[0], "=") { // VAR=value prefixes
				words = words[1:]
			}
			if len(words) == 0 || !commandTools[words[0]] {
				continue
			}
			command := []string{words[0]}
			for _, word := range words[1:] {
				if strings.HasPrefix(word, "-") || strings.ContainsAny(word, `"'$\=`) || len(command) == 4 {
					break
				}
				command = append(command, word)
			}
			name := strings.Join(command, " ")
			if !seen[name] && len(commands) < 15 {
				seen[name] = true
				commands = append(commands, name)
			}
		}
	}
	condensed := append([]string{}, lines[:3]...)
	summary := fmt.Sprintf("… (%d lines in total", len(lines))
	if len(commands) > 0 {
		summary += "; commands run: " + strings.Join(commands, ", ")
	}
	return append(condensed, summary+")")
}
