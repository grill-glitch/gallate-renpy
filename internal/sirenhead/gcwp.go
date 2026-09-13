package sirenhead

// GCWP — the Protocol layer (docs/protocol/).
//
// A Wrapper launches this binary, writes ONE request as a JSON Line on
// stdin, and reads the response plus the event stream as JSON Lines on
// stdout. stderr stays human-readable diagnostics, and the process exit
// code carries the final result.
//
// Message flow (docs/protocol/02 § Operation flow):
//
//	stdin   {"type":"request","id":…,"operation":"extract",…}
//	stdout  {"type":"response",…}
//	stdout  {"type":"event","event":"started",…}
//	stdout  {"type":"event","event":"phase",…}
//	stdout  {"type":"event","event":"progress",…}
//	stdout  {"type":"event","event":"file",…}
//	stdout  {"type":"event","event":"statistics",…}
//	stdout  {"type":"event","event":"completed",…}
//	exit    0
//
// Exit codes follow docs/protocol/02 § Exit codes (0..8), NOT the shell
// table — `7` means "protocol error" here and "extraction failure"
// there. The two tables are deliberately kept separate.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// gcwpWriter serializes protocol output. One JSON object per line, no
// HTML escaping, flushed per message so a Wrapper can stream.
type gcwpWriter struct {
	w io.Writer
}

func (g *gcwpWriter) emit(obj any) error {
	data, err := marshalCompact(obj)
	if err != nil {
		return err
	}
	if _, err := g.w.Write(data); err != nil {
		return err
	}
	if _, err := g.w.Write([]byte("\n")); err != nil {
		return err
	}
	if f, ok := g.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// event emits one `event` message.
func (g *gcwpWriter) event(name string, fields *orderedMap) error {
	m := &orderedMap{}
	m.Set("type", "event")
	m.Set("event", name)
	if fields != nil {
		for i, k := range fields.keys {
			m.Set(k, fields.vals[i])
		}
	}
	return g.emit(m)
}

// RunGCWP reads one message from stdin and handles it.
//
// Returns (exitCode, handled). `handled` is false when the process was
// launched interactively (a human at a terminal always gets the Shell
// layer) or when stdin carried no protocol message, in which case the
// caller falls through to the Shell layer.
func RunGCWP(in io.Reader, out io.Writer, errOut io.Writer, interactive bool) (int, bool) {
	if interactive {
		return 0, false
	}
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return 0, false
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return 0, false
	}
	w := &gcwpWriter{w: out}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		fmt.Fprintln(errOut, "malformed JSON on stdin")
		return GCWPProtocolError, true
	}
	switch msg["type"] {
	case "protocol":
		// Handshake. Shape satisfies schema/protocol.schema.yaml
		// (required: type, protocol{name,version}, version) and keeps the
		// `supported` flag earlier Wrappers look for.
		reply := &orderedMap{}
		reply.Set("type", "protocol")
		reply.Set("name", ProtocolName)
		proto := &orderedMap{}
		proto.Set("name", ProtocolName)
		proto.Set("version", ProtocolVersion)
		reply.Set("protocol", proto)
		reply.Set("version", ProtocolVersion)
		reply.Set("supported", true)
		if err := w.emit(reply); err != nil {
			return GCWPInternalError, true
		}
		return GCWPOK, true
	case "request":
		return runRequest(w, msg, errOut), true
	case "command":
		// Only `cancel` exists, and Full-tier cancellation is not
		// implemented; say so rather than hanging. See README conformance.
		fmt.Fprintln(errOut, "cancellation is not supported by this CLI")
		return GCWPUnsupportedOperation, true
	default:
		fmt.Fprintf(errOut, "unknown stdin message type: %v\n", msg["type"])
		return GCWPProtocolError, true
	}
}

