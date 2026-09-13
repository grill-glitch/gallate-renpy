package sirenhead

// `.meta.json` — derived project metadata (docs/shell-layer/13-meta-json.md).
//
// `.meta.json` is the CLI's bookkeeping file: it records the actual
// mapping between gallate project files and original game resources.
// It is NOT a copy of `gallate.yaml` (intent) and NOT the CLI manifest
// (identity).
//
// Two rules matter here:
//
//   - Ownership (§ 13.10): entries written by another CLI are never
//     silently overwritten; this CLI reads the existing file, keeps
//     foreign `files[]` entries verbatim, and only replaces its own.
//   - Atomicity (§ 13.9): temp file → validate by reading back → rename.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// MetaSchemaVersion is the `.meta.json` format version this CLI writes
// and the highest it will load.
const MetaSchemaVersion = 1

// MetaEntry is one `files[]` mapping entry.
type MetaEntry struct {
	Project    string
	Source     string
	Type       string
	SubMedia   string
	Size       int64
	Hash       string
	ResourceID string
	Encoding   string
	CLI        struct{ ID, Version string }
	Engine     struct{ ID, Version string }
	Raw        map[string]any // the entry as read, for verbatim preservation
}

// MetaDoc is a loaded `.meta.json`.
type MetaDoc struct {
	SchemaVersion int
	GeneratedAt   string
	Root          string
	Files         []MetaEntry
}

// readMeta loads `.meta.json` from the project root.
//
// A missing file returns (nil, nil). A `schema_version` higher than
// this CLI knows is refused (§ 13.4.1) rather than misinterpreted.
func readMeta(projectRoot string) (*MetaDoc, error) {
	path := filepath.Join(projectRoot, ".meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, exitErr(ExitProjectConfig, "cannot read .meta.json: "+err.Error())
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, exitErr(ExitProjectConfig, ".meta.json is corrupted: "+err.Error())
	}
	doc := &MetaDoc{}
	if v, ok := asInt(raw["schema_version"]); ok {
		doc.SchemaVersion = v
	} else {
		return nil, exitErr(ExitProjectConfig, ".meta.json has a non-integer schema_version")
	}
	if doc.SchemaVersion > MetaSchemaVersion {
		return nil, exitErr(ExitProjectConfig,
			"`.meta.json` schema_version is higher than this CLI knows; refusing to load")
	}
	doc.GeneratedAt, _ = raw["generated_at"].(string)
	if proj := asMap(raw["project"]); proj != nil {
		doc.Root, _ = proj["root"].(string)
	}
	entries, _ := raw["files"].([]any)
	for _, item := range entries {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		e := MetaEntry{Raw: m}
		e.Project, _ = m["project"].(string)
		e.Source, _ = m["source"].(string)
		e.Type, _ = m["type"].(string)
		e.SubMedia, _ = m["sub_media"].(string)
		e.Hash, _ = m["hash"].(string)
		e.ResourceID, _ = m["resource_id"].(string)
		e.Encoding, _ = m["encoding"].(string)
		if n, ok := asInt(m["size"]); ok {
			e.Size = int64(n)
		}
		if cli := asMap(m["cli"]); cli != nil {
			e.CLI.ID, _ = cli["id"].(string)
			e.CLI.Version, _ = cli["version"].(string)
		}
		if eng := asMap(m["engine"]); eng != nil {
			e.Engine.ID, _ = eng["id"].(string)
			e.Engine.Version, _ = eng["version"].(string)
		}
		doc.Files = append(doc.Files, e)
	}
	return doc, nil
}

// foreignMetaEntries returns the entries of an existing `.meta.json`
// that belong to another CLI, in their original shape.
func foreignMetaEntries(existing *MetaDoc) []any {
	var out []any
	if existing == nil {
		return nil
	}
	for _, e := range existing.Files {
		if e.CLI.ID != "" && e.CLI.ID != CLIID {
			out = append(out, e.Raw)
		}
	}
	return out
}

// textMetaEntry builds a `files[]` entry for one text unit file, in the
// reference implementation's key order.
func textMetaEntry(projectRoot string, w writtenUnitFile) (*orderedMap, error) {
	abs := filepath.Join(projectRoot, filepath.FromSlash(w.rel))
	hash, err := sha256File(abs)
	if err != nil {
		return nil, exitErr(ExitOutputFailure, err.Error())
	}
	var size int64
	if st, serr := os.Stat(abs); serr == nil {
		size = st.Size()
	}
	// docs/shell-layer/13 § 13.4.1: `sub_media` describes the resource's
	// sub-media category. The reference implementation records the first
	// unit's kind here; a unit file can of course mix kinds.
	subMedia := ""
	byteCount := 0
	if len(w.units) > 0 {
		subMedia = w.units[0].Kind
	}
	for _, u := range w.units {
		byteCount += len(u.Source) // UTF-8 bytes, as the key name says
	}
	m := &orderedMap{}
	m.Set("project", w.rel)
	if len(w.sources) == 1 {
		m.Set("source", w.sources[0])
	}
	m.Set("type", "text")
	cli := &orderedMap{}
	cli.Set("id", CLIID)
	cli.Set("version", CLIVersion)
	m.Set("cli", cli)
	eng := &orderedMap{}
	eng.Set("id", EngineID)
	m.Set("engine", eng)
	m.Set("sub_media", subMedia)
	m.Set("size", size)
	m.Set("hash", hash)
	ext := &orderedMap{}
	ours := &orderedMap{}
	ours.Set("renpy_kind", subMedia)
	ours.Set("byte_count", byteCount)
	ext.Set(CLIID, ours)
	m.Set("extensions", ext)
	return m, nil
}

// newMetaDoc assembles a `.meta.json` document: this CLI's entries plus
// any foreign entries preserved from the previous file.
func newMetaDoc(entries []any) *orderedMap {
	doc := &orderedMap{}
	doc.Set("schema_version", MetaSchemaVersion)
	gen := &orderedMap{}
	gen.Set("id", CLIID)
	gen.Set("version", CLIVersion)
	doc.Set("generated_by", gen)
	doc.Set("generated_at", nowISO())
	proj := &orderedMap{}
	proj.Set("root", ".")
	doc.Set("project", proj)
	doc.Set("files", entries)
	return doc
}

// writeMeta writes `.meta.json` atomically.
func writeMeta(projectRoot string, doc *orderedMap) error {
	path := filepath.Join(projectRoot, ".meta.json")
	if err := atomicWriteJSON(path, doc); err != nil {
		return exitErr(ExitOutputFailure, "cannot write .meta.json: "+err.Error())
	}
	return nil
}
