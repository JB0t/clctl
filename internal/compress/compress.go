// Package compress shortens Markdown before it goes to a local model, and marks
// planned work so the model does not report it as done.
//
// Prose gets caveman-style compression (articles, filler, sentence openers,
// connectives and wordy phrases go) but keeps everything that carries meaning for a
// status summary: modal and hedging words ("will", "could", "potentially",
// "proposed"), negations, numbers, names, commands and code. Machine output pasted
// into a comment (logs, terminal output, JSON, tables, diffs, config, test reports)
// goes through the Caveman engine (github.com/JuliusBrussee/caveman/engine), which
// keeps errors, anomalies and the lines around them and states what it dropped.
// Inline code is never touched.
package compress

import (
	"regexp"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/engine"
)

var (
	headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	// Headings whose sections describe intentions rather than finished work.
	planningRE = regexp.MustCompile(`(?i)\b(overview|goals?|aims?|objectives?|requirements?|scope|plan(s|ned|ning)?|proposal|proposed|next steps?|to-?dos?|todo|design|considerations?|ideas?|options?|open questions|questions|future|roadmap|approach)\b`)
	articleRE  = regexp.MustCompile(`(?i)\b(a|an|the)\s+`)
	fillerRE   = regexp.MustCompile(`(?i)\b(just|really|basically|actually|simply|very|quite|pretty much|of course|kind of|sort of|please|thanks|thank you)\b\s*`)
	urlRE      = regexp.MustCompile(`https?://([^\s)>\]]+)`)
	emphasisRE = regexp.MustCompile(`\*\*|__`)
	spacesRE   = regexp.MustCompile(`[ \t]{2,}`)
	inlineCode = regexp.MustCompile("`[^`]*`")
	listPrefix = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])?\s*`)
	// Sentence openers and connectives that add no information to a status update.
	leaderRE     = regexp.MustCompile(`(?i)^(\s*(?:[-*+]|\d+[.)])?\s*)(?:okay|ok|so|well|now|alright|anyway)[,.!]?\s+`)
	connectiveRE = regexp.MustCompile(`(?i)\b(?:however|furthermore|additionally|in addition|moreover|also note that|note that)\b[,]?\s+`)
	// Wordy phrases and their short forms.
	phrases = []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`(?i)\bin order to\b`), "to"},
		{regexp.MustCompile(`(?i)\bmake sure (?:to|that)\s+`), ""},
		{regexp.MustCompile(`(?i)\bthe reason (?:is|was) (?:because|that)\b`), "because"},
		{regexp.MustCompile(`(?i)\bdue to the fact that\b`), "because"},
		{regexp.MustCompile(`(?i)\bfor example\b`), "e.g."},
		{regexp.MustCompile(`(?i)\bat this point in time\b`), "now"},
		{regexp.MustCompile(`(?i)\bin the event that\b`), "if"},
		{regexp.MustCompile(`(?i)\bas well as\b`), "and"},
		{regexp.MustCompile(`(?i)\ba number of\b`), "several"},
		{regexp.MustCompile(`(?i)\bit is worth noting that\s+`), ""},
	}

	// Signs that a line is machine output rather than prose.
	timestampRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}|\b\d{2}:\d{2}:\d{2}\b`)
	logLevelRE  = regexp.MustCompile(`\b(?:TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL|CRITICAL)\b`)
	columnsRE   = regexp.MustCompile(`\S {2,}\S.* {2,}\S`) // at least two aligned column gaps
	promptRE    = regexp.MustCompile(`^\s*(?:\$|#|>|PS [A-Z]:\\)\s`)
)

// minMachineLines is the shortest pasted run worth sending through the engine.
const minMachineLines = 8

var cavemanEngine = sync.OnceValue(func() *engine.Engine { return engine.New(nil, nil) })

// Machine compresses machine output with the Caveman engine. It returns the text and
// false, unchanged, for prose and source code (or anything the engine cannot shrink).
// The engine normally keeps the originals for an agent to fetch back; clctl does not
// need them (ClickUp has the comments), so they are not stored.
func Machine(text string) (string, bool) {
	options := engine.Options{Mode: engine.ModeCompress, ExternalRecovery: true}
	switch kind := cavemanEngine().Detect([]byte(text)); kind {
	case engine.TypeCode:
		return text, false
	case engine.TypeText:
		if !columnar(text) {
			return text, false
		}
		options.Type = engine.TypeTerminal // aligned columns: kubectl, ps, df and the like
	}
	result, err := cavemanEngine().Compress([]byte(text), options)
	if err != nil || result.TokensAfter >= result.TokensBefore {
		return text, false
	}
	return cleanEngineOutput(string(result.Output)), true
}

