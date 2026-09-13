package sirenhead

// Shell layer: the human-facing invocation
// (docs/shell-layer/01-overview.md).
//
// Two hard rules shape everything in this file:
//
//   - stdout carries only the operation result (a single path, so
//     `result=$(tool -e ./gallate.yaml)` works), and
//   - stderr carries every progress line, warning and error.
//
// Exit codes are the Shell-layer table (0..10); the GCWP layer in
// gcwp.go uses the protocol table instead.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Main is the process entry point.
func Main(argv []string) int {
	return Run(argv, os.Stdin, os.Stdout, os.Stderr, stdinIsTerminal())
}

// stdinIsTerminal reports whether stdin is attached to a terminal.
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// Run dispatches one invocation. `interactive` mirrors "stdin is a
// terminal": only a non-interactive stdin can be a GCWP Wrapper.
//
// Main wires this to the real process streams; tests wire it to buffers.
func Run(argv []string, in io.Reader, out, errOut io.Writer, interactive bool) int {
	// A Wrapper drives the CLI over stdin/stdout; a human at a terminal
	// never does. That difference is the only dispatch signal needed.
	if code, handled := RunGCWP(in, out, errOut, interactive); handled {
		return code
	}

	args, err := Parse(argv)
	if err != nil {
		return reportError(err, errOut)
	}

	switch {
	case args.IsHelp:
		fmt.Fprint(out, HelpText())
		return ExitOK
	case args.IsVersion:
		fmt.Fprint(out, versionText())
		return ExitOK
	case args.IsManifest:
		return emitJSONLine(out, Manifest(), errOut)
	case args.IsFeatures:
		return emitJSONLine(out, Features(), errOut)
	case args.IsValidation:
		return emitJSONLine(out, ValidationRules(), errOut)
	case args.IsInit:
		return doInit(args, out, errOut)
	case args.IsExtract:
		return doExtractShell(args, out, errOut)
	case args.IsInject:
		return doInjectShell(args, out, errOut)
	case args.IsUnpack:
		return doUnpackShell(args, out, errOut)
	case args.IsRepack:
		return doRepackShell(args, out, errOut)
	}
	fmt.Fprintf(errOut, "%s: no operation specified\n", CLIID)
	return ExitUsage
}

// reportError prints an error and returns its exit code.
func reportError(err error, errOut io.Writer) int {
	if ee, ok := err.(*ExitError); ok {
		if ee.Code == ExitUsage {
			fmt.Fprintf(errOut, "%s: %s\n", CLIID, ee.Msg)
			fmt.Fprint(errOut, UsageText())
			return ee.Code
		}
		fmt.Fprintf(errOut, "%s: %s\n", CLIID, ee.Msg)
		return ee.Code
	}
	fmt.Fprintf(errOut, "%s: %v\n", CLIID, err)
	return ExitGeneral
}

// emitJSONLine writes one compact JSON object plus newline to stdout.
func emitJSONLine(out io.Writer, v any, errOut io.Writer) int {
	data, err := marshalCompact(v)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", CLIID, err)
		return ExitGeneral
	}
	fmt.Fprintf(out, "%s\n", data)
	return ExitOK
}

// doExtractShell runs extract from the shell.
func doExtractShell(args *Args, out, errOut io.Writer) int {
	rep := &Reporter{Stderr: errOut, Quiet: args.Quiet, Verbose: args.Verbose}
	stats, err := doExtract(args, rep)
	if err != nil {
		return reportError(err, errOut)
	}
	if !args.DryRun {
		rep.note("extracted %d unit(s) from %d string(s) in %.2fs",
			stats.TextExtracted, stats.FilesScanned, stats.Duration)
	}
	// stdout: the result path, as the last (only) line.
	fmt.Fprintf(out, "%s\n", filepath.Join(args.ProjectRoot, "text"))
	return ExitOK
}

// doInjectShell runs inject from the shell.
func doInjectShell(args *Args, out, errOut io.Writer) int {
	rep := &Reporter{Stderr: errOut, Quiet: args.Quiet, Verbose: args.Verbose}
	stats, err := doInject(args, rep)
	if err != nil {
		return reportError(err, errOut)
	}
	if !args.DryRun {
		rep.note("injected %d unit(s) (%d drift) in %.2fs",
			stats.TextInjected, stats.UnitsDrifted, stats.Duration)
	}
	fmt.Fprintf(out, "%s\n", args.ProjectRoot)
	return ExitOK
}

// doInit implements the `init` subcommand
// (docs/shell-layer/07-init.md).
//
// The generated `gallate.yaml` holds `input` / `output` / `media` /
// `ignore`, plus the `text:` and `engine:` blocks this CLI understands.
func doInit(args *Args, out, errOut io.Writer) int {
	target := args.InitTarget
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return reportError(exitErr(ExitGeneral, err.Error()), errOut)
		}
		target = cwd
	}
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	if !dirExists(target) {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return reportError(exitErr(ExitOutputFailure, err.Error()), errOut)
		}
	}
	yamlPath := filepath.Join(target, "gallate.yaml")
	if fileExists(yamlPath) && !args.Force {
		return reportError(exitErr(ExitUsage,
			fmt.Sprintf("Error: %s already exists. Use --force to overwrite.", yamlPath)), errOut)
	}

	// Docs/shell-layer/07 § 7.5: paths given to init are resolved against
	// the final project root, not the shell's current directory.
	toProjectPath := func(p string) string {
		abs := resolvePath(p, target)
		if rel, err := filepath.Rel(target, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return "./" + filepath.ToSlash(rel)
		}
		return filepath.ToSlash(abs)
	}

	var sb strings.Builder
	sb.WriteString("# gallate.yaml — generated by " + CLIID + " init\n")
	if args.InitInput != "" {
		sb.WriteString("input: " + toProjectPath(args.InitInput) + "\n")
	} else {
		sb.WriteString("input: ./game\n")
	}
	if args.InitOutput != "" {
		sb.WriteString("output: " + toProjectPath(args.InitOutput) + "\n")
	}
	sb.WriteString("\nmedia:\n")
	media := dedupStrings(args.InitMedia)
	if len(media) == 0 {
		// Docs/shell-layer/07 § 7.6: the default is the standard
		// baseline (text + image). Engine-extension media are never
		// written without an explicit flag.
		media = []string{"text", "image"}
	}
	for _, m := range media {
		sb.WriteString("  - " + m + "\n")
	}
	if len(args.Ignore) > 0 {
		sb.WriteString("\nignore:\n")
		for _, ig := range args.Ignore {
			sb.WriteString("  - " + yamlQuote(ig) + "\n")
		}
	}
	sb.WriteString("\ntext:\n  format: json\n  layout: flat\n")
	sb.WriteString("\nengine:\n  text_encoding: utf-8\n")

	if err := atomicWriteBytes(yamlPath, []byte(sb.String())); err != nil {
		return reportError(exitErr(ExitOutputFailure, err.Error()), errOut)
	}
	fmt.Fprintf(out, "Wrote %s\n", yamlPath)
	return ExitOK
}

