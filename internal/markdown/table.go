package markdown

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var tableSeparatorRE = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

// TableEmbed is ClickUp's "table-embed" op, as its editor writes it: cells keyed
// "row:column" (1-based) holding Quill deltas, plus row and column IDs.
type TableEmbed struct {
	Cells   map[string]TableCell `json:"cells"`
	Columns []TableColumn        `json:"columns"`
	Rows    []TableRow           `json:"rows"`
}

type TableCell struct {
	Attributes map[string]string `json:"attributes"`
	Content    []Delta           `json:"content"`
}

// Delta is one Quill insert inside a table cell.
type Delta struct {
	Insert     string         `json:"insert"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type TableColumn struct {
	Attributes map[string]string `json:"attributes"`
	Insert     map[string]string `json:"insert"`
}

type TableRow struct {
	Insert map[string]string `json:"insert"`
}

// splitRow splits a Markdown table row into cells, honouring \| and `code | spans`.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var current strings.Builder
	inCode := false
	for index := 0; index < len(line); index++ {
		char := line[index]
		switch {
		case char == '\\' && index+1 < len(line) && line[index+1] == '|':
			current.WriteByte('|')
			index++
		case char == '`':
			inCode = !inCode
			current.WriteByte(char)
		case char == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteByte(char)
		}
	}
	return append(cells, strings.TrimSpace(current.String()))
}

func alignments(separator string) []string {
	var aligns []string
	for _, cell := range splitRow(separator) {
		left, right := strings.HasPrefix(cell, ":"), strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			aligns = append(aligns, "center")
		case right:
			aligns = append(aligns, "right")
		default:
			aligns = append(aligns, "left")
		}
	}
	return aligns
}

func randomID(prefix string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, 6)
	_, _ = rand.Read(buffer)
	for index := range buffer {
		buffer[index] = alphabet[int(buffer[index])%len(alphabet)]
	}
	return prefix + "-" + string(buffer)
}

// buildTable converts Markdown table lines (header first, separator excluded) into a
// ClickUp table. Header cells are bold; column widths follow the longest cell.
func buildTable(rows []string, separator string, image ImageFunc) *TableEmbed {
	aligns := alignments(separator)
	columns := len(aligns)
	table := &TableEmbed{Cells: map[string]TableCell{}}
	longest := make([]int, columns)
	for rowIndex, row := range rows {
		cells := splitRow(row)
		table.Rows = append(table.Rows, TableRow{Insert: map[string]string{"id": randomID("row")}})
		for column := 0; column < columns; column++ {
			text := ""
			if column < len(cells) {
				text = cells[column]
			}
			longest[column] = max(longest[column], utf8.RuneCountInString(text))
			var content []Delta
			for _, op := range Inline(text, image) {
				attributes := op.Attributes
				if rowIndex == 0 {
					attributes = map[string]any{"bold": true}
					for key, value := range op.Attributes {
						attributes[key] = value
					}
				}
				if len(attributes) == 0 {
					attributes = nil
				}
				content = append(content, Delta{Insert: op.Text, Attributes: attributes})
			}
			content = append(content, Delta{Insert: "\n", Attributes: map[string]any{"align": aligns[column]}})
			table.Cells[fmt.Sprintf("%d:%d", rowIndex+1, column+1)] = TableCell{
				Attributes: map[string]string{"colspan": "1", "rowspan": "1"},
				Content:    content,
			}
		}
	}
	for column := 0; column < columns; column++ {
		width := min(max(longest[column]*7+24, 100), 360)
		table.Columns = append(table.Columns, TableColumn{
			Attributes: map[string]string{"width": fmt.Sprint(width)},
			Insert:     map[string]string{"id": randomID("column")},
		})
	}
	return table
}