// runRequest executes one operation request and returns the GCWP exit code.
func runRequest(w *gcwpWriter, req map[string]any, errOut io.Writer) int {
	opID, _ := req["id"].(string)
	op, _ := req["operation"].(string)

	// Acknowledge first: the response precedes every event.
	resp := &orderedMap{}
	resp.Set("type", "response")
	resp.Set("id", opID)
	resp.Set("operation", op)
	resp.Set("accepted", true)
	proto := &orderedMap{}
	proto.Set("version", ProtocolVersion)
	resp.Set("protocol", proto)
	if err := w.emit(resp); err != nil {
		return GCWPInternalError
	}

	switch op {
	case "identify":
		return gcwpIdentify(w, req, opID)
	case "extract":
		return gcwpOperation(w, req, errOut, opID, "extract")
	case "inject":
		return gcwpOperation(w, req, errOut, opID, "inject")
	case "validate":
		// Rules were already declared by `cli validation`; the Wrapper
		// runs them. Findings this CLI does produce are emitted during
		// inject as `validation` events.
		_ = w.event("started", fieldsOf("id", opID, "operation", "validate"))
		_ = w.event("completed", fieldsOf("id", opID, "operation", "validate"))
		return GCWPOK
	case "unpack":
		return gcwpArchive(w, req, errOut, opID, "unpack")
	case "repack":
		return gcwpArchive(w, req, errOut, opID, "repack")
	default:
		fmt.Fprintf(errOut, "unsupported operation: %q\n", op)
		return GCWPUnsupportedOperation
	}
}

// fieldsOf builds an orderedMap from alternating key/value pairs.
func fieldsOf(kv ...any) *orderedMap {
	m := &orderedMap{}
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		m.Set(key, kv[i+1])
	}
	return m
}

// gcwpOperation runs extract/inject with an event stream.
//
// The GCWP request is translated into Shell-layer argv and run through
// the same parser the shell layer uses — one code path, two front doors.
func gcwpOperation(w *gcwpWriter, req map[string]any, errOut io.Writer, opID, op string) int {
	started := fieldsOf("id", opID, "operation", op)
	if err := w.event("started", started); err != nil {
		return GCWPInternalError
	}

	argv := gcwpRequestToShell(req, op)
	args, err := Parse(argv)
	if err != nil {
		code := GCWPInvalidArguments
		if ee, ok := err.(*ExitError); ok && ee.Code == ExitProjectConfig {
			code = GCWPInvalidConfig
		}
		fmt.Fprintln(errOut, err.Error())
		_ = w.event("error", fieldsOf("code", "INVALID_ARGUMENTS", "message", err.Error()))
		return code
	}

	rep := &Reporter{
		Stderr: errOut,
		Quiet:  args.Quiet,
		PhaseHook: func(name string) {
			_ = w.event("phase", fieldsOf("name", name))
		},
		FileHook: func(action, path string) {
			_ = w.event("file", fieldsOf("action", action, "path", path))
		},
		ProgressHook: func(current, total int, indeterminate bool) {
			var totalVal any
			if !indeterminate {
				totalVal = total
			}
			_ = w.event("progress", fieldsOf("current", current, "total", totalVal))
		},
		WarningHook: func(code, message, path string) {
			_ = w.event("warning", fieldsOf("code", code, "message", message, "path", path))
		},
		ValidationHook: func(finding map[string]any) {
			m := &orderedMap{}
			for _, k := range []string{"rule", "severity", "source", "target", "message", "translation_unit"} {
				if v, ok := finding[k]; ok {
					m.Set(k, v)
				}
			}
			_ = w.event("validation", m)
		},
	}

	var stats *Stats
	if op == "extract" {
		stats, err = doExtract(args, rep)
	} else {
		stats, err = doInject(args, rep)
	}
	if err != nil {
		code := GCWPOperationFailed
		errCode := "EXTRACT_FAILED"
		errName := "extract"
		if op == "inject" {
			errCode, errName = "INJECT_FAILED", "inject"
		}
		if ee, ok := err.(*ExitError); ok {
			switch ee.Code {
			case ExitProjectConfig:
				code, errCode = GCWPInvalidConfig, "INVALID_CONFIG"
			case ExitInputNotFound:
				code, errCode = GCWPOperationFailed, "INPUT_NOT_FOUND"
			case ExitUsage:
				code, errCode = GCWPInvalidArguments, "INVALID_ARGUMENTS"
			case ExitUnsupportedMedia:
				code, errCode = GCWPInvalidConfig, "UNSUPPORTED_MEDIA"
			case ExitExtractionFailure:
				code, errCode = GCWPOperationFailed, "EXTRACT_FAILED"
			case ExitInjectionFailure:
				code, errCode = GCWPValidationFailed, "INJECT_FAILED"
			case ExitOutputFailure:
				code, errCode = GCWPOperationFailed, "OUTPUT_FAILED"
			case ExitScriptFailure:
				code, errCode = GCWPOperationFailed, "SCRIPT_FAILED"
			}
		}
		fmt.Fprintf(errOut, "%s failed: %v\n", errName, err)
		_ = w.event("error", fieldsOf("code", errCode, "message", err.Error()))
		return code
	}

	if err := w.event("statistics", fieldsOf("statistics", stats.statisticsJSON())); err != nil {
		return GCWPInternalError
	}
	if err := w.event("completed", fieldsOf("id", opID, "operation", op, "statistics", stats.statisticsJSON())); err != nil {
		return GCWPInternalError
	}
	return GCWPOK
}

