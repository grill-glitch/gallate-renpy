package sirenhead

// Translation units: the `gallate.translation` v1 documents under
// `text/` (docs/shell-layer/12-file-structure.md § 12.4).
//
// Entries carry three kinds of field (docs/shell-layer/12 § 12.13):
//
//	derived   id, source, source_context, placeholders, location,
//	          original_file, engine_path — recomputable from the source
//	authored  target, state — the translator's work, never recomputed
//	engine    metadata — this CLI's own namespace
//
// Unit ids are position-derived (`<resource-path>:L<line>[#n]`) per
// docs/shell-layer/05-config-file.md § `id`: a run-order counter
// renumbers every entry after an insertion and silently invalidates
// translated work, while a positional id survives re-extraction.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// UnitFormat / UnitVersion identify the unit document shape.
const (
	UnitFormat  = "gallate.translation"
	UnitVersion = 1
)

// Placeholder is a Ren'Py `[name]`-style substitution the translator
// must preserve.
type Placeholder struct {
	ID     string
	Syntax string
	Type   string
}

// Unit is one translation entry.
type Unit struct {
	ID           string
	Source       string
	Target       string
	State        string
	SourceFile   string // path relative to the game root
	Line         int
	Column       int
	Offset       int // byte offset in the source file
	Length       int // byte length of the matched literal
	Kind         string
	Speaker      string
	HasSpeaker   bool
	Snippet      string
	EndLine      int
	Placeholders []Placeholder

	// Authored fields carried over from a previous extraction
	// (docs/shell-layer/12 § 12.13): the CLI MUST NOT recompute or
	// overwrite them. `Extra` holds any other key the previous unit file
	// carried, in its original order, so a Wrapper's own extensions
	// survive an extract too.
	Extra *orderedMap
}

// buildUnits turns extraction findings into translation units.
//
// `body` is the inner content of the quoted literal, recovered with the
// same regex the inject pass uses for its drift check, so the two always
// agree on what "the source string" is.
func buildUnits(findings []finding, tc TextConfig) []Unit {
	units := make([]Unit, 0, len(findings))
	for _, f := range findings {
		body := bodyOf(f)
		snippet, endLine := snippetFor(f, tc)
		units = append(units, Unit{
			ID:           positionID(f.rel, f.line),
			Source:       body,
			Target:       "",
			State:        "initial",
			SourceFile:   f.rel,
			Line:         f.line,
			Column:       f.column,
			Offset:       f.offset,
			Length:       f.length,
			Kind:         f.kind,
			Speaker:      f.speaker,
			HasSpeaker:   f.hasSpeaker,
			Snippet:      snippet,
			EndLine:      endLine,
			Placeholders: detectPlaceholders(body),
		})
	}
	return units
}

// positionID is the position-derived unit id (docs/shell-layer/05 § `id`):
// `<resource-path>:L<line>`, zero-padded to at least four digits.
func positionID(sourceRel string, line int) string {
	return fmt.Sprintf("%s:L%04d", sourceRel, line)
}

// detectPlaceholders finds Ren'Py `[name]` substitutions.
func detectPlaceholders(body string) []Placeholder {
	var out []Placeholder
	for _, m := range placeholderRE.FindAllStringSubmatch(body, -1) {
		out = append(out, Placeholder{
			ID:     m[1],
			Syntax: "[" + m[1] + "]",
			Type:   "variable",
		})
	}
	return out
}

