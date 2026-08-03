#!/usr/bin/env bash
# M0125-0031 goal (a) — the WARM arm of the TPC-DS SF0.5 gate.
#
# No flag is exported and no script change was needed: since M0125-0029 the
# SF0.5 cluster's ANALYZE statistics survive restart, so the gate's own "fresh
# S-cold server" contract now means "fresh server, warm statistics". The warm
# regime was verified on the live cluster before the run (24/25 tables at
# reltuples > 0, 25 tables in pg_stats) rather than assumed.
#
# The arm is the SHIPPED DEFAULT (GOOPG_RELSIZE_FALLBACK unset => 2), the same
# arm as the S-cold baseline in ../m0125-0003-sf05-relsize-20260730/, so
# statistics are the only intended variable. The report header echoes it back.
#
# GOOPG_BIN is private: tmp/goopg-bench-bin is shared with the nightly CI lane,
# which runs servers from it for hours.
#
# Chunking is sound for the same reason as the baseline's four chunks — each
# chunk sf05_goopg_start-s a fresh server, which is the gate's determinism
# contract, and ~2 h exceeds one foreground call. Each chunk report is stamped
# SUBSET PROBE; the merged sweep-COMPLETE-*.txt in this directory is the
# gate-shaped artefact (99/99, one binary sha across every chunk).
#
# As run, 2026-07-30 22:04 -> 23:58, quiet host, no FORCE:
#   run-chunks.sh "1 25" ; "26 50" ; "51 63" ; "64 75" ; "76 99"
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
export GOOPG_BIN="$PWD/tmp/goopg-sf05-warm-bin"
export TIMEOUT_SEC=300 RESTART_AFTER_TIMEOUT=1

range="${1:?usage: run-chunks.sh \"<first> <last>\"}"
set -- $range
echo "===== CHUNK Q$1-Q$2 begins $(date -Iseconds) ====="
QUERIES="$(seq "$1" "$2")" scripts/tpcds-sf05-regression.sh sweep
echo "===== CHUNK Q$1-Q$2 rc=$? ends $(date -Iseconds) ====="
