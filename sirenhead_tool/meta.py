"""Project metadata (`.meta.json`) read/write.

Per docs/shell-layer/13-meta-json.md, `.meta.json` is the
**derived project metadata** — the CLI's bookkeeping file that
records the actual mapping between project files and original
game resources. It is NOT a copy of `gallate.yaml`.

Atomic write pattern:

  1. Build the new metadata object in memory.
  2. Write to a temporary file in the same directory.
  3. Validate that the temporary file parses back.
  4. `os.replace` the temp file onto `.meta.json` (atomic on POSIX
     and Windows when both paths are on the same filesystem).

If any step fails, the existing `.meta.json` (if any) is left
untouched.
"""

from __future__ import annotations

import json
import os
import re
import tempfile
from datetime import datetime, timezone
from pathlib import Path

from .discovery import CLI_ID, CLI_VERSION
from .renpy_extract import ExtractedString

META_SCHEMA_VERSION = 1
"""Bump when the on-disk format changes incompatibly.

Per docs/shell-layer/13 § 13.4.1, the CLI refuses to load a
`.meta.json` whose `schema_version` is higher than the CLI knows.
"""

# Engine id is reported in `.meta.json` per docs/shell-layer/13 §
# 13.4.1. We use the same engine id as in `manifest.targets`.
ENGINE_ID = "renpy"


def now_iso() -> str:
    """ISO-8601 UTC timestamp with second precision and Z suffix.

    Ren'Py's bundled tooling prefers this exact shape.
    """
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def sha256_file(path: Path) -> str:
    """Compute the lowercase hex SHA-256 of a file's bytes."""
    import hashlib

    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return f"sha256:{h.hexdigest()}"


def new_meta(
    project_root: Path,
    entries: list[dict],
    schema_version: int = META_SCHEMA_VERSION,
) -> dict:
    """Build a fresh `.meta.json` object from a list of file entries.

    Each entry has the shape from `extract.build_meta_entries`.
    Returns a dict ready for `atomic_write`.
    """
    return {
        "schema_version": schema_version,
        "generated_by": {"id": CLI_ID, "version": CLI_VERSION},
        "generated_at": now_iso(),
        "project": {"root": "."},
        "files": entries,
    }


def read_meta(project_root: Path) -> dict | None:
    """Load `.meta.json` from the project root.

    Returns None if the file doesn't exist.
    Raises ValueError if the schema_version is higher than this
    CLI knows (per docs/shell-layer/13 § 13.8).
    """
    path = project_root / ".meta.json"
    if not path.is_file():
        return None
    data = json.loads(path.read_text(encoding="utf-8"))
    sv = data.get("schema_version")
    if not isinstance(sv, int):
        raise ValueError(f".meta.json has non-integer schema_version: {sv!r}")
    if sv > META_SCHEMA_VERSION:
        raise ValueError(
            f".meta.json schema_version {sv} is higher than this CLI "
            f"knows ({META_SCHEMA_VERSION}). Refusing to load."
        )
    return data


def atomic_write(project_root: Path, meta: dict) -> None:
    """Write `.meta.json` atomically.

    Pattern: temp file → validate → `os.replace`. The temp file
    lives in the same directory as the final file so `os.replace`
    is atomic on the same filesystem.
    """
    final = project_root / ".meta.json"
    fd, tmp_path = tempfile.mkstemp(
        prefix=".meta.", suffix=".json.tmp", dir=str(project_root),
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(meta, f, ensure_ascii=False, indent=2)
            f.write("\n")
        # Validate the temp file parses back identically.
        with open(tmp_path, encoding="utf-8") as f:
            reread = json.load(f)
        if reread != meta:
            raise RuntimeError(
                ".meta.json round-trip failed: written != re-read"
            )
        os.replace(tmp_path, final)
    except Exception:
        # Best-effort cleanup of the temp file.
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise


def build_meta_entries(
    project_root: Path,
    units_by_file: dict[Path, list[dict]],
    source_root: Path,
) -> list[dict]:
    """Build the `files[]` entries from a set of unit-file results.

    `units_by_file` maps `Path` (project file under
    `text/units/`) to a list of unit dicts. We pair each project
    file with its source .rpy file (looked up via `extensions`),
    the CLI/engine ids, and the units' aggregate size + hash.

    The mapping from project file → source file is encoded in the
    unit entries themselves (the CLI emits a `source_file` field
    per unit). We extract the unique source files here.
    """
    entries: list[dict] = []
    for project_file, units in units_by_file.items():
        if not units:
            continue
        # All units in this project file share the same source.
        source_rel = units[0]["source_file"]
        source_abs = source_root / source_rel
        size = project_file.stat().st_size if project_file.is_file() else 0
        # Use the bytes we already have in memory for the hash to
        # avoid a second read.
        h = sha256_file(project_file) if project_file.is_file() else None
        entries.append({
            "project": str(project_file.relative_to(project_root)),
            "source": source_rel,
            "type": "text",
            "cli": {"id": CLI_ID, "version": CLI_VERSION},
            "engine": {"id": ENGINE_ID},
            "sub_media": units[0].get("sub_media", "dialog"),
            "size": size,
            "hash": h,
            "extensions": {
                CLI_ID: {
                    "renpy_kind": units[0].get("renpy_kind", "dialog"),
                    "byte_count": sum(
                        len(u["source"]) for u in units
                    ),
                },
            },
        })
    return entries
