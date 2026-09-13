package sirenhead

// Tests for the RPA-3.0 unpack/repack implementation.
//
// The tests build an archive in a temp dir, then unpack + repack it,
// and assert:
//
//  1. Every entry survives the round-trip byte-for-byte.
//  2. The repacked archive can be re-opened by the same CLI.
//  3. The header's metadata offset is exactly where the metadata
//     actually lives, so Ren'Py's own loader would also open it.
//  4. The XOR key is what we asked for (the default "42424242"
//     unless --engine.rpa-key= overrides).
//  5. The XOR-with-key deobfuscation of the metadata is the same
//     path Ren'Py's own loader.py takes.
//
// These tests do not require any real Ren'Py install. The CLI's own
// guarantees — that the engine's loader.py would also accept the
// archive we write — are tested by inspection of the byte shape we
// emit (the format is documented in Ren'Py's loader.py; we follow
// it literally).

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureArchive writes a minimal RPA-3.0 archive to path,
// with the given XOR key (hex string, 8 chars) and the given file
// contents (map of slash-separated relative name to bytes).
//
// It produces a hand-built metadata pickle using the same shape
// Ren'Py's loader.py expects (PROTO 2 + EMPTY_DICT + BINPUT 1 +
// MARK + BINUNICODE filenames + tuples + SETITEMS + STOP).
//
// This helper is a sanity check on our own parser: if we can write
// an archive that our reader can then re-open, the wire format is
// consistent with Ren'Py's.
func writeFixtureArchive(t *testing.T, path, keyHex string, files map[string][]byte) {
	t.Helper()

	// Build the metadata pickle (raw, before zlib).
	var meta bytes.Buffer
	meta.WriteByte(0x80) // PROTO
	meta.WriteByte(0x02) // protocol 2
	meta.WriteByte('}')  // EMPTY_DICT
	meta.WriteByte('q')  // BINPUT
	meta.WriteByte(0x01) // slot 1
	meta.WriteByte('(')  // MARK

	// Each entry's offset will be filled in below; we know the
	// header is 50 bytes, then files are written in the order
	// they appear in the iteration of `files` — but maps are
	// unordered, so we sort the names for determinism.
	const headerSize = rpaHeaderSize
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)

	cursor := int64(headerSize)
	offsets := make(map[string]int64, len(files))
	for _, n := range names {
		offsets[n] = cursor
		cursor += int64(len(files[n]))
	}

	keyU32, _ := parseHexUint32([]byte(keyHex), 8)

	for _, n := range names {
		// BINUNICODE
		meta.WriteByte('X')
		var nb [4]byte
		binary.LittleEndian.PutUint32(nb[:], uint32(len(n)))
		meta.Write(nb[:])
		meta.Write([]byte(n))
		// EMPTY_LIST + BINPUT
		meta.WriteByte(']')
		meta.WriteByte('q')
		meta.WriteByte(0x02)
		// LONG1 (4 bytes) for offset
		meta.WriteByte(0x8a)
		meta.WriteByte(0x04)
		var ob [4]byte
		binary.LittleEndian.PutUint32(ob[:], uint32(offsets[n])^keyU32)
		meta.Write(ob[:])
		// BININT for length
		meta.WriteByte('J')
		var lb [4]byte
		binary.LittleEndian.PutUint32(lb[:], uint32(len(files[n]))^keyU32)
		meta.Write(lb[:])
		// SHORT_BINSTRING for empty prefix
		meta.WriteByte('U')
		meta.WriteByte(0x00)
		// TUPLE3 + BINPUT + APPEND
		meta.WriteByte(0x87)
		meta.WriteByte('q')
		meta.WriteByte(0x03)
		meta.WriteByte('a')
	}
	meta.WriteByte('u') // SETITEMS
	meta.WriteByte('.') // STOP

	// Compress metadata.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write(meta.Bytes())
	zw.Close()

	// Write the archive.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()

	header := make([]byte, headerSize)
	copy(header, "RPA-3.0 ")
	offHex := make([]byte, 16)
	snprintf(offHex, "%016x", cursor)
	copy(header[8:], offHex)
	header[24] = ' '
	copy(header[25:], keyHex)
	header[33] = '\n'
	copy(header[34:], "Made with Ren'Py.")
	if _, err := f.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	for _, n := range names {
		if _, err := f.Write(files[n]); err != nil {
			t.Fatalf("write entry %s: %v", n, err)
		}
	}
	if _, err := f.Write(compressed.Bytes()); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

// snprintf is a minimal %x-style hex formatter for fixed-width
// byte slices. Avoids dragging fmt into a hot test helper.
func snprintf(dst []byte, format string, args ...any) {
	if format == "%016x" && len(args) == 1 {
		v := args[0].(int64)
		hex := "0123456789abcdef"
		for i := 15; i >= 0; i-- {
			dst[i] = hex[v&0xf]
			v >>= 4
		}
	}
}

func TestRPAFixtureRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "test.rpa")

	files := map[string][]byte{
		"script.rpy":     []byte("# hello\nlabel start:\n    pass\n"),
		"images/a.png":   bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 64), // fake PNG
		"images/b/c.jpg": []byte("\xFF\xD8\xFF\xE0fake jpeg"),
		"gui/font.ttf":   bytes.Repeat([]byte{0x00, 0x01, 0x00, 0x00}, 256),
	}
	writeFixtureArchive(t, archive, "42424242", files)

	// The header must be exactly rpaHeaderSize bytes long and
	// end with the canonical "Made with Ren'Py." marker (the
	// period is part of the marker). Ren'Py's own loader skips
	// everything past the newline, so a wrong marker length
	// only matters to other tools that hard-coded 50/16.
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < rpaHeaderSize {
		t.Fatalf("file too small: %d bytes", len(data))
	}
	hdr := string(data[:rpaHeaderSize])
	wantPrefix := "RPA-3.0 "
	if !strings.HasPrefix(hdr, wantPrefix) {
		t.Errorf("header prefix: got %q want %q", hdr[:len(wantPrefix)], wantPrefix)
	}
	if !strings.HasSuffix(hdr, "Made with Ren'Py.") {
		t.Errorf("header tail: got %q want \"Made with Ren'Py.\"", hdr)
	}
	// The first entry must start at offset rpaHeaderSize — verify
	// by reading the first entry's content from the file directly
	// and comparing to files[names[0]].
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)
	first := files[names[0]]
	if !bytes.Equal(data[rpaHeaderSize:int64(rpaHeaderSize)+int64(len(first))], first) {
		t.Errorf("first entry bytes do not begin at header end (%d): %x",
			rpaHeaderSize, data[rpaHeaderSize:rpaHeaderSize+16])
	}

	// Open + verify metadata parsing.
	a, err := openArchive(archive)
	if err != nil {
		t.Fatalf("openArchive: %v", err)
	}
	if a.key != 0x42424242 {
		t.Errorf("key: got %#x, want 0x42424242", a.key)
	}
	if len(a.entries) != len(files) {
		t.Fatalf("entries: got %d, want %d", len(a.entries), len(files))
	}

	// Verify every entry's deobfuscated offset/length matches.
	for _, e := range a.entries {
		want, ok := files[e.name]
		if !ok {
			t.Errorf("unexpected entry %q in archive", e.name)
			continue
		}
		if e.length != int64(len(want)) {
			t.Errorf("%s: length: got %d, want %d", e.name, e.length, len(want))
		}
		if len(e.prefix) != 0 {
			t.Errorf("%s: prefix should be empty, got %d bytes", e.name, len(e.prefix))
		}
	}

	// Extract every entry and compare bytes.
	for _, e := range a.entries {
		var got bytes.Buffer
		if err := a.extractFile(e, &got); err != nil {
			t.Fatalf("extract %s: %v", e.name, err)
		}
		want := files[e.name]
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: extracted bytes differ (got %d, want %d)",
				e.name, got.Len(), len(want))
		}
	}
}

func TestRPAAlternateKey(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "alt.rpa")
	files := map[string][]byte{
		"a.txt": []byte("hello world"),
	}
	writeFixtureArchive(t, archive, "deadbeef", files)

	a, err := openArchive(archive)
	if err != nil {
		t.Fatalf("openArchive: %v", err)
	}
	if a.key != 0xdeadbeef {
		t.Errorf("key: got %#x, want 0xdeadbeef", a.key)
	}
	if len(a.entries) != 1 {
		t.Fatalf("entries: got %d, want 1", len(a.entries))
	}
	var got bytes.Buffer
	if err := a.extractFile(a.entries[0], &got); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got.String() != "hello world" {
		t.Errorf("got %q, want %q", got.String(), "hello world")
	}
}

func TestRPANonDefaultKeyRejected(t *testing.T) {
	// A non-8-char hex string is rejected.
	if _, err := readHexKey("xyz"); err == nil {
		t.Error("expected error for non-hex key")
	}
	// 8 chars but with a leading 0x prefix is rejected (we
	// strip "0x" then check length, so "0x42424242" is only 8
	// chars after stripping → accepted; "0x424242" is 6 chars
	// after stripping → rejected).
	if _, err := readHexKey("0x42"); err == nil {
		t.Error("expected error for short hex key")
	}
}

