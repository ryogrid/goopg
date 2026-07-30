#!/usr/bin/env bash
# M0125-0003 §D8 — the TPC-DS instrument for the relation-size fallback.
#
# The flag-ON arm of the SF0.5 gate: 99 queries at GOOPG_RELSIZE_FALLBACK=2, the
# stage whose TPC-H C1→C2 reading was 1.40x with zero regressions
# (analysis/tpch-relsize-fallback-20260730.md). TPC-DS timeout relief — not a
# TPC-H win — is M0125's acceptance criterion, so this is the arm that decides
# whether stage 2 earns M0125-0005's default flip.
#
# The flag-OFF arm is NOT re-run. `git diff e29faca9..HEAD -- '*.go'` is empty,
# so analysis/m0125-sf05-fullgate-20260730/sweep-COMPLETE-20260730-102025.txt is
# this same engine, the same 300 s cap and the same quiet-host condition. The
# report's `# engine-id:` line is the check that licenses the reuse; if it
# differs from that run's, the comparison is void and the off-arm must be re-run.
#
# No FORCE: the host was verified quiet (no ci/batch, no SF=1 harness, load 0.9
# and falling, only PG:65438 idle), so per-query SECONDS are valid here — which
# matters, because the verdict this arm is asked for is a TIMEOUT count.
#
# Chunking is the same shape and sound for the same reason as the 2026-07-30
# off-arm: each chunk sf05_goopg_start-s a fresh server and runs S-cold, which
# is the gate's own determinism contract. ~2 h total exceeds one foreground call.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
OUT="analysis/m0125-0003-sf05-relsize-20260730"
export GOOPG_BIN="$PWD/tmp/goopg-sf05-bin"
export SF05_RESULTS_DIR="$OUT"
export TIMEOUT_SEC=300 RESTART_AFTER_TIMEOUT=1
# The arm itself. Exported here rather than per-chunk so no chunk can silently
# run the wrong arm; the report header echoes it back (`# planner-flags:`).
export GOOPG_RELSIZE_FALLBACK=2

range="${1:?usage: run-chunks.sh \"<first> <last>\"}"
set -- $range
echo "===== CHUNK Q$1-Q$2 begins $(date -Iseconds) ====="
QUERIES="$(seq "$1" "$2")" scripts/tpcds-sf05-regression.sh sweep
echo "===== CHUNK Q$1-Q$2 rc=$? ends $(date -Iseconds) ====="
