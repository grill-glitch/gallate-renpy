package sirenhead

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// Config is a loaded `gallate.yaml`.
//
// The document is kept as a generic map rather than a struct because
// engines MAY add their own keys (docs/shell-layer/05-config-file.md
// § 5.1) — `engine:` in particular is an open namespace. Standard keys
// are read through the accessors below.
type Config struct {
	Path string // absolute path to gallate.yaml
	Root string // Project Root: the directory containing gallate.yaml

	raw map[string]any
}

// LoadConfig reads and minimally validates a gallate.yaml.
//
// A missing file is an error, an empty file (or a file with only
// comments) is a valid empty config, and malformed YAML is an
// "invalid project configuration" failure (shell exit code 3).
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, exitErr(ExitProjectConfig, fmt.Sprintf("cannot read %s: %v", path, err))
	}
	cfg := &Config{Path: path, Root: filepath.Dir(path), raw: map[string]any{}}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, exitErr(ExitProjectConfig, fmt.Sprintf("%s is not valid YAML: %v", filepath.Base(path), err))
	}
	if m != nil {
		cfg.raw = m
	}
	return cfg, nil
}

// asString converts a scalar YAML value to its string form.
func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int:
		return fmt.Sprintf("%d", t), true
	case int64:
		return fmt.Sprintf("%d", t), true
	case float64:
		return trimFloat(t), true
	case bool:
		return fmt.Sprintf("%t", t), true
	case nil:
		return "", false
	default:
		return "", false
	}
}

// asBool converts a scalar YAML value to a bool.
func asBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "on":
			return true, true
		case "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

// asStringList accepts either a scalar or a list of scalars.
func asStringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := asString(item); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{t}
	default:
		if s, ok := asString(v); ok {
			return []string{s}
		}
		return nil
	}
}

// asMap returns a nested mapping.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// rawValue looks up a top-level key.
func (c *Config) rawValue(key string) (any, bool) {
	v, ok := c.raw[key]
	return v, ok
}

// String returns a top-level string value.
func (c *Config) String(key string) (string, bool) {
	v, ok := c.rawValue(key)
	if !ok {
		return "", false
	}
	return asString(v)
}

// List returns a top-level list of strings.
func (c *Config) List(key string) []string {
	v, ok := c.rawValue(key)
	if !ok {
		return nil
	}
	return asStringList(v)
}

// Map returns a top-level nested mapping.
func (c *Config) Map(key string) map[string]any {
	v, ok := c.rawValue(key)
	if !ok {
		return nil
	}
	return asMap(v)
}

// MediaList returns the project's default `media:` list.
func (c *Config) MediaList() []string { return c.List("media") }

// Ignore returns the project's default `ignore:` patterns.
func (c *Config) Ignore() []string { return c.List("ignore") }

// TargetLanguage returns the `target` language tag, defaulting to
// `zh-CN` (the language this CLI was written for).
func (c *Config) TargetLanguage() string {
	if v, ok := c.String("target"); ok && v != "" {
		return v
	}
	if v, ok := c.String("target_language"); ok && v != "" {
		return v
	}
	return "zh-CN"
}

// SourceLanguage returns the `source` language tag, defaulting to `en`.
func (c *Config) SourceLanguage() string {
	if v, ok := c.String("source"); ok && v != "" {
		return v
	}
	return "en"
}

// EngineBlock returns the `engine:` mapping (an open namespace).
func (c *Config) EngineBlock() map[string]any { return c.Map("engine") }

// Scripts returns the configured pre / post scripts, in order.
func (c *Config) Scripts() (pre, post []string) {
	s := c.Map("scripts")
	if s == nil {
		return nil, nil
	}
	return asStringList(s["pre"]), asStringList(s["post"])
}

// TextMetadata is the set of metadata-emission flags from the
// `text.metadata:` block (docs/shell-layer/05-config-file.md § 5.8).
type TextMetadata struct {
	OriginalFile  bool
	SourceContext bool
	Location      bool
	Placeholders  bool
	EnginePath    bool
}

// TextConfig is the resolved `text:` block.
type TextConfig struct {
	Layout    string // flat | mirror | single
	Metadata  TextMetadata
	WrittenOn string // extract | never
	MetaKeys  map[string]string

	// Source-context capture (docs/shell-layer/05 § 5.8 `hardcoded`).
	ContextLines    int // lines of surrounding source per entry (default 3)
	MaxContextBytes int // byte cap on one snippet (default 4096)
	EngineExts      []string
}

// Default metadata key names (docs/shell-layer/05-config-file.md § `meta:`).
func defaultMetaKeys() map[string]string {
	return map[string]string{
		"original_file":  "original_file",
		"source_context": "source_context",
		"location":       "location",
		"placeholders":   "placeholders",
		"engine_path":    "engine_path",
	}
}

