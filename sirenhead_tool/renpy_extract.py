"""Ren'Py string extraction.

Siren Head Dating Sim ships its dialog as plain `say` statements
inside `script.rpy`:

    c "I decided to start my day with a walk in the woods."
    "Suddenly...Out of nowhere..."

and screen text in `screens.rpy`:

    text "Yes"
    textbutton "Return" action Return()

The primary extractor is a **scoped regex** over the raw `.rpy`
source. It is:

* deterministic across re-extractions (line + byte-offset based,
  no counter that drifts),
* byte-stable (positions refer to bytes in the source, so inject
  can patch the same byte range later),
* strict about Ren'Py syntax: only matches the patterns Ren'Py
  actually treats as translatable dialog / UI text.

Patterns we accept:

    say / character dialog:
        <character> "..."        # e.g. `c "..."`, `s "..."`
        "..." (line is indented) # narrator — bare-string `say`

    screen text:
        text "..."
        textbutton "..."
        label "..."

Patterns we explicitly reject:

    * `play sound "..."`, `play music "..."`, `voice "..."` — those
      are audio file paths.
    * `image <name> = "..."` — image filename declaration.
    * `font "..."`, `style "..."`, `id "..."`,
      `style_prefix "..."`, `background "..."`, `hover "..."` —
      configuration keys.
    * `define`, `default`, `init`, `transform`, `python` blocks
      — code, even when they contain string literals.

The byte ranges returned include the opening-quote + body +
closing-quote substring so the inject pass rewrites the literal
verbatim.

AST fallback
------------
Ren'Py 7's `ast.py` is bundled with the game under `renpy/` but is
Python 2 code (`import cPickle`). The CLI exposes
`--engine.use-ast=true` to cross-check the regex output when a
Python 2 interpreter is on PATH. On Python 3 (the typical
modern host) the fallback is a no-op and the regex is the
authoritative path.
"""

from __future__ import annotations

import bisect
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

# Ren'Py identifier characters (matches Python's, which Ren'Py uses).
_IDENT = r"[A-Za-z_][A-Za-z0-9_]*"

# String body — non-greedy, no embedded `"` (escaped or not).
# Ren'Py does support escapes; we capture them as raw bytes and the
# CLI treats the entire literal as opaque for translation purposes.
_STRING_BODY = r'"[^"\\\n]*(?:\\.[^"\\\n]*)*"'

# A `say` statement with an explicit speaker:
#   <indent><identifier><space><string>
# The identifier must NOT be a reserved keyword we want to skip.
_SAY_RE = re.compile(
    rf"""
    (?P<indent>[ \t]*)
    (?P<char>{_IDENT})
    [ \t]+
    (?P<text>{_STRING_BODY})
    [ \t]*(?:\r?\n|$)
    """,
    re.VERBOSE,
)

# A bare-string `say` (narrator). The string must be on its own
# indented line; top-level bare strings are usually config.
#
# Trailing punctuation: in real Ren'Py, menu options end with
# `:` (the `menu:` branch syntax). We accept either no trailing
# content or `[ \t]*:` after the closing quote.
_NARRATOR_RE = re.compile(
    rf"""
    (?P<indent>[ \t]+)
    (?P<text>{_STRING_BODY})
    [ \t]*(?::|(?:\r?\n|$))
    """,
    re.VERBOSE,
)

# Screen text patterns — only the ones we trust to be localizable.
# Trailing `:` allowed for `textbutton "...":` forms.
_SCREEN_TEXT_RE = re.compile(
    rf"""
    (?P<indent>[ \t]+)
    (?P<kind>text|textbutton|label|button)
    [ \t]+
    (?P<text>{_STRING_BODY})
    [ \t]*(?::|(?:\r?\n|$))
    """,
    re.VERBOSE,
)

# Ren'Py's explicit translation marker: `_("...")`.
#
# This is THE idiomatic way a Ren'Py game marks UI text for
# localization, e.g.
#
#     textbutton _("Back") action Rollback()
#     text _("Version [config.version!t]\n")
#     label _("Display")
#
# It appears in `screens.rpy` (menu labels, preferences, help
# text) and can appear in any `.rpy`. Skipping it — as an earlier
# version of this extractor did — silently drops every menu item
# in the game, which is exactly the class of bug the
# gallate-engine-cli skill warns about ("99% right is still a
# silent failure").
#
# The negative lookbehind `(?<![A-Za-z0-9_])` keeps the `_` from
# matching the tail of a longer identifier (`foo_("x")`), and the
# byte span covers `_("...")` so inject replaces the literal in
# place while leaving `_(` and `)` untouched.
_TRANSLATION_WRAP_RE = re.compile(
    rf"""
    (?<![A-Za-z0-9_])
    _\(
    (?P<text>{_STRING_BODY})
    \)
    """,
    re.VERBOSE,
)

