package sirenhead

// AST cross-check (docs: the CLI's own README § "What this CLI does NOT
// do").
//
// Ren'Py 7 bundles its parser as `renpy/ast.py`, which is **Python 2**
// code (`import cPickle`). To cross-check the regex extractor against
// the engine's own notion of a translatable string we would need to run
// that parser, which requires a Python 2 interpreter on PATH. Modern
// hosts do not have one.
//
// So this is honest about its own limits:
//
//   - no Python 2 on PATH       → no-op, the regex is authoritative;
//   - Python 2 but no renpy/    → no-op;
//   - both present              → the AST's Say nodes are matched back to
//     the regex findings by text, and a speaker is filled in when the AST
//     knows one the regex did not.
//
// It never removes a regex finding: the regex is the authority, the AST
// is a second opinion.

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
)

// astDumpScript dumps every Ren'Py Say node as JSON Lines. Run under
// Python 2 because `renpy.ast` imports cPickle.
const astDumpScript = `
import sys, json
sys.path.insert(0, ".")
try:
    import renpy.ast
except Exception as e:
    sys.stderr.write("ast import failed: %s\n" % e)
    sys.exit(2)

game_root = sys.argv[1]
import os
for dirpath, _dirs, files in os.walk(game_root):
    for f in files:
        if not f.endswith(".rpy"):
            continue
        path = os.path.join(dirpath, f)
        try:
            tree = renpy.ast.parse(open(path).read())
        except Exception as e:
            sys.stderr.write("parse failed: %s: %s\n" % (path, e))
            continue
        for node in tree:
            if isinstance(node, renpy.ast.Say):
                what = node.what or node.attributes.get("text", "")
                sys.stdout.write(json.dumps({"who": node.who, "what": what}) + "\n")
`

// astFallbackValidate cross-checks extraction against Ren'Py's parser
// when (and only when) both a Python 2 interpreter and the engine's own
// `renpy/ast.py` are available.
func astFallbackValidate(findings []finding, gameRoot string, args *Args, rep *Reporter) {
	if len(findings) == 0 {
		return
	}
	enabled := false
	if v, ok := args.EngineOpts["use-ast"]; ok {
		b, ok := asBool(v)
		if ok {
			enabled = b
		} else {
			enabled = true // `--engine.use-ast` with no value means on
		}
	}
	if !enabled {
		return
	}
	py2 := findPython2()
	if py2 == "" {
		rep.verbosef("ast: no Python 2 interpreter on PATH; regex is authoritative")
		return
	}
	renpyRoot := filepath.Dir(gameRoot)
	if !fileExists(filepath.Join(renpyRoot, "renpy", "ast.py")) {
		rep.verbosef("ast: %s/renpy/ast.py not found; regex is authoritative", renpyRoot)
		return
	}

	cmd := exec.Command(py2, "-c", astDumpScript, gameRoot)
	cmd.Dir = renpyRoot
	out, err := cmd.Output()
	if err != nil {
		rep.verbosef("ast: parser run failed (%v); regex is authoritative", err)
		return
	}

	byBody := map[string][]int{}
	for i, f := range findings {
		body := f.text
		if m := bodyRE.FindStringSubmatch(f.text); m != nil {
			body = m[1]
		}
		byBody[body] = append(byBody[body], i)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var entry struct {
			Who  string `json:"who"`
			What string `json:"what"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		for _, i := range byBody[entry.What] {
			if !findings[i].hasSpeaker && entry.Who != "" && entry.Who != "None" {
				findings[i].speaker = entry.Who
				findings[i].hasSpeaker = true
			}
		}
	}
	rep.verbosef("ast: cross-checked %d finding(s) against renpy.ast", len(findings))
}

// findPython2 returns the path of a working Python 2 interpreter, or "".
// Ren'Py 7's ast.py cannot be imported by Python 3 (`import cPickle`).
func findPython2() string {
	for _, name := range []string{"python2", "python2.7"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if err := exec.Command(path, "-c", "import cPickle").Run(); err == nil {
			return path
		}
	}
	return ""
}
