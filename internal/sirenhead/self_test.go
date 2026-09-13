package sirenhead

// Self-tests: the guarantees the README claims, asserted against real
// files on disk. Every assertion is a byte-level or exit-code-level
// judgement; "it ran without error" is never the standard.
//
//  1. extract → inject with empty targets is byte-identical.
//  2. N translated units ⇒ exactly N changed lines, line count preserved.
//  3. Source drift is detected (exit 8) and NOTHING is written.
//  4. Re-extraction is idempotent (stable ids and .meta.json).
//  5. GCWP handshake + extract over stdin.
//  6. Media round-trip: empty inject byte-identical; a target is copied.
//  7. Extraction completeness: every `_("…")` payload is captured, every
//     recorded byte span holds its own source, line numbers are right.
//  8. The three shapes that once shipped silently dropped.
//  9. --dry-run writes nothing; --ignore is honoured and merged.
// 10. Media gating: flags override the YAML list; text is the baseline.
// 11. Layout modes (flat / mirror / single) all round-trip byte-stable.
// 12. Another CLI's .meta.json entries survive a re-extract.
// 13. Exit codes follow docs/shell-layer/10 § 10.3.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type harness struct {
	t       *testing.T
	root    string
	game    string
	project string
}

// newHarness builds a scratch game + project pair.
func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	game := filepath.Join(root, "game")
	if err := writeFixtures(game); err != nil {
		t.Fatalf("writing fixtures: %v", err)
	}
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("creating project: %v", err)
	}
	h := &harness{t: t, root: root, game: game, project: project}
	h.writeYAML("input: " + game + "\nmedia:\n  - text\ntext:\n  format: json\n  layout: flat\n")
	return h
}

func (h *harness) gallateYAML() string { return filepath.Join(h.project, "gallate.yaml") }

func (h *harness) writeYAML(body string) {
	h.t.Helper()
	if err := os.WriteFile(h.gallateYAML(), []byte(body), 0o644); err != nil {
		h.t.Fatalf("writing gallate.yaml: %v", err)
	}
}

// run invokes the CLI in-process with a terminal stdin (Shell layer).
func (h *harness) run(args ...string) (code int, stdout, stderr string) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code = Run(args, strings.NewReader(""), &out, &errOut, true)
	return code, out.String(), errOut.String()
}

// runGCWP invokes the CLI with a Wrapper-style stdin (Protocol layer).
func (h *harness) runGCWP(line string) (code int, stdout, stderr string) {
	h.t.Helper()
	var out, errOut bytes.Buffer
	code = Run(nil, strings.NewReader(line+"\n"), &out, &errOut, false)
	return code, out.String(), errOut.String()
}

// readJSON loads a JSON file.
func (h *harness) readJSON(path string) map[string]any {
	h.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("reading %s: %v", path, err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		h.t.Fatalf("parsing %s: %v", path, err)
	}
	return v
}