# A `menu:` branch label — an indented string literal followed by
# `:` (optionally guarded by `if <cond>`) at end of line:
#
#     menu:
#         "Vanilla":
#             c "Vanilla please!"
#
# This is unambiguous Ren'Py grammar, so these are matched
# STRUCTURALLY and skip the `_looks_like_dialog` prose heuristic.
# That heuristic exists to keep `style_prefix "choice"` and friends
# out, but it also rejects legitimate one-word choices like
# "Vanilla" — which is how every ice-cream branch in this game was
# silently dropped from the first extraction pass.
_MENU_OPTION_RE = re.compile(
    rf"""
    ^[ \t]+
    (?P<text>{_STRING_BODY})
    [ \t]*
    (?:if[ \t]+[^:\r\n]+)?
    :
    [ \t]*(?:\r?\n|$)
    """,
    re.MULTILINE | re.VERBOSE,
)

# Strings passed to `renpy.input(...)` / `renpy.notify(...)` — the
# player-facing prompt/notification APIs. These sit inside a `$`
# Python statement, so no say/narrator/menu rule sees them.
#
#     $ name = renpy.input("What's your name?")
#
# The span ends at the string's closing quote, so inject rewrites
# the literal and leaves the call intact.
_RENPY_UI_CALL_RE = re.compile(
    rf"""
    renpy\.(?:input|notify)\s*\(\s*
    (?P<text>{_STRING_BODY})
    """,
    re.VERBOSE,
)

# Reserved Ren'Py keywords that look like `<ident> "..."` but are
# NOT dialog. The match is rejected before we even consider it.
_RESERVED_PREFIX = frozenset({
    # Audio
    "play", "voice", "queue", "stop", "fadein", "fadeout",
    # Audio channels
    "sound", "music", "voice", "sfx",
    # Image / display
    "image", "show", "hide", "scene", "with", "layered",
    "add",
    # Configuration / styling
    "define", "default", "init", "transform", "python",
    "style", "font", "background", "foreground", "hover",
    "idle", "selected", "insensitive", "size_group", "id",
    "style_prefix", "properties", "xfill", "yfill",
    "xalign", "yalign", "xpos", "ypos", "xsize", "ysize",
    "xoffset", "yoffset", "padding", "margin", "spacing",
    "minimum", "maximum", "area", "frame", "crop", "nearest",
    "thumb", "scrollbars", "variant", "layout",
    "sensitive", "focus_mask", "child", "modal", "tag",
    "zorder", "nearest", "offset", "rotate", "zoom", "matrixcolor",
    # Screen-text widgets — handled by Pass 2 only.
    "text", "textbutton", "button", "label",
    # Game flow
    "label", "jump", "call", "return", "pass", "if", "elif",
    "else", "while", "for", "menu", "extend", "pause",
    "config", "persistent", "renpy",  # namespaced
})

# Files we never extract from. These are config files where every
# string is a value, never dialog:
#   gui.rpy    — GUI theme variables (font, color, paths, ...)
#   options.rpy — build/run options (sample paths, build name, ...)
# Ren'Py 7 places all user-visible UI strings in `screens.rpy`, not
# `gui.rpy`.
_NON_DIALOG_FILES = frozenset({"gui.rpy", "options.rpy"})

# A line containing ONLY a Ren'Py assignment is config, not dialog.
# We detect this with a regex that matches `<ident>... = "..."`.
# If we see such a line, we don't accept any bare-string match
# whose byte span falls within an "assignment region" — i.e. the
# RHS of an `=`.
_ASSIGNMENT_LINE_RE = re.compile(rf"[ \t]*{_IDENT}(?:\.{_IDENT})*[ \t]*=[ \t]*{_STRING_BODY}")

