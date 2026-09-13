# sirenhead-tool

**[English](./README.md) | [简体中文](./README.zh-CN.md)**

[![License: CC BY-SA 4.0](https://img.shields.io/badge/License-CC%20BY--SA%204.0-lightgrey.svg)](https://creativecommons.org/licenses/by-sa/4.0/)
[![GCWP: 1.0 Standard](https://img.shields.io/badge/GCWP-1.0%20Standard-blueviolet)](https://github.com/grill-glitch/gallate)
[![Python: ≥3.9](https://img.shields.io/badge/python-%E2%89%A53.9-blue)](https://www.python.org/)
[![Ren'Py: 7.x](https://img.shields.io/badge/Ren'Py-7.x-orange)](https://www.renpy.org/)

A [gallate](https://github.com/grill-glitch/gallate) CLI for the
Ren'Py visual novel **"Siren Head Dating Sim"**. Extract dialog and
UI labels from `.rpy` source files into per-source JSON unit files,
translate them, and inject them back — with byte-stable round-trips,
source-drift detection, and atomic writes.

```text
Conformance: GCWP 1.0 Standard tier.

Operations:
  -e / -i   extract / inject
  -t        text media (the only one exposed by this CLI)

Discovery (per docs/protocol/03):
  manifest   CLI identity (GCWP JSON on stdout)
  features   CLI capabilities (GCWP JSON on stdout)
  validation Engine-specific validation rules (GCWP JSON)

Subcommand:
  init       Create a new gallate.yaml project
```

## Why a CLI and not just a script

`gallate` (and the GCWP wire format) lets one Wrapper drive many
unrelated engine CLIs without coupling any layer. This CLI speaks
**both layers** of the gallate spec:

- **Shell layer** — invoked from your shell with `-e` / `-i`.
- **Protocol layer (GCWP)** — same binary, driven by a Wrapper
  through JSON Lines on stdin.

The Wrapper doesn't need to know Ren'Py exists; the CLI doesn't
need to know OmegaT exists.

## Installation

### From PyPI (recommended, when published)

```bash
pip install sirenhead-tool
sirenhead-tool --version
```

### From source (current state)

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
pip install -e .
sirenhead-tool --version
```

Expected output:

```text
sirenhead 0.1.0
protocol gcwp 1.0
engine renpy (7.x compatible)
```

### From the source tree without installing

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
./sirenhead-tool --version   # the executable script in the repo root
```

The only runtime dependency is [PyYAML](https://pyyaml.org/) ≥ 6.0,
which ships with most Python distributions.

## Quick start

```bash
# 1. Initialize a project next to the game folder.
sirenhead-tool init /path/to/project \
    --input /path/to/game \
    --media text

# 2. Extract translation units.
sirenhead-tool -et /path/to/project/gallate.yaml

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
#     "source_context": {
#       "file": "script.rpy",
#       "line": 42,
#       "end_line": 42,
#       "snippet": "    c \"I decided to start my day...\""
#     },
#     "metadata": {
#       "source_file": "script.rpy",
#       "source_offset": 1107,
#       "source_length": 51,
#       "renpy_kind": "dialog",
#       "speaker": "c"
#     }
#   }

# 4. Inject.
sirenhead-tool -it /path/to/project/gallate.yaml
```

The CLI writes the translated text into the same byte ranges
recorded in `metadata.source_offset` /
`metadata.source_length`. Every other byte (indentation, CRLF,
Ren'Py syntax, comments) is left untouched — the file diff has
exactly one line per edited unit.

## Conformance

This CLI conforms to **GCWP 1.0 (Standard tier)** per
[`docs/protocol/13-conformance.md`](https://github.com/grill-glitch/gallate/blob/main/docs/protocol/13-conformance.md)
in the gallate repo:

| Tier    | Capability            | Status |
| ------- | --------------------- | ------ |
| Basic   | `manifest`            | ✅     |
| Basic   | `features`            | ✅     |
| Basic   | `extract` operation   | ✅     |
| Basic   | `inject` operation    | ✅     |
| Basic   | Standard exit codes   | ✅     |
| Standard | event streaming      | ✅     |
| Standard | statistics emission  | ✅     |
| Standard | validation rules     | ✅     |
| Standard | image / audio / video | ✅ (Ren'Py engine-extension media) |
| Full    | cancellation          | ❌ not implemented |
| Full    | status streaming      | ❌ not implemented |
| Full    | all validation types  | partial (regex + constraint) |

### Sub-media defaults

This CLI exposes these engine-extension sub-media per
docs/shell-layer/04 § 4.3:

| Media  | Sub-media identifiers              | Classification source |
| ------ | ---------------------------------- | --------------------- |
| image  | `background`, `portrait`, `cg`, `ui` | `gui/` folder → `ui`; filename hints; `script.rpy` `image`/`scene`/`show` refs |
| audio  | `voice`, `bgm`, `sfx`               | `script.rpy` `voice` / `play music` / `play sound` refs; folder hints |
| video  | `cutscene`, `opening`, `ending`     | filename hints; `renpy.movie_cutscene` refs |

Override via `gallate.yaml`:

```yaml
engine:
  image:
    includes: [background, portrait]
    excludes: [ui]
  audio:
    excludes: [sfx]    # don't ship translation sidecars for SFX
```

## Guarantees (verified by `python -m tests.self_test`)

1. **Byte-identical round-trip.** `extract → inject` with empty
   targets reproduces the original `.rpy` files byte-for-byte.
2. **Minimal diff.** Editing N units produces a diff with
   exactly N changed lines, every other line byte-identical.
3. **Source-drift detection.** If the source string at a
   recorded offset no longer matches the unit's `source`, the
   CLI exits with code 8 (Injection Failure) and the file is
   left untouched. This catches silent corruption.
4. **Idempotent re-extraction.** `extract` twice in a row
   produces the same `.meta.json` and unit files (modulo
   `generated_at`); position-derived IDs do not drift.
5. **GCWP handshake + operation.** When launched from a Wrapper
   with JSON on stdin, the CLI responds to a `protocol` ping and
   runs `extract` / `inject` / `identify` per the wire format.

Run the self-test from a fresh checkout:

```bash
git clone https://github.com/grill-glitch/gallate-renpy
cd gallate-renpy
python3 -m tests.self_test
# ... ALL TESTS PASSED
```

To run against the real Siren Head Dating Sim files (more
realistic but needs the game present locally):

```bash
SIRENHEAD_GAME_DIR=/path/to/SirenHeadDatingSim-1.0-pc/game \
    python3 -m tests.self_test
```

## What this CLI does NOT do

- **Build / repack.** Ren'Py compiles `.rpyc` from `.rpy` on
  demand; the engine owns that step.
- **Audio / video re-encoding.** This CLI does not re-encode
  `.wav`, `.ogg`, `.ogv`, etc. The user supplies replacement
  files at the same path; the CLI copies them in. Per
  docs/shell-layer/12 § 12.7 videos "should not be re-encoded,
  only re-muxed" — that responsibility is upstream.
- **AST validation on Python 3.** Ren'Py 7's bundled parser is
  Python 2 code (`import cPickle`); the regex is authoritative
  on Python 3. An `--engine.use-ast=true` flag is wired up for
  hosts that have a Python 2 interpreter; otherwise it's a
  no-op.
- **Translation memory / re-extraction merge.** Re-extracting
  a project with non-empty `target` fields overwrites them with
  the freshly-extracted `source`. This is per docs/shell-layer/12
  § 12.13 (translation files are disposable caches) — but if
  you want to preserve human edits across re-extracts, use the
  OmegaT sidecar pattern, not this CLI's plain round-trip.

## Exit codes

| Code | Meaning                  | Reference |
| ---- | ------------------------ | --------- |
| 0    | Success                  | shell-layer § 10.3 |
| 1    | General error            | shell-layer § 10.3 |
| 2    | Invalid CLI usage        | shell-layer § 10.3 |
| 3    | Invalid `gallate.yaml`   | shell-layer § 10.3 |
| 4    | Input not found          | shell-layer § 10.3 |
| 5    | Unsupported operation    | shell-layer § 10.3 |
| 6    | Unsupported media        | shell-layer § 10.3 |
| 7    | Extraction failure       | shell-layer § 10.3 |
| 8    | Injection failure        | shell-layer § 10.3 |
| 9    | Output failure           | shell-layer § 10.3 |
| 10   | Script failure           | shell-layer § 10.3 |

## File layout

```text
gallate-renpy/
├── sirenhead-tool             # executable entry script
├── sirenhead_tool/            # Python package
│   ├── __init__.py
│   ├── __main__.py            # main() / dispatcher
│   ├── cli.py                 # Shell-layer argv parser
│   ├── discovery.py           # manifest / features / validation
│   ├── extract.py             # extract() / inject() operations
│   ├── gcwp.py                # Protocol-layer (stdin JSON driver)
│   ├── meta.py                # .meta.json read / atomic write
│   ├── renpy_extract.py       # Ren'Py string extractor
│   └── py.typed               # PEP 561 marker
├── tests/
│   ├── __init__.py
│   ├── fixtures.py            # minimal Ren'Py game for self-tests
│   └── self_test.py           # runnable conformance tests
├── pyproject.toml             # PEP 517 build + entry point
├── LICENSE                    # CC BY-SA 4.0
├── README.md / README.zh-CN.md
└── CHANGELOG.md
```

## See also

- [gallate specification](https://github.com/grill-glitch/gallate)
  — Shell layer (`docs/shell-layer/`) + Protocol layer
  (`docs/protocol/`) + JSON Schemas (`schema/`).
- [Ren'Py documentation](https://www.renpy.org/doc/html/) —
  language reference.

## License

[CC BY-SA 4.0](./LICENSE) — matching the gallate specification.
Game-specific strings (the dialog in "Siren Head Dating Sim") are
not part of this work; they remain the property of their respective
authors.
