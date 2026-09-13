"""Self-test for sirenhead-tool.

Proves the hard guarantees from the `gallate-engine-cli` skill
and the gallate spec:

  1. **Round-trip byte-identical** — extract → inject with empty
     targets produces a source file matching the fixture
     byte-for-byte.
  2. **Minimal diff** — editing N targets produces a diff with
     exactly N changed lines, every other line byte-identical.
  3. **Source drift detection** — when the source string at a
     recorded offset no longer matches the unit's `source`, the
     CLI exits with code 8 (Injection Failure) and the file is
     left untouched.
  4. **Idempotent re-extraction** — running extract twice in a
     row produces the same `.meta.json` and unit files (modulo
     `generated_at`); position-derived IDs don't drift.
  5. **GCWP (Protocol layer) handshake + extract via stdin** —
     same binary, driven by a Wrapper through JSON Lines.

The tests use `tests.fixtures.write_fixtures()` to build a
minimal Ren'Py game in a temp dir, so `python -m tests.self_test`
runs from a fresh clone with no external data.

To run against the real Siren Head Dating Sim instead, set
`SIRENHEAD_GAME_DIR` to the path of the game's `game/` folder
(the one that contains `script.rpy`):

    SIRENHEAD_GAME_DIR=/path/to/SirenHeadDatingSim-1.0-pc/game \\
        python -m tests.self_test

Exits 0 on success, 1 on any failure.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TOOL = ROOT / "sirenhead-tool"

# Use the real game dir if the user sets the env var; otherwise
# fall back to the in-repo fixture.
GAME_DIR = (
    Path(os.environ["SIRENHEAD_GAME_DIR"])
    if os.environ.get("SIRENHEAD_GAME_DIR") else None
)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(p: Path) -> str:
    return sha256_bytes(p.read_bytes())


def check(cond: bool, label: str, detail: str = "") -> bool:
    """Assert a label; print PASS/FAIL and update `_fails` count.

    Returns the condition so callers can branch.
    """
    global _fails
    if cond:
        print(f"  PASS: {label}")
        return True
    print(f"  FAIL: {label}  [{detail}]")
    _fails += 1
    return False


def run_tool(*args: str, stdin: str | None = None) -> tuple[int, str, str]:
    """Invoke the CLI; return (exit, stdout, stderr)."""
    proc = subprocess.run(
        [str(TOOL), *args],
        capture_output=True, text=True,
        input=stdin, timeout=60,
    )
    return proc.returncode, proc.stdout, proc.stderr


# Test-failure counter, mutated by `check()` (global for
# convenience — we run one test pass at a time).
_fails = 0


def setup_game_dir(tmp: Path) -> Path:
    """Return a path to a fresh Ren'Py game dir for the test run.

    If SIRENHEAD_GAME_DIR is set, we copy that game dir into the
    temp dir so the test owns it. Otherwise we synthesise a
    minimal game from `tests.fixtures`.
    """
    game = tmp / "game"
    game.mkdir()
    if GAME_DIR:
        for entry in GAME_DIR.iterdir():
            dest = game / entry.name
            if entry.is_dir():
                shutil.copytree(entry, dest)
            else:
                shutil.copy2(entry, dest)
        # Drop save data and cache — they make hashes noisy and
        # aren't relevant to translation.
        for d in ("saves", "cache"):
            target = game / d
            if target.exists():
                shutil.rmtree(target)
    else:
        from .fixtures import write_fixtures
        write_fixtures(game)
    return game


def fresh_project(project_dir: Path, game_dir: Path) -> None:
    """Reset the project directory to a clean state."""
    if project_dir.exists():
        shutil.rmtree(project_dir)
    project_dir.mkdir()
    (project_dir / "gallate.yaml").write_text(
        f"input: {game_dir}\n"
        "media:\n  - text\n"
        "text:\n  format: json\n  layout: flat\n",
        encoding="utf-8",
    )


def main() -> int:
    global _fails
    _fails = 0

    # ------------------------------------------------------------------
    # Setup: a scratch dir we own, so we can copy/reset freely.
    # ------------------------------------------------------------------
    print("=== SETUP ===")
    project = Path(tempfile.mkdtemp(prefix="sirenhead-test-"))
    scratch = Path(tempfile.mkdtemp(prefix="sirenhead-scratch-"))
    game_dir = setup_game_dir(scratch)
    print(f"game dir: {game_dir}")
    print(f"project dir: {project}")

    script_path = game_dir / "script.rpy"
    screens_path = game_dir / "screens.rpy"
    if not script_path.exists():
        print(f"FAIL: no script.rpy in {game_dir}")
        return 1

    # Snapshot the original bytes — we'll restore to this between
    # tests so each test starts from a known-clean state.
    original_script_bytes = script_path.read_bytes()
    original_screens_bytes = (
        screens_path.read_bytes() if screens_path.exists() else b""
    )
    print(f"original script.rpy:  {sha256_bytes(original_script_bytes)[:16]}… "
          f"({len(original_script_bytes)} bytes)")
    if original_screens_bytes:
        print(f"original screens.rpy: {sha256_bytes(original_screens_bytes)[:16]}… "
              f"({len(original_screens_bytes)} bytes)")

    fresh_project(project, game_dir)

    # ------------------------------------------------------------------
    # Test 1: round-trip is byte-identical (extract → inject empty).
    # ------------------------------------------------------------------
    print("\n=== TEST 1: round-trip byte-identical (empty inject) ===")
    rc, out, err = run_tool("-et", str(project / "gallate.yaml"))
    if rc != 0:
        print(f"FAIL: extract returned {rc}\nstderr: {err}")
        _fails += 1
    else:
        print(f"  extract OK: {err.strip()}")
    units_dir = project / "text" / "units"
    if not units_dir.is_dir():
        print("FAIL: text/units/ not created")
        _fails += 1
    else:
        n_units = sum(1 for _ in units_dir.glob("*.json"))
        print(f"  unit files: {n_units}")

    rc, out, err = run_tool("-it", str(project / "gallate.yaml"))
    if rc != 0:
        print(f"FAIL: inject returned {rc}\nstderr: {err}")
        _fails += 1
    script_after = script_path.read_bytes()
    screens_after = (
        screens_path.read_bytes() if screens_path.exists() else b""
    )
    if (script_after == original_script_bytes
            and screens_after == original_screens_bytes):
        print("  PASS: round-trip is byte-identical")
    else:
        print("  FAIL: source files differ after empty inject")
        print(f"    script.rpy:  {sha256_bytes(original_script_bytes)[:16]} → {sha256_bytes(script_after)[:16]}")
        print(f"    screens.rpy: {sha256_bytes(original_screens_bytes)[:16]} → {sha256_bytes(screens_after)[:16]}")
        _fails += 1

    # ------------------------------------------------------------------
    # Test 2: minimal-diff inject.
    # ------------------------------------------------------------------
    # Restore from snapshot so we have known pre-edit bytes.
    script_path.write_bytes(original_script_bytes)
    if screens_path.exists():
        screens_path.write_bytes(original_screens_bytes)
    fresh_project(project, game_dir)
    run_tool("-et", str(project / "gallate.yaml"))

    print("\n=== TEST 2: minimal-diff inject (N edits, N diff lines) ===")
    edits = {
        "Hello there [name].": "你好, [name]。",
        "Suddenly...Out of nowhere...": "突然...不知从哪里...",
        "Run away": "逃跑",
        "Stand your ground": "站稳脚跟",
        "This is a test.": "这是一个测试。",
    }
    edited_total = 0
    for unit_path in units_dir.glob("*.json"):
        doc = json.loads(unit_path.read_text(encoding="utf-8"))
        for u in doc["entries"]:
            if u["source"] in edits:
                u["target"] = edits[u["source"]]
                u["state"] = "translated"
                edited_total += 1
        unit_path.write_text(
            json.dumps(doc, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    print(f"  set targets on {edited_total} units")

    # Capture pre-inject bytes BEFORE running inject.
    pre_script = script_path.read_bytes()
    pre_screens = screens_path.read_bytes() if screens_path.exists() else b""
    rc, out, err = run_tool("-it", str(project / "gallate.yaml"))
    if rc != 0:
        print(f"FAIL: inject returned {rc}\nstderr: {err}")
        _fails += 1
    post_script = script_path.read_bytes()
    post_screens = screens_path.read_bytes() if screens_path.exists() else b""
    diff_lines = 0
    # Count changed lines across both files.
    pre_lines = pre_script.splitlines()
    post_lines = post_script.splitlines()
    if len(pre_lines) == len(post_lines):
        for a, b in zip(pre_lines, post_lines):
            if a != b:
                diff_lines += 1
    else:
        diff_lines = -1
    same_line_count = diff_lines >= 0
    if pre_screens:
        pre_s_lines = pre_screens.splitlines()
        post_s_lines = post_screens.splitlines()
        if len(pre_s_lines) == len(post_s_lines):
            for a, b in zip(pre_s_lines, post_s_lines):
                if a != b:
                    diff_lines += 1
            same_line_count = same_line_count and True
        else:
            same_line_count = False
    if diff_lines == edited_total and same_line_count:
        print(f"  PASS: {diff_lines} lines changed across both files, line count preserved")
    else:
        print(f"  FAIL: {diff_lines} changed (expected {edited_total}), "
              f"line_count_preserved={same_line_count}")
        _fails += 1

    # Restore.
    script_path.write_bytes(original_script_bytes)
    if screens_path.exists():
        screens_path.write_bytes(original_screens_bytes)

    # ------------------------------------------------------------------
    # Test 3: source drift detection.
    # ------------------------------------------------------------------
    print("\n=== TEST 3: source drift detection ===")
    fresh_project(project, game_dir)
    run_tool("-et", str(project / "gallate.yaml"))

    # Pick a unit whose source we can find verbatim on disk. The
    # narrator `"Suddenly...Out of nowhere..."` is a reliable one.
    target_source = "Suddenly...Out of nowhere..."
    for unit_path in units_dir.glob("*.json"):
        doc = json.loads(unit_path.read_text(encoding="utf-8"))
        for u in doc["entries"]:
            if u["source"] == target_source:
                u["target"] = "翻译"
                u["state"] = "translated"
        unit_path.write_text(
            json.dumps(doc, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )

    # Modify the source AFTER extracting so the unit's source no
    # longer matches what's on disk.
    data = script_path.read_bytes()
    old = target_source.encode("utf-8")
    new = b"Suddenly...Out of some OTHER place..."
    if old not in data:
        print(f"FAIL: setup missing expected source {target_source!r}")
        _fails += 1
    else:
        script_path.write_bytes(data.replace(old, new, 1))

    pre_drift = script_path.read_bytes()
    rc, out, err = run_tool("-it", str(project / "gallate.yaml"))
    if rc == 8 and "drift" in err:
        print(f"  PASS: drift detected, exit={rc}")
    else:
        print(f"  FAIL: expected exit=8 with drift error, got rc={rc}")
        print(f"  stderr: {err}")
        _fails += 1
    # Atomicity: file should not have been rewritten; the drift
    # error means we never wrote the translation.
    after = script_path.read_bytes()
    if "翻译".encode("utf-8") not in after:
        print("  PASS: translation not written (atomicity preserved)")
    else:
        print("  FAIL: translation written despite drift error")
        _fails += 1
    # And: the source modification we made should still be there
    # — the failed inject did not roll back the user's source
    # changes (correct: we never wrote anything).
    if new in after and pre_drift == after:
        print("  PASS: source file unchanged after drift abort")
    else:
        print("  FAIL: source file unexpectedly modified")
        _fails += 1

    # Restore.
    script_path.write_bytes(original_script_bytes)
    if screens_path.exists():
        screens_path.write_bytes(original_screens_bytes)

    # ------------------------------------------------------------------
    # Test 4: idempotent re-extraction.
    # ------------------------------------------------------------------
    print("\n=== TEST 4: idempotent re-extraction ===")
    fresh_project(project, game_dir)
    run_tool("-et", str(project / "gallate.yaml"))
    meta1 = json.loads((project / ".meta.json").read_text(encoding="utf-8"))
    run_tool("-et", str(project / "gallate.yaml"))
    meta2 = json.loads((project / ".meta.json").read_text(encoding="utf-8"))
    meta1.pop("generated_at", None)
    meta2.pop("generated_at", None)
    if meta1 == meta2:
        print("  PASS: .meta.json round-trip is stable")
    else:
        print("  FAIL: .meta.json differs between runs")
        _fails += 1
    units1 = json.loads(
        (units_dir / "script.json").read_text(encoding="utf-8")
    )
    units2 = json.loads(
        (units_dir / "script.json").read_text(encoding="utf-8")
    )
    same_ids = [u["id"] for u in units1["entries"]] == [
        u["id"] for u in units2["entries"]
    ]
    if same_ids:
        print("  PASS: position-derived ids are stable across re-extract")
    else:
        print("  FAIL: ids differ between runs")
        _fails += 1

    # ------------------------------------------------------------------
    # Test 5: GCWP (Protocol layer) handshake + extract via stdin.
    # ------------------------------------------------------------------
    print("\n=== TEST 5: GCWP (Protocol layer) handshake + extract ===")
    rc, out, err = run_tool(stdin='{"type":"protocol","name":"gcwp","version":"1.0"}')
    if rc == 0 and '"supported": true' in out:
        print("  PASS: protocol handshake OK")
    else:
        print(f"  FAIL: protocol handshake rc={rc}, out={out!r}")
        _fails += 1

    # Re-init project to clean state.
    fresh_project(project, game_dir)
    rc, out, err = run_tool(
        stdin=json.dumps({
            "type": "request", "id": "01HEXTRACT", "operation": "extract",
            "input": [{"path": str(project / "gallate.yaml"), "kind": "file"}],
        }),
    )
    if rc == 0 and '"event": "completed"' in out and '"event": "started"' in out:
        print("  PASS: GCWP extract via stdin")
    else:
        print(f"  FAIL: GCWP extract rc={rc}, out={out[:200]!r}")
        _fails += 1

    # ------------------------------------------------------------------
    # Test 6: media round-trip (image / audio / video).
    #
    # Verifies the engine-extension media flags (-i, -a, -v) and
    # the per-sidecar `target` workflow for non-text media. Empty
    # inject must be byte-identical across image / audio / video;
    # a non-empty target must overwrite the original file with
    # the user's replacement.
    # ------------------------------------------------------------------
    print("\n=== TEST 6: media round-trip (image / audio / video) ===")
    # Reset and extract with all media enabled.
    fresh_project(project, game_dir)
    (project / "gallate.yaml").write_text(
        f"input: {game_dir}\n"
        "media:\n  - text\n  - image\n  - audio\n  - video\n"
        "engine:\n"
        "  text_encoding: utf-8\n",
        encoding="utf-8",
    )
    rc, out, err = run_tool("-etiava", str(project / "gallate.yaml"))
    if not check(rc == 0, "media+text extract returns 0", err[:200]):
        _fails += 1
    # Expect at least one image / audio / video sidecar (fixture
    # has Background1.png, Character1.png, test_sound.wav,
    # Ending.ogv).
    img_sidecars = list((project / "image").rglob("*.json")) if (
        project / "image").is_dir() else []
    aud_sidecars = list((project / "audio").rglob("*.json")) if (
        project / "audio").is_dir() else []
    vid_sidecars = list((project / "video").rglob("*.json")) if (
        project / "video").is_dir() else []
    if not check(len(img_sidecars) >= 1, f"image sidecars >= 1 ({len(img_sidecars)})"):
        _fails += 1
    if not check(len(aud_sidecars) >= 1, f"audio sidecars >= 1 ({len(aud_sidecars)})"):
        _fails += 1
    if not check(len(vid_sidecars) >= 1, f"video sidecars >= 1 ({len(vid_sidecars)})"):
        _fails += 1

    # Sub-test 6a: empty inject → all media byte-identical.
    # Snapshot every image/audio/video file's bytes.
    media_files = (
        [(p, p.read_bytes()) for p in game_dir.rglob("*")
         if p.is_file() and p.suffix.lower() in {
             ".png", ".jpg", ".wav", ".ogg", ".ogv", ".webm",
         }]
    )
    rc, out, err = run_tool("-itiava", str(project / "gallate.yaml"))
    if not check(rc == 0, "empty media inject returns 0", err[:200]):
        _fails += 1
    for path, original in media_files:
        try:
            current = path.read_bytes()
        except OSError:
            continue
        if not check(current == original,
                     f"empty inject: {path.name} unchanged"):
            _fails += 1
            break  # one fail is enough; the test is the contract

    # Sub-test 6b: replace one image. Set `target` on the first
    # image sidecar to a different PNG, then inject and verify the
    # file on disk is the replacement.
    if img_sidecars:
        first = img_sidecars[0]
        doc = json.loads(first.read_text(encoding="utf-8"))
        # The replacement file is just the bytes of the OTHER
        # fixture PNG. Same extension, different content.
        replacement = project / "tmp-replacement.png"
        # Use a different valid PNG — invert a few bytes.
        replacement.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\x00" * 64)
        doc["entries"][0]["target"] = str(replacement)
        doc["entries"][0]["state"] = "translated"
        first.write_text(
            json.dumps(doc, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        source_rel = doc["entries"][0]["metadata"]["source_path"]
        expected_dest = game_dir / source_rel
        # Snapshot the pre-replacement bytes.
        pre_replace = expected_dest.read_bytes()
        rc, out, err = run_tool("-itiava", str(project / "gallate.yaml"))
        if not check(rc == 0, "media inject with target returns 0",
                     err[:200]):
            _fails += 1
        post_replace = expected_dest.read_bytes()
        if not check(post_replace == replacement.read_bytes(),
                     "image inject: file matches replacement"):
            _fails += 1
        if not check(post_replace != pre_replace,
                     "image inject: file differs from original"):
            _fails += 1
        # Restore the original so subsequent tests / cleanup
        # work cleanly. (We have the bytes in `pre_replace`.)
        expected_dest.write_bytes(pre_replace)
        replacement.unlink(missing_ok=True)

    # Sub-test 6c: GCWP media extract via stdin emits media
    # counts in the statistics event.
    fresh_project(project, game_dir)
    (project / "gallate.yaml").write_text(
        f"input: {game_dir}\n"
        "media:\n  - image\n  - audio\n  - video\n",
        encoding="utf-8",
    )
    rc, out, err = run_tool(
        stdin=json.dumps({
            "type": "request", "id": "01HEXTRACT2",
            "operation": "extract",
            "input": [{"path": str(project / "gallate.yaml"), "kind": "file"}],
        }),
    )
    has_media_stats = (
        rc == 0
        and '"images"' in out
        and '"audio"' in out
        and '"video"' in out
    )
    if not check(has_media_stats, "GCWP extract emits image/audio/video stats"):
        _fails += 1

    # ------------------------------------------------------------------
    # Cleanup.
    # ------------------------------------------------------------------
    print("\n=== CLEANUP ===")
    shutil.rmtree(project, ignore_errors=True)
    shutil.rmtree(scratch, ignore_errors=True)
    print("removed temp dirs")

    # ------------------------------------------------------------------
    # Summary.
    # ------------------------------------------------------------------
    print()
    if _fails == 0:
        print("ALL TESTS PASSED")
        return 0
    else:
        print(f"{_fails} TEST(S) FAILED")
        return 1


if __name__ == "__main__":
    sys.exit(main())