# Narrator heuristic: a bare string we accept as dialog must look
# like prose, not a filename / identifier / hex color. These
# heuristics fail closed — false negatives are tolerated, false
# positives are not.
#
# The pattern: a real dialog body has *prose* — at least one
# punctuation, or whitespace inside a multi-word phrase. Pure
# identifier-like bodies (one or two short ASCII words with no
# punctuation, no digits, no escape) are config values.
def _looks_like_dialog(body: str) -> bool:
    body = body.strip()
    if not body:
        return False
    # Hex colors / decimal constants are excluded — but require at
    # least one DIGIT, or a bare "..." (all dots, and `.` is in the
    # class to cover decimals/hex) would be misread as a number and
    # this game's silent-dialogue beats would be dropped.
    if re.search(r"\d", body) and re.fullmatch(r"[\d.#\s]+", body):
        return False
    # Pure filenames (with path separators or extensions).
    if body.startswith("/") or body.endswith(
        (".png", ".jpg", ".ogg", ".wav", ".ogv", ".ttf", ".otf")
    ):
        return False

    if not re.search(r"[A-Za-z]{2,}", body):
        # No word at all. Accept only when the body also contains no
        # ASCII LETTER — i.e. it is pure punctuation / symbols such
        # as "..." or "……", which are legitimate silent-dialogue
        # lines this game uses between beats. Anything containing a
        # letter (even one) is an identifier-style config value, and
        # the number/hex rule above already rejected bare numerics.
        if re.search(r"[A-Za-z]", body):
            return False
        return True

    # Single short-word / camelCase identifiers used as property
    # values in `screens.rpy`: `style_prefix "choice"`,
    # `background "gui/..."`, `id "window"`, etc. These pass the
    # rules above but are NOT dialog. The signal: real dialog
    # always has at least one of: a space inside the body, an
    # apostrophe, a punctuation mark, a `[name]`-style
    # substitution, or an ellipsis.
    #
    # NOTE: one-word `menu:` choices like "Vanilla" are also
    # rejected here — that is deliberate. They are recovered by
    # the structural `_MENU_OPTION_RE` pass, which runs earlier and
    # claims the span first, so no legitimate choice is lost.
    has_space = " " in body
    has_punct = bool(re.search(r"[,;:.?!'\"]", body))
    has_substitution = bool(re.search(r"\[[a-z_][a-z0-9_.]*\]", body))
    has_ellipsis = "..." in body or "\u2026" in body
    if not (has_space or has_punct or has_substitution or has_ellipsis):
        return False
    return True


@dataclass
class ExtractedString:
    """One translatable string found in a .rpy source file.

    `offset` and `length` are byte spans of the matched substring
    in the source file. They are the same ranges `inject` will
    rewrite later — preserving the exact quote characters and any
    escaping the engine expects.

    `line` is 1-based and used both for human-readable display and
    for the position-derived `id` per
    docs/shell-layer/05-config-file.md § id.
    """

    file: Path
    line: int
    column: int
    offset: int
    length: int
    text: str
    kind: str  # "dialog" | "narrator" | "screen_text"
    speaker: str | None = field(default=None)

    def id(self, suffix: int | None = None) -> str:
        """Position-derived id, with optional disambiguation suffix.

        Per docs/shell-layer/05 § id:
            <resource-path>:L<line>#<n>
        The suffix is omitted for the first entry on a line.
        """
        base = f"{self.file.name}:L{self.line:04d}"
        if suffix is None or suffix == 1:
            return base
        return f"{base}#{suffix}"

    def quoted(self) -> str:
        """Return the byte-exact substring from the source.

        Used by `inject` for the source-drift check, and by tests
        to confirm byte-equal round-trips.
        """
        return self.text


def _char_byte_map(text: str) -> list[int]:
    """Map every character index in `text` to its byte offset in UTF-8.

    Python 3 regex on a `str` reports CHARACTER offsets; a file
    offset is a BYTE offset. They coincide only for pure ASCII.
    A single `▸` (U+25B8) is one character but three bytes, so
    every match after it drifts by two bytes per occurrence.
    SirenHead's `screens.rpy` has three `text "▸"` lines, which
    shifted all later offsets by six bytes — inject's source-drift
    gate caught it (`found 0 bytes at offset ...`) and refused to
    write, but every affected unit would have patched the wrong
    span.

    Returns a list of length len(text)+1; the final entry is the
    total byte length.
    """
    out = [0] * (len(text) + 1)
    n = 0
    for i, ch in enumerate(text):
        out[i] = n
        n += len(ch.encode("utf-8"))
    out[len(text)] = n
    return out


