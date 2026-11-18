# index

<!-- docgen:begin id=internal/index:overview scope=internal/index hash=d3382d4a578f -->
This module provides a local vector index for searching and summarizing notes and task comments based on semantic meaning. It organizes content into chunks that are embedded into vector representations for similarity search, using a persistent storage format to retain embeddings and avoid reprocessing unchanged content.

The core data structures include `Chunk` for individual searchable pieces of text, `Index` for managing the collection of chunks and their embeddings, and `Result` for search outcomes. Chunks are created by splitting Markdown text at headings and length limits, preserving code blocks and normalizing text for embedding. The index supports updating with new or modified sources, pruning stale entries, and searching with cosine similarity.

The `Update` method handles embedding only new or changed chunks by comparing content hashes, while `Search` retrieves semantically similar chunks using vector similarity. Embeddings are handled through an `Embedder` function that converts text into float32 vectors, and the index stores these vectors efficiently using base64 encoding to minimize file size.
<!-- docgen:end id=internal/index:overview -->

<!-- docgen:begin id=internal/index:reference scope=internal/index hash=d3382d4a578f -->
## Reference

### `index.go`

- **`Chunk`** (struct): Chunk is one searchable piece of a note or comment.
- **`vector.MarshalJSON`** (method)
- **`vector.UnmarshalJSON`** (method)
- **`Index`** (struct)
- **`DefaultPath`** (function): DefaultPath is $XDG_CACHE_HOME/clctl/index.json.
- **`Load`** (function)
- **`Index.Save`** (method)
- **`Split`** (function): Split cuts Markdown into chunks at headings (levels 1-3) and at maxChunk characters, keeping code blocks whole where possible. It returns (title path, text) pairs.
- **`Embedder`** (type): Embedder turns texts into vectors (nomic-embed-text expects task prefixes).
- **`Index.Update`** (method): Update replaces the chunks of the given sources, embedding only new or changed chunks. sources maps a source name to (day, Markdown). It returns how many chunks were embedded.
- **`Index.Prune`** (method): Prune drops chunks whose source no longer exists (keep reports whether a source is live).
- **`Result`** (struct): Result is a scored chunk.
- **`Index.Search`** (method): Search returns the top chunks by cosine similarity to the query vector, optionally limited to chunks that match filter.
<!-- docgen:end id=internal/index:reference -->
