"""Ren'Py media extraction (image / audio / video).

Unlike text — where we extract every say-statement and re-write
the source bytes — Ren'Py media is a **file-replacement** workflow:

  extract:
    Walk the game dir, classify each asset by sub-media, copy
    each file into the project under a stable path, emit a
    `.json` sidecar with `gallate.translation` v1 shape.

  inject:
    Walk the project, for each unit where `target` is non-empty,
    copy that file over the original at its recorded
    `source_path`. Atomic copy + verification.

This module is engine-specific: it knows Ren'Py's resource
layout conventions (`images/` for story art, `gui/` for UI
textures, `play music` for BGM, `play sound` for SFX,
`voice "..."` for voice lines, `renpy.movie_cutscene` for
cutscenes). The classification rules are documented per
sub-media below.

Sub-media defaults exposed (per docs/shell-layer/04 § 4.3):

    image  → background, portrait, cg, ui
    audio  → voice, bgm, sfx
    video  → cutscene, opening, ending

The user can override via `gallate.yaml`:

    engine:
      image:
        includes: [background, portrait]
        excludes: [ui]
"""

from __future__ import annotations

import json
import os
import re
import shutil
import tempfile
from dataclasses import dataclass
from pathlib import Path

# Ren'Py image extensions. PNG is the common one; JPG is rare
# but used by some games. WebP shows up in Ren'Py 8+, not 7.x —
# we include it for forward compat.
_IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".webp"}
_AUDIO_EXTS = {".wav", ".ogg", ".mp3", ".opus"}
_VIDEO_EXTS = {".ogv", ".webm", ".mp4", ".avi", ".mkv"}

# Sub-media identifiers we expose. Per docs/shell-layer/04 § 4.3
# rule 1: each sub-media identifier MUST be declared by the
# engine (in features / manifest / a Sub-Media discovery
# command). The CLI rejects any other identifier at config time.
IMAGE_SUB_MEDIA = ("background", "portrait", "cg", "ui")
AUDIO_SUB_MEDIA = ("voice", "bgm", "sfx")
VIDEO_SUB_MEDIA = ("cutscene", "opening", "ending")


@dataclass
class MediaAsset:
    """One extracted media asset.

    `source_path` is the file's path relative to the Ren'Py game
    directory (the one declared as `input:` in gallate.yaml).
    `sub_media` is one of the engine's exposed sub-media
    identifiers.

    `id` is `media/<sub_media>/<relative-path-with-separator-
    replaced>` so re-extraction produces a stable id across runs.
    """

    source_path: str  # e.g. "images/SirenHead1.png"
    absolute_path: Path
    sub_media: str
    media_kind: str  # "image" | "audio" | "video"
    size: int
    sha256: str

    def id(self) -> str:
        # Replace path separators with `_` for stable filenames.
        flat = self.source_path.replace(os.sep, "_")
        return f"media/{self.media_kind}/{self.sub_media}/{flat}"


# --- Classification rules ---------------------------------------------------

# 1. Image classification.
#
# Ren'Py 7 convention: `images/` (or any top-level *.png if no
# `images/`) holds story art; `gui/` holds UI textures. Within
# story art we use filename hints:
#   * `*Background*.png` or `*BG*.png` → background
#   * `*Ending*.png` or `*CG*.png`     → cg
#   * everything else referenced via `show` or `scene` → portrait
#
# We also cross-reference `script.rpy`: any file appearing in an
# `image Foo = "..."` declaration OR a `scene` / `show` statement
# is story art (not UI). Files in `gui/` are always ui.

# Filename → sub-media heuristic (case-insensitive on the
# basename only).
_IMAGE_FILENAME_HINTS = (
    ("background", re.compile(r"(Background|^BG|_BG|back_)", re.IGNORECASE)),
    ("cg", re.compile(r"(CG|Ending|Event\d)", re.IGNORECASE)),
)


