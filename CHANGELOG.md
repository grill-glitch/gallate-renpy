# Changelog

All notable changes to `sirenhead-tool` are documented here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] — 2026-09-13

Found on a real playthrough: the menu was partly untranslated and the
in-game text was garbled. Both traced back to extraction bugs.

### Fixed

- **Character-vs-byte offset confusion (critical).** A Python `re`
  match on a `str` reports *character* indices, but a file offset is
  a *byte* offset. They coincide only for pure ASCII. `screens.rpy`
  contains `▸` (U+25B8) — one character, three bytes — so every unit
  after those lines recorded a span shifted by two bytes per arrow.
  `screens.rpy:304` stored offset 38814 where the true position is
  38820, i.e. the span read `utton _("` instead of `_("Start")`. The
  inject source-drift gate caught it (`found 0 bytes at offset …`)
  and refused to write, so no file was corrupted — but every
  affected unit would have patched the wrong bytes. Extraction now
  builds an explicit character→byte map and all offsets are in real
  file coordinates.
- **Line numbers were roughly doubled.** `_line_for_offset` kept a
  counter *and* added the loop index, so a string on line 29 was
  reported as `L0055`. The id and the `source_context.line` shown to
  translators were both wrong. Replaced with a `bisect` over the
  line-start table.
- **`gallate.yaml`'s `media:` list was ignored.** Extract processed
  image/audio/video unconditionally, regardless of the declared
  media list or the `-t/-i/-a/-v` flags. Now: CLI media flags
  override the YAML list completely (docs/shell-layer/04 § 4.5);
  with neither, only text is extracted. Verified with a per-kind
  gate test matrix.

### Added

- **`_("...")` extraction pass.** This is Ren'Py's explicit
  translation marker and the idiomatic way a game flags UI text.
  Earlier versions matched only `textbutton "Back"` and therefore
  silently dropped **every one of the 106** `_()` strings in
  `screens.rpy`: the whole navigation menu, preferences, help text,
  and the about page. A game could extract "successfully", report
  106 units, and still ship an entirely English UI.
- **`menu:` option pass (structural).** A `menu` branch label is an
  indented string followed by `:`, which is unambiguous grammar — so
  these are matched structurally and bypass the narrator prose
  heuristic. That heuristic rejects single words with no
  punctuation, which had dropped `"Vanilla"`, `"Chocolate"` and
  `"Strawberry"` — all three ice-cream endings.
- **`renpy.input(...)` / `renpy.notify(...)` pass.** Player-facing
  prompts sit inside a `$` Python statement, where no say / narrator
  / menu rule reaches them. `"What's your name?"` was lost this way.
- **Punctuation-only dialogue.** A line that is just `"..."` is a
  silent-dialogue beat; the decimal/hex exclusion treated three dots
  as a numeric constant and dropped it.
- New unit kinds: `wrapped_text`, `menu_option`, `ui_prompt`.
- Self-test **Test 7** (extraction completeness for `_()` + byte-span
  integrity + line numbers) and **Test 8** (the three silent-drop
  gaps). The `screens.rpy` fixture now carries a multi-byte `▸`
  before the asserted strings, so an offset regression fails loudly.

### Changed

- `features.media.image` / `audio` / `video` are now `true`, and
  `manifest.targets.formats` lists the media extensions and
  directories — both were stale from the text-only era.
- README documents the sub-media defaults and how to override them.

## [0.2.0] — 2026-09-13

### Added

- **Image, audio, and video extraction** (Ren'Py engine-extension
  media). The CLI now picks up every `*.png`/`*.jpg` (image),
  `*.wav`/`*.ogg` (audio), and `*.ogv`/`*.webm` (video) in the
  game directory and classifies each by sub-media.
- **Sub-media classification** per docs/shell-layer/04 § 4.3:
  - image → `background`, `portrait`, `cg`, `ui` (gui/ folder
    → ui; filename hints; script.rpy refs).
  - audio → `voice`, `bgm`, `sfx` (voice/music/sound refs;
    folder hints).
  - video → `cutscene`, `opening`, `ending` (filename hints;
    `renpy.movie_cutscene` refs).
- **Sub-media filtering** via `gallate.yaml` (`engine.<media>.{includes,excludes}`)
  or CLI flags (`--engine.image.includes=...`).
- **Per-file media sidecars** under `image/<sub_media>/<rel>.json`,
  `audio/<sub_media>/<rel>.json`, `video/<sub_media>/<rel>.json`
  — same `gallate.translation` v1 shape as text units, so the
  Wrapper / OmegaT pipeline treats them uniformly.
