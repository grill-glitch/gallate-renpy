// Command sirenhead-tool is a gallate CLI for the Ren'Py visual novel
// "Siren Head Dating Sim".
//
// It implements BOTH layers of the gallate specification:
//
//   - Shell layer — `sirenhead-tool -et ./gallate.yaml` from a shell.
//   - Protocol layer (GCWP) — the same binary driven by a Wrapper
//     through JSON Lines on stdin/stdout.
//
// See https://github.com/grill-glitch/gallate for the specification,
// and the repository README for the CLI's own conformance statement.
package main

import (
	"os"

	"github.com/grill-glitch/gallate-renpy/internal/sirenhead"
)

func main() {
	os.Exit(sirenhead.Main(os.Args[1:]))
}