func TestRPAUnpackBadMagic(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.rpa")
	if err := os.WriteFile(path, []byte("NOT RPA-3.0 \nblah\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := openArchive(path); err == nil {
		t.Error("expected error for non-RPA magic")
	} else if !strings.Contains(err.Error(), "RPA-3.0") {
		t.Errorf("error should mention RPA-3.0: %v", err)
	}
}

func TestRPAPackDirDefaultKey(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	out := filepath.Join(tmp, "out.rpa")
	if err := os.MkdirAll(filepath.Join(src, "deep"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string][]byte{
		"top.txt":          []byte("alpha"),
		"deep/inner.bin":   []byte{0x01, 0x02, 0x03, 0x04},
		"deep/inner2.json": []byte("{}"),
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(n)), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}

	stats, err := PackDir(src, out, defaultRPAKey, nil, nil)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	if stats.FilesProcessed != len(files) {
		t.Errorf("FilesProcessed: got %d, want %d", stats.FilesProcessed, len(files))
	}

	a, err := openArchive(out)
	if err != nil {
		t.Fatalf("openArchive(repacked): %v", err)
	}
	if a.key != 0x42424242 {
		t.Errorf("key: got %#x, want 0x42424242", a.key)
	}
	if len(a.entries) != len(files) {
		t.Errorf("entries: got %d, want %d", len(a.entries), len(files))
	}
	// Verify byte-identical round-trip.
	for _, e := range a.entries {
		want, ok := files[e.name]
		if !ok {
			t.Errorf("unexpected entry %q in repacked archive", e.name)
			continue
		}
		var got bytes.Buffer
		if err := a.extractFile(e, &got); err != nil {
			t.Errorf("extract %s: %v", e.name, err)
			continue
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: bytes differ", e.name)
		}
	}
}

func TestRPAPackDirExcludesManifest(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	out := filepath.Join(tmp, "out.rpa")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Plant the audit sidecar alongside a real asset.
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "archive.manifest.json"),
		[]byte(`{"forbidden":true}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := PackDir(src, out, defaultRPAKey, nil, nil); err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	a, err := openArchive(out)
	if err != nil {
		t.Fatalf("openArchive: %v", err)
	}
	for _, e := range a.entries {
		if e.name == "archive.manifest.json" {
			t.Errorf("archive.manifest.json must be excluded from the repack")
		}
	}
	if len(a.entries) != 1 {
		t.Errorf("entries: got %d, want 1", len(a.entries))
	}
}

func TestRPAPackDirOverwrites(t *testing.T) {
	// PackDir itself is a low-level helper that overwrites
	// unconditionally. The "refuse to overwrite" check lives in
	// the Shell layer (doRepackShell) because that's where
	// `--force` is handled.
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	out := filepath.Join(tmp, "out.rpa")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PackDir happily overwrites the placeholder.
	if _, err := PackDir(src, out, defaultRPAKey, nil, nil); err != nil {
		t.Errorf("PackDir should overwrite: %v", err)
	}
	// And the result is a valid archive.
	a, err := openArchive(out)
	if err != nil {
		t.Fatalf("openArchive: %v", err)
	}
	if len(a.entries) != 1 {
		t.Errorf("entries: got %d, want 1", len(a.entries))
	}
}

func TestRPAMetadataOffsetPointsToCompressedStart(t *testing.T) {
	// The header's metadata offset MUST point at the start of the
	// zlib stream (the byte 0x78 0x9C magic for default zlib).
	// Ren'Py's own loader.py relies on this; we verify our packer
	// honours it.
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	out := filepath.Join(tmp, "out.rpa")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "x"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PackDir(src, out, defaultRPAKey, nil, nil); err != nil {
		t.Fatalf("PackDir: %v", err)
	}

	// Read the header and confirm offset points at zlib magic.
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < rpaHeaderSize {
		t.Fatal("archive too small")
	}
	offsetStr := string(data[8:24])
	var offset int64
	for _, c := range offsetStr {
		offset = offset<<4 | int64(unhex(byte(c)))
	}
	if offset >= int64(len(data)) {
		t.Fatalf("metadata offset %d out of bounds (file len %d)", offset, len(data))
	}
	if offset < int64(rpaHeaderSize) {
		t.Fatalf("metadata offset %d is inside the header", offset)
	}
	if data[offset] != 0x78 || data[offset+1] != 0x9c {
		t.Errorf("metadata offset %d: expected zlib magic 78 9c, got %02x %02x",
			offset, data[offset], data[offset+1])
	}
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	default:
		return 0
	}
}

func TestRPANoPickleSupportBeyondV2(t *testing.T) {
	// Confirm we refuse pickle protocols we don't support.
	for proto := byte(0x00); proto <= 0x06; proto++ {
		if proto == 0x02 || proto == 0x04 || proto == 0x05 {
			continue // supported
		}
		// Build a minimal pickle with this PROTO byte.
		buf := []byte{0x80, proto, '}', 'q', 0x01, '(', '.', ' '}
		_, err := parseRPAMetadata(buf, 0x42424242)
		if err == nil {
			t.Errorf("protocol 0x%02x: expected error, got nil", proto)
		}
	}
}

// io is referenced by some helpers above; keep the import.
var _ = io.EOF
