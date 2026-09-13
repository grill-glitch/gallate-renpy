package sirenhead

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mediaLetters maps the Shell-layer flag letter to the media identifier
// (docs/shell-layer/02-cli-grammar.md § Operation + Media shorthand).
var mediaLetters = map[byte]string{
	't': "text",
	'i': "image",
	'a': "audio",
	'v': "video",
}

// supportedMedia is the media this engine's CLI exposes.
//
// `text` and `image` are the standard baseline; `audio` and `video`
// are engine-extension media this engine has declared in `features`
// (docs/shell-layer/04-media.md § 4.2). Requesting a declared-but-
// unsupported media is exit code 6 (Unsupported Media); a letter that
// is not a media flag at all is exit code 2 (Invalid CLI Usage).
var supportedMedia = map[string]bool{
	"text":  true,
	"image": true,
	"audio": true,
	"video": true,
}

// Args is a parsed Shell-layer invocation.
//
// Exactly one of the mode booleans is set. Per
// docs/shell-layer/02-cli-grammar.md the grammar is:
//
//	tool [operation][media] project [options]
type Args struct {
	// Modes.
	IsInit       bool
	IsHelp       bool
	IsVersion    bool
	IsManifest   bool
	IsFeatures   bool
	IsValidation bool
	IsExtract    bool
	IsInject     bool
	IsUnpack     bool
	IsRepack     bool

	// Subcommand positionals (unpack/repack).
	// Unpack: SrcPath is the .rpa file; OutDir is the destination directory.
	// Repack: SrcDir is the input directory; OutPath is the .rpa output file.
	SubSrc string
	SubDst string

	// Operation scope.
	ProjectRoot string // Project Root: directory holding gallate.yaml
	GallateYAML string // absolute path to gallate.yaml

	// Media selected by flags. Empty means "use gallate.yaml's media list".
	Media map[string]bool

	// Standard flags.
	Output  string
	Ignore  []string
	DryRun  bool
	Force   bool
	Verbose int
	Quiet   bool

	// Engine extension options, from `--engine.KEY=VALUE`.
	EngineOpts map[string]string

	// `init` options.
	InitTarget string
	InitInput  string
	InitOutput string
	InitMedia  []string
}

