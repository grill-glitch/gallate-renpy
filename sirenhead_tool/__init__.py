# sirenhead-tool — a gallate CLI for "Siren Head Dating Sim"
# (a Ren'Py game).
#
# This package is one CLI that speaks both gallate layers:
#   * Shell layer — invoked directly from the user's shell with
#     `tool -et ./gallate.yaml`. See `docs/shell-layer/`.
#   * Protocol layer (GCWP) — invoked by a Wrapper through JSON Lines
#     on stdin/stdout. See `docs/protocol/`.
#
# Both layers run from the same binary; this file just makes Python
# importable as a package so `python -m sirenhead_tool` works.