// snippetFor captures the source lines around a finding.
//
// Per docs/shell-layer/05 § `source_context` the record is
// file / line / end_line / snippet, with snippet holding up to
// `text.hardcoded.context_lines` lines of surrounding source joined by
// "\n". The line table is built from the RAW bytes (the reference
// implementation does the same), so a snippet on line 1 keeps whatever
// the BOM decodes to.
func snippetFor(f finding, tc TextConfig) (snippet string, endLine int) {
	if tc.WrittenOn == "never" {
		return "", f.line
	}
	raw, err := os.ReadFile(f.file)
	if err != nil {
		return "", f.line
	}
	starts := lineStarts(raw)
	lineCount := len(starts) - 1 // number of real lines (sentinel excluded)
	context := tc.ContextLines
	if context < 1 {
		context = 1
	}
	before := (context - 1) / 2
	ownLine := f.line
	if ownLine > lineCount {
		ownLine = lineCount
	}
	if ownLine < 1 {
		ownLine = 1
	}
	first := ownLine - before
	if first < 1 {
		first = 1
	}
	last := first + context - 1
	if last > lineCount {
		last = lineCount
	}
	if ownLine > last { // clamped near the end of the file
		last = ownLine
		first = last - context + 1
		if first < 1 {
			first = 1
		}
	}

	var lines []string
	for ln := first; ln <= last; ln++ {
		lines = append(lines, sourceLine(raw, starts, ln))
	}
	joined := strings.Join(lines, "\n")
	if tc.MaxContextBytes > 0 && len(joined) > tc.MaxContextBytes {
		// Truncate at the byte limit, but keep the line containing the
		// string intact (docs/shell-layer/05 § source_context).
		return sourceLine(raw, starts, ownLine), ownLine
	}
	return joined, last
}