// entries returns the entries of a unit document.
func entries(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, _ := doc["entries"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// setTargets writes translations into the unit files and returns how
// many units were touched.
func (h *harness) setTargets(edits map[string]string) int {
	h.t.Helper()
	n := 0
	dir := filepath.Join(h.project, "text", "units")
	files, err := os.ReadDir(dir)
	if err != nil {
		h.t.Fatalf("reading units dir: %v", err)
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Name())
		doc := h.readJSON(path)
		changed := false
		for _, e := range entries(h.t, doc) {
			src, _ := e["source"].(string)
			if tr, ok := edits[src]; ok {
				e["target"] = tr
				e["state"] = "translated"
				changed = true
				n++
			}
		}
		if changed {
			data, err := marshalJSON(doc)
			if err != nil {
				h.t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	return n
}

// ---- 1. byte-identical round-trip -----------------------------------------

func TestRoundTripByteIdentical(t *testing.T) {
	h := newHarness(t)
	script := filepath.Join(h.game, "script.rpy")
	screens := filepath.Join(h.game, "screens.rpy")
	beforeScript := readBytes(t, script)
	beforeScreens := readBytes(t, screens)

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if code, _, stderr := h.run("-it", h.gallateYAML()); code != ExitOK {
		t.Fatalf("inject exit=%d stderr=%s", code, stderr)
	}
	if got := readBytes(t, script); !bytes.Equal(got, beforeScript) {
		t.Errorf("script.rpy changed after an empty-target round-trip")
	}
	if got := readBytes(t, screens); !bytes.Equal(got, beforeScreens) {
		t.Errorf("screens.rpy changed after an empty-target round-trip")
	}
}

// ---- 2. minimal diff -------------------------------------------------------

func TestMinimalDiffInject(t *testing.T) {
	h := newHarness(t)
	script := filepath.Join(h.game, "script.rpy")
	screens := filepath.Join(h.game, "screens.rpy")

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	edits := map[string]string{
		"Hello there [name].":          "你好, [name]。",
		"Suddenly...Out of nowhere...": "突然...不知从哪里...",
		"Run away":                     "逃跑",
		"Stand your ground":            "站稳脚跟",
		"This is a test.":              "这是一个测试。",
	}
	edited := h.setTargets(edits)
	if edited != len(edits) {
		t.Fatalf("set %d target(s), expected %d", edited, len(edits))
	}

	preScript, preScreens := readBytes(t, script), readBytes(t, screens)
	if code, _, stderr := h.run("-it", h.gallateYAML()); code != ExitOK {
		t.Fatalf("inject exit=%d stderr=%s", code, stderr)
	}
	diff, sameCount := countChangedLines(preScript, readBytes(t, script), preScreens, readBytes(t, screens))
	if !sameCount {
		t.Errorf("line count changed: the CLI must not add or remove lines")
	}
	if diff != edited {
		t.Errorf("%d changed line(s), expected exactly %d", diff, edited)
	}
}

// countChangedLines reports how many lines differ across both files and
// whether every line count was preserved.
func countChangedLines(a, b, c, d []byte) (int, bool) {
	diff := 0
	same := true
	for _, pair := range [][2][]byte{{a, b}, {c, d}} {
		pre := strings.Split(string(pair[0]), "\n")
		post := strings.Split(string(pair[1]), "\n")
		if len(pre) != len(post) {
			same = false
			continue
		}
		for i := range pre {
			if pre[i] != post[i] {
				diff++
			}
		}
	}
	return diff, same
}

// ---- 3. source drift -------------------------------------------------------

func TestSourceDriftDetection(t *testing.T) {
	h := newHarness(t)
	script := filepath.Join(h.game, "script.rpy")

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if n := h.setTargets(map[string]string{"Suddenly...Out of nowhere...": "翻译"}); n != 1 {
		t.Fatalf("expected 1 unit to translate, got %d", n)
	}

	// Change the source AFTER extracting, so the unit's recorded source
	// no longer matches the file.
	original := readBytes(t, script)
	old := []byte("Suddenly...Out of nowhere...")
	replacement := []byte("Suddenly...Out of some OTHER place...")
	if !bytes.Contains(original, old) {
		t.Fatalf("fixture no longer contains the drift probe string")
	}
	drifted := bytes.Replace(original, old, replacement, 1)
	if err := os.WriteFile(script, drifted, 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := h.run("-it", h.gallateYAML())
	if code != ExitInjectionFailure {
		t.Errorf("drift exit=%d, expected %d", code, ExitInjectionFailure)
	}
	if !strings.Contains(stderr, "drift") {
		t.Errorf("stderr does not mention drift: %q", stderr)
	}
	after := readBytes(t, script)
	if bytes.Contains(after, []byte("翻译")) {
		t.Errorf("the translation was written despite the drift abort")
	}
	if !bytes.Equal(after, drifted) {
		t.Errorf("the source file was modified by a failed inject")
	}
}

// ---- 4. idempotent re-extraction ------------------------------------------

func TestIdempotentReExtraction(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	first := h.readJSON(filepath.Join(h.project, ".meta.json"))
	firstIDs := unitIDs(t, h.readJSON(filepath.Join(h.project, "text", "units", "script.json")))

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("second extract exit=%d stderr=%s", code, stderr)
	}
	second := h.readJSON(filepath.Join(h.project, ".meta.json"))
	secondIDs := unitIDs(t, h.readJSON(filepath.Join(h.project, "text", "units", "script.json")))

	delete(first, "generated_at")
	delete(second, "generated_at")
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Errorf(".meta.json is not stable across re-extraction")
	}
	if strings.Join(firstIDs, ",") != strings.Join(secondIDs, ",") {
		t.Errorf("position-derived ids drifted: %v vs %v", firstIDs, secondIDs)
	}
}

func unitIDs(t *testing.T, doc map[string]any) []string {
	t.Helper()
	var out []string
	for _, e := range entries(t, doc) {
		id, _ := e["id"].(string)
		out = append(out, id)
	}
	return out
}

// ---- 5. GCWP handshake + extract ------------------------------------------

func TestGCWPHandshakeAndExtract(t *testing.T) {
	h := newHarness(t)

	code, stdout, stderr := h.runGCWP(`{"type":"protocol","name":"gcwp","version":"1.0"}`)
	if code != GCWPOK {
		t.Fatalf("handshake exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"supported":true`) {
		t.Errorf("handshake reply missing supported flag: %s", stdout)
	}
	if !strings.Contains(stdout, `"protocol":{"name":"gcwp","version":"1.0"}`) {
		t.Errorf("handshake reply is not schema-shaped: %s", stdout)
	}

	req := `{"type":"request","id":"01HEXTRACT","operation":"extract","input":[{"path":"` +
		h.gallateYAML() + `","kind":"file"}]}`
	code, stdout, stderr = h.runGCWP(req)
	if code != GCWPOK {
		t.Fatalf("GCWP extract exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{`"event":"started"`, `"event":"progress"`, `"event":"file"`,
		`"event":"statistics"`, `"event":"completed"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("event stream missing %s: %s", want, stdout)
		}
	}
	// stdout must be pure JSON Lines: no banner, no human text.
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("stdout line is not JSON: %q (%v)", line, err)
		}
	}
}

// ---- 6. media round-trip ---------------------------------------------------

func TestMediaRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.writeYAML("input: " + h.game + "\nmedia:\n  - text\n  - image\n  - audio\n  - video\n")

	if code, _, stderr := h.run("-etiava", h.gallateYAML()); code != ExitOK {
		t.Fatalf("media extract exit=%d stderr=%s", code, stderr)
	}
	for _, kind := range []string{"image", "audio", "video"} {
		sidecars := globJSON(t, filepath.Join(h.project, kind))
		if len(sidecars) == 0 {
			t.Fatalf("no %s sidecar was written", kind)
		}
	}

	// Empty targets ⇒ every media file byte-identical.
	media := mediaFiles(t, h.game)
	if code, _, stderr := h.run("-itiava", h.gallateYAML()); code != ExitOK {
		t.Fatalf("media inject exit=%d stderr=%s", code, stderr)
	}
	for path, original := range media {
		if !bytes.Equal(readBytes(t, path), original) {
			t.Errorf("%s changed after an empty-target inject", path)
		}
	}

	// A non-empty target must be copied over the original.
	var sidecars []string
	for _, kind := range []string{"image", "audio", "video"} {
		sidecars = append(sidecars, globJSON(t, filepath.Join(h.project, kind))...)
	}
	sort.Strings(sidecars)
	first := sidecars[0]
	doc := h.readJSON(first)
	replacement := filepath.Join(h.project, "replacement.bin")
	want := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4}
	if err := os.WriteFile(replacement, want, 0o644); err != nil {
		t.Fatal(err)
	}
	entry := entries(t, doc)[0]
	entry["target"] = replacement
	entry["state"] = "translated"
	writeJSON(t, first, doc)

	sourceRel := entry["metadata"].(map[string]any)["source_path"].(string)
	dest := filepath.Join(h.game, filepath.FromSlash(sourceRel))
	before := readBytes(t, dest)
	if code, _, stderr := h.run("-itiava", h.gallateYAML()); code != ExitOK {
		t.Fatalf("media inject with target exit=%d stderr=%s", code, stderr)
	}
	if got := readBytes(t, dest); !bytes.Equal(got, want) {
		t.Errorf("replacement was not copied into place")
	}
	if bytes.Equal(before, want) {
		t.Errorf("test is vacuous: replacement equals the original")
	}
}

func mediaFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".jpg", ".wav", ".ogg", ".ogv":
			out[path] = readBytes(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func globJSON(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".json") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ---- 7. extraction completeness (`_()` + byte spans + line numbers) -------

func TestExtractionCompletenessWrapped(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	doc := h.readJSON(filepath.Join(h.project, "text", "units", "screens.json"))

	sources := map[string]bool{}
	for _, e := range entries(t, doc) {
		src, _ := e["source"].(string)
		sources[src] = true
	}
	for _, want := range expectedWrapped {
		if !sources[want] {
			t.Errorf("`_(%q)` was not extracted", want)
		}
	}

	// Every recorded byte span must hold its own source, byte for byte —
	// this is the assertion that catches character-vs-byte offset drift.
	srcBytes := readBytes(t, filepath.Join(h.game, "screens.rpy"))
	for _, e := range entries(t, doc) {
		meta, _ := e["metadata"].(map[string]any)
		off := int(meta["source_offset"].(float64))
		length := int(meta["source_length"].(float64))
		span := srcBytes[off : off+length]
		q1 := bytes.IndexByte(span, '"')
		q2 := bytes.LastIndexByte(span, '"')
		if q1 < 0 || q2 <= q1 {
			t.Errorf("no quote pair inside the span for %v", e["id"])
			continue
		}
		if got, want := string(span[q1+1:q2]), e["source"].(string); got != want {
			t.Errorf("span for %v holds %q, source is %q", e["id"], got, want)
		}
	}

	// Wrapped entries must sit inside a `_( … )` call.
	for _, e := range entries(t, doc) {
		src, _ := e["source"].(string)
		if !containsString(expectedWrapped, src) {
			continue
		}
		meta := e["metadata"].(map[string]any)
		off := int(meta["source_offset"].(float64))
		length := int(meta["source_length"].(float64))
		span := string(srcBytes[off : off+length])
		if !strings.HasPrefix(span, `_("`) || !strings.HasSuffix(span, `")`) {
			t.Errorf("wrapped span for %v does not cover `_(\"…\")`: %q", e["id"], span)
		}
	}

	// Line numbers are 1-based and match the fixture.
	wantLine := 0
	for i, line := range strings.Split(screensContent, "\r\n") {
		if strings.Contains(line, `_("Back")`) {
			wantLine = i + 1
		}
	}
	gotLine := 0
	for _, e := range entries(t, doc) {
		if e["source"] == "Back" {
			ctx := e["source_context"].(map[string]any)
			gotLine = int(ctx["line"].(float64))
		}
	}
	if wantLine == 0 || gotLine != wantLine {
		t.Errorf("line number for 'Back' is %d, want %d", gotLine, wantLine)
	}
}

// ---- 8. the three silent-drop gaps ----------------------------------------

func TestSilentDropGaps(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	doc := h.readJSON(filepath.Join(h.project, "text", "units", "script.json"))

	got := map[string][]string{}
	for _, e := range entries(t, doc) {
		meta, _ := e["metadata"].(map[string]any)
		kind, _ := meta["renpy_kind"].(string)
		src, _ := e["source"].(string)
		got[src] = append(got[src], kind)
	}
	for literal, wantKind := range expectedScript {
		kinds := got[literal]
		if len(kinds) != 1 || kinds[0] != wantKind {
			t.Errorf("%q extracted as %v, want exactly [%s]", literal, kinds, wantKind)
		}
	}

	// And every span still holds its own source.
	srcBytes := readBytes(t, filepath.Join(h.game, "script.rpy"))
	for _, e := range entries(t, doc) {
		meta := e["metadata"].(map[string]any)
		off := int(meta["source_offset"].(float64))
		length := int(meta["source_length"].(float64))
		span := srcBytes[off : off+length]
		q1 := bytes.IndexByte(span, '"')
		q2 := bytes.LastIndexByte(span, '"')
		if q1 < 0 || q2 <= q1 {
			t.Errorf("no quote pair in span for %v", e["id"])
			continue
		}
		if body, want := string(span[q1+1:q2]), e["source"].(string); body != want {
			t.Errorf("span for %v holds %q, want %q", e["id"], body, want)
		}
	}
}

// ---- 9. dry run and ignore -------------------------------------------------

func TestDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	before := map[string][]byte{}
	for path, data := range mediaFiles(t, h.game) {
		before[path] = data
	}
	scriptBefore := readBytes(t, filepath.Join(h.game, "script.rpy"))

	code, stdout, stderr := h.run("-et", h.gallateYAML(), "--dry-run")
	if code != ExitOK {
		t.Fatalf("dry-run extract exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "dry run") {
		t.Errorf("dry-run said nothing on stderr: %q", stderr)
	}
	if stdout == "" {
		t.Errorf("dry run must still print the result path on stdout")
	}
	if _, err := os.Stat(filepath.Join(h.project, "text")); !os.IsNotExist(err) {
		t.Errorf("dry-run extract created text/")
	}
	if _, err := os.Stat(filepath.Join(h.project, ".meta.json")); !os.IsNotExist(err) {
		t.Errorf("dry-run extract created .meta.json")
	}

	// Real extract, then a dry-run inject must not touch the game.
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if n := h.setTargets(map[string]string{"Run away": "逃跑"}); n != 1 {
		t.Fatalf("expected 1 edit, got %d", n)
	}
	if code, _, stderr := h.run("-it", h.gallateYAML(), "--dry-run"); code != ExitOK {
		t.Fatalf("dry-run inject exit=%d stderr=%s", code, stderr)
	}
	if !bytes.Equal(readBytes(t, filepath.Join(h.game, "script.rpy")), scriptBefore) {
		t.Errorf("dry-run inject modified the source file")
	}
	for path, data := range before {
		if !bytes.Equal(readBytes(t, path), data) {
			t.Errorf("dry-run inject modified %s", path)
		}
	}
}

func TestIgnorePatternsMergeAndApply(t *testing.T) {
	h := newHarness(t)
	h.writeYAML("input: " + h.game + "\nmedia:\n  - text\nignore:\n  - \"screens.rpy\"\n")

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(h.project, "text", "units", "screens.json")); !os.IsNotExist(err) {
		t.Errorf("YAML ignore pattern did not exclude screens.rpy")
	}

	// The CLI flag merges with (does not replace) the YAML list, so with
	// both patterns in force nothing under the game root is extracted.
	// Clear the previous run's output first — this run must produce the
	// files itself if the ignore rules are not honoured.
	if err := os.RemoveAll(filepath.Join(h.project, "text")); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := h.run("-et", h.gallateYAML(), "--ignore", "script.rpy"); code != ExitOK {
		t.Fatalf("extract with --ignore exit=%d stderr=%s", code, stderr)
	}
	for _, name := range []string{"screens.json", "script.json"} {
		if _, err := os.Stat(filepath.Join(h.project, "text", "units", name)); !os.IsNotExist(err) {
			t.Errorf("%s was extracted despite being ignored", name)
		}
	}
}

// ---- 10. media gating ------------------------------------------------------

func TestMediaGating(t *testing.T) {
	h := newHarness(t)
	h.writeYAML("input: " + h.game + "\nmedia:\n  - text\n")

	if code, _, stderr := h.run("-e", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	for _, kind := range []string{"image", "audio", "video"} {
		if _, err := os.Stat(filepath.Join(h.project, kind)); !os.IsNotExist(err) {
			t.Errorf("`media: [text]` + `-e` created %s/", kind)
		}
	}

	// Flags completely override the YAML list — and `text` stays because
	// it is the standard baseline, not because the flag asked for it.
	if code, _, stderr := h.run("-ei", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract -ei exit=%d stderr=%s", code, stderr)
	}
	if !dirExists(filepath.Join(h.project, "image")) {
		t.Errorf("`-ei` did not extract image")
	}
	for _, kind := range []string{"audio", "video"} {
		if _, err := os.Stat(filepath.Join(h.project, kind)); !os.IsNotExist(err) {
			t.Errorf("`-ei` extracted %s, which was not requested", kind)
		}
	}
	if !fileExists(filepath.Join(h.project, "text", "units", "script.json")) {
		t.Errorf("`-ei` dropped the text baseline")
	}
}

func TestSubMediaFilterMisconfiguration(t *testing.T) {
	h := newHarness(t)
	h.writeYAML("input: " + h.game + "\nmedia:\n  - text\n  - image\n" +
		"engine:\n  image:\n    includes:\n      - background\n    excludes:\n      - ui\n")
	code, _, stderr := h.run("-e", h.gallateYAML())
	if code != ExitProjectConfig {
		t.Errorf("exclude-not-in-includes exit=%d, want %d (stderr=%s)", code, ExitProjectConfig, stderr)
	}
	if !strings.Contains(stderr, "excludes") {
		t.Errorf("error does not name the offending key: %q", stderr)
	}
}

// ---- 11. layout modes ------------------------------------------------------

func TestLayoutModes(t *testing.T) {
	for _, layout := range []string{"flat", "mirror", "single"} {
		t.Run(layout, func(t *testing.T) {
			h := newHarness(t)
			h.writeYAML("input: " + h.game + "\nmedia:\n  - text\ntext:\n  layout: " + layout + "\n")
			beforeScript := readBytes(t, filepath.Join(h.game, "script.rpy"))
			beforeScreens := readBytes(t, filepath.Join(h.game, "screens.rpy"))

			if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
				t.Fatalf("extract exit=%d stderr=%s", code, stderr)
			}
			units := globJSON(t, filepath.Join(h.project, "text"))
			if len(units) == 0 {
				t.Fatalf("layout %s wrote no unit file", layout)
			}
			if code, _, stderr := h.run("-it", h.gallateYAML()); code != ExitOK {
				t.Fatalf("inject exit=%d stderr=%s", code, stderr)
			}
			if !bytes.Equal(readBytes(t, filepath.Join(h.game, "script.rpy")), beforeScript) ||
				!bytes.Equal(readBytes(t, filepath.Join(h.game, "screens.rpy")), beforeScreens) {
				t.Errorf("layout %s did not round-trip byte-identically", layout)
			}
		})
	}
}

// ---- 12. .meta.json ownership ---------------------------------------------

func TestForeignMetaEntriesPreserved(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	path := filepath.Join(h.project, ".meta.json")
	doc := h.readJSON(path)
	foreign := map[string]any{
		"project": "text/units/other.json",
		"source":  "scenes/intro.bin",
		"type":    "text",
		"cli":     map[string]any{"id": "artemis", "version": "1.2.0"},
	}
	doc["files"] = append(doc["files"].([]any), foreign)
	writeJSON(t, path, doc)

	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("second extract exit=%d stderr=%s", code, stderr)
	}
	after := h.readJSON(path)
	found := false
	for _, item := range after["files"].([]any) {
		m := item.(map[string]any)
		if cli, ok := m["cli"].(map[string]any); ok && cli["id"] == "artemis" {
			found = true
		}
	}
	if !found {
		t.Errorf("another CLI's .meta.json entry was silently dropped")
	}
}

// ---- 13. exit codes --------------------------------------------------------

func TestExitCodes(t *testing.T) {
	h := newHarness(t)
	missing := filepath.Join(h.root, "nope", "gallate.yaml")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no arguments", []string{}, ExitUsage},
		{"unknown option", []string{"-et", h.gallateYAML(), "--bogus"}, ExitUsage},
		{"missing target", []string{"-et"}, ExitUsage},
		{"target not found", []string{"-et", missing}, ExitUsage},
		{"bad media letter", []string{"-ex", h.gallateYAML()}, ExitUsage},
		{"help", []string{"--help"}, ExitOK},
		{"version", []string{"--version"}, ExitOK},
		{"manifest", []string{"manifest"}, ExitOK},
		{"features", []string{"features"}, ExitOK},
		{"validation", []string{"validation"}, ExitOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := h.run(tc.args...)
			if code != tc.want {
				t.Errorf("exit=%d, want %d", code, tc.want)
			}
		})
	}

	// A gallate.yaml with no `input:` is an invalid project configuration.
	h.writeYAML("media:\n  - text\n")
	if code, _, _ := h.run("-et", h.gallateYAML()); code != ExitProjectConfig {
		t.Errorf("missing input: exit=%d, want %d", code, ExitProjectConfig)
	}

	// An `input:` that does not exist is "Input Not Found".
	h.writeYAML("input: ./does-not-exist\nmedia:\n  - text\n")
	if code, _, _ := h.run("-et", h.gallateYAML()); code != ExitInputNotFound {
		t.Errorf("missing input dir exit=%d, want %d", code, ExitInputNotFound)
	}

	// Malformed YAML is also an invalid project configuration.
	h.writeYAML("input: [unterminated\n")
	if code, _, _ := h.run("-et", h.gallateYAML()); code != ExitProjectConfig {
		t.Errorf("malformed YAML exit=%d, want %d", code, ExitProjectConfig)
	}
}