def _classify_image(rel_path: str, ref_set: set[str]) -> str:
    """Return one of `background`, `portrait`, `cg`, `ui`."""
    # UI textures always live in gui/.
    parts = Path(rel_path).parts
    if "gui" in parts:
        return "ui"
    # Story art — but ONLY if referenced by script.rpy. Files in
    # `images/` that aren't referenced are still classified by
    # filename hint (might be cut content the engine ships).
    name = Path(rel_path).name
    for sub, pat in _IMAGE_FILENAME_HINTS:
        if pat.search(name):
            return sub
    # Default story art without hint: portrait if referenced via
    # show, else background (scene art that the engine uses by
    # `scene <name>` but isn't formally declared).
    if rel_path in ref_set or any(rel_path.endswith(p) for p in ref_set):
        return "portrait"
    return "background"


# 2. Audio classification.
#
# Ren'Py audio statements:
#   * `voice "foo.ogg"`            → voice line
#   * `play music "foo.ogg"`       → BGM
#   * `play sound "foo.ogg"`       → SFX
#
# Filename conventions in some games:
#   * `voice/` folder              → voice
#   * `music/` folder              → bgm
#   * `audio/voice|music|sfx/`     → voice|bgm|sfx
#
# If a file isn't referenced in script.rpy, we default to sfx.

_AUDIO_FILENAME_HINTS = {
    "voice": re.compile(r"(voice|line)", re.IGNORECASE),
    "bgm": re.compile(r"(music|bgm|theme)", re.IGNORECASE),
}


def _classify_audio(rel_path: str, voice_refs: set[str],
                    music_refs: set[str], sound_refs: set[str]) -> str:
    if rel_path in voice_refs:
        return "voice"
    if rel_path in music_refs:
        return "bgm"
    if rel_path in sound_refs:
        return "sfx"
    name = Path(rel_path).name
    # Folder-based hint.
    parts = Path(rel_path).parts
    for folder_name in ("voice", "music", "sfx"):
        if folder_name in parts:
            return "voice" if folder_name == "voice" else (
                "bgm" if folder_name == "music" else "sfx")
    # Filename-based hint.
    for sub, pat in _AUDIO_FILENAME_HINTS.items():
        if pat.search(name):
            return sub
    # Unreferenced audio — default to sfx (most common).
    return "sfx"


# 3. Video classification.
#
# Ren'Py uses `renpy.movie_cutscene("foo.ogv")` for in-engine
# movies. Convention:
#   * `*Ending*.ogv` → ending
#   * `*Opening*.ogv` or `*OP*.ogv` → opening
#   * everything else → cutscene

_VIDEO_FILENAME_HINTS = (
    ("ending", re.compile(r"(Ending|ED_|EndRoll)", re.IGNORECASE)),
    ("opening", re.compile(r"(Opening|^OP|OP_)", re.IGNORECASE)),
)


def _classify_video(rel_path: str, ref_set: set[str]) -> str:
    name = Path(rel_path).name
    for sub, pat in _VIDEO_FILENAME_HINTS:
        if pat.search(name):
            return sub
    return "cutscene"


# --- script.rpy reference scan ---------------------------------------------

# These regexes scan script.rpy for the audio/video/image refs.
# Re-using the extraction patterns from renpy_extract would couple
# this module tightly; we keep them narrow here.

# `image <name> = "..."` and `image <name> = Movie(...)`.
_IMG_DECL_RE = re.compile(r'^\s*image\s+\w+\s*=\s*["\']([^"\']+)["\']', re.MULTILINE)
# `scene <name>` and `show <name>` — name is an identifier, not a path.
_SCENE_SHOW_RE = re.compile(r'^\s*(?:scene|show)\s+(\w+)', re.MULTILINE)

# `voice "..."`, `play music "..."`, `play sound "..."` etc.
_VOICE_RE = re.compile(r'^\s*voice\s+["\']([^"\']+)["\']', re.MULTILINE)
_PLAY_MUSIC_RE = re.compile(r'^\s*play\s+music\s+["\']([^"\']+)["\']', re.MULTILINE)
_PLAY_SOUND_RE = re.compile(r'^\s*play\s+sound\s+["\']([^"\']+)["\']', re.MULTILINE)

# `renpy.movie_cutscene("...")`
_MOVIE_RE = re.compile(r'^\s*\$?\s*renpy\.movie_cutscene\s*\(\s*["\']([^"\']+)["\']',
                        re.MULTILINE)