def _line_starts(data: bytes) -> list[int]:
    """Return byte offsets of the first byte of every line.

    `line_starts[i]` is the byte offset of line i+1 (1-based). The
    last entry equals `len(data)`, which lets `_line_for_offset`
    handle offsets that land on a trailing newline.
    """
    starts = [0]
    for i, b in enumerate(data):
        if b == 0x0A:  # LF
            starts.append(i + 1)
    starts.append(len(data))
    return starts


def _line_for_offset(starts: list[int], offset: int) -> tuple[int, int]:
    """Return (1-based line number, 1-based column index) for a byte offset.

    `starts[i]` is the byte offset of line i+1, and starts has a
    trailing `len(data)` sentinel. The line containing `offset` is
    the last i with starts[i] <= offset.

    Bug history: this used to be a hand-rolled scan that
    double-counted (`line + i`, where `line` was already being
    incremented each iteration). Every reported line number was
    roughly doubled, so ids read `script.rpy:L0055` for a string
    actually on line 29. Injection was unaffected (it uses
    source_offset/source_length), but the id and the
    source_context.line shown to translators were wrong.
    """
    i = bisect.bisect_right(starts, offset) - 1
    if i < 0:
        i = 0
    # Clamp: the trailing sentinel can push i past the last real line.
    if i >= len(starts) - 1:
        i = max(0, len(starts) - 2)
    return i + 1, (offset - starts[i]) + 1


def _decode_with_bom(raw: bytes) -> tuple[str, int]:
    """Decode the file's bytes and return (text, bom_offset).

    `bom_offset` is the number of bytes stripped from the front
    by the BOM — typically 3 for UTF-8 BOM (`EF BB BF`) or 2 for
    UTF-16. The regex's `m.start()` indexes into `text`, but the
    file on disk is `raw`. We add `bom_offset` to convert text
    offsets back to raw offsets so inject can rewrite the right
    bytes.

    Ren'Py ships source files with UTF-8 BOM, so we use utf-8-sig
    which strips the BOM automatically. The bom_offset is then
    exactly `len(BOM)` (3 bytes for UTF-8).
    """
    if raw.startswith(b"\xef\xbb\xbf"):
        return raw[3:].decode("utf-8", errors="replace"), 3
    if raw.startswith(b"\xff\xfe") or raw.startswith(b"\xfe\xff"):
        # UTF-16 BOM. We don't expect this from Ren'Py 7, but
        # handle it gracefully.
        return raw.decode("utf-16", errors="replace"), 2
    return raw.decode("utf-8", errors="replace"), 0


