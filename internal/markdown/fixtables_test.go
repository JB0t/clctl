package markdown

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFixTablesOnExistingComment(t *testing.T) {
	// The shape of a comment pushed before tables were supported.
	var ops []map[string]any
	_ = json.Unmarshal([]byte(`[
		{"text":"9. State","attributes":{"bold":true,"underline":true}},{"text":"\n","attributes":{}},
		{"text":"| | Azure | AWS |","attributes":{}},{"text":"\n","attributes":{}},
		{"text":"|---|---|---|","attributes":{}},{"text":"\n","attributes":{}},
		{"text":"| Store | One account (","attributes":{}},{"text":"a|b","attributes":{"code":true}},{"text":") | Bucket |\n| Locking | Lease | Lockfile |","attributes":{}},{"text":"\n","attributes":{"block-id":"x"}},
		{"text":"\n","attributes":{}},
		{"text":"After.","attributes":{}},{"text":"\n","attributes":{}}
	]`), &ops)
	fixed, count := FixTables(ops)
	if count != 1 {
		t.Fatalf("fixed %d tables", count)
	}
	data, _ := json.Marshal(fixed)
	text := string(data)
	for _, want := range []string{
		`"type":"table-embed"`,
		`"1:2":{"attributes":{"colspan":"1","rowspan":"1"},"content":[{"attributes":{"bold":true},"insert":"Azure"}`,
		`"2:2":{"attributes":{"colspan":"1","rowspan":"1"},"content":[{"insert":"One account ("},{"attributes":{"code":true},"insert":"a|b"},{"insert":")"}`,
		`"3:3":{"attributes":{"colspan":"1","rowspan":"1"},"content":[{"insert":"Lockfile"}`,
		`{"attributes":{"bold":true,"underline":true},"text":"9. State"}`,
		`{"attributes":{},"text":"After."}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "|---|") {
		t.Error("separator line left behind")
	}
	if _, again := FixTables(fixed); again != 0 {
		t.Error("fixing twice should find nothing")
	}
}