// sourceLine returns one 1-based line of raw, with CR/LF stripped.
func sourceLine(raw []byte, starts []int, line int) string {
	if line < 1 || line > len(starts)-1 {
		return ""
	}
	lo := starts[line-1]
	hi := len(raw)
	if line < len(starts) {
		hi = starts[line]
	}
	if lo > len(raw) {
		return ""
	}
	return strings.TrimRight(sanitizeUTF8(raw[lo:hi]), "\r\n")
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// groupUnitsByLine adds the `#<n>` disambiguation suffix to the second
// and later entries sharing a (file, line) (docs/shell-layer/05 § `id`).
func groupUnitsByLine(units []Unit) {
	type key struct {
		file string
		line int
	}
	seen := map[key]int{}
	for i := range units {
		k := key{units[i].SourceFile, units[i].Line}
		seen[k]++
		if n := seen[k]; n > 1 {
			units[i].ID = fmt.Sprintf("%s#%d", units[i].ID, n)
		}
	}
}

// entryJSON renders one unit as a JSON object with a stable key order
// and the caller's `text.meta:` key mapping.
func entryJSON(u Unit, tc TextConfig) *orderedMap {
	m := &orderedMap{}
	m.Set("id", u.ID)
	m.Set("source", u.Source)
	m.Set("target", u.Target)
	m.Set("state", u.State)
	if u.Extra != nil {
		for i, key := range u.Extra.keys {
			m.Set(key, u.Extra.vals[i])
		}
	}
	if tc.WrittenOn != "never" {
		if tc.Metadata.OriginalFile {
			m.Set(tc.MetaKeys["original_file"], u.SourceFile)
		}
		if tc.Metadata.SourceContext {
			sc := &orderedMap{}
			sc.Set("file", u.SourceFile)
			sc.Set("line", u.Line)
			sc.Set("end_line", u.EndLine)
			sc.Set("snippet", u.Snippet)
			m.Set(tc.MetaKeys["source_context"], sc)
		}
		if tc.Metadata.Location {
			loc := &orderedMap{}
			loc.Set("line", u.Line)
			loc.Set("offset", u.Offset)
			loc.Set("length", u.Length)
			m.Set(tc.MetaKeys["location"], loc)
		}
		if tc.Metadata.Placeholders {
			ph := make([]any, 0, len(u.Placeholders))
			for _, p := range u.Placeholders {
				o := &orderedMap{}
				o.Set("id", p.ID)
				o.Set("syntax", p.Syntax)
				o.Set("type", p.Type)
				ph = append(ph, o)
			}
			m.Set(tc.MetaKeys["placeholders"], ph)
		}
		if tc.Metadata.EnginePath {
			m.Set(tc.MetaKeys["engine_path"], u.SourceFile)
		}
		// Engine namespace: what inject needs to locate the byte span
		// without re-scanning the source (docs/shell-layer/13 § 13.4.2).
		md := &orderedMap{}
		md.Set("source_file", u.SourceFile)
		md.Set("source_offset", u.Offset)
		md.Set("source_length", u.Length)
		md.Set("renpy_kind", u.Kind)
		if u.HasSpeaker {
			md.Set("speaker", u.Speaker)
		} else {
			md.Set("speaker", nil)
		}
		m.Set("metadata", md)
	}
	return m
}

// documentJSON renders a `gallate.translation` document.
func documentJSON(entries []Unit, cfg *Config, tc TextConfig) *orderedMap {
	list := make([]any, 0, len(entries))
	for _, u := range entries {
		list = append(list, entryJSON(u, tc))
	}
	doc := &orderedMap{}
	doc.Set("format", UnitFormat)
	doc.Set("version", UnitVersion)
	doc.Set("source", cfg.SourceLanguage())
	doc.Set("target", cfg.TargetLanguage())
	doc.Set("entries", list)
	return doc
}

// unitFileName maps a source resource path to its unit file name.
//
//	flat   — text/units/<path-with-underscores>.json
//	mirror — text/units/<path>.json (directory structure preserved)
//	single — text/translation.json (every entry in one document)
func unitFileName(layout, sourceRel string) string {
	base := strings.TrimSuffix(sourceRel, ".rpy")
	switch layout {
	case "mirror":
		return "text/units/" + base + ".json"
	case "single":
		return "text/translation.json"
	default: // flat
		return "text/units/" + strings.ReplaceAll(base, "/", "_") + ".json"
	}
}

// writtenUnitFile is a unit file that extract created (or, under
// --dry-run, would create).
type writtenUnitFile struct {
	rel     string // project-relative path
	abs     string
	sources []string // source resources covered by this file
	units   []Unit
	data    []byte // serialized document, ready to write
}

// planUnitFiles serializes the unit documents without touching the
// filesystem, so `--dry-run` can report exactly what a real run would
// write (docs/shell-layer/09-std-flags.md § 9.3).
func planUnitFiles(units []Unit, projectRoot string, cfg *Config, tc TextConfig) ([]writtenUnitFile, error) {
	byFile := map[string][]Unit{}
	var order []string
	for _, u := range units {
		if _, seen := byFile[u.SourceFile]; !seen {
			order = append(order, u.SourceFile)
		}
		byFile[u.SourceFile] = append(byFile[u.SourceFile], u)
	}
	sort.Strings(order)

	var out []writtenUnitFile
	if tc.Layout == "single" {
		rel := unitFileName(tc.Layout, "")
		data, err := marshalJSON(documentJSON(units, cfg, tc))
		if err != nil {
			return nil, exitErr(ExitOutputFailure, err.Error())
		}
		out = append(out, writtenUnitFile{
			rel: rel, abs: filepath.Join(projectRoot, filepath.FromSlash(rel)),
			sources: order, units: units, data: data,
		})
		return out, nil
	}

	for _, sourceRel := range order {
		group := byFile[sourceRel]
		if len(group) == 0 {
			continue
		}
		rel := unitFileName(tc.Layout, sourceRel)
		data, err := marshalJSON(documentJSON(group, cfg, tc))
		if err != nil {
			return nil, exitErr(ExitOutputFailure, err.Error())
		}
		out = append(out, writtenUnitFile{
			rel: rel, abs: filepath.Join(projectRoot, filepath.FromSlash(rel)),
			sources: []string{sourceRel}, units: group, data: data,
		})
	}
	return out, nil
}

// commitUnitFiles writes the planned unit documents atomically.
func commitUnitFiles(files []writtenUnitFile) error {
	for _, w := range files {
		if err := atomicWriteBytes(w.abs, w.data); err != nil {
			return exitErr(ExitOutputFailure, err.Error())
		}
	}
	return nil
}

// discoverUnitFiles lists the translation-unit documents in a project:
// `text/translation.json` (the `single` layout) plus every JSON file
// under `text/units/` (recursive, so `mirror` is covered too).
func discoverUnitFiles(projectRoot string) ([]string, error) {
	var out []string
	single := filepath.Join(projectRoot, "text", "translation.json")
	if fileExists(single) {
		out = append(out, single)
	}
	unitsDir := filepath.Join(projectRoot, "text", "units")
	if dirExists(unitsDir) {
		walked, err := sortedWalk(unitsDir)
		if err != nil {
			return nil, err
		}
		for _, rel := range walked {
			if strings.HasSuffix(rel, ".json") {
				out = append(out, filepath.Join(unitsDir, filepath.FromSlash(rel)))
			}
		}
	}
	return out, nil
}

// authored carries the fields a translator owns, as found in a previous
// extraction.
type authored struct {
	source string
	target string
	state  string
	extra  *orderedMap
}

// loadAuthored reads the project's existing unit documents and indexes
// the authored fields by unit id.
//
// Re-extraction MUST NOT destroy translated work
// (docs/shell-layer/12 § 12.13: `target`, `state`, `context`, `notes`
// and `provenance` are authored, the CLI must not recompute them).
// Position-derived ids make the match exact: the same id is the same
// position in the same resource.
func loadAuthored(projectRoot string) (map[string]authored, error) {
	files, err := discoverUnitFiles(projectRoot)
	if err != nil {
		return nil, err
	}
	out := map[string]authored{}
	for _, path := range files {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var doc map[string]any
		if unmarshalJSON(data, &doc) != nil {
			continue
		}
		entries, _ := doc["entries"].([]any)
		for _, raw := range entries {
			e, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := e["id"].(string)
			if id == "" {
				continue
			}
			a := authored{}
			a.source, _ = e["source"].(string)
			a.target, _ = e["target"].(string)
			a.state, _ = e["state"].(string)
			extra := &orderedMap{}
			for _, key := range []string{"context", "notes", "provenance"} {
				if v, ok := e[key]; ok {
					extra.Set(key, v)
				}
			}
			if len(extra.keys) > 0 {
				a.extra = extra
			}
			out[id] = a
		}
	}
	return out, nil
}

// applyAuthored merges carried-over translations into freshly extracted
// units.
//
//   - same id, same source  ⇒ the human's work is restored (target +
//     state + notes/context/provenance);
//   - same id, different source ⇒ the source moved under the
//     translation, so the stale target is dropped and the entry is
//     labelled `needs_review` (docs/shell-layer/05 § `state`). Never
//     carry a translation that no longer matches its source.
func applyAuthored(units []Unit, prev map[string]authored) (restored, stale int) {
	for i := range units {
		a, ok := prev[units[i].ID]
		if !ok {
			continue
		}
		if a.source == units[i].Source {
			if a.target != "" {
				units[i].Target = a.target
				units[i].State = a.state
				if units[i].State == "" {
					units[i].State = "translated"
				}
				restored++
			}
			units[i].Extra = a.extra
			continue
		}
		units[i].Target = ""
		units[i].State = "needs_review"
		stale++
	}
	return restored, stale
}

// loadedUnit is a unit read back from the project for injection.
type loadedUnit struct {
	ID          string
	Source      string
	Target      string
	State       string
	SourceFile  string
	Line        int
	Offset      int
	Length      int
	HasSpan     bool
	ProjectFile string
}

// loadUnits reads every translation unit from the project.
//
// `.meta.json` is the authority for the project-file ↔ source-resource
// mapping (docs/shell-layer/13 § 13.5): the CLI MUST NOT infer a source
// from a file name. When `.meta.json` is absent the unit files' own
// engine metadata is used instead (that is an explicit recorded
// mapping, not a guess), with a warning.
func loadUnits(projectRoot string) ([]loadedUnit, bool /*fromMeta*/, error) {
	meta, err := readMeta(projectRoot)
	if err != nil {
		return nil, false, err
	}

	var files []string
	if meta != nil {
		for _, e := range meta.Files {
			if e.Type != "text" {
				continue
			}
			if e.CLI.ID != "" && e.CLI.ID != CLIID {
				// Another CLI owns this entry (docs/shell-layer/13 § 13.10).
				continue
			}
			files = append(files, filepath.Join(projectRoot, filepath.FromSlash(e.Project)))
		}
	}
	fromMeta := len(files) > 0
	if !fromMeta {
		// Fall back to whatever unit documents exist.
		discovered, derr := discoverUnitFiles(projectRoot)
		if derr != nil {
			return nil, false, derr
		}
		files = discovered
	}
	sort.Strings(files)

	var units []loadedUnit
	for _, f := range files {
		if !fileExists(f) {
			// A `.meta.json` entry pointing at a missing project file is
			// never silently ignored (docs/shell-layer/13 § 13.8).
			return nil, fromMeta, exitErr(ExitProjectConfig,
				fmt.Sprintf("project file listed in .meta.json does not exist: %s", f))
		}
		loaded, err := loadUnitFile(f, projectRoot)
		if err != nil {
			return nil, fromMeta, err
		}
		units = append(units, loaded...)
	}
	return units, fromMeta, nil
}

// loadUnitFile reads one `gallate.translation` document.
func loadUnitFile(path, projectRoot string) ([]loadedUnit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, exitErr(ExitProjectConfig, fmt.Sprintf("cannot read %s: %v", path, err))
	}
	var doc map[string]any
	if err := unmarshalJSON(data, &doc); err != nil {
		return nil, exitErr(ExitProjectConfig, fmt.Sprintf("%s is not valid JSON: %v", path, err))
	}
	if v, ok := doc["version"]; ok {
		if n, ok := asInt(v); ok && n > UnitVersion {
			return nil, exitErr(ExitProjectConfig, fmt.Sprintf(
				"%s has version %d; this CLI knows version %d. Refusing to load.",
				filepath.Base(path), n, UnitVersion))
		}
	}
	entries, _ := doc["entries"].([]any)
	projRel, _ := filepath.Rel(projectRoot, path)

	var out []loadedUnit
	for _, raw := range entries {
		e, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		u := loadedUnit{ProjectFile: filepath.ToSlash(projRel)}
		u.ID, _ = e["id"].(string)
		u.Source, _ = e["source"].(string)
		u.Target, _ = e["target"].(string)
		u.State, _ = e["state"].(string)

		// Source resource + byte span. The engine namespace is
		// authoritative; the standard `original_file` / `location` keys
		// (and `source_context.file`) are accepted as equal citizens so a
		// Wrapper-rewritten unit file still injects correctly.
		md := asMap(e["metadata"])
		if md != nil {
			if s, ok := md["source_file"].(string); ok {
				u.SourceFile = s
			}
			if n, ok := asInt(md["source_offset"]); ok {
				u.Offset = n
				u.HasSpan = true
			}
			if n, ok := asInt(md["source_length"]); ok {
				u.Length = n
			}
		}
		if u.SourceFile == "" {
			if s, ok := e["original_file"].(string); ok {
				u.SourceFile = s
			} else if s, ok := e["engine_path"].(string); ok {
				u.SourceFile = s
			} else if sc := asMap(e["source_context"]); sc != nil {
				if s, ok := sc["file"].(string); ok {
					u.SourceFile = s
					u.Line, _ = asInt(sc["line"])
				}
			}
		}
		if loc := asMap(e["location"]); loc != nil {
			if n, ok := asInt(loc["offset"]); ok {
				u.Offset = n
				u.HasSpan = true
			}
			if n, ok := asInt(loc["length"]); ok {
				u.Length = n
			}
		}
		// Last resort: the position-derived id itself names the resource
		// and the line (`<resource-path>:L<line>[#n]`). That is what makes
		// the metadata fields caches rather than requirements — a unit
		// document with only id / source / target / state still injects.
		if u.SourceFile == "" {
			if file, line, ok := splitPositionID(u.ID); ok {
				u.SourceFile = file
				if u.Line == 0 {
					u.Line = line
				}
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// splitPositionID pulls the resource path and line number out of a
// position-derived unit id (docs/shell-layer/05 § `id`):
// `scenario/0083_SS_01_x.lua:L0142#2` → ("scenario/0083_SS_01_x.lua", 142).
func splitPositionID(id string) (string, int, bool) {
	marker := strings.LastIndex(id, ":L")
	if marker <= 0 {
		return "", 0, false
	}
	rest := id[marker+2:]
	if hash := strings.Index(rest, "#"); hash >= 0 {
		rest = rest[:hash]
	}
	line, err := strconv.Atoi(rest)
	if err != nil {
		return "", 0, false
	}
	return id[:marker], line, true
}

// asInt converts a JSON scalar to an int.
func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n, true
		}
	}
	return 0, false
}
