package sirenhead

// Ren'Py string extraction.
//
// The extractor is a set of *scoped regex passes* over the raw `.rpy`
// source. It is:
//
//   - deterministic across re-extractions (line + byte-offset derived,
//     no drifting counter),
//   - byte-stable (every position is a byte offset in the file, so the
//     inject pass patches exactly the same range later),
//   - strict about Ren'Py syntax: it matches only the shapes Ren'Py
//     actually treats as translatable dialog / UI text.
//
// Pass order IS priority. Structured, unambiguous shapes run first and
// claim their byte span (deduplicated by the *closing* quote offset, not
// the start offset — `style_prefix "choice"` is matched by two different
// passes at two different start offsets); the prose heuristic runs last,
// on bare indented strings only.
//
//	Pass 0.0  `_("…")`            → wrapped_text   (Ren'Py's explicit marker)
//	Pass 0.5  `"…":` menu branch  → menu_option    (structural)
//	Pass 0.6  `renpy.input("…")`  → ui_prompt      (inside `$` statements)
//	Pass 1    `<who> "…"`         → dialog         (say statement)
//	Pass 2    `text "…"`          → screen_text    (screen language)
//	Pass 3    `<indent>"…"`       → narrator       (prose heuristic)
//
// Pass 0.0 / 0.5 / 0.6 each exist because a whole class of player-visible
// text was once silently dropped while extract reported success — see
// CHANGELOG 0.3.0 and the repo README.

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Ren'Py identifier characters (the same set Python uses, and Ren'Py).
const identPat = `[A-Za-z_][A-Za-z0-9_]*`

// A double-quoted string literal: no embedded newline, backslash escapes
// allowed. The body is treated as opaque bytes for translation purposes.
const stringBodyPat = `"(?:[^"\\\n]|\\.)*"`

var (
	// sayRE — `<indent><identifier> <space> "<string>"` at end of line.
	sayRE = regexp.MustCompile(`[ \t]*(` + identPat + `)[ \t]+(` + stringBodyPat + `)[ \t]*(?:\r?\n|$)`)

	// narratorRE — a bare indented string on its own line, optionally
	// followed by `:` (menu-branch shape).
	narratorRE = regexp.MustCompile(`([ \t]+)(` + stringBodyPat + `)[ \t]*(?::|(?:\r?\n|$))`)

	// screenTextRE — screen-language text widgets.
	screenTextRE = regexp.MustCompile(`([ \t]+)(text|textbutton|label|button)[ \t]+(` + stringBodyPat + `)[ \t]*(?::|(?:\r?\n|$))`)

	// transWrapRE — Ren'Py's explicit translation marker `_("…")`.
	// The negative lookbehind of the reference implementation
	// (`(?<![A-Za-z0-9_])`) is emulated in code: Go's regexp (RE2) has no
	// lookbehind.
	transWrapRE = regexp.MustCompile(`_\((` + stringBodyPat + `)\)`)

	// menuOptionRE — a `menu:` branch label: an indented string followed
	// by `:` (optionally guarded by `if <cond>`) at end of line. This is
	// unambiguous Ren'Py grammar, which is why these bypass the prose
	// heuristic: one-word choices such as "Vanilla" have neither space
	// nor punctuation and would otherwise be rejected.
	menuOptionRE = regexp.MustCompile(`(?m)^[ \t]+(` + stringBodyPat + `)[ \t]*(?:if[ \t]+[^:\r\n]+)?:[ \t]*(?:\r?\n|$)`)

	// renpyUICallRE — `renpy.input("…")` / `renpy.notify("…")`: the
	// player-facing prompt APIs. They live inside `$` Python statements,
	// where no say / narrator / menu rule can reach them.
	renpyUICallRE = regexp.MustCompile(`renpy\.(?:input|notify)\s*\(\s*(` + stringBodyPat + `)`)

	// assignmentRE — `<ident>(.<ident>)* = "<string>"`: an assignment's
	// right-hand side, i.e. configuration, never dialog.
	assignmentRE = regexp.MustCompile(`[ \t]*` + identPat + `(?:\.` + identPat + `)*[ \t]*=[ \t]*(` + stringBodyPat + `)`)

	// bodyRE — the inner content of the first quoted literal in a match.
	bodyRE = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

	// placeholderRE — Ren'Py `[name]`-style substitutions.
	placeholderRE = regexp.MustCompile(`\[([a-z_][a-z0-9_.]*)\]`)

	// Heuristic helpers (see looksLikeDialog).
	dialogPunctRE = regexp.MustCompile(`[,;:.?!'"]`)
	substRE       = regexp.MustCompile(`\[[a-z_][a-z0-9_.]*\]`)
	wordRE        = regexp.MustCompile(`[A-Za-z]{2,}`)
	letterRE      = regexp.MustCompile(`[A-Za-z]`)
)