// gcwpIdentify answers the engine-recognition query.
//
// The reply shape is schema/identify.schema.yaml: a `target` object and
// a `matched` array of rule-level evidence, best match first. An empty
// `matched` array means "not my engine" — and it is exit code 0, not an
// error, because the Wrapper simply moves on to the next CLI.
func gcwpIdentify(w *gcwpWriter, req map[string]any, opID string) int {
	targetPath := firstInputPath(req)
	if targetPath == "" {
		_ = w.emit(fieldsOf("type", "identify", "id", opID,
			"target", fieldsOf("path", ""), "matched", []any{}))
		return GCWPInvalidArguments
	}
	path := resolvePath(targetPath, ".")
	st, err := os.Stat(path)
	if err != nil {
		_ = w.emit(fieldsOf("type", "identify", "id", opID,
			"target", fieldsOf("path", targetPath), "matched", []any{}))
		return GCWPOperationFailed
	}

	var matched []any = []any{}
	if st.Mode().IsRegular() {
		head := make([]byte, 16)
		if f, ferr := os.Open(path); ferr == nil {
			n, _ := f.Read(head)
			head = head[:n]
			f.Close()
		}
		switch {
		case hasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
			matched = append(matched, identifyMatch("high", "magic_bytes", "ef bb bf", true))
		case hasPrefix(head, []byte("#")):
			matched = append(matched, identifyMatch("high", "magic_bytes", "23", true))
		case hasPrefix(head, []byte("RENPY RPC2")):
			matched = append(matched, identifyMatch("high", "magic_bytes",
				"52 45 4e 50 59 20 52 50 43 32", false))
		case hasPrefix(head, []byte("RPA-3.0 ")):
			// Ren'Py RPA-3.0 archives: the game is inside one or
			// more of these, so identifying a single archive lets
			// the Wrapper drive unpack → extract → repack without
			// knowing Ren'Py's archive scheme.
			matched = append(matched, identifyMatch("high", "magic_bytes",
				"52 50 41 2d 33 2e 30 20", false))
		case hasPrefix(head, []byte("RPA-2.0 ")):
			matched = append(matched, identifyMatch("medium", "magic_bytes",
				"52 50 41 2d 32 2e 30 20", false))
		case strings.HasSuffix(strings.ToLower(path), ".rpy"):
			matched = append(matched, identifyMatch("medium", "extension", ".rpy", false))
		}
	} else if st.IsDir() && dirExists(filepath.Join(path, "game")) {
		matched = append(matched, identifyMatch("medium", "directory", "game/", false))
	}

	target := fieldsOf("path", targetPath, "kind", kindOf(st))
	if st.Mode().IsRegular() {
		target.Set("size", st.Size())
	}
	if err := w.emit(fieldsOf("type", "identify", "id", opID, "target", target, "matched", matched)); err != nil {
		return GCWPInternalError
	}
	_ = w.event("completed", fieldsOf("id", opID, "operation", "identify"))
	return GCWPOK
}

