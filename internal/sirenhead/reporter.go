package sirenhead

// Reporter carries everything an operation wants to tell the outside
// world. The Shell layer renders it as human lines on stderr; the GCWP
// layer renders it as JSON events on stdout. Operations never print
// directly, which is what keeps the two layers honest — and what
// guarantees the hard rule of docs/shell-layer/10-stdout-stderr.md
// § 10.1: stdout carries only the operation result.

import (
	"fmt"
	"io"
	"strings"
)

// Reporter is a nil-safe sink. A nil *Reporter discards everything.
type Reporter struct {
	// Stderr receives human-readable progress / warnings / errors.
	Stderr io.Writer
	// Quiet suppresses non-essential stderr chatter (never warnings or
	// errors, and never the exit code: docs/shell-layer/09 § 9.6).
	Quiet bool
	// Verbose is the -v / -vv level.
	Verbose int

	// Hooks used by the GCWP layer. Nil for the Shell layer.
	PhaseHook    func(name string)
	FileHook     func(action, path string)
	ProgressHook func(current, total int, indeterminate bool)
	WarningHook  func(code, message, path string)
	// ValidationHook receives one `validation` finding.
	ValidationHook func(finding map[string]any)
}

// phase emits a phase transition (GCWP) or an optional stderr note.
func (r *Reporter) phase(name string) {
	if r == nil {
		return
	}
	if r.PhaseHook != nil {
		r.PhaseHook(name)
	}
	r.verbosef("phase: %s", name)
}

// file reports a per-file action.
func (r *Reporter) file(action, path string) {
	if r == nil {
		return
	}
	if r.FileHook != nil {
		r.FileHook(action, path)
	}
	r.verbosef("%s %s", action, path)
}

// progress reports determinate or indeterminate progress.
func (r *Reporter) progress(current, total int, indeterminate bool) {
	if r == nil {
		return
	}
	if r.ProgressHook != nil {
		r.ProgressHook(current, total, indeterminate)
	}
}

// warn reports a non-fatal warning. Warnings never change the exit code
// (docs/protocol/09 § Diagnostics).
func (r *Reporter) warn(code, message, path string) {
	if r == nil {
		return
	}
	if r.WarningHook != nil {
		r.WarningHook(code, message, path)
	}
	if r.Stderr != nil {
		if path != "" {
			fmt.Fprintf(r.Stderr, "warning: %s: %s (%s)\n", code, message, path)
		} else {
			fmt.Fprintf(r.Stderr, "warning: %s: %s\n", code, message)
		}
	}
}

// validation reports one validation finding.
func (r *Reporter) validation(finding map[string]any) {
	if r == nil || r.ValidationHook == nil {
		return
	}
	r.ValidationHook(finding)
}

// note writes a normal stderr line (suppressed by -q).
func (r *Reporter) note(format string, args ...any) {
	if r == nil || r.Quiet || r.Stderr == nil {
		return
	}
	fmt.Fprintf(r.Stderr, format+"\n", args...)
}

// verbosef writes an extra-detail stderr line (-v / -vv only).
func (r *Reporter) verbosef(format string, args ...any) {
	if r == nil || r.Verbose == 0 || r.Stderr == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	io.WriteString(r.Stderr, line)
}