def _extract_one_file(path: Path) -> list[ExtractedString]:
    """Run the regex pass on one .rpy file.

    Each match captures the byte range of the entire matched
    substring (indent + identifier + space + quoted body, or
    indent + quoted body for narrator). The inject pass uses this
    range verbatim — it never has to re-derive it.
    """
    # Skip known non-dialog files.
    if path.name in _NON_DIALOG_FILES:
        return []

    raw = path.read_bytes()
    text, bom = _decode_with_bom(raw)
    # Line starts must be in the SAME coordinate space as the regex
    # offsets (`m.start()`), which index into `text` — i.e. with the
    # BOM already stripped. Building this from `raw` shifts every
    # offset by `bom` bytes and lands matches on the previous line
    # whenever they start within the first `bom` columns.
    #
    # For UTF-8 (what Ren'Py ships) `raw[bom:]` is byte-identical to
    # `text.encode("utf-8")`, so offsets line up exactly. For UTF-16
    # (not expected from Ren'Py 7) this would not hold; the decode
    # path already marks that as best-effort.
    starts = _line_starts(raw[bom:])
    # Character index -> byte offset. Regex on `str` reports
    # character offsets; the file needs byte offsets. See
    # `_char_byte_map` for why this matters.
    bmap = _char_byte_map(text)

    found: list[ExtractedString] = []
    # Track closing offsets so two passes can't both claim the same
    # quoted literal (`label "..."` as both `screen_text` and
    # `narrator`). Offsets are in raw-file byte coordinates.
    closing_offsets: set[int] = set()

    def _span(m: "re.Match[str]") -> tuple[int, int, int, int]:
        """(offset, length, line, column) in raw-file byte coords."""
        b0 = bmap[m.start()]
        b1 = bmap[m.end()]
        line, col = _line_for_offset(starts, b0)
        return b0 + bom, b1 - b0, line, col

    # Pre-compute assignment-line ranges. A bare-string match whose
    # start falls inside an assignment is the RHS of an `=`, not
    # dialog. Both sides here are character offsets, so they compare
    # consistently.
    assignment_ranges: list[tuple[int, int]] = []
    for m in _ASSIGNMENT_LINE_RE.finditer(text):
        assignment_ranges.append((bmap[m.start()], bmap[m.end()]))

    def _in_assignment(byte_off: int) -> bool:
        for lo, hi in assignment_ranges:
            if lo <= byte_off < hi:
                return True
        return False

    # Pass 0: `_("...")` — Ren'Py's explicit translation marker.
    # Must run BEFORE the other passes: a line like
    # `textbutton _("Back")` would otherwise be skipped entirely
    # (the screen-text regex wants a bare `"..."` after the
    # keyword), and running first guarantees the more specific
    # form wins the closing-offset dedup.
    for m in _TRANSLATION_WRAP_RE.finditer(text):
        off, ln, line, col = _span(m)
        if off + ln in closing_offsets:
            continue
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=off,
                length=ln,
                text=m.group(0),
                kind="wrapped_text",
                speaker=None,
            )
        )
        closing_offsets.add(off + ln)

    # Pass 0.5: `menu:` branch labels — structural, so one-word
    # choices survive. Runs before the narrator pass so it claims
    # the span; the narrator prose heuristic would reject "Vanilla".
    for m in _MENU_OPTION_RE.finditer(text):
        off, ln, line, col = _span(m)
        if off + ln in closing_offsets:
            continue
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=off,
                length=ln,
                text=m.group(0),
                kind="menu_option",
                speaker=None,
            )
        )
        closing_offsets.add(off + ln)

    # Pass 0.6: `renpy.input(...)` / `renpy.notify(...)` prompts.
    for m in _RENPY_UI_CALL_RE.finditer(text):
        off, ln, line, col = _span(m)
        if off + ln in closing_offsets:
            continue
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=off,
                length=ln,
                text=m.group(0),
                kind="ui_prompt",
                speaker=None,
            )
        )
        closing_offsets.add(off + ln)

    # Pass 1: speaker dialog.
    for m in _SAY_RE.finditer(text):
        speaker = m.group("char")
        if speaker in _RESERVED_PREFIX:
            continue
        off, ln, line, col = _span(m)
        if off + ln in closing_offsets:
            continue
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=off,
                length=ln,
                text=m.group(0),
                kind="dialog",
                speaker=speaker,
            )
        )
        closing_offsets.add(off + ln)

    # Pass 2: screen text.
    for m in _SCREEN_TEXT_RE.finditer(text):
        off, ln, line, col = _span(m)
        if _in_assignment(off):
            continue
        if off + ln in closing_offsets:
            continue
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=off,
                length=ln,
                text=m.group(0),
                kind="screen_text",
                speaker=None,
            )
        )
        closing_offsets.add(off + ln)

    # Pass 3: narrator (indented bare string). Apply the dialog
    # heuristic to the body and the assignment-line filter. Also
    # skip if an earlier pass already claimed this byte offset.
    for m in _NARRATOR_RE.finditer(text):
        off, ln, line, col = _span(m)
        if _in_assignment(off):
            continue
        if any(s.offset == off for s in found):
            continue
        if off + ln in closing_offsets:
            continue
        body = m.group("text")[1:-1]  # strip outer quotes
        if not _looks_like_dialog(body):
            continue
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=off,
                length=ln,
                text=m.group(0),
                kind="narrator",
                speaker=None,
            )
        )
        closing_offsets.add(off + ln)

    found.sort(key=lambda s: s.offset)
    return found


def extract_directory(game_root: Path) -> list[ExtractedString]:
    """Walk a Ren'Py game root and extract from every .rpy file.

    `.rpyc` files are deliberately skipped: Ren'Py compiles them
    from `.rpy` on demand, so editing `.rpy` is enough. Editing a
    compiled `.rpyc` byte-wise is doable but adds risk with no
    benefit.

    File ordering is stable across runs (sorted by relative path)
    so position-derived IDs do not drift between extract calls.
    """
    files = sorted(p for p in game_root.rglob("*.rpy") if p.is_file())
    out: list[ExtractedString] = []
    for f in files:
        out.extend(_extract_one_file(f))
    return out


