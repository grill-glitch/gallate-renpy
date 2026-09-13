"""Extract + inject operations.

These are the two standard operations per docs/shell-layer/03 and
docs/protocol/04.

extract
-------
Reads `gallate.yaml`'s `input` (the Ren'Py game directory), walks
the .rpy files, and emits JSON unit files under
`text/units/<resource-path>_<...>.json`. Each unit file follows
`gallate.translation` v1 from docs/shell-layer/12 § 12.4.

The unit `id` is **position-derived** per
docs/shell-layer/05-config-file.md § `id`:

    <resource-path>:L<line>#<n>

so re-extractions produce stable ids. The `source` field is
captured at extract time and the `target` field is left empty;
the user / OmegaT fills in `target`.

inject
-------
Reads the same unit files back. For each unit where `target` is
non-empty, we rewrite the byte range in the source .rpy file
recorded in `.meta.json`. Three gates are enforced per the
`gallate-engine-cli` skill:

1. **Source drift gate** — the bytes currently at the unit's
   `source_offset / source_length` must match the unit's `source`.
   If not, the source has changed since extract and the unit is
   marked `state="needs_review"` without rewriting.
2. **Encoding gate** — the target must encode in UTF-8 cleanly.
3. **Atomicity** — we build the new file contents in memory,
   validate every offset gate first, then write once via a
   temporary file + `os.replace`.

The output destination (`<input dir>` or `--output`) is rewritten
in place. If the user wants a separate copy, they pass
`--output`.
"""

from __future__ import annotations

import json
import os
import re
import sys
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from .renpy_extract import (
    ExtractedString,
    extract_directory,
    ast_fallback_validate,
)

# Kept in sync with pyproject.toml.
__version__ = "0.3.0"

# Translation unit file format. Per docs/shell-layer/12 § 12.4.1:
UNIT_FORMAT = "gallate.translation"
UNIT_VERSION = 1


@dataclass
class InjectionResult:
    """Aggregate counts returned by inject()."""

    units_total: int
    units_injected: int
    units_drifted: int
    units_skipped_empty: int
    units_failed: int
    bytes_written: int
    duration: float


def build_units(
    extracted: list[ExtractedString],
    source_root: Path,
) -> list[dict]:
    """Turn extracted strings into translation unit dicts.

    The unit shape follows `gallate.translation` v1 from
    docs/shell-layer/12 § 12.4.2 with one engine-extension field
    `renpy_kind` (in `metadata`) so the inject pass can recover
    the kind without re-extracting.
    """
    units: list[dict] = []
    for s in extracted:
        # The body is the inner string (between the quotes).
        m = re.search(r'"((?:[^"\\]|\\.)*)"', s.text)
        body = m.group(1) if m else s.text
        source_rel = str(s.file.relative_to(source_root))
        unit = {
            "id": _position_id(source_rel, s.line),
            "source": body,
            "target": "",
            "state": "initial",
            "source_context": {
                "file": source_rel,
                "line": s.line,
                "end_line": s.line,
                "snippet": _snippet(s, source_root),
            },
            "placeholders": _detect_placeholders(body),
            "metadata": {
                "source_file": source_rel,
                "source_offset": s.offset,
                "source_length": s.length,
                "renpy_kind": s.kind,
                "speaker": s.speaker,
            },
        }
        units.append(unit)
    return units


def _position_id(source_rel: str, line: int) -> str:
    """Return the position-derived id per docs/shell-layer/05 § id.

    Format: `<resource-path>:L<line>` with line zero-padded to 4
    digits. The disambiguation suffix `#<n>` is added by the
    caller (see `group_units_by_line`) when multiple strings share
    a line.
    """
    return f"{source_rel}:L{line:04d}"


def _snippet(s: ExtractedString, source_root: Path) -> str:
    """Capture the surrounding source code as a snippet.

    Per docs/shell-layer/05 § source_context the snippet is the
    source line(s) joined by `\n`. We just return the line the
    string is on (the engine doesn't have a useful "context
    line above/below" notion for Ren'Py `say` statements, since
    each line is its own statement).
    """
    try:
        raw = s.file.read_bytes()
        # Find the line boundaries in the file's bytes.
        starts = [0]
        for i, b in enumerate(raw):
            if b == 0x0A:
                starts.append(i + 1)
        # Decode just this line.
        if s.line - 1 < len(starts):
            lo = starts[s.line - 1]
            hi = starts[s.line] if s.line < len(starts) else len(raw)
            line_bytes = raw[lo:hi]
            return line_bytes.decode("utf-8", errors="replace").rstrip("\r\n")
    except OSError:
        pass
    return ""


