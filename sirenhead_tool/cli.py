"""Shell-layer argument parsing.

Implements the gallate Shell-layer grammar:

    tool [operation][media] project [options]

Where:

    operation  = -e | -i
    media      = -t | -i | -a | -v   (Ren'Py supports -t for text;
                                     -i (image) is rejected at
                                     runtime because the engine
                                     doesn't expose it)
    project    = path to gallate.yaml
    options    = --output, --ignore, --dry-run, --force, -v, -q,
                 --engine.KEY=VALUE, --help, --version

The operation+media flags form a single token (no separator) per
docs/shell-layer/02-cli-grammar.md:

    tool -et ./gallate.yaml      # extract text
    tool -it ./gallate.yaml      # inject text
    tool -e ./gallate.yaml       # extract default media

We also handle the `init` subcommand and the GCWP discovery
subcommands (`manifest`, `features`, `validation`).
"""

from __future__ import annotations

import argparse
import sys
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class ParsedArgs:
    """Parsed shell-layer invocation."""

    # Subcommand mode. Exactly one of these is set:
    is_init: bool = False
    is_help: bool = False
    is_version: bool = False
    is_manifest: bool = False
    is_features: bool = False
    is_validation: bool = False
    is_extract: bool = False
    is_inject: bool = False

    # Operation scope.
    project_root: Path | None = None
    gallate_yaml: Path | None = None

    # Media flags. For this CLI only `text` is exposed.
    media: set[str] = field(default_factory=set)

    # Standard long flags.
    output: Path | None = None
    ignore: list[str] = field(default_factory=list)
    dry_run: bool = False
    force: bool = False
    verbose: int = 0
    quiet: bool = False

    # Engine extension options. Parsed from `--engine.KEY=VALUE`.
    engine_opts: dict[str, str] = field(default_factory=dict)

    # `init` options.
    init_target: Path | None = None
    init_input: str | None = None
    init_media: list[str] = field(default_factory=list)


def parse(argv: list[str]) -> ParsedArgs:
    """Parse argv (excluding program name) into a ParsedArgs.

    Raises SystemExit on invalid arguments (after printing a usage
    hint on stderr and exiting with code 2 per
    docs/shell-layer/10-stdout-stderr.md § 10.3).
    """
    args = ParsedArgs()

    # Special: discovery subcommands and --help / --version are
    # recognized BEFORE the operation flag, so the user can ask
    # `tool manifest` without supplying a project file.
    if argv and argv[0] in {"manifest", "features", "validation",
                            "help", "--help", "-h", "version",
                            "--version"}:
        sub = argv[0]
        if sub in {"help", "--help", "-h"}:
            args.is_help = True
        elif sub in {"version", "--version"}:
            args.is_version = True
        elif sub == "manifest":
            args.is_manifest = True
        elif sub == "features":
            args.is_features = True
        elif sub == "validation":
            args.is_validation = True
        # consume the subcommand
        argv = argv[1:]
        # Any remaining argv on a discovery subcommand is an error.
        if argv:
            _usage_error(f"{sub} takes no arguments")
        return args

    # Init subcommand.
    if argv and argv[0] == "init":
        args.is_init = True
        argv = argv[1:]
        return _parse_init(argv, args)

    # Operation flag — required for any non-discovery subcommand.
    if not argv or argv[0][0] != "-":
        _usage_error("missing operation flag (-e or -i)")

    op_token = argv[0]
    argv = argv[1:]

    # Parse media letters.
    letters = op_token[1:]
    if not letters or letters[0] not in {"e", "i"}:
        _usage_error(f"invalid operation token: {op_token!r}")
    op = letters[0]
    media_letters = letters[1:]

    if op == "e":
        args.is_extract = True
    elif op == "i":
        args.is_inject = True
    else:
        _usage_error(f"unknown operation: {op}")

    # Map media letters to identifiers. We accept only `t` (text)
    # in this CLI's manifest; other letters are rejected per
    # docs/shell-layer/04-media.md § 4.6 (CLI MUST reject unknown
    # media with exit code 6). To keep Shell-layer exit codes
    # consistent we exit with code 2 (invalid CLI usage) here;
    # runtime media rejection goes through runtime checks in
    # `cli.py`.
    media_map = {"t": "text", "i": "image", "a": "audio", "v": "video"}
    for ml in media_letters:
        if ml not in media_map:
            _usage_error(f"unknown media flag: -{ml}")
        args.media.add(media_map[ml])

    # Positional: project target (path to gallate.yaml).
    if not argv:
        _usage_error("missing project target (path to gallate.yaml)")
    project = Path(argv[0])
    if not project.is_absolute():
        # Per docs/shell-layer/02 § "The positional argument is a
        # path to gallate.yaml". We accept both absolute and
        # relative; relative paths are resolved against the shell's
        # cwd at parse time. Inject operations need to know the
        # Project Root — that's the directory containing
        # gallate.yaml, which we set below.
        project = (Path.cwd() / project).resolve()
    argv = argv[1:]

    if not project.is_file():
        _usage_error(f"gallate.yaml not found: {project}")

    args.gallate_yaml = project
    args.project_root = project.parent.resolve()

    # Remaining argv: standard long flags + engine options.
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in {"--help", "-h"}:
            args.is_help = True
        elif a == "--version":
            args.is_version = True
        elif a == "--output":
            if i + 1 >= len(argv):
                _usage_error("--output requires a value")
            args.output = Path(argv[i + 1])
            i += 1
        elif a == "--ignore":
            if i + 1 >= len(argv):
                _usage_error("--ignore requires a value")
            args.ignore.append(argv[i + 1])
            i += 1
        elif a == "--dry-run":
            args.dry_run = True
        elif a == "--force":
            args.force = True
        elif a in {"-v", "--verbose"}:
            args.verbose += 1
        elif a == "-vv":
            args.verbose += 2
        elif a in {"-q", "--quiet"}:
            args.quiet = True
        elif a.startswith("--engine."):
            if "=" not in a:
                _usage_error(f"--engine.KEY=VALUE required: {a}")
            key, _, val = a[len("--engine."):].partition("=")
            args.engine_opts[key] = val
        else:
            _usage_error(f"unknown option: {a}")
        i += 1

    return args


