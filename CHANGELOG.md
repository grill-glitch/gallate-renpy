# Changelog

All notable changes to `sirenhead-tool` are documented here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.5.0] — 2026-09-13

`unpack` / `repack` engine-extension operations: turn any
Ren'Py `.rpa` archive into a directory and back, so a
Wrapper / Wrapper-less workflow can drive `extract → inject`
on games that ship their assets packed.

### Added

- **`unpack <archive.rpa> <dir>`** (Shell) / **`operation: unpack`**
  (GCWP). Reads RPA-3.0 archives produced by every Ren'Py 7.x
  and 8.x release we have encountered (DDLC's `fonts.rpa` /
  `scripts.rpa` / `audio.rpa` / `images.rpa`, BAD END THEATER's
  `archive.rpa`), writes one file per entry, and emits an
  `archive.manifest.json` audit sidecar next to the tree.
  Supports pickle protocols 2, 4, and 5 (Ren'Py's choice
  depends on the engine version and the archive's size).
- **`repack <dir> <archive.rpa>`** (Shell) / **`operation: repack`**
  (GCWP). Walks the directory in sorted order, XORs the
  per-entry offsets and lengths with the archive's key
  (default `42424242`, Ren'Py's own default), compresses the
  metadata with zlib, and writes the result to disk. A
  round-trip (`unpack` → `repack` → `unpack`) produces
  byte-identical files.
- **Hand-rolled pickle parser** (`internal/sirenhead/rpa.go`).
  Ren'Py ships a Python 2 cPickle blob that we can't read with
  Go's stdlib; we implement just enough of the pickle
  machine — `LONG1`, `BININT`, `SHORT_BINSTRING`, `SHORT_BINBYTES`,
  `SHORT_BINUNICODE`, `BINUNICODE`, `EMPTY_LIST`, `EMPTY_DICT`,
  `TUPLE3`, `MARK`, `STOP`, `PROTO`, `FRAME`, `MEMOIZE`,
  `BINPUT`, `LONG_BINPUT`, `BINGET` — to read every Ren'Py
  archive variant we have on disk. The wire format is the
  same one Ren'Py's `loader.py` reads; we follow the same
  XOR-with-key and incremental-dict (`SETITEMS` repeated
  every ~1000 entries) rules.
- **`features.operations.unpack` / `features.operations.repack`**
  now report `true`. `manifest.targets.formats` now lists
  `.rpa`; `manifest.targets.magic_bytes` now lists `52 50 41 2d
  33 2e 30 20` and `52 50 41 2d 32 2e 30 20` so a Wrapper can
  recognise `.rpa` files without invoking this CLI. The
  `identify` operation matches `.rpa` files by their magic
  header at `high` confidence.
- **`--engine.rpa-key=HEXHEXHEX`** (Shell) / `options["rpa-key"]`
  (GCWP). Override the default `42424242` XOR key when
  packing. Reading a key always succeeds; writing an invalid
  key exits with a usage error.
- **`archive.manifest.json` audit sidecar**. Written next to
  every unpacked tree (and re-read by no one — it is for
  humans and for diff review). Explicitly excluded from a
  repack of the same directory so a `unpack → repack` round-trip
  doesn't embed the CLI's own bookkeeping.

### Tested

- All four DDLC `.rpa` archives (`fonts`, `scripts`, `audio`,
  `images`) — 12 + 42 + 64 + 455 entries respectively.
- BAD END THEATER `archive.rpa` — 1901 entries spanning
  pickle protocol 4 with FRAME opcodes.
- In-repo self-tests: byte-identical round-trip on synthetic
  fixtures, protocol rejection (0, 1, 3, 6+), bad-magic
  rejection, audit-sidecar exclusion, header-offset
  sanity (`0x78 0x9c` zlib magic at the recorded offset).
- GCWP `unpack` over stdin/stdout against `scripts.rpa` —
  42 events + statistics + completed message.

## [0.4.0] — 2026-09-13

Rewritten in Go. One static binary, no Python runtime, no `pip install`.
Same CLI grammar, same unit ids, same byte offsets, same exit codes,
same wire format — plus the conformance gaps the Python version had.

### Changed

- **Implementation language: Python → Go** (`cmd/sirenhead-tool` +
  `internal/sirenhead/`, ~6 300 lines including tests). The Python
  package, the `sirenhead-tool` wrapper script and `pyproject.toml`
  are gone; the Python implementation lives on `main`. Build with
  `go build -o sirenhead-tool ./cmd/sirenhead-tool` (Go ≥ 1.22, one
  dependency: `gopkg.in/yaml.v3`).
- `--version` reports `0.4.0`; the version constant exists in exactly
  one place (`internal/sirenhead/version.go`) and is asserted equal to
  the `manifest` document by a test.

### Fixed

- **`--dry-run` was parsed and then ignored.** It now plans without
  executing: config/input/media/ignore are resolved, counts are
  reported, and nothing is written — no unit files, no media sidecars,
  no `.meta.json`, no source edits, and no pre/post scripts
  (docs/shell-layer/09 § 9.3).
- **`--ignore` was parsed and then ignored.** Patterns now apply to the
  whole resource lifecycle — ignored `.rpy` files are never read, and
  ignored media is never classified, extracted or injected
  (docs/shell-layer/09 § 9.2). CLI patterns MERGE with the YAML
  `ignore:` list, they do not replace it.
- **`scripts:` was never executed.** `scripts.pre` / `scripts.post` now
  run in definition order with the Project Root as working directory,
  and a failing script exits 10 (Script Failure). `--dry-run` skips
  them.
- **The GCWP handshake did not match `schema/protocol.schema.yaml`**
  (it emitted `{"type","name","version","supported"}` with no
  `protocol` object). It now carries the required
  `protocol{name,version}` plus a top-level `version`, and keeps
  `supported` for older Wrappers.
- **`response.protocol.version` was a JSON number** (`1.0`) where the
  schema requires the string `"1.0"`.
- **Protocol-layer exit codes were the shell table's.** A failed GCWP
  extract returned 7 (which means "protocol error" in
  docs/protocol/02) and a failed inject returned 8 ("internal error").
  The protocol table is now used properly: 1 operation failed,
  2 invalid arguments, 3 invalid configuration, 4 unsupported
  operation, 5 validation failed, 7 protocol error, 8 internal error.
- **Standard tier requires `progress` events**; the old CLI emitted
  none, and exactly one `file` event per operation. Both are now
  streamed per phase and per file.
- **Inject could write some files and then fail on a later one.** It is
  now strictly two-pass: pass 1 validates every gate (span inside the
  file, quoted literal present, source matches, target encodes) and
  rebuilds every file in memory; pass 2 writes. Any failure ⇒ exit 8
  and **nothing** is written.
- **Re-extraction destroyed translated work.** `target` and friends are
  *authored* fields, and docs/shell-layer/12 § 12.13 says the CLI MUST
  NOT recompute them. Extract now reads the previous unit files and
  keeps `target` / `state` / `context` / `notes` / `provenance` for
  every id whose source is unchanged; when a position's source changed,
  the stale translation is dropped and the entry is labelled
  `needs_review` instead of being carried into a string it no longer
  describes.
- **`--engine.in-place=false` without `--output`** silently wrote
  in-place; it is now a configuration error (3).
- Sub-media `excludes` naming something outside `includes` exited 7;
  it is a configuration error and now exits 3
  (docs/shell-layer/04 § 4.3 rule 5).
- The declared validation rules are now actually **run** during inject
  and reported as `validation` events (`double-quote-balance`,
  `renpy-substitution-preserved`, `max-target-length`, the last one
  configurable via `--engine.max-length=N`). All are `warning`/`info`,
  so a finding never changes the exit code.
- `.meta.json` is read and preserved: entries owned by another CLI
  (`cli.id` ≠ `sirenhead`) survive a re-extract instead of being
  overwritten, and a `schema_version` newer than this CLI knows is
  refused (3) instead of being misinterpreted.
- Ren'Py `.rpy` files in UTF-16 are now skipped with an explicit
  warning rather than extracted with offsets that do not address the
  file (the byte-offset model is UTF-8).

### Added

- **`text:` block** (docs/shell-layer/05 § 5.8):
  `layout: flat|mirror|single`, `metadata.{original_file,
  source_context, location, placeholders, engine_path}` emission flags,
  `hardcoded.context_lines` / `max_bytes`, `lifecycle.written_on:
  extract|never`, and `meta:` key mappings for the five spec fields.
- **`init` follows docs/shell-layer/07**: default media is the standard
  baseline (`text` + `image`) per § 7.6, `--media` values are
  deduplicated as a union, `--input` / `--output` are resolved against
  the final Project Root (§ 7.5), and generated `ignore:` patterns are
  double-quoted as the spec example shows.
- Media injection is ignore-aware and reports — instead of silently
  skipping — a sidecar whose `target` or source file has disappeared
  (`MEDIA_TARGET_MISSING`, `SOURCE_MISSING`), per the "never silent
  corruption" rule of docs/shell-layer/13 § 13.8.
- `text.kept` / `text.stale` counters in the statistics payload.
- `scripts/verify.sh` + `make verify`: gofmt, vet, the self-tests, a
  build, and a real-game round-trip (extract and empty-target inject
  must leave every game file byte-identical; a source edited behind the
  CLI's back must make inject exit 8 without writing).

### Compatibility notes (output shape)

Unit ids, source strings, kinds, speakers, byte offsets, byte lengths
and line numbers are unchanged — verified field-by-field against 0.3.0
on a real game (see below). Three deliberate document changes:

- Entries now also carry the spec's standard metadata keys
  `original_file`, `location` and `engine_path` (this is the
  `text.metadata.*` default of docs/shell-layer/05 § 5.8). Turn them off
  individually, or set `text.lifecycle.written_on: never` to emit none
  of the metadata (inject then re-derives locations by re-extracting).
- `source_context.snippet` now spans up to 3 lines of surrounding source
  (`text.hardcoded.context_lines`, default 3 per the spec) and
  `end_line` follows it, instead of holding the string's own line only.
  Set `text.hardcoded.context_lines: 1` for the 0.3.0 shape.
- `.meta.json`: `files[].size` / `files[].hash` now describe the
  **project file** as docs/shell-layer/13 § 13.4.1 specifies; 0.3.0
  recorded the game asset's size/hash there (both are still available
  under `extensions.sirenhead.media_byte_count` / `media_hash`).
  `extensions.sirenhead.byte_count` now counts UTF-8 bytes, matching its
  name (0.3.0 counted characters).

### Verification

Run on this commit against the real game
(`SirenHeadDatingSim-1.0-pc`, `script.rpy` + `screens.rpy`):

- `go test ./...` — 22 test functions (37 cases), all passing.
- `./scripts/verify.sh` — gofmt clean, vet clean, tests pass, and on the
  real game: 223 units extracted, every game file byte-identical after
  extract and after an empty-target inject, drift probe aborting with
  exit 8 and writing nothing.
- Equivalence harness vs the 0.3.0 Python implementation, on the same
  inputs (the in-repo fixture, and the real game): **3 250 checks, 0
  failures** — identical unit ids / sources / kinds / speakers /
  offsets / lengths / lines (223 units on the real game), identical
  `.meta.json` mappings, identical media classification
  (background/portrait/cg/ui, voice/bgm/sfx, cutscene/opening/ending),
  and **byte-identical game files after translating 54 units** (105
  files compared). 25 differences, all of them the documented output
  changes above.
- Schema validation of every machine-readable document against the
  spec's `schema/*.schema.yaml` (Draft 7, via `jsonschema`): manifest,
  features, validation-rules, protocol, response, every event, identify,
  statistics payload and `.meta.json` — **3 202 checks, 0 failures**,
  plus the non-schema rules (`<resource-path>:L<line>` ids, unique ids
  per document, `gallate.translation` v1 container, four-field
  `source_context`, `line <= end_line`, standard `state` labels).

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
