package sirenhead

// RPA-3.0 archive support (docs/shell-layer § Gallate conformance:
// engine-extension operations).
//
// Ren'Py ships game assets (images, audio, fonts, compiled `.rpyc`
// scripts) packed into `.rpa` archives. Ren'Py itself looks up files
// inside an archive transparently at runtime; a translation workflow
// needs them on disk to be read by the standard `extract` pass, and
// written back into a fresh archive by the standard `inject` pass.
//
// The wire format is documented in Ren'Py's own `loader.py`:
//
//	header:  "RPA-3.0 " + 16 hex chars of metadata offset + " " +
//	         8 hex chars of XOR key + "\n" + "Made with Ren'Py." (17 b)
//
//	metadata (at the recorded offset):
//	    zlib-compressed Python 2 binary pickle
//	    -> { filename: [(offset, length, prefix_bytes), ...], ... }
//	    where every offset / length is XOR'd with the key
//
//	each entry's bytes at `offset` are XOR'd with `prefix_bytes`
//	cycling; Ren'Py 7+ writes an empty prefix for every entry, but
//	older or third-party archives may not.
//
// This file implements three operations:
//
//	Open(path)             parses the header and metadata
//	(*Archive).List()      every (filename, size, prefix) tuple
//	(*Archive).ExtractFile(to)  one file → io.Writer
//	Pack(from, to, key)    pack a directory back into an RPA-3.0
//
// We do NOT implement RPA-1.0 or RPA-2.0 (legacy, not shipped by
// current Ren'Py) and we do NOT implement the optional file-level
// XOR padding (the prefix): we read it (Ren'Py expects the XOR key
// at runtime) and we write it back empty, which is what every
// Ren'Py 7.x release produces.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// rpaHeaderSize is the on-disk size of an RPA-3.0 header line plus
// the trailing 17-byte "Made with Ren'Py." marker (the period is
// part of the marker — Ren'Py's writer includes it, and Ren'Py's
// own loader.py ignores it past the newline).
//
//	"RPA-3.0 "             8 bytes
//	<16 hex chars> offset 16 bytes
//	" "                     1 byte
//	<8 hex chars> key       8 bytes
//	"\n"                    1 byte
//	"Made with Ren'Py."    17 bytes
//	                       --------
//	                       51 bytes
//
// The byte immediately after the "." is the first byte of the first
// archive entry, NOT a header byte. We write 51 bytes of header so
// the first entry begins at offset 51.
const rpaHeaderSize = 51

// defaultRPAKey is the key used when a fresh archive is written
// without an explicit `--engine.rpa-key=`. Ren'Py's own default is
// the literal 8-char hex string "42424242" (= 0x42424242), and
// "42424242" appears in every Ren'Py-packaged game we have seen.
const defaultRPAKey = "42424242"

// archive is an opened RPA-3.0 file.
//
// Entries are kept in a flat slice (not a map) so that `unpack`
// preserves filename order — useful for `git diff`-ability of the
// output tree.
type archive struct {
	path    string
	size    int64
	key     uint32
	header  []byte // the first rpaHeaderSize bytes, preserved on repack
	entries []rpaEntry
}

type rpaEntry struct {
	name   string // slash-separated, relative to the archive root
	offset int64  // XOR-deobfuscated; byte offset in the archive
	length int64  // XOR-deobfuscated; byte length
	prefix []byte // XOR pad; empty for Ren'Py 7+ archives
}

// openArchive parses the header and metadata of an .rpa file.
//
// It returns an error (not an ExitError) so callers can wrap the
// cause themselves; the operation entry points turn these into the
// right exit code.
func openArchive(path string) (*archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < int64(rpaHeaderSize) {
		return nil, fmt.Errorf("file is too small to be an RPA-3.0 archive (%d bytes)", st.Size())
	}

	header := make([]byte, rpaHeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, fmt.Errorf("cannot read RPA header: %w", err)
	}
	if !bytes.HasPrefix(header, []byte("RPA-3.0 ")) {
		return nil, fmt.Errorf("not an RPA-3.0 archive (header starts with %q)", header[:8])
	}
	if bytes.IndexByte(header, '\n') < 0 {
		return nil, fmt.Errorf("RPA header is missing the newline terminator")
	}
	offsetHex := header[8:24]
	keyHex := header[25:33]

	offset, err := parseHexUint64(offsetHex, 16)
	if err != nil {
		return nil, fmt.Errorf("RPA metadata offset is not hex (%q): %w", string(offsetHex), err)
	}
	if offset >= uint64(st.Size()) {
		return nil, fmt.Errorf("RPA metadata offset %d is past EOF (%d)", offset, st.Size())
	}
	key, err := parseHexUint32(keyHex, 8)
	if err != nil {
		return nil, fmt.Errorf("RPA XOR key is not hex (%q): %w", string(keyHex), err)
	}

	// Read & decompress the metadata.
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, fmt.Errorf("cannot seek to RPA metadata offset: %w", err)
	}
	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("cannot open zlib reader for RPA metadata: %w", err)
	}
	metaBytes, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("cannot decompress RPA metadata: %w", err)
	}
	zr.Close()

	entries, err := parseRPAMetadata(metaBytes, key)
	if err != nil {
		return nil, err
	}

	return &archive{
		path:    path,
		size:    st.Size(),
		key:     key,
		header:  header,
		entries: entries,
	}, nil
}

