package index

import (
	"path/filepath"
	"strings"
	"testing"
)

func fakeEmbed(calls *int) Embedder {
	return func(texts []string) ([][]float32, error) {
		*calls += len(texts)
		var out [][]float32
		for _, text := range texts {
			v := make([]float32, 3)
			if strings.Contains(text, "postgres") {
				v[0] = 1
			}
			if strings.Contains(text, "terraform") {
				v[1] = 1
			}
			v[2] = 0.1
			out = append(out, v)
		}
		return out, nil
	}
}

func TestUpdateSearchAndIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	idx := Load(path)
	sources := map[string][2]string{
		"a.md": {"2026-09-21", "# A\n## DB\nThe postgres instance keeps timing out on connect.\n## IaC\nWe will move the clusters to terraform soon, maybe."},
	}
	calls := 0
	if _, err := idx.Update("m", sources, fakeEmbed(&calls)); err != nil {
		t.Fatal(err)
	}
	if len(idx.Chunks) != 2 || calls != 2 {
		t.Fatalf("chunks %d, embeds %d", len(idx.Chunks), calls)
	}
	if err := idx.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded := Load(path)
	results := reloaded.Search([]float32{1, 0, 0}, 1, nil)
	if len(results) != 1 || results[0].Title != "A > DB" {
		t.Fatalf("search = %+v", results)
	}
	if !strings.Contains(reloaded.Chunks[1].Compressed, "move clusters to terraform") {
		t.Errorf("compressed text = %q", reloaded.Chunks[1].Compressed)
	}
	calls = 0
	if _, err := reloaded.Update("m", sources, fakeEmbed(&calls)); err != nil || calls != 0 {
		t.Fatalf("unchanged sources re-embedded %d chunks (%v)", calls, err)
	}
}

func TestSplitLongSections(t *testing.T) {
	long := "# T\n" + strings.Repeat("A line of text that is long enough to count.\n", 80)
	if chunks := Split(long); len(chunks) < 2 {
		t.Fatalf("long section not split: %d chunks", len(chunks))
	}
}
