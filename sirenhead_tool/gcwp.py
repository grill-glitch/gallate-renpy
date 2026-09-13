"""GCWP (protocol layer) — stdin/stdout JSON Line Protocol.

When a Wrapper (e.g. an OmegaT plugin) drives this CLI, it sends
operation requests as JSON Lines on stdin, and reads the
acknowledgement + event stream on stdout.

The wire format follows docs/protocol/02-core-protocol.md:

    stdin   ← request / command (one JSON per line)
    stdout  → response / event stream
    stderr  → human-readable diagnostics (NOT protocol)
    exit    → integer final exit code

This module is a thin dispatcher. It reads one request, runs the
operation, emits a `response` followed by an event stream, and
exits. Multi-request sessions are not supported — one Wrapper =
one CLI process per operation, per docs/protocol/11-process.md.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any


def emit(obj: dict) -> None:
    """Write one JSON object line to stdout and flush."""
    sys.stdout.write(json.dumps(obj, ensure_ascii=False))
    sys.stdout.write("\n")
    sys.stdout.flush()


def emit_event(event: str, **kwargs: Any) -> None:
    """Emit a GCWP event on stdout.

    Per docs/protocol/05-events.md, every event MUST be a single
    JSON object with `type: "event"` and `event: <name>`.
    """
    payload: dict[str, Any] = {"type": "event", "event": event}
    payload.update(kwargs)
    emit(payload)


def run_request(request: dict) -> int:
    """Run a single GCWP request, emit the response + event stream,
    and return the exit code.

    The caller (typically `__main__.main` or a small driver) is
    responsible for actually invoking `sys.exit(code)`.
    """
    op_id = request.get("id", "")
    op = request.get("operation", "")

    # Initial response per docs/protocol/02 § "Operation flow".
    emit({
        "type": "response",
        "id": op_id,
        "operation": op,
        "accepted": True,
        "protocol": {"version": 1.0},
    })

    if op == "identify":
        return _do_identify(request)
    if op == "extract":
        return _do_extract_gcwp(request)
    if op == "inject":
        return _do_inject_gcwp(request)
    if op == "validate":
        return _do_validate_gcwp(request)
    # Per docs/protocol/04 § "Engine-extension operations",
    # unsupported operation => exit 4.
    sys.stderr.write(f"unsupported operation: {op!r}\n")
    return 4


def _do_identify(request: dict) -> int:
    """Identify a game path.

    Per docs/protocol/03 § "Identify", we return a list of
    `matched` rules for the given path. For now we accept any
    path inside a Ren'Py game root (the manifest.targets block
    does the coarse filter; identify is the per-path check).
    """
    op_id = request.get("id", "")
    inputs = request.get("input", [])
    target_path = ""
    if inputs and isinstance(inputs[0], dict):
        target_path = inputs[0].get("path", "")
    elif inputs and isinstance(inputs[0], str):
        target_path = inputs[0]

    target = Path(target_path)
    if not target.exists():
        emit_event("error", code="INPUT_NOT_FOUND",
                   message=f"path not found: {target_path}",
                   path=target_path)
        return 4

    # Magic-byte check: Ren'Py compiled scripts start with `RENPY
    # RPC2`; .rpy files start with UTF-8 BOM (this game) or are
    # plain text. We declare a `medium` confidence match if we see
    # any of these signatures.
    matched: list[dict] = []
    if target.is_file():
        head = target.read_bytes()[:16]
        if head.startswith(b"\xef\xbb\xbf") or head.startswith(b"#"):
            matched.append({
                "engine": "renpy",
                "confidence": "high",
                "rule": {"kind": "magic_bytes",
                         "matched": "ef bb bf"},
                "game": {"name": "Siren Head Dating Sim",
                         "engine_versions": ["7.x"]},
            })
        elif head.startswith(b"RENPY RPC2"):
            matched.append({
                "engine": "renpy",
                "confidence": "high",
                "rule": {"kind": "magic_bytes",
                         "matched": "52 45 4e 50 59 20 52 50 43 32"},
            })
    elif target.is_dir() and (target / "game").is_dir():
        matched.append({
            "engine": "renpy",
            "confidence": "medium",
            "rule": {"kind": "directory", "matched": "game/"},
        })

    emit({
        "type": "identify",
        "id": op_id,
        "target": {"path": str(target), "kind": "file" if target.is_file() else "directory",
                   "size": target.stat().st_size if target.is_file() else None},
        "matched": matched,
    })
    emit_event("completed", id=op_id, operation="identify")
    return 0


def _do_extract_gcwp(request: dict) -> int:
    """Run extract, emitting events on stdout."""
    op_id = request.get("id", "")
    from .cli import parse, _usage_error
    from .extract import do_extract
    from . import cli as cli_mod

    emit_event("started", id=op_id, operation="extract")
    emit_event("phase", name="scanning")

    # The GCWP request format is different from the Shell layer.
    # Translate the GCWP request into Shell-layer argv and run.
    argv = _gcwp_request_to_shell(request, "extract")
    try:
        args = cli_mod.parse(argv)
    except SystemExit as e:
        return e.code if isinstance(e.code, int) else 2

    emit_event("file", action="scan", path=str(args.gallate_yaml))

    emit_event("phase", name="extracting")
    try:
        stats = do_extract(args)
    except Exception as e:
        emit_event("error", code="EXTRACT_FAILED",
                   message=str(e))
        return 7

    emit_event("phase", name="finalizing")
    emit_event("statistics",
               statistics={
                   "files": stats["files"],
                   "text": stats["text"],
                   "output": stats["output"],
                   "duration": stats["duration"],
               })
    emit_event("completed", id=op_id, operation="extract",
               statistics=stats)
    return 0


def _do_inject_gcwp(request: dict) -> int:
    op_id = request.get("id", "")
    from . import cli as cli_mod
    from .extract import do_inject

    emit_event("started", id=op_id, operation="inject")
    argv = _gcwp_request_to_shell(request, "inject")
    try:
        args = cli_mod.parse(argv)
    except SystemExit as e:
        return e.code if isinstance(e.code, int) else 2

    emit_event("phase", name="validating")
    try:
        stats = do_inject(args)
    except RuntimeError as e:
        emit_event("error", code="INJECT_FAILED", message=str(e))
        return 8

    emit_event("statistics", statistics=stats)
    emit_event("completed", id=op_id, operation="inject",
               statistics=stats)
    return 0


def _do_validate_gcwp(request: dict) -> int:
    """Run validation findings across the project's unit files.

    This is a no-op for now: we declare the validation rules in
    `discovery.validation_rules_dict()` and the Wrapper runs them
    in its own validator (per docs/protocol/08 § "Layered rules").
    A more thorough implementation would re-evaluate each unit's
    target against the rules and emit `event: validation`
    findings.
    """
    op_id = request.get("id", "")
    emit_event("started", id=op_id, operation="validate")
    emit_event("completed", id=op_id, operation="validate")
    return 0


def _gcwp_request_to_shell(request: dict, op_name: str) -> list[str]:
    """Translate a GCWP request into Shell-layer argv.

    The GCWP request carries `input` and `output` as path lists.
    We pick the first input as the gallate.yaml and pass the rest
    via `--ignore` (if appropriate) or `--output`. Engine
    extension options pass through as `--engine.KEY=VALUE`.
    """
    argv: list[str] = [f"-{'e' if op_name == 'extract' else 'i'}"]
    # Media flags: only -t for now (text).
    media = request.get("media", ["text"])
    if "text" in media:
        argv[0] += "t"
    inputs = request.get("input", [])
    if inputs:
        first = inputs[0]
        if isinstance(first, dict):
            argv.append(first.get("path", ""))
        else:
            argv.append(str(first))
    # Output path
    outputs = request.get("output", [])
    if outputs:
        first = outputs[0]
        if isinstance(first, dict):
            out_path = first.get("path", "")
        else:
            out_path = str(first)
        if out_path:
            argv.extend(["--output", out_path])
    # Ignore
    for ig in request.get("ignore", []):
        argv.extend(["--ignore", ig])
    # Engine options
    for k, v in request.get("options", {}).items():
        argv.append(f"--engine.{k}={v}")
    return argv