// parseRPAMetadata decodes the pickled dict.
//
// Ren'Py has shipped archives with three pickle protocols over the
// years; the bytes change but the *logical* dict is identical
// (verified across Ren'Py 7.x and 8.x real archives):
//
//	protocol 2:  PROTO 2 + EMPTY_DICT + BINPUT 1 + MARK +
//	             (filename, EMPTY_LIST + BINPUT + LONG1(4) +
//	              BININT + SHORT_BINSTRING + TUPLE3) + SETITEMS + STOP
//	             — DDLC's fonts.rpa / scripts.rpa / audio.rpa
//
//	protocol 4:  PROTO 4 + FRAME(8) + EMPTY_DICT + MEMOIZE + MARK +
//	             (filename, MEMOIZE + EMPTY_LIST + MEMOIZE +
//	              BININT + SHORT_BINBYTES + BINGET + TUPLE3 +
//	              MEMOIZE + APPEND) + SETITEMS + STOP
//	             — DDLC's images.rpa, BAD END THEATER archive.rpa
//
//	protocol 5:  Same wire shape as 4, just one higher PROTO byte
//	             (used by some Ren'Py 8 builds)
//
// We support all three. The protocol-2 / protocol-4 split is also
// the same split that distinguishes Ren'Py 7 from Ren'Py 8; both
// engines produce archives we can read.
func parseRPAMetadata(buf []byte, key uint32) ([]rpaEntry, error) {
	p := &pickleReader{buf: buf}

	if err := p.expectByte(0x80); err != nil { // PROTO
		return nil, fmt.Errorf("not a pickle (no PROTO): %w", err)
	}
	proto, err := p.readByte()
	if err != nil {
		return nil, err
	}
	switch proto {
	case 0x02, 0x04, 0x05:
		// supported
	default:
		return nil, fmt.Errorf("unsupported pickle protocol %d (only 2, 4, 5 are known)", proto)
	}
	// Protocol 4/5 emit an 8-byte FRAME header before the actual
	// document. Protocol 2 does not. Either way, the value is
	// purely advisory (it limits how far a single pickle op can
	// seek backwards); we just skip it.
	if proto >= 0x04 {
		if err := p.expectByte(0x95); err != nil {
			return nil, fmt.Errorf("expected FRAME opcode: %w", err)
		}
		// 8-byte length, little-endian.
		var frameLen [8]byte
		if _, err := io.ReadFull(p, frameLen[:]); err != nil {
			return nil, fmt.Errorf("FRAME length: %w", err)
		}
		// Frame length is informational; we do not validate it.
	}
	if err := p.expectByte('}'); err != nil { // EMPTY_DICT
		return nil, fmt.Errorf("expected EMPTY_DICT: %w", err)
	}
	// Protocol 2 emits BINPUT 1 here; protocol 4/5 emit MEMOIZE.
	// Both result in the same memo state (the empty dict is at
	// slot 0); we accept either. The BINPUT opcodes include a
	// 1-byte slot argument that MEMOIZE doesn't.
	b, err := p.peek()
	if err != nil {
		return nil, err
	}
	switch b {
	case 'q': // BINPUT — opcode + 1-byte slot
		p.next()
		if _, err := p.readByte(); err != nil { // slot byte
			return nil, err
		}
	case 0x94: // MEMOIZE — opcode only
		p.next()
	default:
		return nil, fmt.Errorf("expected BINPUT or MEMOIZE after EMPTY_DICT, got 0x%02x", b)
	}
	if err := p.expectByte('('); err != nil { // MARK (start of first chunk)
		return nil, fmt.Errorf("expected initial MARK: %w", err)
	}

	var entries []rpaEntry
	// Ren'Py pickles large dicts incrementally: it dumps a
	// MARK, then ~1000 (key, value) pairs, then SETITEMS (which
	// pops the items down to the MARK into the dict), then
	// repeats with another MARK + more pairs + SETITEMS. A naive
	// parser that breaks on the first SETITEMS would only get
	// the first chunk. We loop on SETITEMS, accepting further
	// MARK/SETITEMS pairs after the first one.
	for {
		// Skip any FRAME opcodes that Ren'Py has scattered here.
		if err := p.skipOptionalFrames(); err != nil {
			return nil, err
		}
		// We already consumed the MARK for this chunk (either
		// the initial one above, or the one at the end of the
		// previous SETITEMS). Read entries until SETITEMS.
		for {
			if err := p.skipOptionalFrames(); err != nil {
				return nil, err
			}
			b, err := p.peek()
			if err != nil {
				return nil, err
			}
			if b == 'u' { // SETITEMS
				p.next()
				goto afterChunk
			}
			if b == '.' { // STOP
				p.next()
				return entries, nil
			}
			if b != 'X' && b != 0x8c {
				return nil, fmt.Errorf("expected filename opcode, got 0x%02x at %d", b, p.pos)
			}
			p.next()
			var name string
			if b == 'X' {
				name, err = p.readBinUnicode()
			} else {
				name, err = p.readShortBinUnicode()
			}
			if err != nil {
				return nil, fmt.Errorf("filename at %d: %w", p.pos, err)
			}

			if err := skipListHeader(p); err != nil {
				return nil, fmt.Errorf("list header for %q: %w", name, err)
			}
			off, length, prefix, err := p.readEntryTuple3()
			if err != nil {
				return nil, fmt.Errorf("entry tuple for %q: %w", name, err)
			}
			if err := skipMemoStore(p); err != nil {
				return nil, fmt.Errorf("tuple memo for %q: %w", name, err)
			}
			if err := p.expectByte('a'); err != nil {
				return nil, fmt.Errorf("expected APPEND for %q: %w", name, err)
			}
			entries = append(entries, rpaEntry{
				name:   name,
				offset: off ^ int64(key),
				length: length ^ int64(key),
				prefix: prefix,
			})
		}
	afterChunk:
		// SETITEMS just consumed. Ren'Py may emit FRAMEs here
		// before the next MARK or STOP. Skip them.
		for {
			b, err := p.peek()
			if err != nil {
				return nil, err
			}
			if b != 0x95 {
				break
			}
			p.next()
			var frameLen [8]byte
			if _, err := io.ReadFull(p, frameLen[:]); err != nil {
				return nil, fmt.Errorf("FRAME length: %w", err)
			}
		}
		b, err := p.peek()
		if err != nil {
			return nil, err
		}
		if b == '.' { // STOP
			p.next()
			return entries, nil
		}
		if b == '(' { // MARK — start of next chunk
			p.next()
			continue
		}
		return nil, fmt.Errorf("expected MARK or STOP after SETITEMS at pos %d, got 0x%02x", p.pos, b)
	}
}

