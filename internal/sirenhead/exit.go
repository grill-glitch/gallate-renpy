package sirenhead

// Exit codes.
//
// A CLI that implements both gallate layers carries TWO exit-code
// tables, and they MUST NOT be conflated (the same integer means
// different things in each layer):
//
//   - Shell layer  → docs/shell-layer/10-stdout-stderr.md § 10.3 (0..10)
//   - Protocol layer (GCWP) → docs/protocol/02-core-protocol.md § Exit codes (0..8)
//
// Engine extensions MAY use codes >= 11 for the shell layer.
const (
	// --- Shell layer (docs/shell-layer/10-stdout-stderr.md § 10.3) ---

	// ExitOK — success.
	ExitOK = 0
	// ExitGeneral — general error.
	ExitGeneral = 1
	// ExitUsage — invalid CLI usage (bad flags, missing/garbage target).
	ExitUsage = 2
	// ExitProjectConfig — invalid project configuration
	// (unparseable gallate.yaml, missing `input:`, illegal sub-media
	// filter, `.meta.json` schema_version newer than this CLI).
	ExitProjectConfig = 3
	// ExitInputNotFound — the configured input, or a source resource
	// recorded in the project, does not exist.
	ExitInputNotFound = 4
	// ExitUnsupportedOperation — the engine does not implement the operation.
	ExitUnsupportedOperation = 5
	// ExitUnsupportedMedia — the engine does not support the requested media.
	ExitUnsupportedMedia = 6
	// ExitExtractionFailure — extract failed.
	ExitExtractionFailure = 7
	// ExitInjectionFailure — inject failed (source drift, encode failure,
	// missing quote pair, …). Nothing is written when this is returned.
	ExitInjectionFailure = 8
	// ExitOutputFailure — writing output failed.
	ExitOutputFailure = 9
	// ExitScriptFailure — a configured pre/post script failed.
	ExitScriptFailure = 10
)

// --- Protocol layer (GCWP), docs/protocol/02-core-protocol.md § Exit codes ---
const (
	// GCWPOK — success.
	GCWPOK = 0
	// GCWPOperationFailed — operation failed.
	GCWPOperationFailed = 1
	// GCWPInvalidArguments — invalid arguments.
	GCWPInvalidArguments = 2
	// GCWPInvalidConfig — invalid configuration.
	GCWPInvalidConfig = 3
	// GCWPUnsupportedOperation — unsupported operation.
	GCWPUnsupportedOperation = 4
	// GCWPValidationFailed — validation failed.
	GCWPValidationFailed = 5
	// GCWPCancelled — cancelled.
	GCWPCancelled = 6
	// GCWPProtocolError — protocol error (malformed stdin, unknown message type).
	GCWPProtocolError = 7
	// GCWPInternalError — internal error.
	GCWPInternalError = 8
)

// ExitError is an error carrying the process exit code the caller
// should return. Shell-layer codes are used unless a GCWP path builds
// one explicitly with a protocol code.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// exitErr builds an ExitError.
func exitErr(code int, msg string) *ExitError {
	return &ExitError{Code: code, Msg: msg}
}

// usageErr reports an invalid CLI invocation (shell code 2).
func usageErr(msg string) *ExitError {
	return exitErr(ExitUsage, msg)
}