func TestGCWPExitCodes(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		line string
		want int
	}{
		{"unsupported operation", `{"type":"request","id":"01H1","operation":"demux"}`, GCWPUnsupportedOperation},
		{"invalid config", `{"type":"request","id":"01H2","operation":"extract","input":[{"path":"` +
			filepath.Join(h.root, "nope", "gallate.yaml") + `"}]}`, GCWPInvalidArguments},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := h.runGCWP(tc.line)
			if code != tc.want {
				t.Errorf("exit=%d, want %d", code, tc.want)
			}
		})
	}
	// Malformed stdin is a protocol error.
	code, _, _ := h.runGCWP(`{"type":`)
	if code != GCWPProtocolError {
		t.Errorf("malformed stdin exit=%d, want %d", code, GCWPProtocolError)
	}
}

// ---- 14. document shape ----------------------------------------------------

func TestUnitDocumentShape(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	path := filepath.Join(h.project, "text", "units", "script.json")
	raw := readBytes(t, path)
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Errorf("unit file does not end with a newline")
	}
	if bytes.Contains(raw, []byte(`\u003c`)) || bytes.Contains(raw, []byte(`\u0026`)) {
		t.Errorf("unit file HTML-escapes text")
	}

	var doc struct {
		Format  string           `json:"format"`
		Version int              `json:"version"`
		Source  string           `json:"source"`
		Target  string           `json:"target"`
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Format != UnitFormat || doc.Version != UnitVersion {
		t.Errorf("container is %s v%d, want %s v%d", doc.Format, doc.Version, UnitFormat, UnitVersion)
	}
	if doc.Source == "" || doc.Target == "" {
		t.Errorf("container language pair is incomplete: %q → %q", doc.Source, doc.Target)
	}
	for i, e := range doc.Entries {
		for _, key := range []string{"id", "source", "target", "state"} {
			if _, ok := e[key]; !ok {
				t.Errorf("entry %d has no %q", i, key)
			}
		}
		id, _ := e["id"].(string)
		if !strings.Contains(id, ":L") {
			t.Errorf("entry %d id %q is not position-derived", i, id)
		}
	}
	meta := h.readJSON(filepath.Join(h.project, ".meta.json"))
	if meta["schema_version"] != float64(MetaSchemaVersion) {
		t.Errorf(".meta.json schema_version=%v", meta["schema_version"])
	}
	gen := meta["generated_by"].(map[string]any)
	if gen["id"] != CLIID || gen["version"] != CLIVersion {
		t.Errorf("generated_by=%v, want %s %s", gen, CLIID, CLIVersion)
	}
}