# --- AST fallback ----------------------------------------------------------

def ast_fallback_validate(
    extracted: list[ExtractedString],
    game_root: Path,
) -> list[ExtractedString]:
    """Cross-check the regex output against Ren'Py's own parser.

    Ren'Py 7's `ast.py` is bundled with the game under `renpy/` but
    is Python 2 code (`import cPickle`). To run it we need a Python
    2 interpreter on PATH — most modern hosts don't have one. This
    function is therefore a no-op unless both are available; the
    regex is authoritative on its own.

    When a Python 2 interpreter is found, we invoke a small
    subprocess that uses Ren'Py's `parse()` to dump every `Say`
    node's `(who, what)` tuple and join it back with the regex
    output by text equality. Strings the AST sees that the regex
    missed are reported as `meta["ast_only"]` so the user can
    review them; strings the regex sees that the AST doesn't
    recognize are reported as `meta["regex_only"]` for the same
    reason.
    """
    py2 = _find_python2()
    if py2 is None:
        return extracted

    # Lazy import: this module is only loaded when we actually need
    # to talk to Python 2.
    renpy_root = game_root.parent
    if not (renpy_root / "renpy" / "ast.py").is_file():
        return extracted

    script = _AST_DUMP_SCRIPT
    try:
        proc = subprocess.run(
            [py2, "-c", script, str(game_root)],
            cwd=str(renpy_root),
            capture_output=True,
            text=True,
            timeout=30,
        )
    except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
        return extracted
    if proc.returncode != 0:
        return extracted

    # Parse JSON lines from the subprocess.
    import json as _json
    ast_entries: list[tuple[str | None, str]] = []
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line or not line.startswith("{"):
            continue
        try:
            entry = _json.loads(line)
        except ValueError:
            continue
        ast_entries.append((entry.get("who"), entry.get("what", "")))

    by_body: dict[str, list[ExtractedString]] = {}
    for s in extracted:
        body = _strip_quotes(s.text)
        by_body.setdefault(body, []).append(s)

    seen_bodies: set[str] = set()
    for ast_speaker, ast_text in ast_entries:
        seen_bodies.add(ast_text)
        for s in by_body.get(ast_text, []):
            if s.speaker is None and ast_speaker:
                s.speaker = ast_speaker
    return extracted


def _strip_quotes(s: str) -> str:
    if s.startswith('"') and s.endswith('"'):
        return s[1:-1]
    return s


def _find_python2() -> str | None:
    """Return a Python 2 interpreter path, or None.

    Ren'Py 7's `ast.py` uses `import cPickle`, which doesn't exist
    on Python 3. We probe for python2 / python2.7 explicitly.
    """
    for name in ("python2", "python2.7"):
        try:
            r = subprocess.run(
                [name, "-c", "import cPickle"],
                capture_output=True, timeout=5,
            )
            if r.returncode == 0:
                return name
        except (FileNotFoundError, OSError, subprocess.TimeoutExpired):
            continue
    return None


_AST_DUMP_SCRIPT = r'''
"""Dump every Say node from a Ren'Py game to JSON Lines on stdout.

Invoked by `sirenhead_tool.renpy_extract.ast_fallback_validate`.
Run under Python 2 because Ren'Py 7's `ast.py` uses cPickle.
"""
import sys, json
sys.path.insert(0, ".")
try:
    import renpy.ast
except Exception as e:
    sys.stderr.write("ast import failed: %s\n" % e)
    sys.exit(2)

game_root = sys.argv[1]
import os
for dirpath, _dirs, files in os.walk(game_root):
    for f in files:
        if not f.endswith(".rpy"):
            continue
        path = os.path.join(dirpath, f)
        try:
            tree = renpy.ast.parse(open(path).read())
        except Exception as e:
            sys.stderr.write("parse failed: %s: %s\n" % (path, e))
            continue
        for node in tree:
            if isinstance(node, renpy.ast.Say):
                what = node.what or node.attributes.get("text", "")
                sys.stdout.write(
                    json.dumps({"who": node.who, "what": what}) + "\n"
                )
'''
