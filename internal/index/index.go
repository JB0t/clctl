// Package index is a small local vector index over notes and task comments, for
// meaning-based search and for picking relevant context for summaries.
//
// Chunks are embedded from lightly normalised text (embeddings work best on natural
// prose) and also stored compressed, which is the form that goes into prompts.
// Vectors are kept per chunk content hash, so re-indexing only embeds what changed.
package index

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JB0t/clctl/internal/compress"
)

// Chunk is one searchable piece of a note or comment.
type Chunk struct {
	Source     string `json:"source"` // note path relative to the notes root, or "TASK-ID#comment-id"
	Title      string `json:"title"`  // heading path within the source
	Day        string `json:"day,omitempty"`
	Text       string `json:"text"`       // normalised text that was embedded
	Compressed string `json:"compressed"` // compressed text for prompts
	Hash       string `json:"hash"`
	Vector     vector `json:"vector"`
}

// vector stores float32s as base64 to keep the index file small.
type vector []float32

func (v vector) MarshalJSON() ([]byte, error) {
	buffer := make([]byte, 4*len(v))
	for index, value := range v {
		binary.LittleEndian.PutUint32(buffer[4*index:], math.Float32bits(value))
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(buffer))
}

func (v *vector) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	buffer, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return err
	}
	*v = make(vector, len(buffer)/4)
	for index := range *v {
		(*v)[index] = math.Float32frombits(binary.LittleEndian.Uint32(buffer[4*index:]))
	}
	return nil
}

type Index struct {
	Model  string  `json:"model"`
	Chunks []Chunk `json:"chunks"`
	path   string
}

// DefaultPath is $XDG_CACHE_HOME/clctl/index.json.
func DefaultPath(cacheDir string) string { return filepath.Join(cacheDir, "index.json") }

func Load(path string) *Index {
	idx := &Index{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, idx)
	}
	idx.path = path
	return idx
}

func (idx *Index) Save() error {
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(idx.path), 0o755); err != nil {
		return err
	}
	temp := idx.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, idx.path)
}

var (
	headingRE = regexp.MustCompile(`^(#{1,3})\s+(.*)$`)
	spacesRE  = regexp.MustCompile(`[ \t]+`)
)

const maxChunk = 1500 // characters of normalised text per chunk

// Split cuts Markdown into chunks at headings (levels 1-3) and at maxChunk characters,
// keeping code blocks whole where possible. It returns (title path, text) pairs.
func Split(markdown string) [][2]string {
	var chunks [][2]string
	var titles [3]string
	var current []string
	size := 0
	flush := func() {
		text := strings.TrimSpace(strings.Join(current, "\n"))
		if len([]rune(text)) >= 40 {
			var path []string
			for _, title := range titles {
				if title != "" {
					path = append(path, title)
				}
			}
			chunks = append(chunks, [2]string{strings.Join(path, " > "), text})
		}
		current, size = nil, 0
	}
	inFence := false
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if match := headingRE.FindStringSubmatch(line); match != nil && !inFence {
			flush()
			level := len(match[1])
			titles[level-1] = strings.TrimSpace(match[2])
			for deeper := level; deeper < 3; deeper++ {
				titles[deeper] = ""
			}
			continue
		}
		line = spacesRE.ReplaceAllString(strings.TrimRight(line, " \t"), " ")
		if size+len(line) > maxChunk && !inFence && size > 0 {
			flush()
		}
		current = append(current, line)
		size += len(line) + 1
	}
	flush()
	return chunks
}

func hash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Embedder turns texts into vectors (nomic-embed-text expects task prefixes).
type Embedder func(texts []string) ([][]float32, error)

// Update replaces the chunks of the given sources, embedding only new or changed chunks.
// sources maps a source name to (day, Markdown). It returns how many chunks were embedded.
func (idx *Index) Update(model string, sources map[string][2]string, embed Embedder) (int, error) {
	if idx.Model != model {
		idx.Model, idx.Chunks = model, nil // vectors from another model are not comparable
	}
	known := map[string]vector{}
	for _, chunk := range idx.Chunks {
		known[chunk.Hash] = chunk.Vector
	}
	var kept []Chunk
	for _, chunk := range idx.Chunks {
		if _, replaced := sources[chunk.Source]; !replaced {
			kept = append(kept, chunk)
		}
	}
	var fresh []Chunk
	var pending []int
	for source, entry := range sources {
		for _, part := range Split(entry[1]) {
			compressed, _ := compress.Text(part[1])
			chunk := Chunk{Source: source, Title: part[0], Day: entry[0], Text: part[1], Compressed: compressed, Hash: hash(source, part[0], part[1])}
			if vector, ok := known[chunk.Hash]; ok {
				chunk.Vector = vector
			} else {
				pending = append(pending, len(fresh))
			}
			fresh = append(fresh, chunk)
		}
	}
	const batch = 32
	for start := 0; start < len(pending); start += batch {
		end := min(start+batch, len(pending))
		var texts []string
		for _, position := range pending[start:end] {
			chunk := fresh[position]
			texts = append(texts, "search_document: "+chunk.Title+"\n"+chunk.Text)
		}
		vectors, err := embed(texts)
		if err != nil {
			return start, err
		}
		for offset, position := range pending[start:end] {
			fresh[position].Vector = normalise(vectors[offset])
		}
	}
	idx.Chunks = append(kept, fresh...)
	sort.SliceStable(idx.Chunks, func(i, j int) bool { return idx.Chunks[i].Source < idx.Chunks[j].Source })
	return len(pending), nil
}

// Prune drops chunks whose source no longer exists (keep reports whether a source is live).
func (idx *Index) Prune(keep func(source string) bool) int {
	var kept []Chunk
	for _, chunk := range idx.Chunks {
		if keep(chunk.Source) {
			kept = append(kept, chunk)
		}
	}
	removed := len(idx.Chunks) - len(kept)
	idx.Chunks = kept
	return removed
}

func normalise(values []float32) vector {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	norm := float32(math.Sqrt(sum))
	out := make(vector, len(values))
	for index, value := range values {
		if norm > 0 {
			out[index] = value / norm
		}
	}
	return out
}

// Result is a scored chunk.
type Result struct {
	Chunk
	Score float32
}

// Search returns the top chunks by cosine similarity to the query vector, optionally
// limited to chunks that match filter.
func (idx *Index) Search(query []float32, limit int, filter func(Chunk) bool) []Result {
	q := normalise(query)
	var results []Result
	for _, chunk := range idx.Chunks {
		if filter != nil && !filter(chunk) {
			continue
		}
		if len(chunk.Vector) != len(q) {
			continue
		}
		var score float32
		for index, value := range chunk.Vector {
			score += value * q[index]
		}
		results = append(results, Result{chunk, score})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}