def _scan_script_refs(game_root: Path) -> dict:
    """Scan every .rpy file for media references.

    Returns a dict with:
        image_refs:  set of relative file paths used in `image` decls.
        voice_refs:   set of relative paths used in `voice` statements.
        music_refs:   set of relative paths used in `play music`.
        sound_refs:   set of relative paths used in `play sound`.
        video_refs:   set of relative paths used in movie_cutscene.

    Paths in the sets are relative to `game_root` (matching how
    Ren'Py itself resolves them).
    """
    refs = {
        "image_refs": set(),
        "voice_refs": set(),
        "music_refs": set(),
        "sound_refs": set(),
        "video_refs": set(),
    }
    for rpy in sorted(game_root.rglob("*.rpy")):
        text = rpy.read_text(encoding="utf-8-sig", errors="replace")
        for m in _IMG_DECL_RE.finditer(text):
            refs["image_refs"].add(m.group(1))
        for m in _VOICE_RE.finditer(text):
            refs["voice_refs"].add(m.group(1))
        for m in _PLAY_MUSIC_RE.finditer(text):
            refs["music_refs"].add(m.group(1))
        for m in _PLAY_SOUND_RE.finditer(text):
            refs["sound_refs"].add(m.group(1))
        for m in _MOVIE_RE.finditer(text):
            refs["video_refs"].add(m.group(1))
    return refs


# --- Main extract -----------------------------------------------------------

@dataclass
class MediaInventory:
    """Aggregate results from `extract_media`."""
    images: list[MediaAsset]
    audios: list[MediaAsset]
    videos: list[MediaAsset]


def _classify_all(
    game_root: Path,
    refs: dict,
) -> MediaInventory:
    """Walk `game_root` and classify every media asset.

    Classification depends on file extension and the references
    found in `.rpy` files. Files that exist on disk but aren't
    referenced anywhere are still picked up — the user might
    want to localise assets that aren't yet wired into the
    script.
    """
    images: list[MediaAsset] = []
    audios: list[MediaAsset] = []
    videos: list[MediaAsset] = []

    for path in sorted(game_root.rglob("*")):
        if not path.is_file():
            continue
        rel = str(path.relative_to(game_root))
        ext = path.suffix.lower()
        if ext in _IMAGE_EXTS:
            sub = _classify_image(rel, refs["image_refs"])
            images.append(MediaAsset(
                source_path=rel,
                absolute_path=path,
                sub_media=sub,
                media_kind="image",
                size=path.stat().st_size,
                sha256=_sha256(path),
            ))
        elif ext in _AUDIO_EXTS:
            sub = _classify_audio(
                rel, refs["voice_refs"], refs["music_refs"], refs["sound_refs"],
            )
            audios.append(MediaAsset(
                source_path=rel,
                absolute_path=path,
                sub_media=sub,
                media_kind="audio",
                size=path.stat().st_size,
                sha256=_sha256(path),
            ))
        elif ext in _VIDEO_EXTS:
            sub = _classify_video(rel, refs["video_refs"])
            videos.append(MediaAsset(
                source_path=rel,
                absolute_path=path,
                sub_media=sub,
                media_kind="video",
                size=path.stat().st_size,
                sha256=_sha256(path),
            ))
        # Anything else (e.g. .rpyc, .save) is ignored — these are
        # build/cache artifacts, not localizable resources.
    return MediaInventory(images=images, audios=audios, videos=videos)


def _sha256(p: Path) -> str:
    import hashlib
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return f"sha256:{h.hexdigest()}"


# --- File-level include / exclude ------------------------------------------

def _filter_sub_media(
    assets: list[MediaAsset],
    includes: set[str] | None,
    excludes: set[str] | None,
) -> list[MediaAsset]:
    """Apply docs/shell-layer/04 § 4.3 sub-media rules.

    `includes` is a whitelist. `excludes` is subtractive. If
    both omitted, no filtering (caller may pass empty set to
    mean "none"). If `excludes` references something not in
    `includes`, that's a misconfiguration — we raise.
    """
    if includes is None and excludes is None:
        return list(assets)
    if includes is None:
        includes = {a.sub_media for a in assets}
    out = [a for a in assets if a.sub_media in includes]
    if excludes:
        unknown = excludes - includes
        if unknown:
            raise ValueError(
                f"sub-media excludes references items not in "
                f"includes: {sorted(unknown)}"
            )
        out = [a for a in out if a.sub_media not in excludes]
    return out


