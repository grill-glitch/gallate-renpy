package sirenhead

// Discovery documents: `manifest`, `features`, `validation`.
//
// These are the CLI identity / capability documents of
// docs/protocol/03-discovery.md and docs/protocol/08-validation.md.
// Each is a single JSON object on stdout (JSON Line Protocol), and
// each validates against the corresponding schema in the
// specification's `schema/` directory:
//
//	manifest          → schema/manifest.schema.yaml
//	features          → schema/features.schema.yaml
//	validation-rules  → schema/validation-rules.schema.yaml
//
// Key order mirrors the reference (Python) implementation so the two
// are byte-comparable.

// Manifest returns the CLI identity document.
func Manifest() orderedMap {
	return orderedMap{
		keys: []string{"type", "protocol", "id", "name", "version", "engine", "targets"},
		vals: []any{
			"manifest",
			orderedMap{
				keys: []string{"name", "version"},
				vals: []any{ProtocolName, ProtocolVersion},
			},
			CLIID,
			CLIName,
			CLIVersion,
			orderedMap{
				keys: []string{"id", "versions"},
				vals: []any{EngineID, EngineVersions},
			},
			orderedMap{
				keys: []string{"games", "formats", "magic_bytes"},
				vals: []any{
					[]any{
						orderedMap{
							keys: []string{"name", "engine_versions", "ids"},
							vals: []any{GameName, EngineVersions, GameIDs},
						},
					},
					[]any{
						formatRule(".rpy", "", "file"),
						formatRule(".rpyc", "", "file"),
						formatRule(".png", "", "file"),
						formatRule(".jpg", "", "file"),
						formatRule(".wav", "", "file"),
						formatRule(".ogg", "", "file"),
						formatRule(".ogv", "", "file"),
						formatRule("", "game/", "directory"),
						formatRule("", "images/", "directory"),
						formatRule("", "gui/", "directory"),
						formatRule("", "audio/", "directory"),
					},
					[]any{
						orderedMap{
							keys: []string{"offset", "bytes", "encoding", "description"},
							vals: []any{0, "52 45 4e 50 59 20 52 50 43 32", "hex",
								"Ren'Py compiled script magic header"},
						},
						orderedMap{
							keys: []string{"offset", "bytes", "encoding", "description"},
							vals: []any{0, "ef bb bf", "hex",
								"UTF-8 BOM used by script.rpy"},
						},
					},
				},
			},
		},
	}
}

// formatRule builds one `targets.formats` entry (exactly one of
// extension / directory is set).
func formatRule(extension, directory, kind string) orderedMap {
	if extension != "" {
		return orderedMap{
			keys: []string{"extension", "kind"},
			vals: []any{extension, kind},
		}
	}
	return orderedMap{
		keys: []string{"directory", "kind"},
		vals: []any{directory, kind},
	}
}

// Features returns the CLI capability document.
//
// Conformance tier: GCWP 1.0 Standard — Basic (manifest, features,
// operations, exit codes) + events + statistics + validation rules.
// `cancellation` and `status` are Full-tier capabilities this CLI does
// not implement, and they are reported as false rather than omitted
// (docs/protocol/13-conformance.md).
func Features() orderedMap {
	return orderedMap{
		keys: []string{"type", "operations", "media", "validation", "runtime"},
		vals: []any{
			"features",
			orderedMap{
				keys: []string{"extract", "inject", "build", "unpack", "repack", "identify", "validate"},
				vals: []any{
					true,
					true,
					// Ren'Py compiles `.rpyc` from `.rpy` itself at
					// runtime; there is no package step for the CLI to
					// own.
					false,
					// This game ships no `.rpa` archives.
					false,
					false,
					// Engine-extension operations.
					true,
					true,
				},
			},
			orderedMap{
				keys: []string{"text", "image", "audio", "video"},
				vals: []any{true, true, true, true},
			},
			orderedMap{
				keys: []string{"syntax", "regex", "placeholder", "constraint"},
				vals: []any{
					true,
					true,
					// Placeholders are detected automatically from the
					// source (`[name]`, `[config.version]`) and carried
					// on each unit; no separate declared rule type.
					false,
					true,
				},
			},
			orderedMap{
				keys: []string{"events", "status", "statistics", "cancellation"},
				vals: []any{true, false, true, false},
			},
		},
	}
}

// ValidationRules returns the engine-specific validation rule
// declaration.
//
// The top-level `type` is the literal `validation-rules` (per
// schema/validation-rules.schema.yaml). `validation` is the per-finding
// type of the event payload instead — the two MUST NOT be conflated.
func ValidationRules() orderedMap {
	return orderedMap{
		keys: []string{"type", "rules"},
		vals: []any{
			"validation-rules",
			[]any{
				orderedMap{
					keys: []string{"id", "type", "scope", "pattern", "flags", "severity", "description"},
					vals: []any{
						"double-quote-balance",
						"regex",
						"target",
						`"`,
						[]any{},
						"warning",
						"Warns when the translated target contains a `\"` that isn't balanced " +
							"against the source. Ren'Py uses balanced double quotes for strings — " +
							"an extra `\"` silently truncates the literal at parse time.",
					},
				},
				orderedMap{
					keys: []string{"id", "type", "scope", "pattern", "flags", "severity", "description"},
					vals: []any{
						"renpy-substitution-preserved",
						"regex",
						"target",
						`\[(name|config\.name|config\.version|persistent\.[a-z_]+)\]`,
						[]any{},
						"warning",
						"Ren'Py uses `[name]`, `[config.name]`, etc. as string substitutions. " +
							"If a translator deletes or rewrites one, runtime text changes silently.",
					},
				},
				orderedMap{
					keys: []string{"id", "type", "scope", "constraint", "severity", "description"},
					vals: []any{
						"max-target-length",
						"constraint",
						"target",
						orderedMap{
							keys: []string{"maxLength"},
							vals: []any{240},
						},
						"info",
						"Soft cap on the byte length of a translated target. Ren'Py itself " +
							"has no length limit, but very long strings wrap badly inside the textbox.",
					},
				},
			},
		},
	}
}
