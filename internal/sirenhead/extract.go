package sirenhead

// The two standard operations: extract and inject.
//
// extract — read the Ren'Py game directory, write `gallate.translation`
// documents under `text/`, media sidecars under `image/` `audio/`
// `video/`, and `.meta.json`.
//
// inject — read those documents back and rewrite the recorded byte
// ranges in the `.rpy` sources, or copy replacement media over the
// originals.
//
// inject runs in TWO PASSES. Pass 1 validates every gate for every file
// and rebuilds the bytes in memory; if anything fails, nothing at all is
// written (not even the files that were already fine). Pass 2 writes.
// A partially applied inject is the one outcome this CLI must never
// produce: the source-drift gate is the last line of defence, not a
// reason to skip the first.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Stats carries the counters of one operation.
type Stats struct {
	Operation string

	FilesScanned   int
	FilesProcessed int
	FilesFailed    int

	TextExtracted     int
	TextInjected      int
	UnitsSkippedEmpty int
	UnitsDrifted      int

	ImagesExtracted int
	ImagesInjected  int
	AudioExtracted  int
	AudioInjected   int
	VideoExtracted  int
	VideoInjected   int

	OutputCreated int
	BytesWritten  int64

	ValidationErrors   int
	ValidationWarnings int
	ValidationInfos    int

	// Authored-field accounting: how many previous translations were kept,
	// and how many were dropped because their source moved.
	AuthoredRestored int
	AuthoredStale    int

	Duration float64
	DryRun   bool
}

// statisticsJSON renders the statistics payload shared by the Shell
// layer's stderr summary and the GCWP `statistics` / `completed` events
// (docs/protocol/07-statistics.md).
func (s *Stats) statisticsJSON() *orderedMap {
	files := &orderedMap{}
	files.Set("scanned", s.FilesScanned)
	files.Set("processed", s.FilesProcessed)
	files.Set("failed", s.FilesFailed)

	text := &orderedMap{}
	text.Set("extracted", s.TextExtracted)
	text.Set("injected", s.TextInjected)
	text.Set("kept", s.AuthoredRestored)
	text.Set("stale", s.AuthoredStale)

	images := &orderedMap{}
	images.Set("extracted", s.ImagesExtracted)
	images.Set("injected", s.ImagesInjected)
	audio := &orderedMap{}
	audio.Set("extracted", s.AudioExtracted)
	audio.Set("injected", s.AudioInjected)
	video := &orderedMap{}
	video.Set("extracted", s.VideoExtracted)
	video.Set("injected", s.VideoInjected)

	output := &orderedMap{}
	output.Set("created", s.OutputCreated)
	output.Set("bytes_written", s.BytesWritten)

	validation := &orderedMap{}
	validation.Set("errors", s.ValidationErrors)
	validation.Set("warnings", s.ValidationWarnings)
	validation.Set("info", s.ValidationInfos)

	m := &orderedMap{}
	m.Set("files", files)
	m.Set("text", text)
	m.Set("images", images)
	m.Set("audio", audio)
	m.Set("video", video)
	m.Set("output", output)
	m.Set("validation", validation)
	m.Set("duration", s.Duration)
	if s.DryRun {
		m.Set("dry_run", true)
	}
	return m
}

// resolveInput returns the configured `input:` directory.
func resolveInput(cfg *Config, projectRoot string) (string, error) {
	raw, ok := cfg.String("input")
	if !ok || strings.TrimSpace(raw) == "" {
		return "", exitErr(ExitProjectConfig, "gallate.yaml has no `input:` setting")
	}
	path := resolvePath(raw, projectRoot)
	if !dirExists(path) {
		return "", exitErr(ExitInputNotFound, fmt.Sprintf("`input:` is not a directory: %s", path))
	}
	return path, nil
}

// ignoreRules merges the YAML `ignore:` list with `--ignore` flags.
// Per docs/shell-layer/09-std-flags.md § 9.2 the flag patterns MERGE
// with the YAML list; they do not replace it.
func ignoreRules(cfg *Config, args *Args) ([]ignoreRule, error) {
	patterns := append(append([]string{}, cfg.Ignore()...), args.Ignore...)
	rules, err := compileIgnore(patterns)
	if err != nil {
		return nil, exitErr(ExitProjectConfig, err.Error())
	}
	return rules, nil
}