func TestVersionHasSingleSourceOfTruth(t *testing.T) {
	h := newHarness(t)
	_, stdout, _ := h.run("--version")
	if !strings.Contains(stdout, CLIID+" "+CLIVersion) {
		t.Errorf("--version does not carry %s %s: %q", CLIID, CLIVersion, stdout)
	}
	_, stdout, _ = h.run("manifest")
	if !strings.Contains(stdout, `"version":"`+CLIVersion+`"`) {
		t.Errorf("manifest version does not match --version: %q", stdout)
	}
}

func TestTextMetaKeyMapping(t *testing.T) {
	h := newHarness(t)
	h.writeYAML("input: " + h.game + "\nmedia:\n  - text\n" +
		"text:\n  meta:\n    location: pos\n  metadata:\n    original_file: false\n    engine_path: false\n")
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	doc := h.readJSON(filepath.Join(h.project, "text", "units", "script.json"))
	for _, e := range entries(t, doc) {
		if _, ok := e["pos"]; !ok {
			t.Fatalf("text.meta.location mapping was ignored: %v", e)
		}
		if _, ok := e["original_file"]; ok {
			t.Errorf("original_file was emitted despite metadata.original_file=false")
		}
		if _, ok := e["engine_path"]; ok {
			t.Errorf("engine_path was emitted despite metadata.engine_path=false")
		}
	}

	// Round-trip still works with renamed keys and dropped metadata.
	before := readBytes(t, filepath.Join(h.game, "script.rpy"))
	if code, _, stderr := h.run("-it", h.gallateYAML()); code != ExitOK {
		t.Fatalf("inject exit=%d stderr=%s", code, stderr)
	}
	if !bytes.Equal(readBytes(t, filepath.Join(h.game, "script.rpy")), before) {
		t.Errorf("round-trip with a renamed metadata key is not byte-identical")
	}
}

