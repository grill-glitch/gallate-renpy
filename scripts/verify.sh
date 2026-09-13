#!/usr/bin/env bash
# Verification for sirenhead-tool (Go).
#
# Four gates, all of them real:
#
#   1. formatting + vet + the in-repo self-tests
#   2. a build of the actual CLI binary
#   3. a byte-level round-trip against a REAL Ren'Py game, using the
#      freshly built binary (not the package under test in-process):
#      extract must not touch a single byte of the game, and inject with
#      empty targets must leave it byte-identical
#   4. a source-drift probe: edit one source string after extract and
#      confirm inject refuses (exit 8) and writes NOTHING
#
# Set SIRENHEAD_GAME_DIR to the game's `game/` directory to run 3 and 4.
# Without it those steps are skipped loudly — the in-repo self-tests use
# a synthetic fixture and are NOT a substitute for the real thing.
#
# Usage:  ./scripts/verify.sh
#         SIRENHEAD_GAME_DIR=/path/to/Game/game ./scripts/verify.sh

set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }

echo "== 1. formatting, vet, tests"
test -z "$(gofmt -l . | grep -v '^$' || true)" || { gofmt -l .; fail "gofmt found unformatted files"; }
ok "gofmt clean"
go vet ./...
ok "go vet clean"
go test ./...
ok "self-tests pass"

echo "== 2. build the CLI"
BIN="$(mktemp -d)/sirenhead-tool"
go build -o "$BIN" ./cmd/sirenhead-tool
"$BIN" --version | head -1
ok "built $("$BIN" --version | head -1)"

GAME="${SIRENHEAD_GAME_DIR:-}"
if [ -z "$GAME" ] || [ ! -d "$GAME" ]; then
    echo "== 3./4. SKIPPED: set SIRENHEAD_GAME_DIR to a Ren'Py game's game/ directory"
    echo "   (the fixture self-tests above do not prove the CLI against a real game)"
    exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cp -a "$GAME" "$WORK/game"
PROJECT="$WORK/project"
mkdir -p "$PROJECT"

# A hash manifest of every file in the game directory.
hash_all() { ( cd "$WORK/game" && find . -type f -print0 | sort -z | xargs -0 sha256sum ); }

echo "== 3. real game: $GAME"
BEFORE="$WORK/before.sha256"
hash_all > "$BEFORE"
"$BIN" init "$PROJECT" --input "$WORK/game" --media text >/dev/null
"$BIN" -et "$PROJECT/gallate.yaml"
hash_all > "$WORK/after-extract.sha256"
diff -q "$BEFORE" "$WORK/after-extract.sha256" >/dev/null \
    || fail "extract modified the game directory"
ok "extract left every game file byte-identical"

"$BIN" -it "$PROJECT/gallate.yaml"
hash_all > "$WORK/after-inject.sha256"
diff -q "$BEFORE" "$WORK/after-inject.sha256" >/dev/null \
    || fail "empty-target inject modified the game directory"
ok "empty-target inject left every game file byte-identical"

UNITS="$(find "$PROJECT/text/units" -name '*.json' | wc -l)"
python3 - "$PROJECT" <<'EOF'
import json, os, sys
project = sys.argv[1]
total = 0
for name in sorted(os.listdir(os.path.join(project, "text", "units"))):
    with open(os.path.join(project, "text", "units", name), encoding="utf-8") as f:
        doc = json.load(f)
    total += len(doc["entries"])
print(f"  ok: {total} translation units across {len(os.listdir(os.path.join(project, 'text', 'units')))} unit file(s)")
EOF

echo "== 4. source drift must abort inject without writing"
DRIFT="$WORK/drift"
cp -a "$PROJECT" "$DRIFT"
python3 - "$DRIFT" <<'EOF'
import json, os, sys
project = sys.argv[1]
units = os.path.join(project, "text", "units")
name = sorted(os.listdir(units))[0]
path = os.path.join(units, name)
doc = json.load(open(path, encoding="utf-8"))
for e in doc["entries"][:1]:
    e["target"] = "【" + e["source"] + "】"
    e["state"] = "translated"
json.dump(doc, open(path, "w", encoding="utf-8"), ensure_ascii=False, indent=2)
EOF
# Change the source the unit points at, behind the CLI's back.
python3 - "$DRIFT" "$WORK/game" <<'EOF'
import json, os, sys
project, game = sys.argv[1], sys.argv[2]
units = os.path.join(project, "text", "units")
name = sorted(os.listdir(units))[0]
doc = json.load(open(os.path.join(units, name), encoding="utf-8"))
e = doc["entries"][0]
src = os.path.join(game, e["metadata"]["source_file"])
raw = open(src, "rb").read()
old = e["source"].encode()
assert old in raw, "drift probe string not found"
open(src, "wb").write(raw.replace(old, old + b" DRIFTED", 1))
EOF
hash_all > "$WORK/before-drift-inject.sha256"
set +e
"$BIN" -it "$DRIFT/gallate.yaml" >"$WORK/drift.out" 2>"$WORK/drift.err"
DRIFT_RC=$?
set -e
[ "$DRIFT_RC" -eq 8 ] || fail "drift inject exit=$DRIFT_RC, expected 8"
grep -q drift "$WORK/drift.err" || fail "drift error message does not say 'drift'"
hash_all > "$WORK/after-drift-inject.sha256"
diff -q "$WORK/before-drift-inject.sha256" "$WORK/after-drift-inject.sha256" >/dev/null \
    || fail "drift inject wrote to the game directory despite aborting"
ok "drift detected (exit 8), nothing written"

echo
echo "ALL VERIFICATION GATES PASSED"