_PLACEHOLDER_RE = re.compile(r"\[([a-z_][a-z0-9_.]*)\]")
def _detect_placeholders(body: str) -> list[dict]:
    """Detect Ren'Py `[name]`-style substitutions.

    These are engine-side placeholders that the translator MUST
    preserve in the target. The `placeholder` rule type in
    docs/protocol/08 § 8.5 declares them as `preserve: true`.
    """
    return [
        {"id": m.group(1), "syntax": f"[{m.group(1)}]", "type": "variable"}
        for m in _PLACEHOLDER_RE.finditer(body)
    ]


def group_units_by_line(units: list[dict]) -> list[dict]:
    """Add disambiguation suffix `#<n>` to units sharing a line.

    Per docs/shell-layer/05 § id: when two entries share the same
    `(file, line)`, the second and later get `#2`, `#3`, ... in
    source order. The first entry on each line keeps the bare id.
    """
    seen: dict[tuple[str, int], int] = {}
    for u in units:
        key = (u["metadata"]["source_file"], u["source_context"]["line"])
        n = seen.get(key, 0) + 1
        seen[key] = n
        if n > 1:
            u["id"] = f"{u['id']}#{n}"
    return units


def write_unit_files(
    units: list[dict],
    project_root: Path,
    language_pair: tuple[str, str] = ("en", "zh-CN"),
) -> dict[Path, list[dict]]:
    """Write per-source-file unit files under `text/units/`.

    Returns a mapping from each created file's absolute path to
    its list of unit dicts — that's the input `build_meta_entries`
    needs.

    The unit file naming convention follows
    docs/shell-layer/12 § 12.2: `<path-with-_>.json`, so
    `game/script.rpy` becomes `script.json`. (We drop the `game/`
    prefix because the source path is always relative to the
    Ren'Py game root.)
    """
    units_dir = project_root / "text" / "units"
    units_dir.mkdir(parents=True, exist_ok=True)

    # Group by source file.
    by_file: dict[str, list[dict]] = {}
    for u in units:
        by_file.setdefault(u["metadata"]["source_file"], []).append(u)

    out: dict[Path, list[dict]] = {}
    for source_rel, group in by_file.items():
        # File name: replace `/` with `_`, drop the `.rpy`.
        flat = source_rel.replace("/", "_").removesuffix(".rpy")
        out_path = units_dir / f"{flat}.json"
        document = {
            "format": UNIT_FORMAT,
            "version": UNIT_VERSION,
            "source": language_pair[0],
            "target": language_pair[1],
            "entries": group,
        }
        _atomic_write_json(out_path, document)
        out[out_path] = group
    return out


def _atomic_write_json(path: Path, data: dict) -> None:
    """Write a JSON file atomically with a round-trip check."""
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp",
                               dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
            f.write("\n")
        with open(tmp, encoding="utf-8") as f:
            reread = json.load(f)
        if reread != data:
            raise RuntimeError(f"{path.name} round-trip mismatch")
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def _parse_sub_media_overrides(
    config: dict,
    args,
    media_kind: str,
) -> tuple[set[str] | None, set[str] | None]:
    """Pull `engine.<media>.{includes,excludes}` from config + flags.

    CLI `--engine.<media>.includes=...` takes precedence over
    `gallate.yaml`'s `engine.<media>.includes`. Empty string
    means "use config"; missing means "use default".
    """
    cfg_block = (config.get("engine") or {}).get(media_kind) or {}
    cfg_includes = cfg_block.get("includes")
    cfg_excludes = cfg_block.get("excludes")
    # CLI flag overrides.
    cli_includes_raw = args.engine_opts.get(f"{media_kind}.includes")
    cli_excludes_raw = args.engine_opts.get(f"{media_kind}.excludes")
    includes = _split_csv(cli_includes_raw) if cli_includes_raw is not None else (
        set(cfg_includes) if cfg_includes else None
    )
    excludes = _split_csv(cli_excludes_raw) if cli_excludes_raw is not None else (
        set(cfg_excludes) if cfg_excludes else None
    )
    return includes, excludes


def _split_csv(raw: str) -> set[str]:
    return {x.strip() for x in raw.split(",") if x.strip()}


def _resolve_in_place(args, config: dict, input_path: Path) -> tuple[bool, Path | None]:
    """Decide inject's destination per `--engine.in-place` and `--output`."""
    in_place = args.engine_opts.get("in-place", "true").lower() != "false"
    if args.output is None or in_place:
        return in_place, None
    p = args.output
    if not p.is_absolute():
        p = (args.project_root / p).resolve()
    return in_place, p