// Parse parses argv (excluding the program name).
//
// Errors are *ExitError values carrying the shell-layer exit code: 2
// for a malformed invocation (docs/shell-layer/10-stdout-stderr.md
// § 10.3).
func Parse(argv []string) (*Args, error) {
	args := &Args{
		Media:      map[string]bool{},
		EngineOpts: map[string]string{},
	}

	// Discovery subcommands and --help / --version are recognized
	// BEFORE an operation flag, so `tool manifest` works without a
	// project file.
	if len(argv) > 0 {
		switch argv[0] {
		case "manifest", "features", "validation", "help", "--help", "-h", "version", "--version":
			sub := argv[0]
			switch sub {
			case "help", "--help", "-h":
				args.IsHelp = true
			case "version", "--version":
				args.IsVersion = true
			case "manifest":
				args.IsManifest = true
			case "features":
				args.IsFeatures = true
			case "validation":
				args.IsValidation = true
			}
			rest := argv[1:]
			if len(rest) > 0 {
				return nil, usageErr(fmt.Sprintf("%s takes no arguments", sub))
			}
			return args, nil
		}
	}

	if len(argv) > 0 && argv[0] == "init" {
		args.IsInit = true
		return args, parseInit(argv[1:], args)
	}

	if len(argv) > 0 && (argv[0] == "unpack" || argv[0] == "repack") {
		if argv[0] == "unpack" {
			args.IsUnpack = true
		} else {
			args.IsRepack = true
		}
		return args, parseArchiveSub(argv[1:], args)
	}

	// An operation flag is required for every other invocation.
	if len(argv) == 0 || !strings.HasPrefix(argv[0], "-") {
		return nil, usageErr("missing operation flag (-e or -i)")
	}

	opToken := argv[0]
	argv = argv[1:]

	letters := opToken[1:]
	if len(letters) == 0 || (letters[0] != 'e' && letters[0] != 'i') {
		return nil, usageErr(fmt.Sprintf("invalid operation token: %q", opToken))
	}
	switch letters[0] {
	case 'e':
		args.IsExtract = true
	case 'i':
		args.IsInject = true
	}

	for i := 1; i < len(letters); i++ {
		name, ok := mediaLetters[letters[i]]
		if !ok {
			return nil, usageErr(fmt.Sprintf("unknown media flag: -%c", letters[i]))
		}
		if !supportedMedia[name] {
			return nil, exitErr(ExitUnsupportedMedia,
				fmt.Sprintf("media %q is not supported by this engine", name))
		}
		args.Media[name] = true
	}

	// Positional: the path to gallate.yaml.
	if len(argv) == 0 {
		return nil, usageErr("missing project target (path to gallate.yaml)")
	}
	project := argv[0]
	argv = argv[1:]
	if !filepath.IsAbs(project) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, exitErr(ExitGeneral, fmt.Sprintf("cannot determine working directory: %v", err))
		}
		project = filepath.Join(cwd, project)
	}
	project = filepath.Clean(project)
	if !fileExists(project) {
		return nil, usageErr(fmt.Sprintf("gallate.yaml not found: %s", project))
	}
	args.GallateYAML = project
	args.ProjectRoot = filepath.Dir(project)

	// Remaining argv: standard long flags + engine extension options.
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--help" || a == "-h":
			args.IsHelp = true
		case a == "--version":
			args.IsVersion = true
		case a == "--output":
			if i+1 >= len(argv) {
				return nil, usageErr("--output requires a value")
			}
			args.Output = argv[i+1]
			i++
		case a == "--ignore":
			if i+1 >= len(argv) {
				return nil, usageErr("--ignore requires a value")
			}
			args.Ignore = append(args.Ignore, argv[i+1])
			i++
		case a == "--dry-run":
			args.DryRun = true
		case a == "--force":
			args.Force = true
		case a == "-v" || a == "--verbose":
			args.Verbose++
			args.Quiet = false
		case a == "-vv":
			args.Verbose += 2
			args.Quiet = false
		case a == "-q" || a == "--quiet":
			// Per docs/shell-layer/09-std-flags.md § 9.6 the last flag
			// wins: `-q -v` is verbose, `-v -q` is quiet.
			args.Quiet = true
			args.Verbose = 0
		case strings.HasPrefix(a, "--engine."):
			body := strings.TrimPrefix(a, "--engine.")
			eq := strings.Index(body, "=")
			if eq < 0 {
				return nil, usageErr(fmt.Sprintf("--engine.KEY=VALUE required: %s", a))
			}
			args.EngineOpts[body[:eq]] = body[eq+1:]
		default:
			return nil, usageErr(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return args, nil
}

// parseInit parses `tool init [target] [options]`.
func parseInit(argv []string, args *Args) error {
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		args.InitTarget = argv[0]
		argv = argv[1:]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return exitErr(ExitGeneral, fmt.Sprintf("cannot determine working directory: %v", err))
		}
		args.InitTarget = cwd
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--help" || a == "-h":
			args.IsHelp = true
		case a == "--input":
			if i+1 >= len(argv) {
				return usageErr("--input requires a value")
			}
			args.InitInput = argv[i+1]
			i++
		case a == "--output":
			if i+1 >= len(argv) {
				return usageErr("--output requires a value")
			}
			args.InitOutput = argv[i+1]
			i++
		case a == "--ignore":
			if i+1 >= len(argv) {
				return usageErr("--ignore requires a value")
			}
			args.Ignore = append(args.Ignore, argv[i+1])
			i++
		case a == "--media":
			if i+1 >= len(argv) {
				return usageErr("--media requires a value")
			}
			for _, m := range strings.Split(argv[i+1], ",") {
				if m = strings.TrimSpace(m); m != "" {
					args.InitMedia = append(args.InitMedia, m)
				}
			}
			i++
		case a == "--force":
			args.Force = true
		default:
			return usageErr(fmt.Sprintf("unknown init option: %s", a))
		}
	}
	return nil
}