# --- Project-side artifacts -------------------------------------------------

# Per docs/shell-layer/12 § 12.5-12.7 we lay out the project files
# under `image/<sub_media>/`, `audio/<sub_media>/`, `video/<sub_media>/`
# mirroring the source's relative path. Each media file gets a
# `<file>.json` sidecar with `gallate.translation` v1 shape so
# the same Wrapper / OmegaT workflow applies as for text.

def _write_sidecar(
    asset: MediaAsset,
    project_root: Path,
) -> Path:
    """Write the .json sidecar for one asset and return its path."""
    # Project path: image/<sub_media>/<rel>.json
    project_path = (
        project_root / asset.media_kind / asset.sub_media
        / (asset.source_path + ".json")
    )
    project_path.parent.mkdir(parents=True, exist_ok=True)
    sidecar = {
        "format": "gallate.translation",
        "version": 1,
        "source": "original",
        "target": "translation",
        "entries": [{
            "id": asset.id(),
            "source": asset.source_path,
            "target": "",
            "state": "initial",
            "metadata": {
                "media_kind": asset.media_kind,
                "sub_media": asset.sub_media,
                "source_path": asset.source_path,
                "size": asset.size,
                "hash": asset.sha256,
            },
        }],
    }
    _atomic_write_json(project_path, sidecar)
    return project_path


def _atomic_write_json(path: Path, data: dict) -> None:
    fd, tmp = tempfile.mkstemp(
        prefix=path.name + ".", suffix=".tmp",
        dir=str(path.parent),
    )
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


# --- Public extract ---------------------------------------------------------

def extract_media(
    game_root: Path,
    project_root: Path,
    image_includes: set[str] | None = None,
    image_excludes: set[str] | None = None,
    audio_includes: set[str] | None = None,
    audio_excludes: set[str] | None = None,
    video_includes: set[str] | None = None,
    video_excludes: set[str] | None = None,
    enabled: dict[str, bool] | None = None,
) -> tuple[MediaInventory, dict[str, list[Path]]]:
    """Extract media from `game_root` into `project_root`.

    Returns (inventory, project_files_by_kind) where
    project_files_by_kind maps the media kind to the list of
    .json sidecar paths we wrote. The caller (extract.py) uses
    the sidecar paths to build `.meta.json`.

    Empty targets leave the project with sidecars only — no
    actual asset copies. Inject reads `target` from each sidecar
    and copies the user-supplied replacement file over the
    original. Round-trip is byte-identical when no targets are
    set.

    `enabled` is a per-kind switch (`{"image": True, ...}`); when
    any value is `False`, that kind's assets are not classified,
    no sidecars are written, and `.meta.json` is not populated
    for them. Defaults to "all kinds enabled" when omitted.
    """
    if enabled is None:
        enabled = {"image": True, "audio": True, "video": True}

    by_kind: dict[str, list[Path]] = {"image": [], "audio": [], "video": []}
    inv = MediaInventory(images=[], audios=[], videos=[])
    if not any(enabled.values()):
        return inv, by_kind

    refs = _scan_script_refs(game_root)
    full = _classify_all(game_root, refs)

    if enabled.get("image"):
        inv.images = _filter_sub_media(
            full.images, image_includes, image_excludes
        )
        for asset in inv.images:
            by_kind["image"].append(
                _write_sidecar(asset, project_root)
            )
    if enabled.get("audio"):
        inv.audios = _filter_sub_media(
            full.audios, audio_includes, audio_excludes
        )
        for asset in inv.audios:
            by_kind["audio"].append(
                _write_sidecar(asset, project_root)
            )
    if enabled.get("video"):
        inv.videos = _filter_sub_media(
            full.videos, video_includes, video_excludes
        )
        for asset in inv.videos:
            by_kind["video"].append(
                _write_sidecar(asset, project_root)
            )
    return inv, by_kind


# --- Public inject ----------------------------------------------------------

@dataclass
class MediaInjectResult:
    images_copied: int
    audios_copied: int
    videos_copied: int
    bytes_copied: int