// reservedPrefix holds Ren'Py keywords that look like
// `<ident> "…"` but are NOT dialog: they are property assignments with
// positional arguments (`font "…"`, `style_prefix "…"`, `play sound "…"`).
// A match whose leading identifier is in this set is rejected outright.
var reservedPrefix = map[string]bool{
	// Audio
	"play": true, "voice": true, "queue": true, "stop": true,
	"fadein": true, "fadeout": true, "sound": true, "music": true, "sfx": true,
	// Image / display
	"image": true, "show": true, "hide": true, "scene": true, "with": true,
	"layered": true, "add": true,
	// Configuration / styling
	"define": true, "default": true, "init": true, "transform": true, "python": true,
	"style": true, "font": true, "background": true, "foreground": true, "hover": true,
	"idle": true, "selected": true, "insensitive": true, "size_group": true, "id": true,
	"style_prefix": true, "properties": true, "xfill": true, "yfill": true,
	"xalign": true, "yalign": true, "xpos": true, "ypos": true, "xsize": true, "ysize": true,
	"xoffset": true, "yoffset": true, "padding": true, "margin": true, "spacing": true,
	"minimum": true, "maximum": true, "area": true, "frame": true, "crop": true,
	"nearest": true, "thumb": true, "scrollbars": true, "variant": true, "layout": true,
	"sensitive": true, "focus_mask": true, "child": true, "modal": true, "tag": true,
	"zorder": true, "offset": true, "rotate": true, "zoom": true, "matrixcolor": true,
	// Screen-text widgets — handled by Pass 2 only.
	"text": true, "textbutton": true, "button": true, "label": true,
	// Game flow
	"jump": true, "call": true, "return": true, "pass": true, "if": true, "elif": true,
	"else": true, "while": true, "for": true, "menu": true, "extend": true, "pause": true,
	"config": true, "persistent": true, "renpy": true,
}

// nonDialogFiles are configuration files where every string literal is a
// value: `gui.rpy` (theme variables, font paths, colours) and
// `options.rpy` (build options). Ren'Py 7 puts user-visible UI text in
// `screens.rpy`, so nothing in these two files is translatable.
var nonDialogFiles = map[string]bool{"gui.rpy": true, "options.rpy": true}

// finding is one translatable string found in one `.rpy` file.
//
// Offset / Length are byte spans of the whole matched substring in the
// raw file (BOM included in the coordinate space), i.e. exactly the
// range the inject pass rewrites.
type finding struct {
	file       string // absolute path
	rel        string // path relative to the game root
	line       int
	column     int
	offset     int
	length     int
	text       string // the whole match, as decoded source text
	kind       string // wrapped_text | menu_option | ui_prompt | dialog | screen_text | narrator
	speaker    string
	hasSpeaker bool
}

