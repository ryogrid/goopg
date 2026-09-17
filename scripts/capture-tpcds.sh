#!/usr/bin/env bash
# scripts/capture-tpcds.sh — capture TPC-DS EXPLAIN plan sections
# (sf05_capture_plans format, normalised to `=== Qn` so
# scripts/pg-plan-parity-diff.py's SECTION_RE matches), GUCs pinned as in
# capture-tpch.sh.
#
# Promoted from
# docs/design/not_ralph/plan_parity_fix_take2/{methodology,r2-instrument}/capture-tpcds.sh
# (M0137-0001) — those two copies are retired; do not resurrect them or cite
# them in a new procedure. This script had the identical K18 "$$" trap in
# TWO places (the EXPLAIN-rewrite scratch file and the psql -f argument
# reuse the same path) — see capture-tpch.sh's header comment for the full
# explanation and fix rationale.
#
# Every capture is also machine-stamped (M0137-0002): engine-id, repo HEAD,
# planner-flags arm, pinned GUCs, serving-binary path/inode/PID and a
# stats-epoch fingerprint, written by scripts/lib/capture-stamp.sh instead of
# resting on the caller's hand-typed <header> — see capture-tpch.sh's header
# comment for the full rationale. The binary/PID stamp needs the server's
# datadir; pass it as an optional 6th arg or it reads UNKNOWN(no datadir given).
#
# This script cannot pin ANALYZE's reservoir-sampler seed itself (M0137-0020):
# it only opens client `psql` sessions and never runs ANALYZE — the TPC-DS
# load procedure ANALYZEs once, durably, at load time (bench/tpcds/README.md
# "3. ANALYZE each table"), against whatever GOOPG_ANALYZE_SEED the server
# process was started with. The seed is now pinned at the earliest common
# point instead: bench/tpcds/env_tpcds.sh (sourced by bench/tpcds/server.sh
# before it starts the server), default 20260905, same value
# scripts/tpch-acceptance-arm.sh already pins. See that file's comment and
# docs/design/0100-0149/m0138-0008-category-shift-bisect.md for why an
# unpinned seed matters here: four unpinned trials at two FIXED commits moved
# plan-parity category counts (join-order, qual-placement, join-method,
# scan-type, aggregation-strategy, parallelism) from reservoir-sampler
# variance alone.
#
# usage: capture-tpcds.sh <port> <db> <user> <outfile> <header> [datadir]
#   env: CAPTURE_ENGINE=goopg|pg (inferred only for the 6543x reference ports;
#        REQUIRED on any other port — exit 1 when unset),
#        GOOPG_EXPECT_BIN_SHA256=<sha256> (REQUIRED for goopg; exit 1 when
#        missing or on mismatch)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PG_BIN="${PG_BIN:-${REPO_ROOT}/postgres/local_install/bin}"
export PATH="${PG_BIN}:${PATH}"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib/capture-stamp.sh"

PORT="$1"; DB="$2"; USER="$3"; OUT="$4"; HDR="$5"; DATADIR="${6:-}"

# H6 (METHODLOGY3 04-actions): verify the serving goopg binary BEFORE touching
# $OUT — datadir required for goopg, "(deleted)" exe refused, and
# GOOPG_EXPECT_BIN_SHA256 required and enforced. Engine from CAPTURE_ENGINE=goopg|pg
# or the shared-cluster port map; see capture_verify_serving_binary.
capture_verify_serving_binary "capture-tpcds.sh" "$PORT" "$DATADIR" || exit 1

QDIR="${TPCDS_QUERY_DIR:-${REPO_ROOT}/bench/tpcds/runtime_goopg/tpcds-data/queries}"
PIN=(-c "SET work_mem='64MB'" -c "SET max_parallel_workers_per_gather=4")
PIN_DESC="work_mem=64MB max_parallel_workers_per_gather=4"

# Deterministic, not $$ — see capture-tpch.sh's header comment.
tmp="${TMPDIR:-/tmp}/goopg-parity-capture-$(basename "$OUT").sql"

{
    echo "# ${HDR}"
    echo "# EXPLAIN only (no ANALYZE)."
    capture_stamp_block "$PORT" "$DB" "$USER" "$DATADIR" "$PIN_DESC" "$(basename "$OUT")"
} > "$OUT"

for q in $(seq 1 99); do
    f="${QDIR}/query${q}.sql"
    if [ ! -f "$f" ]; then
        echo "=== Q${q}" >> "$OUT"
        echo "MISSING QUERY FILE" >> "$OUT"
        continue
    fi
    echo "=== Q${q}" >> "$OUT"
    python3 - "$f" > "$tmp" <<'PY'
import sys
src = open(sys.argv[1]).read()
for stmt in src.split(';'):
    if stmt.strip():
        print("EXPLAIN " + stmt.strip() + ";")
PY
    if ! timeout 120 psql -h 127.0.0.1 -p "$PORT" -U "$USER" -d "$DB" -X \
            "${PIN[@]}" -f "$tmp" 2>&1 | grep -vx SET >> "$OUT"; then
        echo "(explain failed)" >> "$OUT"
    fi
done
rm -f "$tmp"
