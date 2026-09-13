package sirenhead

// CLI identity. These are the single source of truth for every place
// the CLI reports who it is: `--version`, the `manifest` discovery
// document, `.meta.json`'s `generated_by`, and the GCWP handshake.
// Nothing else in this repository may hard-code a version number.
const (
	// CLIID is the stable CLI identifier. It MUST NOT change between
	// compatible releases (docs/protocol/03-discovery.md § Manifest).
	CLIID = "sirenhead"

	// CLIName is the human-readable CLI name. It MAY change.
	CLIName = "Siren Head Dating Sim CLI"

	// CLIVersion is the CLI semantic version.
	CLIVersion = "0.4.0"

	// EngineID is the stable engine identifier.
	EngineID = "renpy"

	// ProtocolName is the GCWP protocol short name.
	ProtocolName = "gcwp"

	// ProtocolVersion is the GCWP protocol version (MAJOR.MINOR).
	ProtocolVersion = "1.0"
)

// EngineVersions lists the engine version ranges this CLI was tested
// against. Ren'Py 7.x is identified by `script_version.txt` patterns,
// pyo/py files dated 2019, and `from __future__ import print_function`
// in the bundled scripts.
var EngineVersions = []string{"7.x"}

// GameName is the game this CLI was written for. It appears in
// `manifest.targets.games` and in the `identify` reply.
const GameName = "Siren Head Dating Sim"

// GameIDs are optional stable game identifiers (manifest.targets.games[].ids).
var GameIDs = []string{"sirenhead-dating-sim"}

// HelpText is printed by `--help` / `-h` / `help`.
//
// Per docs/shell-layer/09-std-flags.md § 9.8 the "Engine Options"
// section header MUST name the engine id.
func HelpText() string {
	return CLIName + ` (` + CLIID + `) ` + CLIVersion + `
A gallate CLI for the Ren'Py game "` + GameName + `".

Usage:
  ` + CLIID + ` -e[media] ./gallate.yaml [options]   Extract translation units
  ` + CLIID + ` -i[media] ./gallate.yaml [options]   Inject translated units
  ` + CLIID + ` init [target] [options]              Initialize a project
  ` + CLIID + ` manifest                             Print CLI manifest (GCWP)
  ` + CLIID + ` features                             Print CLI features (GCWP)
  ` + CLIID + ` validation                           Print validation rules
  ` + CLIID + ` --help                               Show this help
  ` + CLIID + ` --version                            Show version info

Media:
  -t                      text   (standard, always extracted)
  -i                      image  (background / portrait / cg / ui)
  -a                      audio  (voice / bgm / sfx)
  -v                      video  (cutscene / opening / ending)

Standard Options:
  -e, -i                  operations (extract / inject)
  --output PATH           Override output destination
  --ignore PATTERN        Add an ignore pattern (repeatable, merges with YAML)
  --dry-run               Plan without executing (writes nothing, runs no scripts)
  --force                 Override safety checks (init only)
  -v / -vv                Verbose / very verbose (stderr only)
  -q                      Quiet (stderr only)
  --engine.KEY=VALUE      Engine extension option

Engine Options (` + EngineID + `):
  --engine.in-place=true   Allow in-place inject (default: true)
  --engine.use-ast=true    Cross-check against Ren'Py's parser (needs Python 2)
  --engine.max-length=N    Soft cap on a target string (validation)
  --engine.ignore-empty=true  Treat  --engine.KEY=<empty>  as "use config"

Configuration (gallate.yaml), engine.` + EngineID + ` extensions:
  text.layout            flat | mirror | single   (default: flat)
  text.lifecycle.written_on   extract | never     (default: extract)
  text.metadata.*        original_file / source_context / location /
                         placeholders / engine_path emission flags
  text.meta.*            JSON key mapping for the five metadata fields
  engine.image.includes / engine.image.excludes   Sub-media filters
  engine.audio.includes / engine.audio.excludes   (same for video)

Conformance: GCWP ` + ProtocolVersion + ` Standard tier. See
https://github.com/grill-glitch/gallate for the spec.
`
}

// versionText is printed by `--version`.
func versionText() string {
	return CLIID + " " + CLIVersion + "\n" +
		"protocol " + ProtocolName + " " + ProtocolVersion + "\n" +
		"engine " + EngineID + " (" + joinComma(EngineVersions) + " compatible)\n"
}
