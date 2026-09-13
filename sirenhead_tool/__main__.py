"""sirenhead-tool — gallate CLI for "Siren Head Dating Sim".

This is the script entry point. It dispatches to one of:

  * Discovery subcommands (`manifest`, `features`, `validation`)
  * Project init (`init`)
  * Operations (`-e` extract, `-i` inject)
  * Built-in flags (`--help`, `--version`)

It implements BOTH layers of the gallate spec:

  Shell layer — invoked from the user's shell with operation flags.
  Protocol layer (GCWP) — same binary can be driven by a Wrapper
  through JSON Lines on stdin. See `gcwp.py` for the protocol-
  layer driver.

Exit codes follow docs/shell-layer/10 § 10.3 for the shell layer
and docs/protocol/02 § "Exit codes" for the protocol layer. The
two tables intentionally overlap for the codes that matter
(operation failure, validation failure, cancellation).
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

from . import discovery, cli
from .discovery import (
    CLI_ID, CLI_NAME, CLI_VERSION, ENGINE_ID, ENGINE_VERSIONS,
    PROTOCOL,
    manifest_dict, features_dict, validation_rules_dict,
)


HELP_TEXT = f"""\
{CLI_NAME} ({CLI_ID}) {CLI_VERSION}
A gallate CLI for the Ren'Py game "Siren Head Dating Sim".

Usage:
  {CLI_ID} -e[media] ./gallate.yaml [options]   Extract translation units
  {CLI_ID} -i[media] ./gallate.yaml [options]   Inject translated units
  {CLI_ID} init [target] [options]              Initialize a project
  {CLI_ID} manifest                             Print CLI manifest (GCWP)
  {CLI_ID} features                             Print CLI features (GCWP)
  {CLI_ID} validation                           Print validation rules
  {CLI_ID} --help                               Show this help
  {CLI_ID} --version                            Show version info

Standard Options:
  -e, -i                  operations (extract / inject)
  -t                      media (text only — this CLI has no image support)
  --output PATH           Override output destination
  --ignore PATTERN        Add an ignore pattern (repeatable)
  --dry-run               Plan without executing
  --force                 Override safety checks
  -v / -vv / -q           Verbose / very-verbose / quiet
  --engine.KEY=VALUE      Engine extension option

Engine Options (renpy):
  --engine.max-length=N   Override the soft cap on a target string
  --engine.use-ast=true   Cross-check against Ren'Py's parser (needs Python 2)
  --engine.in-place=true  Allow in-place inject (default: true)

