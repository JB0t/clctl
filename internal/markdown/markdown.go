// Package markdown converts Markdown notes into ClickUp comment payloads.
//
// ClickUp comments are Quill-style op lists: each op is {"text", "attributes"} and block
// formatting (bullets, code blocks, quotes) lives on the "\n" op that ends a line.
//
// A note becomes one comment per `##` section. The leading `# title` line is skipped.
// Headings become ClickUp headings of the same level (1-4), tables become ClickUp tables,
// consecutive plain lines stay grouped, and nested lists keep their indent level.
package markdown

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// Op is one ClickUp comment op: a run of text, or an embed such as a table.
type Op struct {
	Text       string         `json:"text"`
	Attributes map[string]any `json:"attributes"`
	// Table is set for table embeds, which have no text or attributes.
	Table *TableEmbed `json:"-"`
}

func (o Op) MarshalJSON() ([]byte, error) {
	if o.Table != nil {
		return json.Marshal(map[string]any{"type": "table-embed", "table-embed": o.Table})
	}
	attributes := o.Attributes
	if attributes == nil {
		attributes = map[string]any{}
	}
	return json.Marshal(struct {
		Text       string         `json:"text"`
		Attributes map[string]any `json:"attributes"`
	}{o.Text, attributes})
}

// Section is one `##` section of a note.
type Section struct {
	Title   string   // "" for text before the first heading
	Source  []string // the section's Markdown lines
	Comment []Op
}

// Fingerprint identifies a section's content, to detect edits between pushes. It matches
// the Python clctl's sha256 of the stripped, newline-joined source.
func (s Section) Fingerprint() string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.Join(s.Source, "\n"))))
	return hex.EncodeToString(sum[:])
}

// PlainText is the comment's text without formatting.
func (s Section) PlainText() string {
	var builder strings.Builder
	for _, op := range s.Comment {
		builder.WriteString(op.Text)
	}
	return builder.String()
}

// ImageFunc renders an image reference (alt text and target) as ops.
type ImageFunc func(alt, source string) []Op

var (
	headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	fenceRE   = regexp.MustCompile("^(\\s*)(```+|~~~+)\\s*([\\w+#.-]*)")
	listRE    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(?:\[([ xX])\]\s+)?(.*)$`)
	quoteRE   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	ruleRE    = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	urlRE     = regexp.MustCompile(`^https?://`)
	// Inline patterns, tried left to right at each position.
	inlineRE = regexp.MustCompile(
		`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)` + // 1,2 image
			`|!\[\[([^\]|]+)(?:\|([^\]]*))?\]\]` + // 3,4 wiki image
			`|\[([^\]]+)\]\(([^)\s]+)\)` + // 5,6 link
			"|`([^`]+)`" + // 7 code
			`|\*\*(.+?)\*\*|__(.+?)__` + // 8,9 bold
			`|~~(.+?)~~` + // 10 strike
			`|\*(\S(?:.*?\S)?)\*` + // 11 italic
			`|_(\S(?:.*?\S)?)_`, // 12 italic
	)
)

// maxHeading is the deepest heading level ClickUp comments offer; deeper Markdown
// headings are shown at this level.
const maxHeading = 4