def _parse_init(argv: list[str], args: ParsedArgs) -> ParsedArgs:
    """Parse `tool init [target] [options]`."""
    # Optional target.
    if argv and not argv[0].startswith("-"):
        args.init_target = Path(argv[0])
        argv = argv[1:]
    else:
        args.init_target = Path.cwd()

    i = 0
    while i < len(argv):
        a = argv[i]
        if a in {"--help", "-h"}:
            args.is_help = True
        elif a == "--input":
            if i + 1 >= len(argv):
                _usage_error("--input requires a value")
            args.init_input = argv[i + 1]
            i += 1
        elif a == "--output":
            if i + 1 >= len(argv):
                _usage_error("--output requires a value")
            args.output = Path(argv[i + 1])
            i += 1
        elif a == "--ignore":
            if i + 1 >= len(argv):
                _usage_error("--ignore requires a value")
            args.ignore.append(argv[i + 1])
            i += 1
        elif a == "--media":
            if i + 1 >= len(argv):
                _usage_error("--media requires a value")
            args.init_media.extend(m.strip() for m in argv[i + 1].split(","))
            i += 1
        elif a == "--force":
            args.force = True
        else:
            _usage_error(f"unknown init option: {a}")
        i += 1

    return args


def _usage_error(msg: str) -> None:
    """Print a usage error and exit with code 2."""
    sys.stderr.write(f"sirenhead-tool: {msg}\n")
    sys.stderr.write(
        "Usage: sirenhead-tool [op][media] ./gallate.yaml [options]\n"
        "       sirenhead-tool init [target] [options]\n"
        "       sirenhead-tool {manifest|features|validation|--help|--version}\n"
    )
    sys.exit(2)


# --- gallate.yaml parsing ---------------------------------------------------

def load_gallate_yaml(path: Path) -> dict:
    """Load and minimally validate a gallate.yaml.

    The full schema is in docs/shell-layer/05-config-file.md. We
    don't enforce every field — engines MAY add their own keys
    under `engine:` — but we extract the standard ones the
    Shell-layer spec mandates.

    Returns an empty dict if the file is empty or has only
    comments.
    """
    import yaml  # type: ignore[import-not-found]

    text = path.read_text(encoding="utf-8")
    if not text.strip():
        return {}
    data = yaml.safe_load(text)
    return data if isinstance(data, dict) else {}