// yamlQuote renders a YAML double-quoted scalar.
func yamlQuote(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}

// dedupStrings removes duplicates while keeping first-seen order.
func dedupStrings(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out
}

// doUnpackShell implements `tool unpack <archive.rpa> <dir>`.
//
// The Shell-layer contract is the same as for extract/inject:
// stdout carries only the result path; stderr carries progress,
// warnings, and the final summary; the exit code follows the
// Shell-layer table (ExitExtractionFailure on archive error,
// ExitOutputFailure on write failure, ExitOK on success).
//
// The archive is independent of `gallate.yaml` (it lives at the
// game root, not inside the project tree), so we deliberately do
// not resolve paths against Project Root.
func doUnpackShell(args *Args, out, errOut io.Writer) int {
	rep := &Reporter{Stderr: errOut, Quiet: args.Quiet, Verbose: args.Verbose}
	rep.phase("scanning")
	if !fileExists(args.SubSrc) {
		return reportError(exitErr(ExitInputNotFound,
			fmt.Sprintf("archive not found: %s", args.SubSrc)), errOut)
	}
	arc, err := openArchive(args.SubSrc)
	if err != nil {
		return reportError(exitErr(ExitExtractionFailure,
			fmt.Sprintf("cannot open RPA archive: %v", err)), errOut)
	}
	rep.phase("extracting")
	rules, err := compileIgnore(args.Ignore)
	if err != nil {
		return reportError(exitErr(ExitUsage, err.Error()), errOut)
	}
	stats, err := arc.extractAll(args.SubDst, rep, rules)
	if err != nil {
		return reportError(exitErr(ExitOutputFailure, err.Error()), errOut)
	}
	if !args.DryRun {
		writeManifest(args.SubSrc, args.SubDst, arc)
		rep.note("unpacked %d file(s) (%d bytes) from %s",
			stats.FilesProcessed, stats.BytesWritten, args.SubSrc)
	} else {
		rep.note("dry run: %d file(s) would be created in %s; nothing written",
			len(arc.entries), args.SubDst)
	}
	fmt.Fprintf(out, "%s\n", args.SubDst)
	return ExitOK
}

// doRepackShell implements `tool repack <dir> <archive.rpa>`.
//
// The packer reads every file under the source directory (in
// sorted order, so the metadata is stable), assigns offsets in
// that order, and writes the metadata at the end. The XOR key
// defaults to "42424242" — the value Ren'Py 7+ writes by default
// and what every Ren'Py-packaged game we have seen uses. Override
// with `--engine.rpa-key=HEXHEXHEX`.
func doRepackShell(args *Args, out, errOut io.Writer) int {
	rep := &Reporter{Stderr: errOut, Quiet: args.Quiet, Verbose: args.Verbose}
	rep.phase("scanning")
	if !dirExists(args.SubSrc) {
		return reportError(exitErr(ExitInputNotFound,
			fmt.Sprintf("source directory not found: %s", args.SubSrc)), errOut)
	}
	if !args.DryRun && fileExists(args.SubDst) && !args.Force {
		return reportError(exitErr(ExitUsage,
			fmt.Sprintf("output already exists: %s (use --force to overwrite)", args.SubDst)), errOut)
	}
	key := defaultRPAKey
	if v, ok := args.EngineOpts["rpa-key"]; ok && v != "" {
		parsed, err := readHexKey(v)
		if err != nil {
			return reportError(exitErr(ExitUsage, err.Error()), errOut)
		}
		key = parsed
	}
	rep.phase("writing")
	stats, err := PackDir(args.SubSrc, args.SubDst, key, rep, args.Ignore)
	if err != nil {
		// PackDir returns plain errors; map them to the right exit
		// code. Output-side errors (Create, Write) become
		// ExitOutputFailure; everything else (e.g. an invalid key
		// mid-run, which compileIgnore already catches) is general.
		return reportError(exitErr(ExitOutputFailure, err.Error()), errOut)
	}
	if args.DryRun {
		// PackDir still wrote stats, but we want no side effects.
		_ = os.Remove(args.SubDst)
		rep.note("dry run: %d file(s) would be packed into %s; nothing written",
			stats.FilesProcessed, args.SubDst)
	} else {
		rep.note("packed %d file(s) (%d bytes) into %s (key=%s)",
			stats.FilesProcessed, stats.BytesWritten, args.SubDst, key)
	}
	fmt.Fprintf(out, "%s\n", args.SubDst)
	return ExitOK
}