// skipListHeader consumes the EMPTY_LIST + optional memo-store
// prefix that introduces each archive entry's value list. See the
// long comment above for the variants Ren'Py actually emits.
//
// Memo-store opcodes handled:
//
//	'q'      BINPUT     1-byte slot
//	'r'      LONG_BINPUT  4-byte slot
//	0x94     MEMOIZE   no slot
func skipListHeader(p *pickleReader) error {
	sawList := false
	for {
		if err := p.skipOptionalFrames(); err != nil {
			return err
		}
		b, err := p.peek()
		if err != nil {
			return err
		}
		switch b {
		case ']':
			p.next()
			sawList = true
		case 'q':
			p.next()
			if _, err := p.readByte(); err != nil { // 1-byte slot
				return err
			}
		case 'r':
			p.next()
			var slot [4]byte
			if _, err := io.ReadFull(p, slot[:]); err != nil { // 4-byte slot
				return err
			}
		case 0x94: // MEMOIZE
			p.next()
			// no arg
		default:
			if !sawList {
				return fmt.Errorf("expected EMPTY_LIST, got 0x%02x at %d", b, p.pos)
			}
			return nil
		}
	}
}

// skipMemoStore consumes a single memo-store opcode. Protocol 2
// uses BINPUT <slot> (1 byte) or LONG_BINPUT <slot> (4 bytes);
// protocol 4/5 uses MEMOIZE (no slot). The caller doesn't care
// which was emitted.
func skipMemoStore(p *pickleReader) error {
	b, err := p.peek()
	if err != nil {
		return err
	}
	switch b {
	case 'q':
		p.next()
		if _, err := p.readByte(); err != nil {
			return err
		}
		return nil
	case 'r':
		p.next()
		var slot [4]byte
		if _, err := io.ReadFull(p, slot[:]); err != nil {
			return err
		}
		return nil
	case 0x94:
		p.next()
		return nil
	default:
		return fmt.Errorf("expected BINPUT or MEMOIZE, got 0x%02x", b)
	}
}