- **Media inject** via per-unit `target` path: copy the
  user-supplied replacement file atomically over the original.
  Empty target = file untouched (byte-identical round-trip).
- **Manifest enrichment**: `manifest.targets.formats` now lists
  `.png`, `.jpg`, `.wav`, `.ogg`, `.ogv` and the `images/`,
  `gui/`, `audio/` directories.
- **Self-test Test 6** (`media round-trip`) covers image /
  audio / video byte-identity + replacement workflow.

### Changed

- `features.media.{image, audio, video}` now `true` (was
  `false` in 0.1.0). This is the engine-extension media
  declaration per docs/protocol/03 § "Features".
- `do_extract()` and `do_inject()` now drive media in addition
  to text; the statistics payload includes `images`,
  `audio`, `video` counts.
- `_inject_text()` is now a separate helper; the new
  `do_inject()` composes text + media via `_resolve_in_place()`.

### Fixed

- Verify script and self-test now snapshot every file in the
  user's game directory and refuse to proceed if any were
  modified by the test run. (Earlier versions of the script
  could be tricked into overwriting the user's originals;
  this is now an explicit guard.)

## [0.1.0] — 2026-09-13

### Added

- Initial release.
- **Shell layer** (`docs/shell-layer/`):
  - `tool -e[media] ./gallate.yaml` — extract translation units.
  - `tool -i[media] ./gallate.yaml` — inject translated units.
  - `tool init [target]` — initialize a new gallate project.
  - `tool --help`, `tool --version` — standard introspection.
  - Standard long flags: `--output`, `--ignore`, `--dry-run`,
    `--force`, `-v`/`-vv`/`-q`.
  - Engine extension flags: `--engine.KEY=VALUE`.
- **Protocol layer (GCWP 1.0)** (`docs/protocol/`):
  - `manifest` — CLI identity (stable id `sirenhead`).
  - `features` — capability matrix (Standard tier).
  - `validation` — engine-specific validation rules.
  - `identify <path>` — magic-byte detection (UTF-8 BOM for
    `.rpy`, `RENPY RPC2` for `.rpyc`, `game/` directory).
  - `extract` / `inject` driven via JSON Lines on stdin.
- **Ren'Py string extractor** (`sirenhead_tool.renpy_extract`):
  - Three-pass regex: speaker dialog (`c "..."`), screen text
    (`text "..."`, `textbutton "..."`, `label "..."`, `button "..."`),
    and narrator (indented bare string with prose heuristic).
  - Reserved-keyword denylist rejects `play sound`, `image`,
    `define`, `font`, `style_prefix`, `variant`, `thumb`, etc.
  - File-level exclusions for `gui.rpy` and `options.rpy`
    (config-only — no dialog).
  - UTF-8 BOM aware: byte offsets in `.meta.json` are real file
    offsets, not BOM-stripped text offsets.
  - Optional AST fallback (`--engine.use-ast=true`) for hosts
    with Python 2 + Ren'Py 7's bundled parser; no-op on Python 3.
- **Translation unit format** (`gallate.translation` v1):
  - Per-source-file JSON (`text/units/<source>.json`).
  - Position-derived `id` (`<file>:L<line>#<n>`).
  - Auto-detected `[var]` placeholders.
  - Source-context snippet (4-field: file/line/end_line/snippet).
- **`.meta.json`** (Derived Project Metadata):
  - Atomic write via `tempfile` + `os.replace` with round-trip
    validation.
  - Schema-version-gated loader (refuses higher versions).
  - Per-file `project` ↔ `source` mapping.
- **Self-tests** (`python -m tests.self_test`):
  - Round-trip byte-identical.
  - Minimal-diff (N edits → N changed lines).
  - Source-drift detection (exit 8, atomicity preserved).
  - Idempotent re-extraction.
  - GCWP handshake + stdin extract.
- **README** in English + Chinese (cross-linked at top).
- **LICENSE** (CC BY-SA 4.0, matching the gallate spec).

### Known limitations

- AST fallback requires Python 2 (Ren'Py 7's parser uses
  `import cPickle`). On Python 3 hosts the regex is authoritative.
- No image / audio / video extraction (engine-extension media
  not exposed). This game has no localizable image / audio
  strings in source form.
- No `build` / `unpack` / `repack` operations (Ren'Py 7 compiles
  `.rpyc` from `.rpy` on demand; not the CLI's job).
- No cancellation / status streaming (Standard tier doesn't
  require these; Full tier would).

[0.1.0]: https://github.com/grill-glitch/gallate-renpy/releases/tag/v0.1.0