// identifyMatch builds one `matched[]` entry.
func identifyMatch(confidence, ruleKind, matchedVal string, withGame bool) *orderedMap {
	m := &orderedMap{}
	m.Set("engine", EngineID)
	m.Set("confidence", confidence)
	m.Set("rule", fieldsOf("kind", ruleKind, "matched", matchedVal))
	if withGame {
		m.Set("game", fieldsOf("name", GameName, "engine_versions", EngineVersions))
	}
	return m
}

// kindOf renders an os.FileInfo as file / directory.
func kindOf(st os.FileInfo) string {
	if st.IsDir() {
		return "directory"
	}
	return "file"
}

// hasPrefix is a tiny bytes.HasPrefix wrapper kept local so the identify
// code reads as a list of signature checks.
func hasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}

// firstInputPath pulls the first input path out of a request, accepting
// both the plain-string and the structured `{path,kind}` forms.
func firstInputPath(req map[string]any) string {
	inputs, _ := req["input"].([]any)
	if len(inputs) == 0 {
		return ""
	}
	switch v := inputs[0].(type) {
	case string:
		return v
	case map[string]any:
		p, _ := v["path"].(string)
		return p
	}
	return ""
}

// gcwpRequestToShell translates a GCWP request into Shell-layer argv.
func gcwpRequestToShell(req map[string]any, op string) []string {
	flag := "-e"
	if op == "inject" {
		flag = "-i"
	}
	media := map[string]bool{}
	switch v := req["media"].(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				media[s] = true
			}
		}
	case string:
		media[v] = true
	}
	if len(media) == 0 {
		media["text"] = true
	}
	// `text` is the baseline and always processed; only the extension
	// media need a flag. Flag order within the token is irrelevant.
	if media["text"] {
		flag += "t"
	}
	if media["image"] {
		flag += "i"
	}
	if media["audio"] {
		flag += "a"
	}
	if media["video"] {
		flag += "v"
	}
	argv := []string{flag}
	if p := firstInputPath(req); p != "" {
		argv = append(argv, p)
	}
	if outs, ok := req["output"].([]any); ok && len(outs) > 0 {
		var out string
		switch v := outs[0].(type) {
		case string:
			out = v
		case map[string]any:
			out, _ = v["path"].(string)
		}
		if out != "" {
			argv = append(argv, "--output", out)
		}
	}
	if ignores, ok := req["ignore"].([]any); ok {
		for _, ig := range ignores {
			if s, ok := ig.(string); ok {
				argv = append(argv, "--ignore", s)
			}
		}
	}
	if opts, ok := req["options"].(map[string]any); ok {
		keys := make([]string, 0, len(opts))
		for k := range opts {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			argv = append(argv, fmt.Sprintf("--engine.%s=%v", k, opts[k]))
		}
	}
	return argv
}

// firstOutputPath returns the first `output[]` path from a request,
// accepting both the plain-string and structured `{path,kind}` forms.
// Falls back to "" when the request has no output.
func firstOutputPath(req map[string]any) string {
	outs, _ := req["output"].([]any)
	if len(outs) == 0 {
		return ""
	}
	switch v := outs[0].(type) {
	case string:
		return v
	case map[string]any:
		p, _ := v["path"].(string)
		return p
	}
	return ""
}

