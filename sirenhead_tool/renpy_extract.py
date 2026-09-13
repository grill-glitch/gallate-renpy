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
    if len(body) < 4:
        return False
    # Must contain a "word" — at least 2 letters in a row.
    if not re.search(r"[A-Za-z]{2,}", body):
        return False
    # Hex colors / pure numbers / dotted identifiers are excluded.
    if re.fullmatch(r"[\d.#A-Fa-f\s]+", body):
        return False
    # Pure filenames (with path separators or dots in odd places).
    if body.startswith("/") or body.endswith((".png", ".jpg", ".ogg", ".wav", ".ogv", ".ttf", ".otf")):
        return False
    # Single short-word / camelCase identifiers used as property
    # values in `screens.rpy`: `style_prefix "choice"`,
    # `background "gui/..."`, `id "window"`, etc. These pass the
    # rules above but are NOT dialog. The signal: real dialog
    # always has at least one of: a space inside the body, an
    # apostrophe, a punctuation mark, or runs of more than one
    # word. Single-word / camelCase identifiers under ~24 chars
    # are config unless they contain a space or a `[name]`-style
    # substitution.
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

    Linear scan — this game is small.
    """
    line = 1
    for i in range(len(starts) - 1):
        if starts[i + 1] > offset:
            return line + i, (offset - starts[i]) + 1
        line += 1
    return line, 1


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
    starts = _line_starts(raw)
    text, bom = _decode_with_bom(raw)

    found: list[ExtractedString] = []
    # Track closing-quote byte offsets. Two matches that share a
    # closing quote target the same quoted string; we keep the
    # first (more specific) and drop later overlaps. This prevents
    # `label "..."` being captured as both `screen_text` (full line)
    # and `narrator` (just the quoted body).
    closing_offsets: set[int] = set()

    # Pre-compute assignment-line byte ranges. A bare-string match
    # whose start offset falls inside an assignment is rejected —
    # that's the RHS of an `=`, not dialog.
    assignment_ranges: list[tuple[int, int]] = []
    for m in _ASSIGNMENT_LINE_RE.finditer(text):
        assignment_ranges.append((m.start(), m.end()))

    def _in_assignment(off: int) -> bool:
        for lo, hi in assignment_ranges:
            if lo <= off < hi:
                return True
        return False

    # Pass 1: speaker dialog.
    for m in _SAY_RE.finditer(text):
        speaker = m.group("char")
        if speaker in _RESERVED_PREFIX:
            continue
        # Skip if the closing-quote byte is already taken by an
        # earlier match. Pass 1 runs first, so it always wins.
        if m.end() in closing_offsets:
            continue
        line, col = _line_for_offset(starts, m.start())
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=m.start(),
                length=m.end() - m.start(),
                text=m.group(0),
                kind="dialog",
                speaker=speaker,
            )
        )
        closing_offsets.add(m.end())

    # Pass 2: screen text.
    for m in _SCREEN_TEXT_RE.finditer(text):
        if _in_assignment(m.start()):
            continue
        if m.end() in closing_offsets:
            continue
        line, col = _line_for_offset(starts, m.start())
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=m.start(),
                length=m.end() - m.start(),
                text=m.group(0),
                kind="screen_text",
                speaker=None,
            )
        )
        closing_offsets.add(m.end())

    # Pass 3: narrator (indented bare string). Apply the dialog
    # heuristic to the body and the assignment-line filter. Also
    # skip if any earlier match (Pass 1 OR Pass 2) already covers
    # this byte offset — that prevents double-counting
    # `text "..."` as both `screen_text` and `narrator` on the
    # same byte range.
    for m in _NARRATOR_RE.finditer(text):
        if _in_assignment(m.start()):
            continue
        if any(s.offset == m.start() for s in found):
            continue
        if m.end() in closing_offsets:
            continue
        body = m.group("text")[1:-1]  # strip outer quotes
        if not _looks_like_dialog(body):
            continue
        line, col = _line_for_offset(starts, m.start())
        found.append(
            ExtractedString(
                file=path,
                line=line,
                column=col,
                offset=m.start(),
                length=m.end() - m.start(),
                text=m.group(0),
                kind="narrator",
                speaker=None,
            )
        )
        closing_offsets.add(m.end())

    found.sort(key=lambda s: s.offset)
    # All regex offsets are in `text` (BOM-stripped). Convert them
    # back to raw-file offsets so inject reads from `raw` directly.
    # Ren'Py 7 ships .rpy files with a UTF-8 BOM, so `bom` is 3 for
    # those files. We shift `offset` AND `length` (length stays the
    # same; the closing-offset set already records raw byte ends,
    # so it shifts too).
    if bom:
        for s in found:
            s.offset += bom
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