Conformance: GCWP {PROTOCOL['version']} Standard tier. See
https://github.com/grill-glitch/gallate for the spec.
"""


def main(argv: list[str] | None = None) -> int:
    if argv is None:
        argv = sys.argv[1:]

    # ----- GCWP mode: detect request on stdin -----
    # Per docs/protocol/02 § "Communication model", the Wrapper
    # sends a single JSON request on stdin. We only enter GCWP
    # mode when stdin is NOT a TTY (i.e. the CLI was launched by
    # another process, not by a human at a terminal).
    if not sys.stdin.isatty():
        # Read the first line; if it parses as a JSON object with
        # type=protocol or type=request, run as a GCWP worker.
        try:
            line = sys.stdin.readline()
        except (KeyboardInterrupt, EOFError):
            line = ""
        line = line.strip()
        if line.startswith("{"):
            try:
                request = json.loads(line)
            except json.JSONDecodeError:
                sys.stderr.write("malformed JSON on stdin\n")
                return 7  # protocol error
            t = request.get("type")
            if t == "protocol":
                # Wrapper is checking version. Reply and exit.
                sys.stdout.write(json.dumps({
                    "type": "protocol",
                    "name": "gcwp",
                    "version": PROTOCOL["version"],
                    "supported": True,
                }))
                sys.stdout.write("\n")
                return 0
            if t == "request":
                from .gcwp import run_request
                return run_request(request)
            # Unknown request type.
            sys.stderr.write(f"unknown stdin message type: {t!r}\n")
            return 7

    args = cli.parse(argv)

    # ----- Discovery subcommands -----
    if args.is_help:
        sys.stdout.write(HELP_TEXT)
        return 0
    if args.is_version:
        sys.stdout.write(
            f"{CLI_ID} {CLI_VERSION}\n"
            f"protocol gcwp {PROTOCOL['version']}\n"
            f"engine {ENGINE_ID} ({', '.join(ENGINE_VERSIONS)} compatible)\n"
        )
        return 0
    if args.is_manifest:
        sys.stdout.write(json.dumps(manifest_dict(), ensure_ascii=False))
        sys.stdout.write("\n")
        return 0
    if args.is_features:
        sys.stdout.write(json.dumps(features_dict(), ensure_ascii=False))
        sys.stdout.write("\n")
        return 0
    if args.is_validation:
        sys.stdout.write(json.dumps(validation_rules_dict(), ensure_ascii=False))
        sys.stdout.write("\n")
        return 0

    # ----- init -----
    if args.is_init:
        return _do_init(args)

    # ----- Operations -----
    if args.is_extract:
        return _do_extract_shell(args)
    if args.is_inject:
        return _do_inject_shell(args)

    # Should never reach here.
    sys.stderr.write("sirenhead-tool: no operation specified\n")
    return 2


def _do_init(args) -> int:
    """Create a gallate.yaml in the target directory."""
    from . import cli as cli_mod

    target: Path = args.init_target
    if not target.exists():
        target.mkdir(parents=True)
    yaml_path = target / "gallate.yaml"
    if yaml_path.exists() and not args.force:
        sys.stderr.write(
            f"Error: {yaml_path} already exists. Use --force to overwrite.\n"
        )
        return 2

    # Build a minimal gallate.yaml. Per docs/shell-layer/05 § 5.2
    # this includes input, media, ignore, output, text.
    lines = ["# gallate.yaml — generated by sirenhead-tool init"]
    if args.init_input:
        lines.append(f"input: {args.init_input}")
    else:
        lines.append("input: ./game")
    if args.init_media:
        lines.append("media:")
        for m in args.init_media:
            lines.append(f"  - {m}")
    else:
        lines += ["media:", "  - text"]
    if args.ignore:
        lines.append("ignore:")
        for ig in args.ignore:
            lines.append(f"  - {ig!r}")
    if args.output:
        lines.append(f"output: {args.output}")
    lines += [
        "text:",
        "  format: json",
        "  layout: flat",
        "",
        "engine:",
        "  text_encoding: utf-8",
        "",
    ]
    yaml_path.write_text("\n".join(lines), encoding="utf-8")
    sys.stdout.write(f"Wrote {yaml_path}\n")
    return 0


def _do_extract_shell(args) -> int:
    """Shell-layer extract: stderr for progress, stdout for result path."""
    from .extract import do_extract
    try:
        stats = do_extract(args)
    except Exception as e:
        sys.stderr.write(f"extract failed: {e}\n")
        return 7  # Extraction Failure
    # Per docs/shell-layer/10 § 10.2, the shell layer prints a
    # single result path on stdout as the last line, so scripts
    # can `result=$(tool -e ./gallate.yaml)`.
    sys.stderr.write(
        f"extracted {stats['text']['extracted']} units from "
        f"{stats['files']['scanned']} strings in "
        f"{stats['duration']:.2f}s\n"
    )
    sys.stdout.write(str(args.project_root / "text") + "\n")
    return 0


def _do_inject_shell(args) -> int:
    """Shell-layer inject."""
    from .extract import do_inject
    try:
        stats = do_inject(args)
    except RuntimeError as e:
        sys.stderr.write(f"inject failed: {e}\n")
        return 8  # Injection Failure
    sys.stderr.write(
        f"injected {stats['text']['injected']} units "
        f"({stats['validation']['errors']} drift) in "
        f"{stats['duration']:.2f}s\n"
    )
    sys.stdout.write(str(args.project_root) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