// subMediaFilters resolves the per-media sub-media filters.
func subMediaFilters(cfg *Config, args *Args, enabled map[string]bool) (map[string][2]map[string]bool, error) {
	out := map[string][2]map[string]bool{}
	for _, kind := range []string{"image", "audio", "video"} {
		if !enabled[kind] {
			continue
		}
		inc, exc, err := cfg.subMediaFilter(args, kind)
		if err != nil {
			return nil, err
		}
		out[kind] = [2]map[string]bool{inc, exc}
	}
	return out, nil
}

// doExtract performs the extract operation.
func doExtract(args *Args, rep *Reporter) (*Stats, error) {
	start := time.Now()
	stats := &Stats{Operation: "extract", DryRun: args.DryRun}

	cfg, err := LoadConfig(args.GallateYAML)
	if err != nil {
		return nil, err
	}
	tc, err := cfg.Text()
	if err != nil {
		return nil, err
	}
	input, err := resolveInput(cfg, args.ProjectRoot)
	if err != nil {
		return nil, err
	}
	rules, err := ignoreRules(cfg, args)
	if err != nil {
		return nil, err
	}

	pre, post := cfg.Scripts()
	if args.DryRun {
		if len(pre) > 0 {
			rep.note("dry run: skipping %d pre script(s)", len(pre))
		}
	} else if err := runScripts(pre, args.ProjectRoot, "pre", rep); err != nil {
		return nil, err
	}

	// ----- text media (the standard baseline: always processed) -----
	rep.phase("scanning")
	rep.file("scan", args.GallateYAML)
	findings, err := extractDirectory(input, rules, rep)
	if err != nil {
		return nil, exitErr(ExitExtractionFailure, "cannot scan input: "+err.Error())
	}
	astFallbackValidate(findings, input, args, rep)
	units := buildUnits(findings, tc)
	groupUnitsByLine(units)
	// Authored fields from a previous extraction survive (or are dropped
	// as stale when their source moved) — re-extracting must not destroy
	// translated work. Reading the previous unit files has no side
	// effects, so a dry run reports the same numbers a real run would.
	prev, aerr := loadAuthored(args.ProjectRoot)
	if aerr != nil {
		return nil, aerr
	}
	restored, stale := applyAuthored(units, prev)
	stats.AuthoredRestored = restored
	stats.AuthoredStale = stale
	if restored > 0 {
		rep.note("kept %d existing translation(s)", restored)
	}
	if stale > 0 {
		rep.warn("SOURCE_DRIFT",
			fmt.Sprintf("%d unit(s) kept their position but not their source; "+
				"their translations were dropped and they are marked needs_review", stale), "")
	}
	stats.FilesScanned = len(findings)
	stats.TextExtracted = len(units)
	rep.phase("extracting")
	for i, f := range findings {
		rep.progress(i+1, len(findings), false)
		rep.file("extract", f.rel)
	}
	if len(findings) == 0 {
		rep.progress(0, 0, true)
	}

	plannedUnits, err := planUnitFiles(units, args.ProjectRoot, cfg, tc)
	if err != nil {
		return nil, err
	}
	if !args.DryRun {
		rep.phase("writing")
		if err := commitUnitFiles(plannedUnits); err != nil {
			return nil, err
		}
		for _, w := range plannedUnits {
			rep.file("write", w.rel)
		}
	}
	stats.FilesProcessed = len(plannedUnits)
	stats.OutputCreated = len(plannedUnits)
	for _, w := range plannedUnits {
		stats.BytesWritten += int64(len(w.data))
	}

	// ----- engine-extension media (image / audio / video) -----
	//
	// Per docs/shell-layer/04-media.md § 4.4 / § 4.5: media flags
	// completely override the YAML list; with neither, only text is
	// processed. `text` is the baseline and cannot be switched off.
	enabled := args.effectiveMedia(cfg)
	filters, err := subMediaFilters(cfg, args, enabled)
	if err != nil {
		return nil, err
	}
	inv, sidecars, err := extractMedia(input, args.ProjectRoot, filters, map[string]bool{
		"image": enabled["image"], "audio": enabled["audio"], "video": enabled["video"],
	}, rules, rep, args.DryRun)
	if err != nil {
		return nil, err
	}
	stats.ImagesExtracted = len(inv.images)
	stats.AudioExtracted = len(inv.audios)
	stats.VideoExtracted = len(inv.videos)
	stats.OutputCreated += len(inv.images) + len(inv.audios) + len(inv.videos)

	// ----- .meta.json -----
	if !args.DryRun {
		existing, err := readMeta(args.ProjectRoot)
		if err != nil {
			return nil, err
		}
		entries := foreignMetaEntries(existing) // never clobber another CLI's entries
		for _, w := range plannedUnits {
			e, err := textMetaEntry(args.ProjectRoot, w)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
		for _, kind := range []string{"image", "audio", "video"} {
			assets := map[string][]mediaClass{
				"image": inv.images, "audio": inv.audios, "video": inv.videos,
			}[kind]
			for i, asset := range assets {
				if i < len(sidecars[kind]) {
					entries = append(entries, mediaMetaEntry(asset, sidecars[kind][i], args.ProjectRoot))
				}
			}
		}
		if err := writeMeta(args.ProjectRoot, newMetaDoc(entries)); err != nil {
			return nil, err
		}
	}

	if !args.DryRun {
		if err := runScripts(post, args.ProjectRoot, "post", rep); err != nil {
			return nil, err
		}
	}
	if args.DryRun {
		rep.note("dry run: %d unit file(s) and %d media sidecar(s) planned; nothing written",
			len(plannedUnits), stats.ImagesExtracted+stats.AudioExtracted+stats.VideoExtracted)
	}
	stats.Duration = time.Since(start).Seconds()
	return stats, nil
}

// sourcePlan is one source file's rebuilt contents, ready to write.
type sourcePlan struct {
	rel      string
	dest     string
	data     []byte
	injected int
}

// unresolvedSpanError is returned when a unit without a recorded byte
// span cannot be matched back to its source string.
func unresolvedSpanError(u loadedUnit, file string) error {
	return exitErr(ExitInjectionFailure, fmt.Sprintf(
		"unit %s in %s has no byte span and its source could not be re-located (source drift)",
		u.ID, file))
}

// doInject performs the inject operation.
func doInject(args *Args, rep *Reporter) (*Stats, error) {
	start := time.Now()
	stats := &Stats{Operation: "inject", DryRun: args.DryRun}

	cfg, err := LoadConfig(args.GallateYAML)
	if err != nil {
		return nil, err
	}
	input, err := resolveInput(cfg, args.ProjectRoot)
	if err != nil {
		return nil, err
	}
	rules, err := ignoreRules(cfg, args)
	if err != nil {
		return nil, err
	}
	inPlace, outputRoot, err := args.inPlace(cfg)
	if err != nil {
		return nil, err
	}

	pre, post := cfg.Scripts()
	if args.DryRun {
		if len(pre) > 0 {
			rep.note("dry run: skipping %d pre script(s)", len(pre))
		}
	} else if err := runScripts(pre, args.ProjectRoot, "pre", rep); err != nil {
		return nil, err
	}

	rep.phase("reading")
	units, fromMeta, err := loadUnits(args.ProjectRoot)
	if err != nil {
		return nil, err
	}
	if len(units) > 0 && !fromMeta {
		rep.warn("META_MISSING",
			".meta.json not found; the project-file → source mapping was read from the unit files themselves",
			args.ProjectRoot)
	}

	// Group by source resource so each file is read once.
	bySource := map[string][]loadedUnit{}
	var order []string
	for _, u := range units {
		if u.SourceFile == "" {
			return nil, exitErr(ExitProjectConfig,
				fmt.Sprintf("unit %s has no source_file mapping; .meta.json is incomplete", u.ID))
		}
		if ignored(rules, u.SourceFile, false) {
			rep.file("skip", u.SourceFile)
			continue
		}
		if _, seen := bySource[u.SourceFile]; !seen {
			order = append(order, u.SourceFile)
		}
		bySource[u.SourceFile] = append(bySource[u.SourceFile], u)
	}
	sort.Strings(order)

	// ----- Pass 1: validate every gate, rebuild in memory -----
	rep.phase("validating")
	var plans []sourcePlan
	for _, rel := range order {
		group := bySource[rel]
		sourceAbs := filepath.Join(input, filepath.FromSlash(rel))
		if !fileExists(sourceAbs) {
			return nil, exitErr(ExitInputNotFound,
				"source file listed in the project does not exist: "+sourceAbs)
		}
		raw, rerr := os.ReadFile(sourceAbs)
		if rerr != nil {
			return nil, exitErr(ExitInputNotFound, rerr.Error())
		}
		rep.file("read", rel)
		rep.progress(len(plans)+1, len(order), false)

		type edit struct {
			offset, length int
			source, target string
			id             string
		}
		var edits []edit
		for _, u := range group {
			if u.Target == "" {
				stats.UnitsSkippedEmpty++
				continue
			}
			offset, length := u.Offset, u.Length
			if !u.HasSpan {
				var e error
				offset, length, e = relocalize(raw, u)
				if e != nil {
					return nil, e
				}
			}
			if !utf8.ValidString(u.Target) {
				return nil, exitErr(ExitInjectionFailure,
					"target of unit "+u.ID+" is not valid UTF-8; refusing to write a replacement character")
			}
			edits = append(edits, edit{offset: offset, length: length, source: u.Source, target: u.Target, id: u.ID})
			stats.TextInjected++
		}
		if len(edits) == 0 {
			continue
		}
		sort.SliceStable(edits, func(i, j int) bool { return edits[i].offset < edits[j].offset })

		rewritten := make([]byte, len(raw))
		copy(rewritten, raw)
		// Rewrite back-to-front so earlier offsets stay valid.
		for i := len(edits) - 1; i >= 0; i-- {
			e := edits[i]
			if e.offset < 0 || e.offset+e.length > len(raw) {
				return nil, exitErr(ExitInjectionFailure, fmt.Sprintf(
					"unit %s in %s records a byte span outside the file (offset %d, length %d)",
					e.id, rel, e.offset, e.length))
			}
			firstQuote := bytes.IndexByte(raw[e.offset:e.offset+e.length], '"')
			lastQuote := bytes.LastIndexByte(raw[e.offset:e.offset+e.length], '"')
			if firstQuote < 0 || lastQuote < 0 || lastQuote <= firstQuote {
				// The recorded span no longer holds a quoted literal: the
				// source changed under us. This is source drift, and it is
				// reported as such rather than as a separate error class,
				// because "the file moved" and "the file changed" have the
				// same safe answer: write nothing.
				stats.UnitsDrifted++
				return nil, exitErr(ExitInjectionFailure, fmt.Sprintf(
					"source drift in %s unit %q (no quoted literal at offset %d)",
					rel, e.source, e.offset))
			}
			bodyStart := e.offset + firstQuote + 1
			bodyEnd := e.offset + lastQuote
			body := raw[bodyStart:bodyEnd]
			expect := []byte(e.source)
			if !bytes.Equal(body, expect) {
				stats.UnitsDrifted++
				return nil, exitErr(ExitInjectionFailure, fmt.Sprintf(
					"source drift in %s unit %q (expected %d bytes, found %d bytes at offset %d)",
					rel, e.source, len(expect), len(body), bodyStart))
			}
			validateTarget(e.id, e.source, e.target, args, rep, stats)
			newSpan := make([]byte, 0, e.length+len(e.target))
			newSpan = append(newSpan, raw[e.offset:bodyStart]...)
			newSpan = append(newSpan, []byte(e.target)...)
			newSpan = append(newSpan, raw[bodyEnd:e.offset+e.length]...)
			rewritten = append(append(append([]byte{}, rewritten[:e.offset]...), newSpan...), rewritten[e.offset+e.length:]...)
			stats.BytesWritten += int64(len(e.target))
		}
		dest := sourceAbs
		if !inPlace && outputRoot != "" {
			dest = filepath.Join(outputRoot, filepath.FromSlash(rel))
		}
		plans = append(plans, sourcePlan{rel: rel, dest: dest, data: rewritten, injected: len(edits)})
	}

	if stats.UnitsDrifted > 0 {
		stats.FilesFailed++
	}

	// ----- Pass 2: write -----
	if !args.DryRun {
		rep.phase("writing")
		for _, p := range plans {
			if err := atomicWriteBytes(p.dest, p.data); err != nil {
				return nil, exitErr(ExitOutputFailure, err.Error())
			}
			rep.file("modify", p.rel)
		}
	} else if len(plans) > 0 {
		rep.note("dry run: %d file(s) would be rewritten; nothing written", len(plans))
	}
	stats.FilesProcessed = len(plans)

	// ----- media -----
	mediaRes, err := injectMedia(args.ProjectRoot, input, inPlace, outputRoot, rules, rep)
	if err != nil {
		return nil, err
	}
	if args.DryRun {
		mediaRes = &mediaInjectResult{} // nothing was copied
	}
	stats.ImagesInjected = mediaRes.images
	stats.AudioInjected = mediaRes.audios
	stats.VideoInjected = mediaRes.videos
	stats.BytesWritten += mediaRes.bytes
	stats.FilesFailed += mediaRes.failed

	if !args.DryRun {
		if err := runScripts(post, args.ProjectRoot, "post", rep); err != nil {
			return nil, err
		}
	}
	stats.Duration = time.Since(start).Seconds()
	return stats, nil
}

// relocalize re-derives the byte span of a unit that has none recorded
// (`text.lifecycle.written_on: never`, or a Wrapper that dropped the
// location). The unit documents are caches, so re-extraction is the
// prescribed recovery (docs/shell-layer/12 § 12.13).
func relocalize(raw []byte, u loadedUnit) (int, int, error) {
	findings, err := extractFromRaw(raw, "", u.SourceFile, nil)
	if err != nil {
		return 0, 0, unresolvedSpanError(u, u.SourceFile)
	}
	var fallback *finding
	for i := range findings {
		f := &findings[i]
		if bodyOf(*f) != u.Source {
			continue
		}
		if u.Line > 0 && f.line == u.Line {
			return f.offset, f.length, nil
		}
		if fallback == nil {
			fallback = f
		}
	}
	if fallback != nil {
		return fallback.offset, fallback.length, nil
	}
	return 0, 0, unresolvedSpanError(u, u.SourceFile)
}

// bodyOf returns the inner content of a finding's quoted literal —
// the string the translator sees, and the value the drift gate compares.
func bodyOf(f finding) string {
	if m := bodyRE.FindStringSubmatch(f.text); m != nil {
		return m[1]
	}
	return f.text
}

// validateTarget runs the declared validation rules against one target.
//
// Findings are reported as `validation` events and counted, but they do
// not block the inject: every declared rule is severity `warning` or
// `info`, and per docs/protocol/09 § Diagnostics a warning MUST NOT
// change the exit code.
func validateTarget(id, source, target string, args *Args, rep *Reporter, stats *Stats) {
	emit := func(rule, severity, message string) {
		switch severity {
		case "error":
			stats.ValidationErrors++
		case "warning":
			stats.ValidationWarnings++
		default:
			stats.ValidationInfos++
		}
		rep.validation(map[string]any{
			"rule":             rule,
			"severity":         severity,
			"source":           source,
			"target":           target,
			"message":          message,
			"translation_unit": id,
		})
	}

	// double-quote-balance — an odd number of quotes truncates the
	// literal at Ren'Py parse time.
	if strings.Count(target, `"`)%2 != 0 {
		emit("double-quote-balance", "warning",
			`Target contains an unbalanced " — Ren'Py would truncate the literal.`)
	}

	// renpy-substitution-preserved — `[name]`-style substitutions must
	// survive translation.
	for _, m := range placeholderRE.FindAllStringSubmatch(source, -1) {
		if !strings.Contains(target, "["+m[1]+"]") {
			emit("renpy-substitution-preserved", "warning",
				"Required substitution is missing from the target: ["+m[1]+"]")
		}
	}

	// max-target-length — soft cap, configurable via
	// `--engine.max-length=N`.
	limit := 240
	if v, ok := args.EngineOpts["max-length"]; ok {
		if n, ok := asInt(v); ok && n > 0 {
			limit = n
		}
	}
	if len(target) > limit {
		emit("max-target-length", "info",
			fmt.Sprintf("Target is %d UTF-8 bytes, over the %d-byte soft cap.", len(target), limit))
	}
}