// cleanEngineOutput drops the engine's notes addressed to agents that can fetch the
// original back (the local model cannot) and its brand from the elision markers.
func cleanEngineOutput(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "caveman_retrieve") {
			continue
		}
		kept = append(kept, strings.ReplaceAll(line, " (caveman)", ""))
	}
	return strings.Join(kept, "\n")
}

func columnar(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	aligned := 0
	for _, line := range lines {
		if columnsRE.MatchString(line) {
			aligned++
		}
	}
	return len(lines) >= minMachineLines && aligned*10 >= len(lines)*7
}

// machineBlock reports whether most lines of a pasted run look like machine output.
func machineBlock(lines []string) bool {
	machine := 0
	for _, line := range lines {
		if timestampRE.MatchString(line) || logLevelRE.MatchString(line) || columnsRE.MatchString(line) ||
			promptRE.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "{") {
			machine++
		}
	}
	return machine*10 >= len(lines)*6
}

// PlannedMarker prefixes lines that sit under a planning heading.
const PlannedMarker = "(planned) "

// Stats reports what compression did.
type Stats struct {
	Before, After int // characters
}

// Text compresses Markdown text: prose line by line, pasted machine output (a run of
// minMachineLines or more lines, outside code fences) through the Caveman engine.
func Text(text string) (string, Stats) {
	var out, run []string
	inFence := false
	planningLevel := 0 // heading level of the current planning section, 0 = none
	seen := map[string]bool{}
	flush := func() {
		if len(run) >= minMachineLines && machineBlock(run) {
			if compressed, ok := Machine(strings.Join(run, "\n")); ok {
				out = append(out, strings.Split(compressed, "\n")...)
				run = nil
				return
			}
		}
		for _, line := range run {
			trimmed := strings.TrimSpace(line)
			// Long lines repeated verbatim (copied output, repeated log lines) add nothing.
			if len(trimmed) > 30 && seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			compressed := shorten(line)
			if planningLevel != 0 {
				// Keep list markers first so the line is still a list item.
				prefix := listPrefix.FindString(compressed)
				compressed = prefix + PlannedMarker + compressed[len(prefix):]
			}
			out = append(out, compressed)
		}
		run = nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flush()
			inFence = !inFence
			out = append(out, trimmed)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if trimmed == "" {
			flush()
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if match := headingRE.FindStringSubmatch(trimmed); match != nil {
			flush()
			level := len(match[1])
			if planningLevel != 0 && level <= planningLevel {
				planningLevel = 0
			}
			if planningLevel == 0 && planningRE.MatchString(match[2]) {
				planningLevel = level
			}
			out = append(out, match[1]+" "+shorten(match[2]))
			continue
		}
		run = append(run, line)
	}
	flush()
	result := strings.TrimSpace(strings.Join(out, "\n"))
	return result, Stats{Before: len(text), After: len(result)}
}

// shorten drops articles, filler and URL clutter outside inline code.
func shorten(line string) string {
	var builder strings.Builder
	last := 0
	for _, span := range inlineCode.FindAllStringIndex(line, -1) {
		builder.WriteString(shortenProse(line[last:span[0]]))
		builder.WriteString(line[span[0]:span[1]])
		last = span[1]
	}
	builder.WriteString(shortenProse(line[last:]))
	return strings.TrimRight(builder.String(), " \t")
}

func shortenProse(text string) string {
	text = urlRE.ReplaceAllStringFunc(text, func(url string) string {
		url = strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
		if index := strings.IndexAny(url, "?#"); index >= 0 {
			url = url[:index]
		}
		return strings.TrimSuffix(url, "/")
	})
	text = leaderRE.ReplaceAllString(text, "$1")
	for _, phrase := range phrases {
		text = phrase.re.ReplaceAllString(text, phrase.with)
	}
	text = connectiveRE.ReplaceAllString(text, "")
	text = articleRE.ReplaceAllString(text, "")
	text = fillerRE.ReplaceAllString(text, "")
	text = emphasisRE.ReplaceAllString(text, "")
	return spacesRE.ReplaceAllString(text, " ")
}

var (
	preambleRE = regexp.MustCompile(`(?i)^(here (is|are)|sure[,!.]|below (is|are)|this is)\b.*:?\s*$`)
	fillerLine = regexp.MustCompile(`(?i)^\s*[-*+]\s*(no (further|new|other) (details|information|updates|changes)|nothing (else|further))\b`)
)

// CleanSummary removes chatty preambles and "no further details" bullets that small
// models add to their answers.
func CleanSummary(text string) string {
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if len(kept) == 0 && (strings.TrimSpace(line) == "" || preambleRE.MatchString(strings.TrimSpace(line))) {
			continue
		}
		if fillerLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