def do_extract(args) -> dict:
    """Run the extract operation; return the operation's statistics.

    Side effects: writes text + image + audio + video sidecars
    under the project root, plus `.meta.json`.
    """
    t0 = datetime.now(timezone.utc)
    project_root: Path = args.project_root
    config = _load_config(args.gallate_yaml)
    input_path = _resolve(config.get("input"), project_root)
    if input_path is None:
        raise RuntimeError("gallate.yaml has no `input:` setting")
    if not input_path.is_dir():
        raise RuntimeError(f"`input:` is not a directory: {input_path}")

    # ----- text media -----
    extracted = extract_directory(input_path)
    ast_fallback_validate(extracted, input_path)
    units = build_units(extracted, input_path)
    group_units_by_line(units)
    units_by_file = write_unit_files(
        units, project_root,
        language_pair=("en", _target_lang(config)),
    )

    # ----- image / audio / video media -----
    # Per docs/shell-layer/04 § 4.4 / 4.5: CLI media flags
    # override the YAML `media:` list completely. If neither is
    # set, no media extraction happens (text-only).
    from . import media as media_mod

    # Decide which media kinds to process.
    config_media = config.get("media") or []
    kinds_requested = (
        set(args.media) if args.media
        else set(config_media)
    )
    # The CLI exposes only `text` per `features.media`; image/
    # audio/video are engine-extension. We accept whichever kinds
    # the user named, regardless of whether they are declared —
    # media_mod.extract_media raises if a sub-media identifier
    # is unknown.
    do_image = "image" in kinds_requested
    do_audio = "audio" in kinds_requested
    do_video = "video" in kinds_requested

    img_inc, img_exc = _parse_sub_media_overrides(config, args, "image")
    aud_inc, aud_exc = _parse_sub_media_overrides(config, args, "audio")
    vid_inc, vid_exc = _parse_sub_media_overrides(config, args, "video")
    try:
        inv, media_by_kind = media_mod.extract_media(
            input_path, project_root,
            image_includes=img_inc, image_excludes=img_exc,
            audio_includes=aud_inc, audio_excludes=aud_exc,
            video_includes=vid_inc, video_excludes=vid_exc,
            enabled={
                "image": do_image,
                "audio": do_audio,
                "video": do_video,
            },
        )
    except ValueError as e:
        # Sub-media misconfiguration.
        sys.stderr.write(f"extract failed: {e}\n")
        raise

    # ----- .meta.json -----
    from .meta import atomic_write, new_meta
    meta_entries: list[dict] = []
    for project_file, group in units_by_file.items():
        source_rel = group[0]["metadata"]["source_file"]
        meta_entries.append({
            "project": str(project_file.relative_to(project_root)),
            "source": source_rel,
            "type": "text",
            "cli": {"id": "sirenhead", "version": __version__},
            "engine": {"id": "renpy"},
            "sub_media": group[0]["metadata"]["renpy_kind"],
            "size": project_file.stat().st_size,
            "hash": _sha256_file(project_file),
            "extensions": {
                "sirenhead": {
                    "renpy_kind": group[0]["metadata"]["renpy_kind"],
                    "byte_count": sum(len(u["source"]) for u in group),
                },
            },
        })
    meta_entries.extend(
        media_mod.build_media_meta_entries(inv, media_by_kind, project_root)
    )
    atomic_write(project_root, new_meta(project_root, meta_entries))

    t1 = datetime.now(timezone.utc)
    n_media = (
        len(media_by_kind["image"])
        + len(media_by_kind["audio"])
        + len(media_by_kind["video"])
    )
    return {
        "files": {"scanned": len(extracted), "processed": len(units)},
        "text": {"extracted": len(units)},
        "images": {
            "extracted": len(media_by_kind["image"]),
            "copied": 0,
        },
        "audio": {
            "extracted": len(media_by_kind["audio"]),
            "copied": 0,
        },
        "video": {
            "extracted": len(media_by_kind["video"]),
            "copied": 0,
        },
        "output": {
            "created": len(units_by_file) + n_media,
            "bytes_written": sum(p.stat().st_size for p in units_by_file),
        },
        "duration": (t1 - t0).total_seconds(),
    }


def do_inject(args) -> dict:
    """Run the inject operation; return the operation's statistics.

    Side effects: rewrites .rpy files (text media) and copies
    image / audio / video replacement files in
    `gallate.yaml`'s `input` (or `--output`) directory.
    """
    import time

    t0 = time.monotonic()
    project_root: Path = args.project_root
    config = _load_config(args.gallate_yaml)
    input_path = _resolve(config.get("input"), project_root)
    if input_path is None or not input_path.is_dir():
        raise RuntimeError(f"input directory not found: {input_path}")

    # Destination per `--engine.in-place` + `--output`.
    in_place, output_root = _resolve_in_place(args, config, input_path)

    text_stats = _inject_text(args, project_root, input_path, in_place, output_root)
    from . import media as media_mod
    media_res = media_mod.inject_media(
        project_root, input_path,
        in_place=in_place, output_root=output_root,
    )
    text_stats["duration"] = time.monotonic() - t0
    text_stats["images"] = {"copied": media_res.images_copied}
    text_stats["audio"] = {"copied": media_res.audios_copied}
    text_stats["video"] = {"copied": media_res.videos_copied}
    text_stats["output"]["bytes_written"] = (
        text_stats["output"]["bytes_written"] + media_res.bytes_copied
    )
    return text_stats


