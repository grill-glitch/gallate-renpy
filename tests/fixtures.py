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

import base64
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
    '        "Tea":\r\n'
    #      ^ one word, no punctuation, no space. The narrator prose
    #        heuristic rejects this shape, so it is only reachable
    #        via the structural `menu:` pass. This game's real
    #        ice-cream branches (Vanilla / Chocolate / Strawberry)
    #        were lost exactly this way.
    '            "..."\r\n'
    #      ^ pure-punctuation silent-dialogue beat. Excluded by the
    #        number/hex rule unless that rule insists on a digit.
    '        "Stand your ground":\r\n'
    '            "Your head explodes."\r\n'
    '            return\r\n'
    '    return\r\n'
)

# Units the script.rpy fixture MUST yield, keyed by the literal body
# `_(...)`/quote pair holds. Guards three extraction gaps that each
# shipped silently once:
#   * `renpy.input("...")`  — prompt inside a `$` Python statement
#   * `"Tea":`             — one-word menu choice
#   * `"..."`              — punctuation-only dialogue
EXPECTED_SCRIPT = {
    "What is your name?": "ui_prompt",
    "Tea": "menu_option",
    "...": "narrator",
    "Run away": "menu_option",
    "Stand your ground": "menu_option",
    "Hello there [name].": "dialog",
}

# A second fixture: a `screens.rpy` with UI labels, also CRLF + BOM.
# Includes `_("...")` wrapped strings — Ren'Py's explicit
# translation marker. An earlier extractor skipped these entirely
# and silently dropped every menu item, so the self-test asserts
# they are captured.
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
    '\r\n'
    # Non-ASCII BEFORE the strings we assert on. `▸` is one
    # character but THREE bytes in UTF-8. A `str` regex reports
    # character offsets; file injection needs byte offsets. If
    # those are conflated, every entry after this line gets a span
    # shifted by 2 bytes per `▸`, and inject patches the wrong
    # range. Keeping this line here makes that bug fail the
    # self-test instead of corrupting a game.
    'screen skip_indicator():\r\n'
    '    text "▸" at delayed_blink(0.0, 1.0)\r\n'
    '    text "▸" at delayed_blink(0.2, 1.0)\r\n'
    '\r\n'
    'screen navigation():\r\n'
    '    textbutton _("Back") action Rollback()\r\n'
    '    textbutton _("History") action ShowMenu("history")\r\n'
    '    textbutton _("Save") action ShowMenu("save")\r\n'
    '    textbutton _("Quit") action Quit(confirm=False)\r\n'
    '\r\n'
    'screen preferences():\r\n'
    '    label _("Display")\r\n'
    '    textbutton _("Window") action Preference("display", "window")\r\n'
    '    textbutton _("Fullscreen") action Preference("display", "fullscreen")\r\n'
)

# Expected `_("...")` payloads in SCREENS_CONTENT, in source order.
# The self-test asserts every one is extracted, so a regression in
# the `_(` pass fails loudly instead of silently shipping an
# untranslated menu.
EXPECTED_WRAPPED = (
    "Back", "History", "Save", "Quit",
    "Display", "Window", "Fullscreen",
)

# Minimal valid PNG (1x1 black, 67 bytes after base64 decode).
# We use a tiny but valid file so image-inject tests have
# something to copy.
_FIXTURE_PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"
    "YPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
)
# Minimal valid WAV (RIFF header + 1 sample of silence).
import struct as _struct
_FIXTURE_WAV = (
    b"RIFF" + _struct.pack("<I", 36) + b"WAVE"
    + b"fmt " + _struct.pack("<I", 16)
    + _struct.pack("<H", 1)         # PCM
    + _struct.pack("<H", 1)         # mono
    + _struct.pack("<I", 22050)     # sample rate
    + _struct.pack("<I", 22050)     # byte rate
    + _struct.pack("<H", 1)         # block align
    + _struct.pack("<H", 16)        # bits per sample
    + b"data" + _struct.pack("<I", 0)
)
# Minimal video stub — engine doesn't care about content for
# these byte-stability tests.
_FIXTURE_OGV = b"\x00" * 256


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
    # Media fixtures — minimal valid files so image / audio /
    # video extract has something to chew on.
    images = game_dir / "images"
    images.mkdir(exist_ok=True)
    (images / "Background1.png").write_bytes(_FIXTURE_PNG)
    (images / "Character1.png").write_bytes(_FIXTURE_PNG)
    (images / "Ending.ogv").write_bytes(_FIXTURE_OGV)
    (game_dir / "test_sound.wav").write_bytes(_FIXTURE_WAV)
    return script, screens
