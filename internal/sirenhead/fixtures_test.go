package sirenhead

// Minimal Ren'Py fixture for the self-tests.
//
// This is a hand-crafted, byte-stable `.rpy` pair that exercises every
// string shape the extractor handles, including the three shapes that
// once shipped broken:
//
//   - `renpy.input("…")` — a prompt inside a `$` Python statement, where
//     no say / narrator / menu rule can reach it;
//   - `"Tea":` — a one-word `menu:` choice with no space and no
//     punctuation, which the narrator prose heuristic rejects (it is
//     recovered by the structural `menu:` pass);
//   - `"..."` — a punctuation-only silent-dialogue beat, which the
//     number/hex exclusion used to read as a numeric constant.
//
// Byte layout is BOM + CRLF, exactly like the real game's files.

import (
	"encoding/base64"
	"os"
	"path/filepath"
)

// scriptContent is the script.rpy fixture body (CRLF line endings).
const scriptContent = "# Test fixture for sirenhead-tool.\r\n" +
	"\r\n" +
	"define s = Character(\"Siren Head\")\r\n" +
	"define c = Character(\"[name]\")\r\n" +
	"\r\n" +
	"init python:\r\n" +
	"    pass\r\n" +
	"\r\n" +
	"label start:\r\n" +
	"    $ name = renpy.input(\"What is your name?\")\r\n" +
	"    c \"Hello there [name].\"\r\n" +
	"    play sound \"test_sound.wav\"\r\n" +
	"    stop sound\r\n" +
	"    \"Suddenly...Out of nowhere...\"\r\n" +
	"    menu:\r\n" +
	"        \"Run away\":\r\n" +
	"            \"You sprint toward the treeline.\"\r\n" +
	"        \"Tea\":\r\n" +
	"            \"...\"\r\n" +
	"        \"Stand your ground\":\r\n" +
	"            \"Your head explodes.\"\r\n" +
	"            return\r\n" +
	"    return\r\n"

// expectedScript maps each script.rpy literal to the kind it must be
// extracted as. A missing or differently-classified entry is a silent
// extraction failure.
var expectedScript = map[string]string{
	"What is your name?":  "ui_prompt",
	"Tea":                 "menu_option",
	"...":                 "narrator",
	"Run away":            "menu_option",
	"Stand your ground":   "menu_option",
	"Hello there [name].": "dialog",
}

// screensContent is the screens.rpy fixture body (CRLF line endings).
//
// The `▸` (U+25B8) lines are deliberate: one character, three bytes in
// UTF-8. They sit BEFORE the strings under test so that a character-vs-
// byte offset mix-up produces a span that does not hold its own source —
// which the self-test asserts against, instead of corrupting a real game.
const screensContent = "# Test fixture for screens.rpy.\r\n" +
	"\r\n" +
	"screen main_menu():\r\n" +
	"    text \"New Game\" action Start()\r\n" +
	"    text \"Continue\" action ShowMenu(\"load\")\r\n" +
	"    text \"About\" action ShowMenu(\"about\")\r\n" +
	"\r\n" +
	"screen about():\r\n" +
	"    text \"This is a test.\"\r\n" +
	"    text \"Version 0.1.0\"\r\n" +
	"\r\n" +
	"screen skip_indicator():\r\n" +
	"    text \"▸\" at delayed_blink(0.0, 1.0)\r\n" +
	"    text \"▸\" at delayed_blink(0.2, 1.0)\r\n" +
	"\r\n" +
	"screen navigation():\r\n" +
	"    textbutton _(\"Back\") action Rollback()\r\n" +
	"    textbutton _(\"History\") action ShowMenu(\"history\")\r\n" +
	"    textbutton _(\"Save\") action ShowMenu(\"save\")\r\n" +
	"    textbutton _(\"Quit\") action Quit(confirm=False)\r\n" +
	"\r\n" +
	"screen preferences():\r\n" +
	"    label _(\"Display\")\r\n" +
	"    textbutton _(\"Window\") action Preference(\"display\", \"window\")\r\n" +
	"    textbutton _(\"Fullscreen\") action Preference(\"display\", \"fullscreen\")\r\n"

// expectedWrapped is every `_("…")` payload in screensContent.
var expectedWrapped = []string{"Back", "History", "Save", "Quit", "Display", "Window", "Fullscreen"}

// fixturePNG is a minimal valid 1x1 PNG.
const fixturePNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk" +
	"YPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// fixtureWAV is a minimal valid RIFF/WAVE header with one silent sample.
var fixtureWAV = func() []byte {
	le := func(v uint32) []byte {
		return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}
	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }
	var out []byte
	out = append(out, []byte("RIFF")...)
	out = append(out, le(36)...)
	out = append(out, []byte("WAVE")...)
	out = append(out, []byte("fmt ")...)
	out = append(out, le(16)...)
	out = append(out, le16(1)...)   // PCM
	out = append(out, le16(1)...)   // mono
	out = append(out, le(22050)...) // sample rate
	out = append(out, le(22050)...) // byte rate
	out = append(out, le16(1)...)   // block align
	out = append(out, le16(16)...)  // bits per sample
	out = append(out, []byte("data")...)
	out = append(out, le(0)...)
	return out
}()

// fixtureOGV is a byte-stable video stub; the engine does not care about
// its content for byte-identity tests.
var fixtureOGV = make([]byte, 256)

// writeFixtures writes the fixture game into gameDir.
func writeFixtures(gameDir string) error {
	if err := os.MkdirAll(filepath.Join(gameDir, "images"), 0o755); err != nil {
		return err
	}
	png, err := base64.StdEncoding.DecodeString(fixturePNGB64)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"script.rpy":             append([]byte{0xEF, 0xBB, 0xBF}, []byte(scriptContent)...),
		"screens.rpy":            append([]byte{0xEF, 0xBB, 0xBF}, []byte(screensContent)...),
		"gui.rpy":                append([]byte{0xEF, 0xBB, 0xBF}, []byte("define gui.text_font = \"DejaVuSans.ttf\"\r\n")...),
		"images/Background1.png": png,
		"images/Character1.png":  png,
		"images/Ending.ogv":      fixtureOGV,
		"test_sound.wav":         fixtureWAV,
	}
	for rel, data := range files {
		path := filepath.Join(gameDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