func TestLifecycleNeverDropsMetadataAndStillInjects(t *testing.T) {
	h := newHarness(t)
	if code, _, stderr := h.run("-et", h.gallateYAML()); code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	if n := h.setTargets(map[string]string{"Run away": "逃跑"}); n != 1 {
		t.Fatalf("expected 1 edit, got %d", n)
	}
	// Re-extract with metadata emission disabled; the target is authored
	// and must survive, and inject must re-derive the location itself.
	h.writeYAML("input: " + h.game + "\nmedia:\n  - text\ntext:\n  lifecycle:\n    written_on: never\n")
	code, _, stderr := h.run("-et", h.gallateYAML())
	if code != ExitOK {
		t.Fatalf("extract exit=%d stderr=%s", code, stderr)
	}
	doc := h.readJSON(filepath.Join(h.project, "text", "units", "script.json"))
	for _, e := range entries(t, doc) {
		if _, ok := e["metadata"]; ok {
			t.Fatalf("lifecycle.written_on=never still wrote metadata: %v", e)
		}
		if _, ok := e["source_context"]; ok {
			t.Fatalf("lifecycle.written_on=never still wrote source_context")
		}
	}
	if code, _, stderr := h.run("-it", h.gallateYAML()); code != ExitOK {
		t.Fatalf("inject without spans exit=%d stderr=%s", code, stderr)
	}
}

// ---- helpers ---------------------------------------------------------------

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := marshalJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