// extractDirectory walks a Ren'Py game root and extracts every
// translatable string from every `.rpy` file.
//
// `.rpyc` files are deliberately skipped: Ren'Py recompiles them from
// `.rpy` on demand. Files matching an ignore rule are never read at all
// (docs/shell-layer/09-std-flags.md § 9.2 Scope).
//
// File order is stable (sorted by relative path) so that nothing about
// the output depends on filesystem enumeration order.
func extractDirectory(gameRoot string, ignore []ignoreRule, rep *Reporter) ([]finding, error) {
	var files []string
	err := filepath.WalkDir(gameRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".rpy") {
			return nil
		}
		rel, rerr := filepath.Rel(gameRoot, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var out []finding
	for i, rel := range files {
		if ignored(ignore, rel, false) {
			continue
		}
		rep.file("scan", rel)
		rep.progress(i+1, len(files), false)
		found, err := extractFile(filepath.Join(gameRoot, filepath.FromSlash(rel)), rel, rep)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

// extractFile runs every pass over one `.rpy` file.
func extractFile(path, rel string, rep *Reporter) ([]finding, error) {
	if nonDialogFiles[filepath.Base(path)] {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return extractFromRaw(raw, path, rel, rep)
}

// extractFromRaw runs every pass over one file's raw bytes. `path` is
// the file the bytes came from (used for snippets); `rel` is its
// project-relative name, used for ids and messages.
func extractFromRaw(raw []byte, path, rel string, rep *Reporter) ([]finding, error) {
	text, bom, ok := decodeWithBOM(raw)
	if !ok {
		// UTF-16: this CLI's byte-offset model does not cover it. Skipping
		// loudly is the honest option — silently extracting unit ids whose
		// offsets do not address the file would corrupt the game on inject.
		rep.warn("UNSUPPORTED_ENCODING",
			"UTF-16 source encoding is not supported by this CLI; file skipped", rel)
		return nil, nil
	}
	starts := lineStarts([]byte(text))

	var out []finding
	// closing tracks the byte offset just past a literal's closing quote.
	// Two passes may match the same literal at *different* start offsets
	// (screen_text takes `style_prefix "choice"` whole, narrator takes
	// `"choice"` alone); deduplicating on the closing quote keeps the
	// earlier, more specific pass. Deduplicating on the start offset would
	// keep both and make inject rewrite the same bytes twice.
	closing := map[int]bool{}

	add := func(m []int, kind, speaker string, hasSpeaker bool) {
		off := m[0] + bom
		length := m[1] - m[0]
		if closing[off+length] {
			return
		}
		line, col := lineForOffset(starts, m[0])
		f := finding{
			file: path, rel: rel, line: line, column: col,
			offset: off, length: length, text: text[m[0]:m[1]],
			kind: kind, speaker: speaker, hasSpeaker: hasSpeaker,
		}
		out = append(out, f)
		closing[off+length] = true
	}

	// Assignment ranges: RHS of an `=`. A bare-string match that starts
	// inside one is configuration, not dialog. Coordinates here are
	// decoded-text bytes, the same space as match offsets.
	var assignRanges [][2]int
	for _, m := range assignmentRE.FindAllStringSubmatchIndex(text, -1) {
		assignRanges = append(assignRanges, [2]int{m[0], m[1]})
	}
	inAssignment := func(textOff int) bool {
		for _, r := range assignRanges {
			if r[0] <= textOff && textOff < r[1] {
				return true
			}
		}
		return false
	}

	// Pass 0.0 — `_("…")`, Ren'Py's explicit translation marker. This is
	// how a game flags UI text; missing it drops an entire menu while
	// extract still reports success.
	for _, m := range transWrapRE.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > 0 && isIdentByte(text[m[0]-1]) {
			// Emulated `(?<![A-Za-z0-9_])`: a longer identifier such as
			// `foo_("x")` is not the marker.
			continue
		}
		add(m, "wrapped_text", "", false)
	}

	// Pass 0.5 — `"…":` menu branches (structural).
	for _, m := range menuOptionRE.FindAllStringSubmatchIndex(text, -1) {
		add(m, "menu_option", "", false)
	}

	// Pass 0.6 — `renpy.input("…")` / `renpy.notify("…")`.
	for _, m := range renpyUICallRE.FindAllStringSubmatchIndex(text, -1) {
		add(m, "ui_prompt", "", false)
	}

	// Pass 1 — say statements with an explicit speaker.
	for _, m := range sayRE.FindAllStringSubmatchIndex(text, -1) {
		speaker := text[m[2]:m[3]]
		if reservedPrefix[speaker] {
			continue
		}
		add(m, "dialog", speaker, true)
	}

	// Pass 2 — screen-language text widgets.
	for _, m := range screenTextRE.FindAllStringSubmatchIndex(text, -1) {
		if inAssignment(m[0]) {
			continue
		}
		add(m, "screen_text", "", false)
	}

	// Pass 3 — bare indented strings (narrator), guarded by the prose
	// heuristic and by the assignment filter.
	for _, m := range narratorRE.FindAllStringSubmatchIndex(text, -1) {
		if inAssignment(m[0]) {
			continue
		}
		claimed := false
		for _, f := range out {
			if f.offset == m[0]+bom {
				claimed = true
				break
			}
		}
		if claimed {
			continue
		}
		// Body = the literal's inner bytes (`m.group("text")[1:-1]`).
		// The literal is capture group 2 of this pattern (group 1 is the
		// indentation), i.e. indices m[4]:m[5].
		body := text[m[4]+1 : m[5]-1]
		if !looksLikeDialog(body) {
			continue
		}
		add(m, "narrator", "", false)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].offset < out[j].offset })
	return out, nil
}

// isIdentByte reports whether b can appear in a Ren'Py identifier
// (the set used by the `(?<![A-Za-z0-9_])` lookbehind of the reference
// implementation).
func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// looksLikeDialog is the prose heuristic applied to bare indented
// strings only.
//
// It fails closed: a false negative (a config value kept out) is
// tolerable, a false positive (a config value shown to a translator as
// dialog) is not. The structural passes (0.0 / 0.5 / 0.6) deliberately
// bypass it, which is what lets one-word menu choices survive.
func looksLikeDialog(body string) bool {
	body = strings.TrimSpace(body)
	if body == "" {
		return false
	}
	// Numbers and hex colours are excluded — but only when the body
	// actually contains a digit, otherwise a bare "..." (all dots, and
	// `.` is in the class to cover decimals) would be read as a numeric
	// constant and silent-dialogue beats would be dropped.
	if hasDigit(body) && isNumericish(body) {
		return false
	}
	// File paths and filenames.
	if strings.HasPrefix(body, "/") {
		return false
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".ogg", ".wav", ".ogv", ".ttf", ".otf"} {
		if strings.HasSuffix(body, ext) {
			return false
		}
	}

	if !wordRE.MatchString(body) {
		// No two-letter word at all. Accept only bodies that also contain
		// no ASCII letter: pure punctuation / symbols such as "..." or
		// "……" are legitimate silent-dialogue lines. Anything with even
		// one letter is identifier-shaped config.
		if letterRE.MatchString(body) {
			return false
		}
		return true
	}

	// Real dialog carries at least one of: an inner space, punctuation, a
	// `[name]` substitution, or an ellipsis. Single-word property values
	// (`style_prefix "choice"`) carry none.
	hasSpace := strings.Contains(body, " ")
	hasPunct := dialogPunctRE.MatchString(body)
	hasSubstitution := substRE.MatchString(body)
	hasEllipsis := strings.Contains(body, "...") || strings.Contains(body, "\u2026")
	return hasSpace || hasPunct || hasSubstitution || hasEllipsis
}

// hasDigit reports whether the body contains a (Unicode) decimal digit,
// matching Python's `\d` on a `str`.
func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// isNumericish reports whether every rune is a digit, `.`, `#` or
// whitespace (Python's `re.fullmatch(r"[\d.#\s]+", body)`).
func isNumericish(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsDigit(r) || r == '.' || r == '#' || unicode.IsSpace(r) {
			continue
		}
		return false
	}
	return true
}

