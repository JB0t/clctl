# markdown

<!-- docgen:begin id=internal/markdown:overview scope=internal/markdown hash=bd08fad2a73a -->
Package markdown converts Markdown notes into ClickUp comment payloads. It handles text, images, and tables. Tables from Markdown syntax get converted to ClickUp's internal table-embed format. The package also summarizes comments and renders them as plain text for models.

The core types are `Op` for comment operations and `Section` for note sections. Functions like `Inline` and `Sections` parse Markdown into these structures. `FixTables` upgrades Markdown tables in existing comments to ClickUp's table-embed format.

Helper functions like `Summarize`, `PlainText`, and `PlainTextForModel` produce concise or formatted versions of comments. These are useful for generating summaries or preparing content for AI models. The `table.go` file defines the `TableEmbed` structure and functions to build tables from Markdown rows and separators. The `fixtables.go` file implements logic to detect and convert plain Markdown table lines into ClickUp table-embed ops. The `summary.go` file contains functions for summarizing and rendering comments in various formats.
<!-- docgen:end id=internal/markdown:overview -->

<!-- docgen:begin id=internal/markdown:reference scope=internal/markdown hash=bd08fad2a73a -->
## Reference

### `fixtables.go`

- **`FixTables`** (function): FixTables finds Markdown tables typed as plain lines in an existing comment and replaces them with ClickUp tables. It returns the new ops and how many tables it built.

### `markdown.go`

- **`Op`** (struct): Op is one ClickUp comment op: a run of text, or an embed such as a table.
- **`Op.MarshalJSON`** (method)
- **`Section`** (struct): Section is one `##` section of a note.
- **`Section.Fingerprint`** (method): Fingerprint identifies a section's content, to detect edits between pushes. It matches the Python clctl's sha256 of the stripped, newline-joined source.
- **`Section.PlainText`** (method): PlainText is the comment's text without formatting.
- **`ImageFunc`** (type): ImageFunc renders an image reference (alt text and target) as ops.
- **`DefaultImage`** (function): DefaultImage renders remote images as links and local ones as a placeholder.
- **`Inline`** (function): Inline splits one line into ops carrying inline formatting.
- **`Sections`** (function): Sections splits a note into `##` sections. skipTitle drops a leading `# title` line.

### `summary.go`

- **`Summarize`** (function): Summarize condenses an existing comment without AI: headings are kept, the first paragraph is cut to two sentences, and lists, code blocks, tables and images are reduced to one-line markers. The result is Markdown, at most maxChars long.
- **`PlainText`** (function): PlainText renders an existing comment as light Markdown: headings as #, list items as -, code blocks fenced, and tables as | rows (ClickUp's own comment_text drops tables).
- **`PlainTextForModel`** (function): PlainTextForModel is PlainText with long code blocks condensed to their first lines plus the commands they run, which a small model reads far better than raw scripts.

### `table.go`

- **`TableEmbed`** (struct): TableEmbed is ClickUp's "table-embed" op, as its editor writes it: cells keyed "row:column" (1-based) holding Quill deltas, plus row and column IDs.
- **`TableCell`** (struct)
- **`Delta`** (struct): Delta is one Quill insert inside a table cell.
- **`TableColumn`** (struct)
- **`TableRow`** (struct)
<!-- docgen:end id=internal/markdown:reference -->
