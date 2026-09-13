package sirenhead

// Pre / post scripts (docs/shell-layer/05-config-file.md § 5.7).
//
// `gallate.yaml` may declare `scripts.pre` / `scripts.post`. They run
// with the Project Root as working directory, in definition order, and
// their failure is a distinct exit code (10, Script Failure). They are
// skipped under `--dry-run`, which per docs/shell-layer/09 § 9.3 MUST
// NOT run scripts by default.

import (
	"os"
	"os/exec"
	"strings"
)

// runScripts executes the configured scripts in order.
func runScripts(scripts []string, projectRoot string, phase string, rep *Reporter) error {
	for _, script := range scripts {
		if strings.TrimSpace(script) == "" {
			continue
		}
		abs := resolvePath(script, projectRoot)
		rep.file("read", script)
		cmd := exec.Command(abs)
		cmd.Dir = projectRoot
		cmd.Stdin = nil
		cmd.Stdout = os.Stderr // script chatter is diagnostic, never stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(),
			"GALLATE_PROJECT_ROOT="+projectRoot,
			"GALLATE_PHASE="+phase,
		)
		if err := cmd.Run(); err != nil {
			return exitErr(ExitScriptFailure,
				phase+" script failed: "+script+": "+err.Error())
		}
	}
	return nil
}