// decodeWithBOM decodes a source file and reports how many bytes the
// byte-order mark occupied.
//
// The BOM matters twice over: the regex works on decoded text while
// inject works on raw file bytes, so every offset has to be shifted by
// the BOM length on the way out. Ren'Py 7 ships UTF-8 with BOM.
func decodeWithBOM(raw []byte) (text string, bom int, ok bool) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return sanitizeUTF8(raw[3:]), 3, true
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}), bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return "", 0, false
	default:
		return sanitizeUTF8(raw), 0, true
	}
}

// sanitizeUTF8 decodes bytes as UTF-8, replacing every invalid byte with
// U+FFFD (the same behaviour as Python's `errors="replace"`).
func sanitizeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		sb.Write(b[i : i+size])
		i += size
	}
	return sb.String()
}

// lineStarts returns the byte offset of the first byte of every line,
// with a trailing sentinel equal to len(data) (line i is starts[i-1]).
func lineStarts(data []byte) []int {
	starts := make([]int, 1, bytes.Count(data, []byte{'\n'})+2)
	starts[0] = 0
	for i, b := range data {
		if b == 0x0A {
			starts = append(starts, i+1)
		}
	}
	starts = append(starts, len(data))
	return starts
}

// lineForOffset returns the 1-based line number and column for a byte
// offset.
//
// This is a binary search over the line table, not a hand-rolled scan:
// an earlier implementation incremented a counter *and* added the loop
// index, roughly doubling every reported line number (ids read
// `L0055` for a string on line 29). Line numbers feed both the unit id
// and `source_context.line`, so being wrong silently misleads every
// translator.
func lineForOffset(starts []int, offset int) (line, column int) {
	i := sort.SearchInts(starts, offset+1) - 1 // bisect_right(starts, offset) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(starts)-1 {
		i = len(starts) - 2
		if i < 0 {
			i = 0
		}
	}
	return i + 1, offset - starts[i] + 1
}