def _inject_text(
    args,
    project_root: Path,
    input_path: Path,
    in_place: bool,
    output_root: Path | None,
) -> dict:
    """Inject text-only changes. Returns the stats dict."""
    units_dir = project_root / "text" / "units"
    if not units_dir.is_dir():
        # No text units is fine (the project may be media-only).
        return {
            "files": {"processed": 0},
            "text": {"injected": 0},
            "output": {"bytes_written": 0},
            "validation": {"errors": 0, "warnings": 0},
            "duration": 0.0,
        }
    unit_files = sorted(units_dir.glob("*.json"))
    if not unit_files:
        return {
            "files": {"processed": 0},
            "text": {"injected": 0},
            "output": {"bytes_written": 0},
            "validation": {"errors": 0, "warnings": 0},
            "duration": 0.0,
        }

    # Group units by source file so we read each .rpy once.
    units_by_source: dict[str, list[dict]] = {}
    for uf in unit_files:
        doc = json.loads(uf.read_text(encoding="utf-8"))
        for u in doc["entries"]:
            units_by_source.setdefault(u["metadata"]["source_file"], []).append(u)

    result = InjectionResult(0, 0, 0, 0, 0, 0, 0.0)
    for source_rel, units in units_by_source.items():
        source_abs = input_path / source_rel
        if not source_abs.is_file():
            # Per docs/shell-layer/13 § 13.8, a `.meta.json` entry
            # pointing at a missing source is an error, not silent.
            raise RuntimeError(
                f"source file listed in units not found: {source_abs}"
            )
        edits: list[tuple[int, int, str, str]] = []
        for u in units:
            result.units_total += 1
            target = u.get("target", "")
            if not target:
                result.units_skipped_empty += 1
                continue
            offset = u["metadata"]["source_offset"]
            length = u["metadata"]["source_length"]
            edits.append((offset, length, u["source"], target))

        if not edits:
            continue

        edits.sort(key=lambda e: e[0])

        raw = source_abs.read_bytes()
        rewritten = bytearray(raw)
        for offset, length, expected_source, new_body in reversed(edits):
            current = raw[offset:offset + length]
            try:
                first_quote = raw.index(b'"', offset, offset + length)
                last_quote = raw.rindex(b'"', offset, offset + length)
            except ValueError:
                raise RuntimeError(
                    f"could not locate quote pair in {source_rel} "
                    f"at offset {offset}"
                )
            body_span = raw[first_quote + 1:last_quote]
            expected_body = expected_source.encode("utf-8")
            if body_span != expected_body:
                result.units_drifted += 1
                raise RuntimeError(
                    f"source drift in {source_rel} unit {expected_source!r} "
                    f"(expected {len(expected_body)} bytes, found "
                    f"{len(body_span)} bytes at offset {first_quote + 1})"
                )
            new_body_bytes = new_body.encode("utf-8")
            new_span = (
                raw[offset:first_quote + 1]
                + new_body_bytes
                + raw[last_quote:offset + length]
            )
            rewritten = (
                rewritten[:offset]
                + new_span
                + rewritten[offset + length:]
            )
            result.units_injected += 1
            result.bytes_written += len(new_span)

        if not in_place and output_root is not None:
            output_root.mkdir(parents=True, exist_ok=True)
        dest = (output_root or input_path) / source_rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        _atomic_write_bytes(dest, bytes(rewritten))

    return {
        "files": {"processed": len(units_by_source)},
        "text": {"injected": result.units_injected},
        "output": {"bytes_written": result.bytes_written},
        "validation": {
            "errors": result.units_drifted,
            "warnings": 0,
        },
        "duration": 0.0,  # filled by caller
    }


def _atomic_write_bytes(path: Path, data: bytes) -> None:
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp",
                               dir=str(path.parent))
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
        # Round-trip: re-read and confirm we wrote the right bytes.
        with open(tmp, "rb") as f:
            reread = f.read()
        if reread != data:
            raise RuntimeError(f"{path.name} byte round-trip mismatch")
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def _load_config(path: Path) -> dict:
    import yaml
    text = path.read_text(encoding="utf-8")
    if not text.strip():
        return {}
    return yaml.safe_load(text) or {}


def _resolve(p, root: Path):
    if p is None:
        return None
    p = Path(str(p))
    if not p.is_absolute():
        p = (root / p).resolve()
    return p


def _target_lang(config: dict) -> str:
    """Pull a `target:` language from the config, defaulting to zh-CN."""
    return config.get("target_language") or config.get("target") or "zh-CN"


def _sha256_file(path: Path) -> str:
    import hashlib
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return f"sha256:{h.hexdigest()}"
