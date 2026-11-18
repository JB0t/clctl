package markdown

import (
	"encoding/json"
	"strings"
	"testing"
)

func ops(t *testing.T, section Section) string {
	t.Helper()
	data, _ := json.Marshal(section.Comment)
	return string(data)
}

func TestSectionsSplitAndStyle(t *testing.T) {
	note := strings.Split(`# TECH-1: title

intro line

## First
### Sub ~~old~~
* item **bold**
    * nested `+"`code`"+`
1. one
- [x] done

`+"```bash\necho hi\n```"+`
## Second
> quote with [link](https://x.y) and snake_case_name`, "\n")
	sections := Sections(note, nil, true)
	if len(sections) != 3 || sections[0].Title != "" || sections[1].Title != "First" || sections[2].Title != "Second" {
		t.Fatalf("unexpected sections: %+v", sections)
	}
	first := ops(t, sections[1])
	for _, want := range []string{
		`{"text":"First","attributes":{}},{"text":"\n","attributes":{"header":2}}`,
		`{"text":"old","attributes":{"strike":true}},{"text":"\n","attributes":{"header":3}}`,
		`{"text":"bold","attributes":{"bold":true}}`,
		`{"text":"\n","attributes":{"indent":1,"list":{"list":"bullet"}}}`,
		`{"text":"\n","attributes":{"list":{"list":"ordered"}}}`,
		`{"text":"\n","attributes":{"list":{"list":"checked"}}}`,
		`{"text":"echo hi","attributes":{}},{"text":"\n","attributes":{"code-block":{"code-block":"bash"}}}`,
	} {
		if !strings.Contains(first, want) {
			t.Errorf("section 1 lacks %s\n%s", want, first)
		}
	}
	second := ops(t, sections[2])
	if !strings.Contains(second, `"link":"https://x.y"`) || !strings.Contains(second, `and snake_case_name`) {
		t.Errorf("inline parsing wrong: %s", second)
	}
	if strings.Contains(strings.Join(sections[1].Source, "\n"), "# TECH-1") {
		t.Error("title line leaked into a section source")
	}
}

func TestImagesUseCallback(t *testing.T) {
	var seen []string
	image := func(alt, source string) []Op {
		seen = append(seen, alt+"|"+source)
		return []Op{{Text: "IMG", Attributes: map[string]any{"link": "u"}}}
	}
	Sections([]string{"## S", "see ![shot](dir/a.png) and ![[b.png]]"}, image, true)
	if strings.Join(seen, ",") != "shot|dir/a.png,|b.png" {
		t.Fatalf("images seen: %v", seen)
	}
}

func TestTables(t *testing.T) {
	sections := Sections(strings.Split(`## T
before
| Name | Notes |
|:---|:---:|
| `+"`a|b`"+` | x \| y |
| **short** |
after`, "\n"), nil, true)
	var table *TableEmbed
	for _, op := range sections[0].Comment {
		if op.Table != nil {
			table = op.Table
		}
	}
	if table == nil || len(table.Rows) != 3 || len(table.Columns) != 2 {
		t.Fatalf("table not built: %+v", sections[0].Comment)
	}
	data, _ := json.Marshal(table.Cells)
	for _, want := range []string{
		`"1:1":{"attributes":{"colspan":"1","rowspan":"1"},"content":[{"insert":"Name","attributes":{"bold":true}},{"insert":"\n","attributes":{"align":"left"}}]}`,
		`{"insert":"a|b","attributes":{"code":true}}`,
		`{"insert":"x | y"},{"insert":"\n","attributes":{"align":"center"}}`,
		`"3:2":{"attributes":{"colspan":"1","rowspan":"1"},"content":[{"insert":"\n","attributes":{"align":"center"}}]}`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("cells lack %s\n%s", want, data)
		}
	}
	payload, _ := json.Marshal(sections[0].Comment)
	if !strings.Contains(string(payload), `"type":"table-embed"`) || !strings.Contains(string(payload), `{"text":"after","attributes":{}}`) {
		t.Errorf("payload: %s", payload)
	}
	if got := strings.Join(sections[0].Source, "\n"); !strings.Contains(got, "| **short** |") {
		t.Error("table lines missing from the section source")
	}
}