// DefaultImage renders remote images as links and local ones as a placeholder.
func DefaultImage(alt, source string) []Op {
	label := alt
	if label == "" {
		label = source[strings.LastIndex(source, "/")+1:]
	}
	if urlRE.MatchString(source) {
		return []Op{{Text: label, Attributes: map[string]any{"link": source}}}
	}
	return []Op{{Text: "[image: " + label + "]", Attributes: map[string]any{}}}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// Inline splits one line into ops carrying inline formatting.
func Inline(text string, image ImageFunc) []Op {
	var ops []Op
	var pending strings.Builder // unformatted text, merged into one op
	plain := func(value string) { pending.WriteString(value) }
	flush := func() {
		if pending.Len() > 0 {
			ops = append(ops, Op{Text: pending.String(), Attributes: map[string]any{}})
			pending.Reset()
		}
	}
	position := 0
	for position < len(text) {
		match := inlineRE.FindStringSubmatchIndex(text[position:])
		if match == nil {
			break
		}
		start, end := position+match[0], position+match[1]
		group := func(n int) string {
			if match[2*n] < 0 {
				return ""
			}
			return text[position+match[2*n] : position+match[2*n+1]]
		}
		has := func(n int) bool { return match[2*n] >= 0 }
		// Emphasis with * or _ must not sit inside a word (e.g. snake_case, 2*3*4).
		if has(11) || has(12) {
			marker := text[start]
			before := start > 0 && (isWordByte(text[start-1]) || text[start-1] == marker)
			after := end < len(text) && (isWordByte(text[end]) || text[end] == marker)
			if before || after {
				plain(text[position : start+1])
				position = start + 1
				continue
			}
		}
		plain(text[position:start])
		flush()
		switch {
		case has(2):
			ops = append(ops, image(group(1), group(2))...)
		case has(3):
			ops = append(ops, image(group(4), group(3))...)
		case has(6):
			ops = append(ops, Op{Text: group(5), Attributes: map[string]any{"link": group(6)}})
		case has(7):
			ops = append(ops, Op{Text: group(7), Attributes: map[string]any{"code": true}})
		case has(8) || has(9):
			ops = append(ops, Op{Text: group(8) + group(9), Attributes: map[string]any{"bold": true}})
		case has(10):
			ops = append(ops, Op{Text: group(10), Attributes: map[string]any{"strike": true}})
		default:
			ops = append(ops, Op{Text: group(11) + group(12), Attributes: map[string]any{"italic": true}})
		}
		position = end
	}
	plain(text[position:])
	flush()
	return ops
}

type builder struct {
	title        string
	source       []string
	ops          []Op
	hasBody      bool
	pendingBlank bool
	lastBlock    string
}

func (b *builder) line(ops []Op, attributes map[string]any, kind string) {
	// A blank line between two list items would split the list, so drop it there.
	if b.pendingBlank && b.hasBody && !(kind == "list" && b.lastBlock == "list") {
		b.ops = append(b.ops, Op{Text: "\n", Attributes: map[string]any{}})
	}
	b.pendingBlank = false
	for _, op := range ops {
		if op.Text != "" {
			b.ops = append(b.ops, op)
		}
	}
	if attributes == nil {
		attributes = map[string]any{}
	}
	b.ops = append(b.ops, Op{Text: "\n", Attributes: attributes})
	b.hasBody = b.hasBody || kind != "heading"
	b.lastBlock = kind
}

// embed adds an embed (a table) on its own line.
func (b *builder) embed(op Op) {
	if b.pendingBlank && b.hasBody {
		b.ops = append(b.ops, Op{Text: "\n", Attributes: map[string]any{}})
	}
	b.pendingBlank = false
	b.ops = append(b.ops, op, Op{Text: "\n", Attributes: map[string]any{}})
	b.hasBody = true
	b.lastBlock = "table"
}

func (b *builder) blank() {
	if len(b.ops) > 0 {
		b.pendingBlank = true
	}
}

func indentWidth(whitespace string) int {
	return len(strings.ReplaceAll(whitespace, "\t", "    "))
}

// Sections splits a note into `##` sections. skipTitle drops a leading `# title` line.
func Sections(lines []string, image ImageFunc, skipTitle bool) []Section {
	if image == nil {
		image = DefaultImage
	}
	sections := []*builder{{}}
	var listStack []int
	var fence *struct {
		indent int
		marker string
		lang   string
	}
	skippedTitle := !skipTitle

	for index := 0; index < len(lines); index++ {
		raw := strings.TrimRight(lines[index], "\r\n")
		line := strings.TrimRight(raw, " \t")
		section := sections[len(sections)-1]
		section.source = append(section.source, raw)

		if fence != nil {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, fence.marker) && strings.Trim(trimmed, fence.marker[:1]) == "" {
				fence = nil
				continue
			}
			code := strings.TrimLeft(line, " \t")
			if len(line) >= fence.indent && strings.TrimSpace(line[:fence.indent]) == "" {
				code = line[fence.indent:]
			}
			lang := fence.lang
			if lang == "" {
				lang = "plain"
			}
			section.line([]Op{{Text: code, Attributes: map[string]any{}}},
				map[string]any{"code-block": map[string]any{"code-block": lang}}, "code")
			continue
		}

		if strings.TrimSpace(line) == "" {
			section.blank()
			continue
		}

		if !skippedTitle && len(section.ops) == 0 {
			skippedTitle = true
			if strings.HasPrefix(line, "# ") {
				section.source = section.source[:len(section.source)-1]
				continue
			}
		}

		if match := fenceRE.FindStringSubmatch(line); match != nil {
			listStack = nil
			fence = &struct {
				indent int
				marker string
				lang   string
			}{len(match[1]), match[2], match[3]}
			continue
		}

		if match := headingRE.FindStringSubmatch(line); match != nil {
			listStack = nil
			level, title := len(match[1]), match[2]
			if level <= 2 {
				headingLine := section.source[len(section.source)-1]
				section.source = section.source[:len(section.source)-1]
				section = &builder{title: title, source: []string{headingLine}}
				sections = append(sections, section)
			}
			section.line(Inline(title, image), map[string]any{"header": min(level, maxHeading)}, "heading")
			continue
		}

		// A table is a row of cells followed by a |---|---| separator line.
		if strings.Contains(line, "|") && index+1 < len(lines) && tableSeparatorRE.MatchString(lines[index+1]) {
			listStack = nil
			rows := []string{line}
			end := index + 2
			for end < len(lines) && strings.TrimSpace(lines[end]) != "" && strings.Contains(lines[end], "|") {
				rows = append(rows, lines[end])
				end++
			}
			section.source = append(section.source, lines[index+1:end]...)
			section.embed(Op{Table: buildTable(rows, lines[index+1], image)})
			index = end - 1
			continue
		}

		if match := listRE.FindStringSubmatch(line); match != nil && !ruleRE.MatchString(line) {
			width := indentWidth(match[1])
			for len(listStack) > 0 && listStack[len(listStack)-1] > width {
				listStack = listStack[:len(listStack)-1]
			}
			if len(listStack) == 0 || listStack[len(listStack)-1] < width {
				listStack = append(listStack, width)
			}
			level := len(listStack) - 1
			listType := "bullet"
			switch {
			case match[3] != "":
				listType = "unchecked"
				if strings.EqualFold(match[3], "x") {
					listType = "checked"
				}
			case match[2][0] >= '0' && match[2][0] <= '9':
				listType = "ordered"
			}
			attributes := map[string]any{"list": map[string]any{"list": listType}}
			if level > 0 {
				attributes["indent"] = level
			}
			section.line(Inline(match[4], image), attributes, "list")
			continue
		}

		listStack = nil
		if ruleRE.MatchString(line) {
			section.blank()
			continue
		}
		if match := quoteRE.FindStringSubmatch(line); match != nil {
			section.line(Inline(match[1], image), map[string]any{"blockquote": true}, "quote")
			continue
		}
		section.line(Inline(strings.TrimSpace(line), image), nil, "text")
	}

	var result []Section
	for _, section := range sections {
		if section.hasBody {
			result = append(result, Section{Title: section.title, Source: section.source, Comment: section.ops})
		}
	}
	return result
}

// CommentOps turns Markdown text into one comment's ops: every section of it, headings
// included (Sections splits at ## headings; a comment keeps them all).
func CommentOps(text string) []Op {
	var ops []Op
	for _, section := range Sections(strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), nil, false) {
		ops = append(ops, section.Comment...)
	}
	return ops
}