def _iter_sidecars(project_root: Path, kind: str) -> list[Path]:
    """Return all .json sidecars under `<project_root>/<kind>/`."""
    base = project_root / kind
    if not base.is_dir():
        return []
    return sorted(base.rglob("*.json"))


def _inject_sidecar(
    sidecar_path: Path,
    game_root: Path,
    in_place: bool,
    output_root: Path | None,
) -> tuple[bool, int]:
    """Inject one sidecar; return (copied, bytes_copied)."""
    doc = json.loads(sidecar_path.read_text(encoding="utf-8"))
    if not doc.get("entries"):
        return (False, 0)
    entry = doc["entries"][0]
    target = entry.get("target", "")
    if not target:
        return (False, 0)
    target_path = Path(target)
    if not target_path.is_file():
        # User pointed target at a non-existent file — skip
        # rather than fail. A more thorough CLI would report
        # this as a warning.
        return (False, 0)
    source_rel = entry["metadata"]["source_path"]
    source_abs = game_root / source_rel
    if not source_abs.is_file():
        # Source disappeared since extract. Skip.
        return (False, 0)
    # Decide where to write.
    if in_place or output_root is None:
        dest = source_abs
    else:
        dest = output_root / source_rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    _atomic_copy_bytes(target_path, dest)
    return (True, target_path.stat().st_size)


def _atomic_copy_bytes(src: Path, dest: Path) -> None:
    """Copy a file atomically: temp + fsync + os.replace."""
    fd, tmp = tempfile.mkstemp(
        prefix=dest.name + ".", suffix=".tmp",
        dir=str(dest.parent),
    )
    try:
        with os.fdopen(fd, "wb") as f:
            shutil.copyfileobj(src.open("rb"), f)
        # Verify: the bytes we wrote match what we intended.
        with open(tmp, "rb") as f:
            written = f.read()
        with src.open("rb") as f:
            expected = f.read()
        if written != expected:
            raise RuntimeError(f"{dest.name}: copy verification failed")
        os.replace(tmp, dest)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def inject_media(
    project_root: Path,
    game_root: Path,
    in_place: bool = True,
    output_root: Path | None = None,
) -> MediaInjectResult:
    """Inject media from project sidecars back into game files.

    Returns counts of files copied and total bytes.
    """
    res = MediaInjectResult(0, 0, 0, 0)
    for sidecar in _iter_sidecars(project_root, "image"):
        copied, n = _inject_sidecar(sidecar, game_root, in_place, output_root)
        if copied:
            res.images_copied += 1
            res.bytes_copied += n
    for sidecar in _iter_sidecars(project_root, "audio"):
        copied, n = _inject_sidecar(sidecar, game_root, in_place, output_root)
        if copied:
            res.audios_copied += 1
            res.bytes_copied += n
    for sidecar in _iter_sidecars(project_root, "video"):
        copied, n = _inject_sidecar(sidecar, game_root, in_place, output_root)
        if copied:
            res.videos_copied += 1
            res.bytes_copied += n
    return res


# --- meta.json extension entries -------------------------------------------

def build_media_meta_entries(
    inventory: MediaInventory,
    project_files_by_kind: dict[str, list[Path]],
    project_root: Path,
) -> list[dict]:
    """Build the per-file entries for `.meta.json`.

    These mirror the text-media shape but with a `type` of
    `image` / `audio` / `video` and a `sub_media` field per
    docs/shell-layer/13 § 13.4.1.
    """
    entries: list[dict] = []
    for kind in ("image", "audio", "video"):
        assets = {
            "image": inventory.images,
            "audio": inventory.audios,
            "video": inventory.videos,
        }[kind]
        for asset, sidecar in zip(assets, project_files_by_kind[kind]):
            entries.append({
                "project": str(sidecar.relative_to(project_root)),
                "source": asset.source_path,
                "type": kind,
                "sub_media": asset.sub_media,
                "size": asset.size,
                "hash": asset.sha256,
                "extensions": {
                    "sirenhead": {
                        "renpy_kind": kind,
                        "sub_media": asset.sub_media,
                        "media_byte_count": asset.size,
                    },
                },
            })
    return entries
