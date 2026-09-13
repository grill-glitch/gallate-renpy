# sirenhead-tool

**[English](./README.md) | [简体中文](./README.zh-CN.md)**

[![License: CC BY-SA 4.0](https://img.shields.io/badge/License-CC%20BY--SA%204.0-lightgrey.svg)](https://creativecommons.org/licenses/by-sa/4.0/)
[![GCWP: 1.0 Standard](https://img.shields.io/badge/GCWP-1.0%20Standard-blueviolet)](https://github.com/grill-glitch/gallate)
[![Go: ≥1.22](https://img.shields.io/badge/go-%E2%89%A51.22-00ADD8)](https://go.dev/)
[![Ren'Py: 7.x](https://img.shields.io/badge/Ren'Py-7.x-orange)](https://www.renpy.org/)

A [gallate](https://github.com/grill-glitch/gallate) CLI for the
Ren'Py visual novel **"Siren Head Dating Sim"**, written in **Go**.
Extract dialog and UI labels from `.rpy` source files into per-source
JSON unit files, translate them, and inject them back — with
byte-stable round-trips, source-drift detection, and atomic writes.

```text
Conformance: GCWP 1.0 Standard tier.

Operations:
  -e / -i   extract / inject
  -t        text   (standard baseline — always processed)
  -i        image  (engine-extension: background / portrait / cg / ui)
  -a        audio  (engine-extension: voice / bgm / sfx)
  -v        video  (engine-extension: cutscene / opening / ending)

Engine-extension operations (Ren'Py):
  unpack     <archive.rpa> <dir>   Unpack an RPA-3.0 archive
  repack     <dir> <archive.rpa>   Pack a directory tree into .rpa

Discovery (per docs/protocol/03):
  manifest   CLI identity (GCWP JSON on stdout)
  features   CLI capabilities (GCWP JSON on stdout)
  validation Engine-specific validation rules (GCWP JSON on stdout)

Subcommand:
  init       Create a new gallate.yaml project
```

## Why a CLI and not just a script

`gallate` (and the GCWP wire format) lets one Wrapper drive many
unrelated engine CLIs without coupling any layer. This CLI speaks
**both layers** of the gallate spec:

- **Shell layer** — invoked from your shell with `-e` / `-i`.
- **Protocol layer (GCWP)** — the same binary, driven by a Wrapper
  through JSON Lines on stdin/stdout.

The Wrapper doesn't need to know Ren'Py exists; the CLI doesn't need
to know OmegaT exists.

## Requirements

- **Go ≥ 1.22** to build. The runtime has no other dependency.
- One Go module dependency: [`gopkg.in/yaml.v3`](https://gopkg.in/yaml.v3)
  (for `gallate.yaml`).
- No Python, no virtualenv, no pip. The previous Python implementation
  is on `main`; this branch is the Go rewrite.

## Installation

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
go build -o sirenhead-tool ./cmd/sirenhead-tool   # or: make build
./sirenhead-tool --version
```

Expected output:

```text
sirenhead 0.5.0
protocol gcwp 1.0
engine renpy (7.x compatible)
```

`go install ./cmd/sirenhead-tool` works too, and drops the binary on
your `GOBIN`.

## Quick start

```bash
# 1. Initialize a project next to the game folder.
./sirenhead-tool init /path/to/project \
    --input /path/to/game \
    --media text

# 2. Extract translation units.
./sirenhead-tool -et /path/to/project/gallate.yaml

# This writes:
#   /path/to/project/text/units/script.json
#   /path/to/project/text/units/screens.json
#   /path/to/project/.meta.json

# 3. Translate. Each entry has the shape:
#   {
#     "id": "script.rpy:L0042",
#     "source": "I decided to start my day with a walk in the woods.",
#     "target": "",                       # ← fill this in
#     "state": "initial",
#     "original_file": "script.rpy",
#     "source_context": {
#       "file": "script.rpy",
#       "line": 42,
#       "end_line": 43,
#       "snippet": "...three lines of surrounding source..."
#     },
#     "location": {"line": 42, "offset": 1107, "length": 51},
#     "placeholders": [],
#     "engine_path": "script.rpy",
#     "metadata": {
#       "source_file": "script.rpy",
#       "source_offset": 1107,
#       "source_length": 51,
#       "renpy_kind": "dialog",
#       "speaker": "c"
#     }
#   }

# 4. Inject.
./sirenhead-tool -it /path/to/project/gallate.yaml
```

The CLI writes the translated text into the same byte ranges recorded
in `metadata.source_offset` / `metadata.source_length`. Every other
byte (indentation, CRLF, Ren'Py syntax, comments) is left untouched —
the file diff has exactly one line per edited unit.

`text/metadata.*` flags and `text.lifecycle.written_on: never` in
`gallate.yaml` decide which of those keys are emitted at all; see
[`gallate.yaml` support](#gallateyaml-support).

## Packed archives (`.rpa`)

Ren'Py ships game assets (images, audio, fonts, `.rpyc` scripts) packed
into `.rpa` archives. Ren'Py loads them transparently at runtime; the
`extract` / `inject` pipeline here expects files on disk. Two
companion subcommands turn archives into trees and back:

```bash
# 1. Unpack an archive into a directory tree.
./sirenhead-tool unpack /path/to/game/archive.rpa /tmp/unpacked

# 2. Run the standard extract / inject / build cycle against
#    the unpacked tree (and translated copies inside it).
./sirenhead-tool -et /path/to/project/gallate.yaml
./sirenhead-tool -it /path/to/project/gallate.yaml

# 3. Repack the (now-translated) tree into a fresh archive.
./sirenhead-tool repack /tmp/unpacked /path/to/out.rpa
```

The unpacker emits an `archive.manifest.json` audit sidecar next to
the tree (it is excluded from a subsequent repack of the same
directory). Both subcommands accept the standard `--ignore` flag and
`--dry-run`; the repack additionally accepts `--engine.rpa-key=HEXHEXHEX`
to override the default XOR key (`42424242`, Ren'Py's own default).

Verified against four DDLC archives (`fonts`, `scripts`, `audio`,
`images`) and BAD END THEATER's `archive.rpa` (1901 entries). The
`unpack` → `repack` round-trip is byte-identical for every file.

GCWP wrappers use the same operations: `{"operation":"unpack", ...}` and
`{"operation":"repack", ...}`.

## What gets extracted

Ren'Py has several distinct places a player-visible string can live,
and each needs its own rule. All of these are handled:

| Source shape | Example | Unit kind |
| --- | --- | --- |
| Character dialogue | `c "Hello there."` | `dialog` |
| Narrator line | `"Suddenly...Out of nowhere..."` | `narrator` |
| Silent-dialogue beat | `"..."` | `narrator` |
| `menu:` choice, multi-word | `"Run away":` | `menu_option` |
| `menu:` choice, one word | `"Vanilla":` | `menu_option` |
| Player prompt / notify | `$ n = renpy.input("What's your name?")` | `ui_prompt` |
| `_()`-wrapped UI text | `textbutton _("Back")` | `wrapped_text` |
| Screen text | `text "About"` | `screen_text` |

The extractor runs six passes in priority order
(`wrapped_text` → `menu_option` → `ui_prompt` → `dialog` →
`screen_text` → `narrator`), deduplicated by the **closing quote
offset**: two passes can match the same literal at two different start
offsets (`style_prefix "choice"` matches whole, then `"choice"` alone),
and deduplicating on the start offset would make inject rewrite the
same bytes twice. The prose heuristic runs last, on bare indented
strings only — which is why one-word menu choices survive (the
structural `menu:` pass claims them first).

Deliberately **not** extracted: image/audio/video filenames, Ren'Py
substitutions (`[name]`, `[config.version]`), config values in
`gui.rpy` / `options.rpy`, `if name == "Jack"` input comparisons,
keyboard key names (`Ctrl`, `Tab`), and date format strings.

Three of these shapes once shipped silently dropped while extract
reported success (`_()` UI text — an entire untranslated menu; one-word
`menu:` choices — every ice-cream branch; `renpy.input` prompts). Each
now has a self-test that fails loudly.

## Guarantees (verified by `go test ./...`)

1. **Byte-identical round-trip.** `extract → inject` with empty
   targets reproduces the original `.rpy` files byte-for-byte.
2. **Minimal diff.** Editing N units produces exactly N changed lines,
   every other line byte-identical, line count unchanged.
3. **Source-drift detection.** If the bytes at a unit's recorded offset
   are no longer the unit's `source`, the CLI exits **8** and writes
   **nothing at all** — not even the files that were already fine.
   Inject is a two-pass operation: pass 1 validates every gate and
   rebuilds every file in memory, pass 2 writes.
4. **Idempotent re-extraction.** `extract` twice produces the same
   `.meta.json` (modulo `generated_at`) and the same position-derived
   ids.
5. **GCWP handshake + operations.** Driven from a Wrapper through JSON
   Lines: `protocol` handshake, `identify`, `extract`, `inject`,
   `validate`, with `started` / `phase` / `progress` / `file` /
   `statistics` / `completed` events and no human text on stdout.
6. **Atomic writes.** Unit files, media sidecars and `.meta.json` are
   written to a same-directory temp file, read back and verified, then
   renamed into place.

### Verifying a real game

```bash
go test ./...                                       # in-repo fixture
SIRENHEAD_GAME_DIR=/path/to/Game/game ./scripts/verify.sh   # real game
```

`scripts/verify.sh` formats, vets and tests, builds the binary, then
runs it against the real game: extract must leave every game file
byte-identical, empty-target inject must too, and a source string
edited behind the CLI's back must make inject exit 8 without writing.

## gallate conformance

### Shell layer

Grammar per [docs/shell-layer/02](https://github.com/grill-glitch/gallate/blob/main/docs/shell-layer/02-cli-grammar.md):

```text
sirenhead-tool [operation][media] ./gallate.yaml [options]
```

| Flag | Behaviour |
| --- | --- |
| `--output PATH` | Override the output destination (relative paths resolve against the Project Root) |
| `--ignore PATTERN` | Add an ignore pattern (repeatable). **Merges** with the YAML `ignore:` list; matches `.gitignore`-style globs (`*`, `**`, `?`, `dir/`) |
| `--dry-run` | Plan without executing: reads config/input/media, reports counts, writes nothing and runs no scripts |
| `--force` | `init` only: overwrite an existing `gallate.yaml` |
| `-v` / `-vv` / `-q` | Verbosity. Presentation only — never changes the exit code |
| `--engine.KEY=VALUE` | Engine extension options (see below) |

Exit codes ([docs/shell-layer/10 § 10.3](https://github.com/grill-glitch/gallate/blob/main/docs/shell-layer/10-stdout-stderr.md)):

| Code | Meaning | When this CLI returns it |
| --- | --- | --- |
| 0 | Success | — |
| 1 | General error | unexpected internal failure |
| 2 | Invalid CLI usage | bad flags, missing/garbage target, unknown media letter |
| 3 | Invalid project configuration | unparseable `gallate.yaml`, missing `input:`, illegal sub-media filter, `.meta.json` newer than this CLI, a `.meta.json` entry whose project file is gone |
| 4 | Input not found | `input:` is not a directory, a source resource recorded in the project is missing |
| 5 | Unsupported operation | — |
| 6 | Unsupported media | a declared-but-unsupported media was requested |
| 7 | Extraction failure | scan/read failure during extract |
| 8 | Injection failure | source drift, encode failure, no quoted literal at the recorded span |
| 9 | Output failure | could not write a project file atomically |
| 10 | Script failure | a `scripts.pre` / `scripts.post` script exited non-zero |

stdout carries only the operation result (a single path, so
`result=$(sirenhead-tool -e ./gallate.yaml)` works); progress, warnings
and errors all go to stderr.

### `gallate.yaml` support

```yaml
input: ./game              # required
output: ./out              # optional default destination
media: [text, image]       # default media list when no media flag is given
ignore: ["*.tmp", "cache/"]
scripts:
  pre:  ./scripts/pre.sh   # run before the operation (cwd = Project Root)
  post: ./scripts/post.sh  # run after it; failure exits 10

text:
  format: json             # json is the only standard unit format
  layout: flat             # flat | mirror | single
  metadata:                # emission flags for the five spec fields
    original_file: true
    source_context: true
    location: true
    placeholders: true
    engine_path: true
  hardcoded:
    context_lines: 3       # lines of surrounding source in source_context.snippet
    max_bytes: 4096        # cap on one snippet (the string's own line is kept intact)
  lifecycle:
    written_on: extract    # extract | never (never ⇒ no metadata at all)
  meta:                    # key mapping for the five fields (purely declarative)
    source_context: source_context
    location:       location

engine:
  text_encoding: utf-8
  image:
    includes: [background, portrait]   # whitelist
    excludes: [ui]                      # subtractive; must be a subset of includes
  audio:
    excludes: [sfx]
```

Engine options (`--engine.*`): `in-place` (default `true`;
`false` requires `--output`), `max-length` (soft cap used by the
`max-target-length` rule), `use-ast` (cross-check against Ren'Py's own
parser — a no-op unless a Python 2 interpreter and the game's
`renpy/ast.py` are both present), `image|audio|video.includes|excludes`
(sub-media filters, overriding the YAML values).

### Protocol layer (GCWP)

| Message / event | Status |
| --- | --- |
| `protocol` handshake | ✅ (schema-shaped: `protocol{name,version}` + top-level `version`, plus `supported`) |
| `request` → `response` | ✅ |
| `started` / `phase` / `progress` / `file` / `warning` / `error` / `validation` / `statistics` / `completed` | ✅ |
| `identify` | ✅ |
| `cancel` command, `status` query | ❌ not implemented (Full tier) |

Protocol-layer exit codes follow
[docs/protocol/02](https://github.com/grill-glitch/gallate/blob/main/docs/protocol/02-core-protocol.md)
(0 success, 1 operation failed, 2 invalid arguments, 3 invalid
configuration, 4 unsupported operation, 5 validation failed,
6 cancelled, 7 protocol error, 8 internal error) — a **different
table** from the Shell layer's, where 6 means "unsupported media".

The declared validation rules (`double-quote-balance`,
`renpy-substitution-preserved`, `max-target-length`) are also *run*
during inject; findings are emitted as `validation` events and counted
in the statistics. Every declared rule is severity `warning` or `info`,
so a finding never changes the exit code.

### Conformance tiers

| Tier | Capability | Status |
| --- | --- | --- |
| Basic | `manifest` | ✅ |
| Basic | `features` | ✅ |
| Basic | `extract` operation | ✅ |
| Basic | `inject` operation | ✅ |
| Basic | Standard exit codes | ✅ |
| Standard | event streaming (`started`, `phase`, `progress`, `file`, …) | ✅ |
| Standard | statistics emission | ✅ |
| Standard | validation rules | ✅ |
| Standard | image / audio / video (engine-extension media) | ✅ |
| Full | cancellation | ❌ not implemented |
| Full | status streaming | ❌ not implemented |
| Full | all validation rule types | partial (regex + constraint; placeholders are detected per-unit instead of declared as a rule) |

### Sub-media defaults

| Media | Sub-media identifiers | Classification source |
| --- | --- | --- |
| image | `background`, `portrait`, `cg`, `ui` | `gui/` folder → `ui`; filename hints; `image` / `scene` / `show` references in the scripts |
| audio | `voice`, `bgm`, `sfx` | `voice` / `play music` / `play sound` references; folder and filename hints |
| video | `cutscene`, `opening`, `ending` | filename hints; `renpy.movie_cutscene` references |

Media is a file-replacement workflow: extract writes one JSON sidecar
per asset under `image/<sub-media>/…`, `audio/<sub-media>/…`,
`video/<sub-media>/…`; inject copies the file named by a sidecar's
`target` over the original. Empty targets mean "leave the original
alone", which is what makes the no-translation round-trip
byte-identical.

## What changed in 0.4.0 (Go rewrite)

Same CLI, same unit ids, same offsets, same exit codes, same wire
format — plus the spec-completeness fixes below. See
[CHANGELOG.md](./CHANGELOG.md) for the full list and for the compat
notes (extra unit keys, wider snippets).

- Single static binary; no Python runtime, no `pip install`.
- `--dry-run` and `--ignore` are actually honoured (the Python version
  parsed both and ignored them).
- `progress` and per-file `file` events (Standard tier requires them).
- Pre/post `scripts:` are executed (exit 10 on failure).
- Inject is two-pass and prefers `.meta.json` as the project-file ↔
  source mapping; it can re-derive a span from the position-derived id
  or by re-extracting, since unit files are disposable caches.
- `.meta.json` keeps other CLIs' entries, and `size` / `hash` describe
  the project file as the spec requires.

## What this CLI does NOT do

- **Build / repack.** Ren'Py compiles `.rpyc` from `.rpy` on demand;
  the engine owns that step.
- **Audio / video re-encoding.** Replacement media is copied as-is;
  the user supplies files at the same paths. ([docs/shell-layer/12 § 12.7](https://github.com/grill-glitch/gallate/blob/main/docs/shell-layer/12-file-structure.md)
  says videos should be re-muxed, not re-encoded — that responsibility
  is upstream.)
- **AST validation on a normal host.** Ren'Py 7's bundled parser is
  Python 2 code (`import cPickle`), so `--engine.use-ast=true` is a
  no-op unless you have a Python 2 interpreter *and* the game's
  `renpy/` directory. The regex extractor is authoritative on its own;
  the AST is a second opinion that fills in speakers.
- **Cancellation / status queries** (GCWP Full tier).
- **Translation memory.** Re-extracting a project re-derives the
  derived fields and keeps `target` / `state` for entries whose source
  is unchanged (same id ⇒ same position ⇒ same `target` carried over),
  but there is no TM database — use the OmegaT sidecar pattern for
  cross-project memory.
- **UTF-16 source files.** The extractor's byte-offset model is UTF-8
  (BOM or not). A UTF-16 `.rpy` is skipped with a warning rather than
  given offsets that do not address the file.

## File layout

```text
gallate-renpy/
├── cmd/sirenhead-tool/main.go   # entry point (thin)
├── internal/sirenhead/
│   ├── version.go       # CLI identity — the ONLY place versions live
│   ├── exit.go          # both exit-code tables (shell + GCWP)
│   ├── args.go          # Shell-layer grammar
│   ├── config.go        # gallate.yaml loading and resolution
│   ├── discovery.go     # manifest / features / validation
│   ├── renpy.go         # the six extraction passes
│   ├── units.go         # gallate.translation v1 documents, layouts
│   ├── meta.go          # .meta.json (atomic, ownership-aware)
│   ├── extract.go       # extract / inject operations
│   ├── media.go         # image / audio / video classification
│   ├── gcwp.go          # Protocol layer (stdin JSON Lines)
│   ├── scripts.go       # pre / post scripts
│   ├── reporter.go      # stderr for the shell, events for GCWP
│   └── *_test.go        # self-tests + the in-repo fixture
├── scripts/verify.sh    # formatting, vet, tests, real-game round-trip
├── Makefile
├── go.mod / go.sum
├── LICENSE              # CC BY-SA 4.0
├── README.md / README.zh-CN.md
└── CHANGELOG.md
```

## See also

- [gallate specification](https://github.com/grill-glitch/gallate) —
  Shell layer (`docs/shell-layer/`), Protocol layer (`docs/protocol/`),
  JSON Schemas (`schema/`).
- [Ren'Py documentation](https://www.renpy.org/doc/html/) — language
  reference.

## License

[CC BY-SA 4.0](./LICENSE) — matching the gallate specification.
Game-specific strings (the dialog in "Siren Head Dating Sim") are not
part of this work; they remain the property of their respective
authors.

## Author

Samuel Flores
<5uniljdst@mozmail.com>

GitHub: [@grill-glitch](https://github.com/grill-glitch)
