"""CLI manifest / features / validation declarations.

These are returned by the discovery subcommands:

    sirenhead-tool manifest
    sirenhead-tool features
    sirenhead-tool validation

The shapes follow the GCWP 1.0 schemas in the gallate spec.
"""

from __future__ import annotations

CLI_ID = "sirenhead"
CLI_NAME = "Siren Head Dating Sim CLI"
# Kept in sync with pyproject.toml and sirenhead_tool/extract.__version__.
# Bump in lockstep with those — otherwise `sirenhead-tool --version`
# lies, which the v0.2.0 ad-hoc verify script caught immediately.
CLI_VERSION = "0.3.0"
ENGINE_ID = "renpy"
# Engine versions tested: Ren'Py 7.x (script_version.txt pattern,
# pyo/py files dated 2019, `from __future__ import print_function`).
ENGINE_VERSIONS = ["7.x"]

PROTOCOL = {"name": "gcwp", "version": "1.0"}


def manifest_dict() -> dict:
    """Return the manifest JSON object.

    Per docs/protocol/03-discovery.md the manifest includes a
    `targets` block so a Wrapper can pre-filter and `identify <path>`
    before launching extract/inject.
    """
    return {
        "type": "manifest",
        "protocol": PROTOCOL,
        "id": CLI_ID,
        "name": CLI_NAME,
        "version": CLI_VERSION,
        "engine": {"id": ENGINE_ID, "versions": ENGINE_VERSIONS},
        "targets": {
            "games": [
                {
                    "name": "Siren Head Dating Sim",
                    "engine_versions": ["7.x"],
                    "ids": ["sirenhead-dating-sim"],
                },
            ],
            "formats": [
                {"extension": ".rpy", "kind": "file"},
                {"extension": ".rpyc", "kind": "file"},
                {"extension": ".png", "kind": "file"},
                {"extension": ".jpg", "kind": "file"},
                {"extension": ".wav", "kind": "file"},
                {"extension": ".ogg", "kind": "file"},
                {"extension": ".ogv", "kind": "file"},
                {"directory": "game/", "kind": "directory"},
                {"directory": "images/", "kind": "directory"},
                {"directory": "gui/", "kind": "directory"},
                {"directory": "audio/", "kind": "directory"},
            ],
            "magic_bytes": [
                # Ren'Py compiled script header (`RENPY RPC2`).
                # See /game/script.rpyc first 8 bytes.
                {
                    "offset": 0,
                    "bytes": "52 45 4e 50 59 20 52 50 43 32",
                    "encoding": "hex",
                    "description": "Ren'Py compiled script magic header",
                },
                # UTF-8 BOM is the script.rpy convention this game uses.
                {
                    "offset": 0,
                    "bytes": "ef bb bf",
                    "encoding": "hex",
                    "description": "UTF-8 BOM used by script.rpy",
                },
            ],
        },
    }


def features_dict() -> dict:
    """Return the features JSON object.

    Standard tier per docs/protocol/13-conformance.md:
        Basic + events + statistics + validation (regex).
    """
    return {
        "type": "features",
        "operations": {
            "extract": True,
            "inject": True,
            # Ren'Py doesn't really have a separate "build" step the
            # CLI owns; the engine itself compiles rpyc at runtime
            # from rpy. We expose it but it is a no-op.
            "build": False,
            # Ren'Py archives (rpa) are not used by this game;
            # no unpack/repack.
            "unpack": False,
            "repack": False,
            # Engine-extension operations we add.
            "identify": True,
            "validate": True,
        },
        "media": {
            "text": True,
        # Engine-extension media. Ren'Py ships with image, audio,
        # and video support — every game can localize any of them
        # depending on its assets.
        "image": True,
        # Audio is possible when the game has voice / music / sfx
        # files; this CLI extracts all three sub-media and lets
        # the user filter via `gallate.yaml`'s `engine.audio.*`.
        "audio": True,
        "video": True,
        },
        "validation": {
            "syntax": True,
            "regex": True,
            "placeholder": False,
            "constraint": True,
        },
        "runtime": {
            "events": True,
            "status": False,        # not implemented yet
            "statistics": True,
            "cancellation": False,  # not implemented yet
        },
    }


def validation_rules_dict() -> dict:
    """Return validation rules per docs/protocol/08-validation.md.

    Two rule kinds are wired up to satisfy Standard tier:

    1. `double-quote-balance` (regex) — every double-quoted string in
       the source must appear in the target. Catches accidental
       string truncation when an editor folds a `"` together.
    2. `max-target-length` (constraint) — soft cap on a target's
       length, configurable via `--engine.max-length=N`.

    Placeholders (`{player}`, `[name]`, etc.) are detected
    automatically from the source by the CLI itself, so the
    `placeholder` rule type is not declared.

    The top-level `type` is `validation-rules` per the
    `validation-rules.schema.yaml` const (NOT `validation` —
    `validation` is the per-finding type in
    `validation-result.schema.yaml`).
    """
    return {
        "type": "validation-rules",
        "rules": [
            {
                "id": "double-quote-balance",
                "type": "regex",
                "scope": "target",
                "pattern": '"',
                "flags": [],
                "severity": "warning",
                "description": (
                    "Warns when the translated target contains a `\"` "
                    "that isn't balanced against the source. Ren'Py "
                    "uses balanced double quotes for strings — an "
                    "extra `\"` silently truncates the literal at "
                    "parse time."
                ),
            },
            {
                "id": "renpy-substitution-preserved",
                "type": "regex",
                "scope": "target",
                "pattern": r"\[(name|config\.name|config\.version|persistent\.[a-z_]+)\]",
                "flags": [],
                "severity": "warning",
                "description": (
                    "Ren'Py uses `[name]`, `[config.name]`, etc. as "
                    "string substitutions. If a translator deletes "
                    "or rewrites one, runtime text changes "
                    "silently."
                ),
            },
            {
                "id": "max-target-length",
                "type": "constraint",
                "scope": "target",
                "constraint": {"maxLength": 240},
                "severity": "info",
                "description": (
                    "Soft cap on the byte length of a translated "
                    "target. Ren'Py itself has no length limit, but "
                    "very long strings wrap badly inside the "
                    "textbox."
                ),
            },
        ],
    }