// requestIgnore extracts the `ignore` array from a request as a slice
// of strings; nil is returned when no ignore list is present.
func requestIgnore(req map[string]any) []string {
	arr, ok := req["ignore"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// requestOption returns one engine-namespaced option from a request.
func requestOption(req map[string]any, key string) (string, bool) {
	opts, ok := req["options"].(map[string]any)
	if !ok {
		return "", false
	}
	v, ok := opts[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// gcwpArchive runs `unpack` / `repack` over the GCWP request.
//
// The two operations share their entire shape (two path arguments,
// one option, one ignore list); they differ only in the direction
// of the copy and the operation id echoed in events. We keep them
// in one function so the GCWP error mapping stays in one place.
func gcwpArchive(w *gcwpWriter, req map[string]any, errOut io.Writer, opID, op string) int {
	started := fieldsOf("id", opID, "operation", op)
	if err := w.event("started", started); err != nil {
		return GCWPInternalError
	}

	src := firstInputPath(req)
	dst := firstOutputPath(req)
	if src == "" || dst == "" {
		fmt.Fprintf(errOut, "%s requires both `input` and `output`\n", op)
		_ = w.event("error", fieldsOf("code", "INVALID_ARGUMENTS",
			"message", "both input and output are required"))
		return GCWPInvalidArguments
	}
	_ = w.event("phase", fieldsOf("name", "scanning"))

	rules, err := compileIgnore(requestIgnore(req))
	if err != nil {
		_ = w.event("error", fieldsOf("code", "INVALID_ARGUMENTS", "message", err.Error()))
		return GCWPInvalidArguments
	}
	rep := &Reporter{
		Stderr: errOut,
		PhaseHook: func(name string) {
			_ = w.event("phase", fieldsOf("name", name))
		},
		FileHook: func(action, path string) {
			_ = w.event("file", fieldsOf("action", action, "path", path))
		},
		ProgressHook: func(current, total int, indeterminate bool) {
			var totalVal any
			if !indeterminate {
				totalVal = total
			}
			_ = w.event("progress", fieldsOf("current", current, "total", totalVal))
		},
	}

	var stats *Stats
	switch op {
	case "unpack":
		arc, aerr := openArchive(src)
		if aerr != nil {
			fmt.Fprintf(errOut, "cannot open %s: %v\n", src, aerr)
			_ = w.event("error", fieldsOf("code", "INPUT_NOT_FOUND",
				"message", aerr.Error()))
			return GCWPOperationFailed
		}
		_ = w.event("phase", fieldsOf("name", "extracting"))
		st, eerr := arc.extractAll(dst, rep, rules)
		if eerr != nil {
			fmt.Fprintf(errOut, "unpack failed: %v\n", eerr)
			_ = w.event("error", fieldsOf("code", "OUTPUT_FAILED",
				"message", eerr.Error()))
			return GCWPOperationFailed
		}
		writeManifest(src, dst, arc)
		stats = &st
	case "repack":
		key := defaultRPAKey
		if v, ok := requestOption(req, "rpa-key"); ok && v != "" {
			parsed, kerr := readHexKey(v)
			if kerr != nil {
				fmt.Fprintf(errOut, "%v\n", kerr)
				_ = w.event("error", fieldsOf("code", "INVALID_ARGUMENTS",
					"message", kerr.Error()))
				return GCWPInvalidArguments
			}
			key = parsed
		}
		_ = w.event("phase", fieldsOf("name", "writing"))
		st, perr := PackDir(src, dst, key, rep, requestIgnore(req))
		if perr != nil {
			fmt.Fprintf(errOut, "repack failed: %v\n", perr)
			_ = w.event("error", fieldsOf("code", "OUTPUT_FAILED",
				"message", perr.Error()))
			return GCWPOperationFailed
		}
		stats = &st
	}

	if err := w.event("statistics", fieldsOf("statistics", stats.statisticsJSON())); err != nil {
		return GCWPInternalError
	}
	if err := w.event("completed", fieldsOf("id", opID, "operation", op,
		"statistics", stats.statisticsJSON())); err != nil {
		return GCWPInternalError
	}
	return GCWPOK
}
