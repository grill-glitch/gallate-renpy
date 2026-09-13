package sirenhead

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// joinComma joins a list of strings with ", ".
func joinComma(items []string) string { return strings.Join(items, ", ") }

// resolvePath resolves p against root unless it is already absolute.
// All relative paths in a gallate project are resolved against the
// Project Root (the directory containing gallate.yaml), never against
// the shell's current directory (docs/shell-layer/05-config-file.md
// § 5.11, docs/protocol/04-operations.md § Path resolution).
func resolvePath(p, root string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// nowISO returns an ISO-8601 UTC timestamp with second precision and a
// Z suffix: `2026-09-13T04:58:57Z`.
func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// sha256File returns the algorithm-namespaced content hash of a file,
// e.g. `sha256:abc123…`.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// atomicWriteBytes writes data to path atomically: same-directory temp
// file → read back and verify → rename over the destination. A failed
// write never leaves a partially written file in place, and the
// previous content stays intact.
func atomicWriteBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	// Byte round-trip check: never rename a file we cannot read back
	// byte-for-byte (docs/shell-layer/13-meta-json.md § 13.9).
	back, err := os.ReadFile(tmpName)
	if err != nil {
		cleanup()
		return err
	}
	if len(back) != len(data) || string(back) != string(data) {
		cleanup()
		return fmt.Errorf("%s: byte round-trip mismatch", filepath.Base(path))
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// marshalJSON renders v with the project's canonical JSON shape:
// 2-space indent, no HTML escaping, trailing newline. Python's
// `json.dump(..., ensure_ascii=False, indent=2)` and Go's
// MarshalIndent agree byte-for-byte on this shape, which keeps the Go
// and Python implementations of this CLI interchangeable on disk.
func marshalJSON(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// atomicWriteJSON marshals v and writes it atomically.
func atomicWriteJSON(path string, v any) error {
	data, err := marshalJSON(v)
	if err != nil {
		return err
	}
	return atomicWriteBytes(path, data)
}

// marshalCompact renders v as a single-line JSON object (the JSON Line
// Protocol form used for GCWP messages and discovery documents).
func marshalCompact(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(sb.String(), "\n")), nil
}

// unmarshalJSON parses a JSON document.
func unmarshalJSON(data []byte, v any) error { return json.Unmarshal(data, v) }

// sortStrings sorts a string slice in place (helper so callers do not
// need the sort import for one call).
func sortStrings(items []string) { sort.Strings(items) }

// orderedMap is a JSON object with a caller-controlled key order.
// Unit files need a stable key order (and a configurable one: the
// `text.meta:` block maps spec fields onto JSON keys), which a Go
// struct cannot express.
type orderedMap struct {
	keys []string
	vals []any
}

// Set appends or replaces a key.
func (o *orderedMap) Set(key string, val any) {
	for i, k := range o.keys {
		if k == key {
			o.vals[i] = val
			return
		}
	}
	o.keys = append(o.keys, key)
	o.vals = append(o.vals, val)
}

// MarshalJSON renders the object compactly; encoding/json re-indents
// it when the caller uses an indent.
func (o orderedMap) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		sb.Write(kb)
		sb.WriteByte(':')
		vb, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

// --- ignore patterns --------------------------------------------------------

// ignoreRule is one compiled ignore pattern.
//
// Matching follows the semantics required by
// docs/shell-layer/09-std-flags.md § 9.2: `*` = single segment, `**` =
// multiple segments, `?` = single character, `dir/` = directories
// only. The pattern list is evaluated in order and the last matching
// rule wins, which lets a later `!pattern` re-include something an
// earlier rule excluded.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// compileIgnore compiles a list of gitignore-style patterns.
func compileIgnore(patterns []string) ([]ignoreRule, error) {
	var out []ignoreRule
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(p, "!") {
			rule.negate = true
			p = p[1:]
		}
		if strings.HasSuffix(p, "/") {
			rule.dirOnly = true
			p = strings.TrimSuffix(p, "/")
		}
		p = strings.TrimPrefix(p, "/")
		anchored := strings.Contains(p, "/")
		body := globToRegexp(p)
		if anchored {
			body = "^" + body + "(?:/.*)?$"
		} else {
			body = "(?:^|/)" + body + "(?:/.*)?$"
		}
		re, err := regexp.Compile(body)
		if err != nil {
			return nil, fmt.Errorf("invalid ignore pattern %q: %w", raw, err)
		}
		rule.re = re
		out = append(out, rule)
	}
	return out, nil
}

// globToRegexp converts one glob pattern to an anchored regexp body
// (no anchors; the caller adds them).
func globToRegexp(p string) string {
	var sb strings.Builder
	runes := []rune(p)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				// `**` crosses separators; `**/` also matches zero
				// segments.
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			sb.WriteRune('\\')
			sb.WriteRune(runes[i])
		default:
			sb.WriteRune(runes[i])
		}
	}
	return sb.String()
}

// ignored reports whether the project-relative path rel is excluded by
// the compiled patterns.
func ignored(rules []ignoreRule, rel string, isDir bool) bool {
	rel = filepath.ToSlash(rel)
	state := false
	for _, r := range rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(rel) {
			state = !r.negate
		}
	}
	return state
}

// sortedWalk lists every file under root, in a deterministic order
// (sorted by path), as project-relative slash paths.
func sortedWalk(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