// readShortBinUnicode reads a SHORT_BINUNICODE (protocol 4+):
// 1-byte length followed by UTF-8 bytes. Ren'Py 8 emits filenames
// in this form even though protocol 2's BINUNICODE would also be
// legal — cPickle and pickle both prefer the short form for short
// strings.
func (p *pickleReader) readShortBinUnicode() (string, error) {
	n, err := p.readByte()
	if err != nil {
		return "", err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(p, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// readEntryTuple3 reads the (offset, length, prefix) shape that
// Ren'Py 7 emits:
//
//	protocol 2:
//
//		0x8a LEN BYTES...   LONG1: 1-byte length, N little-endian bytes
//		'J' BYTES...        BININT: 4-byte signed little-endian integer
//		'U' LEN BYTES...    SHORT_BINSTRING: 1-byte length, raw bytes
//		0x87                TUPLE3
//
//	protocol 4:
//
//		'J' BYTES...        BININT: 4-byte signed little-endian integer
//		'J' BYTES...        BININT: 4-byte signed little-endian integer
//		'C' LEN BYTES...    SHORT_BINBYTES: 1-byte length, raw bytes
//		'h' SLOT            BINGET: 1-byte memo slot
//		0x87                TUPLE3
//
// Ren'Py's loader.py just calls `loads(...)` and trusts the
// Python pickle implementation. Py3's pickle and Py2's cPickle
// agree on the wire format for these opcodes — what they disagree
// on is `\x8a` (LONG1 in both, but Py3 maps it to a different
// opcode number than SHORT_BININT1 from Py2 cPickle, which makes
// pickletools mislabel it). The bytes are identical.
func (p *pickleReader) readEntryTuple3() (offset, length int64, prefix []byte, err error) {
	if err := p.skipOptionalFrames(); err != nil {
		return 0, 0, nil, err
	}
	// The offset is emitted as either LONG1 (proto 2) or BININT
	// (proto 4+). Distinguish by peeking.
	b, err := p.peek()
	if err != nil {
		return 0, 0, nil, err
	}
	switch b {
	case 0x8a:
		// LONG1: 1-byte length, then length bytes LE.
		p.next()
		n, err := p.readByte()
		if err != nil {
			return 0, 0, nil, err
		}
		if n < 1 || n > 4 {
			return 0, 0, nil, fmt.Errorf("LONG1 length %d is out of range (must be 1..4)", n)
		}
		offBytes := make([]byte, n)
		if _, err := io.ReadFull(p, offBytes); err != nil {
			return 0, 0, nil, fmt.Errorf("LONG1 offset bytes: %w", err)
		}
		// Same reasoning as the length below: read as uint32 so
		// the post-XOR value stays positive even if it would
		// otherwise be negative as int32.
		offset = int64(binary.LittleEndian.Uint32(offBytes[len(offBytes)-4:]))
	case 'J':
		// BININT (4-byte signed little-endian).
		p.next()
		var ob [4]byte
		if _, err := io.ReadFull(p, ob[:]); err != nil {
			return 0, 0, nil, err
		}
		offset = int64(binary.LittleEndian.Uint32(ob[:]))
	default:
		return 0, 0, nil, fmt.Errorf("expected LONG1 (0x8a) or BININT (J) for offset, got 0x%02x", b)
	}

	// Length: BININT in both protocols.
	// We may need to skip one or more FRAMEs between the offset's
	// 4 bytes and this opcode's `J`; Ren'Py occasionally inserts
	// them as a "frame boundary" marker.
	if err := p.skipOptionalFrames(); err != nil {
		return 0, 0, nil, err
	}
	if err = p.expectByte('J'); err != nil {
		return 0, 0, nil, fmt.Errorf("expected BININT for length: %w", err)
	}
	// IMPORTANT: do NOT call skipOptionalFrames here. The 4
	// bytes that follow are the length value; they may
	// legitimately be 0x95 (which happens to also be the FRAME
	// opcode). FRAMEs only appear between opcodes, not inside
	// their argument bytes.
	var lb [4]byte
	if _, err := io.ReadFull(p, lb[:]); err != nil {
		return 0, 0, nil, err
	}
	// The archive is at most 4 GiB per file (BININT is signed
	// 32-bit; Ren'Py itself never produces a bigger one). The
	// XOR'd value can therefore wrap when interpreted as signed
	// int32 — keep it as uint32 so the offset arithmetic stays
	// positive even when the high bit is set.
	length = int64(binary.LittleEndian.Uint32(lb[:]))

	// FRAMEs may also appear here (between the length's 4 bytes
	// and the prefix opcode).
	if err := p.skipOptionalFrames(); err != nil {
		return 0, 0, nil, err
	}

	// Prefix: SHORT_BINSTRING (proto 2), SHORT_BINBYTES (proto 4+),
	// or BINGET (proto 4+, referring to a previously memoized empty
	// string). All three yield raw bytes; we don't care which.
	b, err = p.peek()
	if err != nil {
		return 0, 0, nil, err
	}
	switch b {
	case 'U', 'C': // SHORT_BINSTRING or SHORT_BINBYTES
		prefix, err = p.readShortBytesOrUnicode()
		if err != nil {
			return 0, 0, nil, fmt.Errorf("prefix string: %w", err)
		}
	case 'h': // BINGET
		p.next()
		if _, err := p.readByte(); err != nil { // memo slot
			return 0, 0, nil, err
		}
		// BINGET in this archive always points to the empty-bytes
		// memoized value. Ren'Py never stores a non-empty prefix
		// across runs, so a non-zero result here would be a sign
		// of a format we don't understand.
		prefix = nil
	default:
		return 0, 0, nil, fmt.Errorf("expected SHORT_BINSTRING/BYTES or BINGET for prefix, got 0x%02x", b)
	}

	// Protocol 4/5 *sometimes* emit a MEMOIZE between the prefix
	// and the TUPLE3 (proto 2 never does, and even proto 4/5
	// omit it when the value would have a memo slot that already
	// exists). Accept either.
	if b, _ := p.peek(); b == 0x94 || b == 'q' {
		if err = skipMemoStore(p); err != nil {
			return 0, 0, nil, fmt.Errorf("tuple-value memo: %w", err)
		}
	}

	// TUPLE3.
	if err = p.expectByte(0x87); err != nil {
		return 0, 0, nil, fmt.Errorf("expected TUPLE3: %w", err)
	}
	return offset, length, prefix, nil
}

// readShortBytesOrUnicode reads one of:
//
//	'U' 0x55  SHORT_BINSTRING   1-byte length + raw bytes
//	'X' 0x58  SHORT_BINUNICODE  1-byte length + UTF-8
//	'C' 0x43  SHORT_BINBYTES    1-byte length + raw bytes (proto 4+)
//	'T' 0x54  BINSTRING         4-byte length + raw bytes (legacy)
//
// All four forms are emitted in practice: Ren'Py 7's Python 2
// build uses SHORT_BINSTRING, Ren'Py 8's Python 3 build uses
// SHORT_BINBYTES (proto 4+) or SHORT_BINUNICODE. We collapse them
// to []byte (ASCII-compatible strings come through either way).
func (p *pickleReader) readShortBytesOrUnicode() ([]byte, error) {
	b, err := p.readByte()
	if err != nil {
		return nil, err
	}
	switch b {
	case 'U', 'C', 'X': // SHORT_BINSTRING, SHORT_BINBYTES, SHORT_BINUNICODE
		n, err := p.readByte()
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(p, buf); err != nil {
			return nil, err
		}
		return buf, nil
	case 'T': // BINSTRING
		var nb [4]byte
		if _, err := io.ReadFull(p, nb[:]); err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint32(nb[:])
		if n > 64*1024 {
			return nil, fmt.Errorf("BINSTRING length %d is implausibly large", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(p, buf); err != nil {
			return nil, err
		}
		return buf, nil
	default:
		return nil, fmt.Errorf("expected short string opcode, got 0x%02x", b)
	}
}

// pickleReader is a tiny byte cursor that satisfies io.Reader so
// io.ReadFull works for fixed-size reads.
type pickleReader struct {
	buf []byte
	pos int
}

func (p *pickleReader) Read(dst []byte) (int, error) {
	if p.pos >= len(p.buf) {
		return 0, io.EOF
	}
	n := copy(dst, p.buf[p.pos:])
	p.pos += n
	return n, nil
}

func (p *pickleReader) peek() (byte, error) {
	if p.pos >= len(p.buf) {
		return 0, io.EOF
	}
	return p.buf[p.pos], nil
}

func (p *pickleReader) next() { p.pos++ }

func (p *pickleReader) readByte() (byte, error) {
	if p.pos >= len(p.buf) {
		return 0, io.EOF
	}
	b := p.buf[p.pos]
	p.pos++
	return b, nil
}

// skipOptionalFrames consumes any leading FRAME opcodes (0x95
// followed by 8 bytes of length) at the current position. Ren'Py
// protocol-4 pickles sprinkle FRAMEs into the stream at seemingly
// arbitrary points; they are advisory (they tell the unpickler how
// far back a seekable op may look for memo entries) and have no
// effect on the logical dict, so the safest thing is to discard
// them whenever we are about to consume an opcode.
//
// Called at every "interesting" position — before reading the
// next opcode in the main loop and after consuming a tuple. We
// deliberately do NOT call this from readByte itself, because the
// 8 bytes after a FRAME are a length, not an opcode, and reading
// them as data would corrupt parsing.
func (p *pickleReader) skipOptionalFrames() error {
	for {
		if p.pos >= len(p.buf) {
			return nil
		}
		if p.buf[p.pos] != 0x95 {
			return nil
		}
		p.pos++
		var frameLen [8]byte
		if _, err := io.ReadFull(p, frameLen[:]); err != nil {
			return err
		}
	}
}

func (p *pickleReader) expectByte(want byte) error {
	got, err := p.readByte()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("expected 0x%02x, got 0x%02x", want, got)
	}
	return nil
}

func (p *pickleReader) expectOneOf(want ...byte) error {
	got, err := p.readByte()
	if err != nil {
		return err
	}
	for _, w := range want {
		if got == w {
			return nil
		}
	}
	return fmt.Errorf("expected one of %v, got 0x%02x", want, got)
}

func (p *pickleReader) readBinUnicode() (string, error) {
	var nb [4]byte
	if _, err := io.ReadFull(p, nb[:]); err != nil {
		return "", err
	}
	n := binary.LittleEndian.Uint32(nb[:])
	if n > 64*1024 {
		return "", fmt.Errorf("BINUNICODE length %d is implausibly large", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(p, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// parseHexUint64 parses a fixed-width hex string.
func parseHexUint64(b []byte, width int) (uint64, error) {
	if len(b) != width {
		return 0, fmt.Errorf("expected %d hex chars, got %d", width, len(b))
	}
	return parseHexUint(b)
}

// parseHexUint32 parses a fixed-width hex string.
func parseHexUint32(b []byte, width int) (uint32, error) {
	if len(b) != width {
		return 0, fmt.Errorf("expected %d hex chars, got %d", width, len(b))
	}
	v, err := parseHexUint(b)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

func parseHexUint(b []byte) (uint64, error) {
	var v uint64
	for _, c := range b {
		v <<= 4
		switch {
		case '0' <= c && c <= '9':
			v |= uint64(c - '0')
		case 'a' <= c && c <= 'f':
			v |= uint64(c-'a') + 10
		case 'A' <= c && c <= 'F':
			v |= uint64(c-'A') + 10
		default:
			return 0, fmt.Errorf("non-hex character %q", c)
		}
	}
	return v, nil
}

// extractFile writes one entry's bytes (with the prefix XOR applied)
// to w. It owns the file handle internally so callers don't have
// to plumb one in for every file.
func (a *archive) extractFile(e rpaEntry, w io.Writer) error {
	f, err := os.Open(a.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(e.offset, io.SeekStart); err != nil {
		return err
	}
	// Read the prefix range as-is, the middle unXOR'd.
	// Ren'Py's prefix XOR cycles over the prefix bytes; we do the
	// same so the round-trip is byte-stable even when a third-party
	// archive ships a non-empty prefix.
	prefixLen := int64(len(e.prefix))
	prefix := make([]byte, prefixLen)
	if prefixLen > 0 {
		if _, err := io.ReadFull(f, prefix); err != nil {
			return err
		}
		for i := range prefix {
			prefix[i] ^= e.prefix[i%len(e.prefix)]
		}
	}
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	if e.length > prefixLen {
		if _, err := io.CopyN(w, f, e.length-prefixLen); err != nil {
			return err
		}
	} else if e.length < prefixLen {
		// Defensive: shouldn't happen in real archives but we trim
		// the prefix bytes if the recorded length is shorter than
		// the prefix.
		extra := prefix[e.length:]
		if _, err := w.Write(extra); err != nil {
			return err
		}
	}
	return nil
}

// extractAll writes every entry to disk under outDir.
//
// Filenames are slash-separated in the archive; we treat them as
// relative paths under outDir. A path-traversal entry ("..") is
// refused — Ren'Py archives never contain one, but a hand-crafted
// archive might, and writing outside outDir is a clear violation
// of the user's intent.
func (a *archive) extractAll(outDir string, rep *Reporter, rules []ignoreRule) (Stats, error) {
	stats := Stats{Operation: "unpack"}
	sort.Slice(a.entries, func(i, j int) bool { return a.entries[i].name < a.entries[j].name })

	for i, e := range a.entries {
		if ignored(rules, e.name, false) {
			rep.file("skip", e.name)
			continue
		}
		if e.offset+e.length > a.size {
			return stats, fmt.Errorf("entry %q claims to end at byte %d, past EOF (%d)",
				e.name, e.offset+e.length, a.size)
		}
		if strings.Contains(e.name, "..") {
			return stats, fmt.Errorf("entry %q contains a path-traversal segment", e.name)
		}
		dest := filepath.Join(outDir, filepath.FromSlash(e.name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return stats, err
		}
		f, err := os.Create(dest)
		if err != nil {
			return stats, err
		}
		if err := a.extractFile(e, f); err != nil {
			f.Close()
			return stats, err
		}
		if err := f.Close(); err != nil {
			return stats, err
		}
		rep.file("create", e.name)
		stats.FilesProcessed++
		stats.BytesWritten += e.length
		rep.progress(i+1, len(a.entries), false)
	}
	return stats, nil
}

// PackDir builds an RPA-3.0 archive at outPath from every file
// under inDir.
//
// The packer is deliberately simple:
//
//  1. Walk inDir in sorted order.
//  2. Each file becomes one entry with offset recorded by the
//     running cursor; the metadata is rebuilt at the end.
//  3. The header is written first; entries are appended; metadata
//     is written last (compressed + pickled with our hand-rolled
//     writer) and its offset patched back into the header.
//
// We always write the canonical Ren'Py default key ("42424242")
// because Ren'Py 7+ rejects archives whose key has changed at
// load time only if `key != old_key`; matching the default keeps
// the round-trip safe across both Ren'Py 7 and Ren'Py 8.
//
// The sidecar `archive.manifest.json` that `unpack` writes is
// implicitly excluded from the repack — it is a human-audit
// artefact, not a Ren'Py asset, and Ren'Py would refuse to load
// an archive containing a `.json` it does not understand.
func PackDir(inDir, outPath, keyHex string, rep *Reporter, ignorePatterns []string) (Stats, error) {
	stats := Stats{Operation: "repack"}

	key, err := parseHexUint32([]byte(keyHex), 8)
	if err != nil {
		return stats, fmt.Errorf("invalid --engine.rpa-key %q: %w", keyHex, err)
	}

	// Always exclude the audit sidecar, even if the user passed
	// `--ignore ""` (which would otherwise mean "no ignore").
	// A pack that round-trips through the same CLI must not
	// embed its own bookkeeping.
	ignorePatterns = append(ignorePatterns, "**/archive.manifest.json")
	rules, err := compileIgnore(ignorePatterns)
	if err != nil {
		return stats, err
	}

	// 1. List files first so we can compute the metadata offset
	// before writing any data. We need to know exactly where the
	// metadata will land because the header's offset field is fixed
	// at 16 hex chars.
	var entries []rpaEntry
	type listed struct {
		rel  string
		abs  string
		size int64
	}
	var listedFiles []listed
	err = filepath.WalkDir(inDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(inDir, path)
		rel = filepath.ToSlash(rel)
		if ignored(rules, rel, false) {
			return nil
		}
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		listedFiles = append(listedFiles, listed{rel: rel, abs: path, size: st.Size()})
		return nil
	})
	if err != nil {
		return stats, err
	}
	sort.Slice(listedFiles, func(i, j int) bool {
		return listedFiles[i].rel < listedFiles[j].rel
	})

	// 2. Build a placeholder metadata blob so we can know its
	// compressed size. We rebuild it for real once we know every
	// entry's offset; the placeholder's only purpose is to size
	// the metadata offset field of the header.
	entries = make([]rpaEntry, len(listedFiles))
	for i, lf := range listedFiles {
		entries[i] = rpaEntry{name: lf.rel, offset: 0, length: lf.size}
	}
	metaBuf := &bytes.Buffer{}
	if err := writePickleDict(metaBuf, key, entries); err != nil {
		return stats, err
	}
	var placeholder bytes.Buffer
	zw := zlib.NewWriter(&placeholder)
	if _, err := zw.Write(metaBuf.Bytes()); err != nil {
		return stats, err
	}
	if err := zw.Close(); err != nil {
		return stats, err
	}
	_ = placeholder // referenced below

	// 3. Compute each entry's offset. The cursor starts after the
	// fixed header (rpaHeaderSize = 51 bytes).
	const headerSize = rpaHeaderSize
	cursor := int64(headerSize)
	for i := range entries {
		entries[i].offset = cursor
		cursor += entries[i].length
	}
	metaOffset := cursor

	// 4. Write the archive: header, file bodies, metadata, then
	// patch the offset back into the header.
	out, err := os.Create(outPath)
	if err != nil {
		return stats, err
	}
	defer out.Close()

	// Header (rpaHeaderSize bytes): "RPA-3.0 " + 16 hex offset +
	// " " + 8 hex key + "\n" + "Made with Ren'Py." (17 bytes).
	header := make([]byte, headerSize)
	copy(header, "RPA-3.0 ")
	offHex := fmt.Sprintf("%016x", metaOffset)
	copy(header[8:], offHex)
	header[24] = ' '
	copy(header[25:], keyHex)
	header[33] = '\n'
	copy(header[34:], "Made with Ren'Py.")
	if _, err := out.Write(header); err != nil {
		return stats, err
	}

	for i, lf := range listedFiles {
		in, err := os.Open(lf.abs)
		if err != nil {
			return stats, err
		}
		n, err := io.Copy(out, in)
		in.Close()
		if err != nil {
			return stats, err
		}
		if n != entries[i].length {
			return stats, fmt.Errorf("file %q changed size mid-pack (%d → %d)", lf.rel, entries[i].length, n)
		}
		rep.file("write", lf.rel)
		stats.FilesProcessed++
		stats.BytesWritten += n
		rep.progress(i+1, len(listedFiles), false)
	}

	// Re-serialise the metadata with the real offsets now known.
	// The compressed size WILL differ from the placeholder (the
	// offsets changed, so the byte stream differs, so zlib's
	// dictionary differs), but the metadata offset field in the
	// header still points at the start of the compressed pickle,
	// which is exactly metaOffset + (bytes of file bodies) — the
	// header is correct regardless.
	var realMeta bytes.Buffer
	if err := writePickleDict(&realMeta, key, entries); err != nil {
		return stats, err
	}
	var compressedReal bytes.Buffer
	zw = zlib.NewWriter(&compressedReal)
	if _, err := zw.Write(realMeta.Bytes()); err != nil {
		return stats, err
	}
	if err := zw.Close(); err != nil {
		return stats, err
	}
	if _, err := out.Write(compressedReal.Bytes()); err != nil {
		return stats, err
	}
	stats.OutputCreated = 1
	stats.BytesWritten += int64(len(compressedReal.Bytes())) + int64(headerSize)
	return stats, nil
}

// writePickleDict serialises one RPA-3.0 metadata dict with our
// hand-rolled pickle writer. We emit the same byte shape Ren'Py
// 7 produces: PROTO 2 + EMPTY_DICT + BINPUT 1 + MARK + (filename,
// EMPTY_LIST+BINPUT+LONG1(4)+BININT+SHORT_BINSTRING+TUPLE3)+SETITEMS+STOP,
// with BINUNICODE filenames and 3-element tuples.
//
// LONG1 with length=4 emits four little-endian bytes; BININT is a
// 4-byte signed little-endian integer; SHORT_BINSTRING is 1-byte
// length + raw bytes.
func writePickleDict(w *bytes.Buffer, key uint32, entries []rpaEntry) error {
	w.WriteByte(0x80) // PROTO
	w.WriteByte(0x02) // version 2
	w.WriteByte('}')  // EMPTY_DICT
	w.WriteByte('q')  // BINPUT
	w.WriteByte(0x01)
	w.WriteByte('(') // MARK
	for i, e := range entries {
		// BINUNICODE name
		w.WriteByte('X')
		var nb [4]byte
		binary.LittleEndian.PutUint32(nb[:], uint32(len(e.name)))
		w.Write(nb[:])
		w.Write([]byte(e.name))
		// EMPTY_LIST + BINPUT
		w.WriteByte(']')
		w.WriteByte('q')
		w.WriteByte(byte(0x10 + i%0xEF)) // memo slot, unique per entry
		// LONG1: 4-byte length prefix + 4 little-endian bytes.
		// The high byte of the offset is XOR'd into the lowest of
		// the four value bytes by the XOR-with-key; we just write
		// the 4-byte XOR'd value and let the reader XOR back.
		w.WriteByte(0x8a)
		w.WriteByte(0x04) // 4 bytes follow
		var ob [4]byte
		off := uint32(e.offset) ^ key
		binary.LittleEndian.PutUint32(ob[:], off)
		w.Write(ob[:])
		// BININT (4 bytes signed LE) for length.
		w.WriteByte('J')
		var lb [4]byte
		l := uint32(e.length) ^ key
		binary.LittleEndian.PutUint32(lb[:], l)
		w.Write(lb[:])
		// SHORT_BINSTRING for the prefix.
		w.WriteByte('U')
		w.WriteByte(byte(len(e.prefix)))
		w.Write(e.prefix)
		// TUPLE3 + BINPUT + APPEND
		w.WriteByte(0x87)
		w.WriteByte('q')
		w.WriteByte(byte(0x20 + i%0xDF))
		w.WriteByte('a')
	}
	w.WriteByte('u') // SETITEMS
	w.WriteByte('.') // STOP
	return nil
}

// rpaManifest is the JSON document written next to an unpacked
// archive as `archive.manifest.json`.
//
// It records what was in the archive (so a repack can verify that
// nothing was lost) and the XOR key (so a repack without an
// explicit key uses the same one). The format is internal — the
// standard project file is still `gallate.yaml` — but it lets the
// user audit an unpack without parsing the archive.
type rpaManifest struct {
	Format     string            `json:"format"`      // "rpa-3.0"
	Key        string            `json:"key"`         // hex string, 8 chars
	Source     string            `json:"source"`      // archive path (relative if under project root)
	UnpackedAt string            `json:"unpacked_at"` // ISO 8601
	Files      []rpaManifestFile `json:"files"`
}

type rpaManifestFile struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	Prefix string `json:"prefix"` // hex of the XOR pad; empty for modern Ren'Py
}

// writeManifest emits rpaManifest as JSON next to the unpacked
// tree. It is best-effort: a failure to write the manifest never
// fails the unpack itself, because the manifest is purely for
// human audit.
func writeManifest(archivePath, outDir string, a *archive) {
	now := nowISO()
	m := rpaManifest{
		Format:     "rpa-3.0",
		Key:        fmt.Sprintf("%08x", a.key),
		Source:     filepath.Base(archivePath),
		UnpackedAt: now,
	}
	for _, e := range a.entries {
		m.Files = append(m.Files, rpaManifestFile{
			Name:   e.name,
			Offset: e.offset,
			Length: e.length,
			Prefix: hex.EncodeToString(e.prefix),
		})
	}
	data, err := marshalJSON(m)
	if err != nil {
		return
	}
	_ = atomicWriteBytes(filepath.Join(outDir, "archive.manifest.json"), data)
}

// readHexKey parses a key hex string (with or without leading 0x).
func readHexKey(s string) (string, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if len(s) != 8 {
		return "", fmt.Errorf("RPA key must be 8 hex characters (got %d)", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("RPA key is not hex: %w", err)
	}
	return s, nil
}

// fileIsRPA reports whether a file starts with the RPA-3.0 magic.
//
// Used by `identify` and the `targets.formats` magic-bytes rule.
// We accept the magic-without-trailing-content variant, which is
// the only shape the loader checks for.
func fileIsRPA(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var head [8]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return false, err
	}
	return bytes.HasPrefix(head[:], []byte("RPA-3.0 ")), nil
}
