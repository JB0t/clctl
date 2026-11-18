package markdown

import (
	"encoding/json"
	"strconv"
	"strings"
)

// commentLine is one line of an existing comment: its inline ops and the attributes
// of the "\n" op that ends it.
type commentLine struct {
	ops        []map[string]any
	attributes any
}

func (l commentLine) plain() (string, bool) {
	if attributes, ok := l.attributes.(map[string]any); ok {
		for key := range attributes {
			if key != "block-id" {
				return "", false // list items, code blocks, headings...
			}
		}
	}
	var text strings.Builder
	for _, op := range l.ops {
		if kind, _ := op["type"].(string); kind != "" && kind != "text" {
			return "", false // embeds (images, tags, tables)
		}
		value, _ := op["text"].(string)
		text.WriteString(value)
	}
	return text.String(), true
}

// splitLines turns a comment's ops into lines, splitting text ops at embedded newlines.
func splitLines(ops []map[string]any) []commentLine {
	var lines []commentLine
	current := commentLine{}
	for _, op := range ops {
		text, isText := op["text"].(string)
		kind, _ := op["type"].(string)
		if !isText || (kind != "" && kind != "text") {
			current.ops = append(current.ops, op)
			continue
		}
		parts := strings.Split(text, "\n")
		for index, part := range parts {
			if part != "" {
				piece := map[string]any{}
				for key, value := range op {
					piece[key] = value
				}
				piece["text"] = part
				current.ops = append(current.ops, piece)
			}
			if index < len(parts)-1 {
				current.attributes = op["attributes"]
				lines = append(lines, current)
				current = commentLine{}
			}
		}
	}
	if len(current.ops) > 0 {
		lines = append(lines, current)
	}
	return lines
}

// cellsFromOps splits a table row's ops into cells at '|' characters, keeping each
// piece's formatting. Pipes inside inline code are not separators.
func cellsFromOps(ops []map[string]any) [][]Delta {
	var cells [][]Delta
	var current []Delta
	started := false
	for _, op := range ops {
		text, _ := op["text"].(string)
		attributes, _ := op["attributes"].(map[string]any)
		if len(attributes) == 0 {
			attributes = nil
		}
		if code, _ := attributes["code"].(bool); code {
			current = append(current, Delta{Insert: text, Attributes: attributes})
			started = true
			continue
		}
		for index, piece := range strings.Split(strings.ReplaceAll(text, `\|`, "\x00"), "|") {
			if index > 0 {
				cells = append(cells, current)
				current = nil
			}
			piece = strings.ReplaceAll(piece, "\x00", "|")
			if piece != "" {
				current = append(current, Delta{Insert: piece, Attributes: attributes})
			}
		}
		started = true
	}
	if started {
		cells = append(cells, current)
	}
	// Drop the empty cells outside the leading and trailing pipes.
	trimmedEmpty := func(cell []Delta) bool {
		for _, delta := range cell {
			if strings.TrimSpace(delta.Insert) != "" {
				return false
			}
		}
		return true
	}
	if len(cells) > 0 && trimmedEmpty(cells[0]) {
		cells = cells[1:]
	}
	if len(cells) > 0 && trimmedEmpty(cells[len(cells)-1]) {
		cells = cells[:len(cells)-1]
	}
	for index, cell := range cells {
		cells[index] = trimCell(cell)
	}
	return cells
}

func trimCell(cell []Delta) []Delta {
	if len(cell) == 0 {
		return cell
	}
	cell[0].Insert = strings.TrimLeft(cell[0].Insert, " \t")
	last := len(cell) - 1
	cell[last].Insert = strings.TrimRight(cell[last].Insert, " \t")
	var kept []Delta
	for _, delta := range cell {
		if delta.Insert != "" {
			kept = append(kept, delta)
		}
	}
	return kept
}

// tableFromLines builds a table from row lines (header first, separator excluded).
func tableFromLines(rows []commentLine, separator string) map[string]any {
	aligns := alignments(separator)
	columns := len(aligns)
	table := &TableEmbed{Cells: map[string]TableCell{}}
	longest := make([]int, columns)
	for rowIndex, row := range rows {
		cells := cellsFromOps(row.ops)
		table.Rows = append(table.Rows, TableRow{Insert: map[string]string{"id": randomID("row")}})
		for column := 0; column < columns; column++ {
			var content []Delta
			length := 0
			if column < len(cells) {
				for _, delta := range cells[column] {
					length += len([]rune(delta.Insert))
					if rowIndex == 0 {
						attributes := map[string]any{"bold": true}
						for key, value := range delta.Attributes {
							attributes[key] = value
						}
						delta.Attributes = attributes
					}
					content = append(content, delta)
				}
			}
			longest[column] = max(longest[column], length)
			content = append(content, Delta{Insert: "\n", Attributes: map[string]any{"align": aligns[column]}})
			table.Cells[cellKey(rowIndex, column)] = TableCell{
				Attributes: map[string]string{"colspan": "1", "rowspan": "1"},
				Content:    content,
			}
		}
	}
	for column := 0; column < columns; column++ {
		table.Columns = append(table.Columns, TableColumn{
			Attributes: map[string]string{"width": itoa(min(max(longest[column]*7+24, 100), 360))},
			Insert:     map[string]string{"id": randomID("column")},
		})
	}
	var embed map[string]any
	data, _ := json.Marshal(table)
	_ = json.Unmarshal(data, &embed)
	return map[string]any{"type": "table-embed", "table-embed": embed}
}

// FixTables finds Markdown tables typed as plain lines in an existing comment and
// replaces them with ClickUp tables. It returns the new ops and how many tables it built.
func FixTables(ops []map[string]any) ([]map[string]any, int) {
	lines := splitLines(ops)
	var out []map[string]any
	emit := func(line commentLine) {
		out = append(out, line.ops...)
		newline := map[string]any{"text": "\n"}
		if line.attributes != nil {
			newline["attributes"] = line.attributes
		}
		out = append(out, newline)
	}
	fixed := 0
	for index := 0; index < len(lines); index++ {
		header, ok := lines[index].plain()
		if ok && strings.Contains(header, "|") && index+1 < len(lines) {
			separator, isPlain := lines[index+1].plain()
			if isPlain && tableSeparatorRE.MatchString(separator) {
				rows := []commentLine{lines[index]}
				end := index + 2
				for end < len(lines) {
					text, plain := lines[end].plain()
					if !plain || strings.TrimSpace(text) == "" || !strings.Contains(text, "|") {
						break
					}
					rows = append(rows, lines[end])
					end++
				}
				out = append(out, tableFromLines(rows, separator), map[string]any{"text": "\n", "attributes": map[string]any{}})
				fixed++
				index = end - 1
				continue
			}
		}
		emit(lines[index])
	}
	return out, fixed
}

func cellKey(row, column int) string { return strconv.Itoa(row+1) + ":" + strconv.Itoa(column+1) }

func itoa(value int) string { return strconv.Itoa(value) }