// parseArchiveSub parses `tool unpack <archive> <dir> [options]` and
// `tool repack <dir> <archive> [options]`.
//
// Both subcommands take two positionals (source and destination, in
// that order). They accept the standard `--ignore` and `--dry-run`
// flags and the engine extension `--engine.rpa-key=KEY` (the 8-char
// hex XOR key written into the new archive's header).
//
// Paths are resolved relative to the shell's current directory (the
// archive lives outside `gallate.yaml`'s project layout), so this
// subcommand deliberately does NOT require a project file.
func parseArchiveSub(argv []string, args *Args) error {
	if len(argv) < 2 {
		return usageErr("expected two positionals: <source> <destination>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return exitErr(ExitGeneral, fmt.Sprintf("cannot determine working directory: %v", err))
	}
	resolveCWD := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Clean(filepath.Join(cwd, p))
	}
	args.SubSrc = resolveCWD(argv[0])
	args.SubDst = resolveCWD(argv[1])
	argv = argv[2:]
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--help" || a == "-h":
			args.IsHelp = true
		case a == "--ignore":
			if i+1 >= len(argv) {
				return usageErr("--ignore requires a value")
			}
			args.Ignore = append(args.Ignore, argv[i+1])
			i++
		case a == "--dry-run":
			args.DryRun = true
		case a == "--force":
			args.Force = true
		case a == "-v" || a == "--verbose":
			args.Verbose++
			args.Quiet = false
		case a == "-q" || a == "--quiet":
			args.Quiet = true
			args.Verbose = 0
		case strings.HasPrefix(a, "--engine."):
			body := strings.TrimPrefix(a, "--engine.")
			eq := strings.Index(body, "=")
			if eq < 0 {
				return usageErr(fmt.Sprintf("--engine.KEY=VALUE required: %s", a))
			}
			args.EngineOpts[body[:eq]] = body[eq+1:]
		default:
			return usageErr(fmt.Sprintf("unknown option: %s", a))
		}
	}
	return nil
}

// UsageText is printed with every usage error.
func UsageText() string {
	return "Usage: " + CLIID + " [op][media] ./gallate.yaml [options]\n" +
		"       " + CLIID + " init [target] [options]\n" +
		"       " + CLIID + " unpack <archive.rpa> <dir> [options]\n" +
		"       " + CLIID + " repack <dir> <archive.rpa> [options]\n" +
		"       " + CLIID + " {manifest|features|validation|--help|--version}\n"
}

// effectiveMedia resolves which media an operation processes.
//
// Three semantics, per docs/shell-layer/04-media.md § 4.4 – § 4.5:
//
//  1. no media flag  ⇒ the gallate.yaml `media:` list;
//  2. no media flag and no YAML list ⇒ the engine default (text);
//  3. media flags given ⇒ they COMPLETELY OVERRIDE the YAML list.
//
// `text` is the standard baseline and is always processed, even when a
// flag list names only extension media (`-ei` = text + image). Only the
// extension media (image / audio / video) are gated by flags and YAML.
func (a *Args) effectiveMedia(cfg *Config) map[string]bool {
	out := map[string]bool{"text": true}
	var requested []string
	if len(a.Media) > 0 {
		for m := range a.Media {
			requested = append(requested, m)
		}
		sort.Strings(requested)
	} else {
		requested = cfg.MediaList()
	}
	for _, m := range requested {
		if m == "text" {
			continue
		}
		out[m] = true
	}
	return out
}

// inPlace decides inject's destination.
//
// `--engine.in-place=false` plus `--output DIR` writes translated
// resources into DIR instead of overwriting the game's files; the
// default is in-place (docs/shell-layer/10-stdout-stderr.md § 10.5).
func (a *Args) inPlace(cfg *Config) (bool, string, error) {
	inPlace := true
	if v, ok := a.EngineOpts["in-place"]; ok {
		b, ok := asBool(v)
		if !ok {
			return false, "", exitErr(ExitProjectConfig,
				fmt.Sprintf("--engine.in-place must be true or false (got %q)", v))
		}
		inPlace = b
	}
	if a.Output == "" && !inPlace {
		return false, "", exitErr(ExitProjectConfig,
			"--engine.in-place=false requires --output DIR (nowhere else to write)")
	}
	if inPlace || a.Output == "" {
		return true, "", nil
	}
	return false, resolvePath(a.Output, a.ProjectRoot), nil
}
