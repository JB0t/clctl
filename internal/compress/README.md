# compress

<!-- docgen:begin id=internal/compress:overview scope=internal/compress hash=2c200e676561 -->
Package compress shortens Markdown text before sending it to a local language model. It handles prose and machine output differently. Prose gets caveman-style compression that removes articles, filler words, and wordy phrases while keeping meaningful content. Machine output like logs, terminal sessions, and tables goes through the Caveman engine to remove redundant lines. The package marks planned work with a special prefix so the model does not treat it as completed. Inline code stays untouched. The engine does not store original versions for clctl since ClickUp keeps the comments. The Text function compresses entire Markdown documents. The Machine function handles individual blocks of machine output. Stats reports compression results. CleanSummary removes chatty preambles and empty update bullets from model responses.
<!-- docgen:end id=internal/compress:overview -->

<!-- docgen:begin id=internal/compress:reference scope=internal/compress hash=2c200e676561 -->
## Reference

### `compress.go`

- **`Machine`** (function): Machine compresses machine output with the Caveman engine. It returns the text and false, unchanged, for prose and source code (or anything the engine cannot shrink). The engine normally keeps the originals for an agent to fetch back; clctl does not need them (ClickUp has the comments), so they are not stored.
- **`PlannedMarker`** (constant): PlannedMarker prefixes lines that sit under a planning heading.
- **`Stats`** (struct): Stats reports what compression did.
- **`Text`** (function): Text compresses Markdown text: prose line by line, pasted machine output (a run of minMachineLines or more lines, outside code fences) through the Caveman engine.
- **`CleanSummary`** (function): CleanSummary removes chatty preambles and "no further details" bullets that small models add to their answers.
<!-- docgen:end id=internal/compress:reference -->