// Text resolves the `text:` block. Defaults follow the spec: layout
// `flat`, every `metadata.*` flag true, `lifecycle.written_on: extract`,
// and the standard JSON key names.
func (c *Config) Text() (TextConfig, error) {
	out := TextConfig{
		Layout:    "flat",
		WrittenOn: "extract",
		MetaKeys:  defaultMetaKeys(),
		// Spec defaults (docs/shell-layer/05-config-file.md § 5.8).
		ContextLines:    3,
		MaxContextBytes: 4096,
		Metadata: TextMetadata{
			OriginalFile:  true,
			SourceContext: true,
			Location:      true,
			Placeholders:  true,
			EnginePath:    true,
		},
	}
	block := c.Map("text")
	if block == nil {
		return out, nil
	}
	if v, ok := asString(block["layout"]); ok {
		switch v {
		case "flat", "mirror", "single":
			out.Layout = v
		default:
			return out, exitErr(ExitProjectConfig,
				fmt.Sprintf("text.layout must be flat, mirror or single (got %q)", v))
		}
	}
	if v, ok := asString(block["format"]); ok && v != "json" {
		return out, exitErr(ExitProjectConfig,
			fmt.Sprintf("text.format %q is not supported; JSON is the only standard unit format", v))
	}
	if hc := asMap(block["hardcoded"]); hc != nil {
		if v, ok := asInt(hc["context_lines"]); ok {
			if v < 1 {
				return out, exitErr(ExitProjectConfig, "text.hardcoded.context_lines must be >= 1")
			}
			out.ContextLines = v
		}
		if v, ok := asInt(hc["max_bytes"]); ok {
			if v < 1 {
				return out, exitErr(ExitProjectConfig, "text.hardcoded.max_bytes must be >= 1")
			}
			out.MaxContextBytes = v
		}
		out.EngineExts = asStringList(hc["engine_extensions"])
	}
	if md := asMap(block["metadata"]); md != nil {
		if err := applyFlag(md, "original_file", &out.Metadata.OriginalFile); err != nil {
			return out, err
		}
		if err := applyFlag(md, "source_context", &out.Metadata.SourceContext); err != nil {
			return out, err
		}
		if err := applyFlag(md, "location", &out.Metadata.Location); err != nil {
			return out, err
		}
		if err := applyFlag(md, "placeholders", &out.Metadata.Placeholders); err != nil {
			return out, err
		}
		if err := applyFlag(md, "engine_path", &out.Metadata.EnginePath); err != nil {
			return out, err
		}
	}
	if lc := asMap(block["lifecycle"]); lc != nil {
		if v, ok := asString(lc["written_on"]); ok {
			switch v {
			case "extract", "never":
				out.WrittenOn = v
			default:
				return out, exitErr(ExitProjectConfig,
					fmt.Sprintf("text.lifecycle.written_on must be extract or never (got %q)", v))
			}
		}
	}
	if meta := asMap(block["meta"]); meta != nil {
		for field, v := range meta {
			key, ok := asString(v)
			if !ok || strings.TrimSpace(key) == "" {
				return out, exitErr(ExitProjectConfig,
					fmt.Sprintf("text.meta.%s must be a non-empty key name", field))
			}
			if _, known := out.MetaKeys[field]; !known {
				return out, exitErr(ExitProjectConfig,
					fmt.Sprintf("text.meta.%s is not one of the five standard metadata fields", field))
			}
			out.MetaKeys[field] = key
		}
	}
	return out, nil
}

// applyFlag reads one boolean emission flag.
func applyFlag(m map[string]any, key string, dst *bool) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	b, ok := asBool(v)
	if !ok {
		return exitErr(ExitProjectConfig, fmt.Sprintf("text.metadata.%s must be a boolean", key))
	}
	*dst = b
	return nil
}

// engineOptsFor merges `gallate.yaml`'s `engine.<media>.{includes,excludes}`
// with the CLI flags of the same names. CLI wins (docs/shell-layer/08
// § Engine Extensions: the command line is the highest-priority
// configuration source).
func (c *Config) subMediaFilter(args *Args, mediaKind string) (includes, excludes map[string]bool, err error) {
	block := asMap(c.EngineBlock()[mediaKind])
	if v, ok := args.EngineOpts[mediaKind+".includes"]; ok {
		includes = splitCSVSet(v)
	} else if block != nil {
		includes = listToSet(asStringList(block["includes"]))
	}
	if v, ok := args.EngineOpts[mediaKind+".excludes"]; ok {
		excludes = splitCSVSet(v)
	} else if block != nil {
		excludes = listToSet(asStringList(block["excludes"]))
	}
	return includes, excludes, nil
}

// splitCSVSet parses a comma-separated flag value into a set.
func splitCSVSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// listToSet converts a list to a set; an empty list yields nil so that
// "not configured" and "configured empty" stay distinguishable.
func listToSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for _, it := range items {
		out[it] = true
	}
	return out
}

// trimFloat renders a float without a trailing `.0` (YAML scalars such
// as `240` decode as float64 in generic maps).
func trimFloat(f float64) string {
	s := fmt.Sprintf("%g", f)
	return s
}
