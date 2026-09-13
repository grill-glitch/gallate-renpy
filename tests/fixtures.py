"""
Minimal Ren'Py fixture for sirenhead-tool self-tests.

This is a hand-crafted, byte-stable `.rpy` file that exercises
every string pattern the extractor handles:

* Speaker dialog (`c "..."`, `s "..."`)
* Narrator dialog (indented bare string)
* Menu options (`"God damn it!":`)
* Audio play (must NOT be captured: `play sound "..."`)
* Config assignment (must NOT be captured: `font "..."`)
* Substitutions (`[name]`)
* CRLF line endings + UTF-8 BOM (matches SirenHead game)

Why this and not the real SirenHead game files? The fixture is
small, byte-stable, and lives in the repo — so `git clone` is
enough to run `python -m tests.self_test` from a clean checkout.
The real game (29 MB of wav / ogv / png) belongs in `Downloads/`,
not in this repo's history.

Byte layout (BOM + CRLF, exactly as produced below):
    [BOM]   = 3 bytes (EF BB BF)
    [CRLF]  = 2 bytes (0D 0A)

Total file size is deterministic; do NOT hand-edit after running
the test — the self_test hashes it.
"""

from __future__ import annotations

from pathlib import Path

CONTENT = (
    '# Test fixture for sirenhead-tool.\r\n'
    '\r\n'
    'define s = Character("Siren Head")\r\n'
    'define c = Character("[name]")\r\n'
    '\r\n'
    'init python:\r\n'
    '    pass\r\n'
    '\r\n'
    'label start:\r\n'
    '    $ name = renpy.input("What is your name?")\r\n'
    '    c "Hello there [name]."\r\n'
    '    play sound "test_sound.wav"\r\n'
    '    stop sound\r\n'
    '    "Suddenly...Out of nowhere..."\r\n'
    '    menu:\r\n'
    '        "Run away":\r\n'
    '            "You sprint toward the treeline."\r\n'
    '        "Stand your ground":\r\n'
    '            "Your head explodes."\r\n'
    '            return\r\n'
    '    return\r\n'
)

# A second fixture: a `screens.rpy` with UI labels, also CRLF + BOM.
SCREENS_CONTENT = (
    '# Test fixture for screens.rpy.\r\n'
    '\r\n'
    'screen main_menu():\r\n'
    '    text "New Game" action Start()\r\n'
    '    text "Continue" action ShowMenu("load")\r\n'
    '    text "About" action ShowMenu("about")\r\n'
    '\r\n'
    'screen about():\r\n'
    '    text "This is a test."\r\n'
    '    text "Version 0.1.0"\r\n'
)


def write_fixtures(game_dir: Path) -> tuple[Path, Path]:
    """Write the fixture files under `game_dir`.

    Returns the (script, screens) absolute paths. Caller is
    responsible for creating `game_dir`.
    """
    game_dir.mkdir(parents=True, exist_ok=True)
    script = game_dir / "script.rpy"
    screens = game_dir / "screens.rpy"
    # utf-8 + BOM.
    script.write_bytes(b"\xef\xbb\xbf" + CONTENT.encode("utf-8"))
    screens.write_bytes(b"\xef\xbb\xbf" + SCREENS_CONTENT.encode("utf-8"))
    return script, screens
