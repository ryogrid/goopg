#!/usr/bin/env bash
# scripts/capture-tpch.sh — capture TPC-H EXPLAIN plan sections from a goopg
# or PG server, with the planner-visible session GUCs PINNED so a cluster's
# ambient config cannot decide the comparison (R2: the TPC-DS pair was
# running 512MB vs 4MB). The `SET` command tags are stripped — they parse as
# a plan node named SET.
#
# Promoted from
# docs/design/not_ralph/plan_parity_fix_take2/{methodology,r2-instrument}/capture-tpch.sh
# (M0137-0001) — those two copies are retired; do not resurrect them or cite
# them in a new procedure.
#
# Fixes the K18 "$$" trap: both retired copies named their scratch SQL file
# after the capturing PROCESS's PID (parity-capture-$$.sql), which the
# r2-instrument copy's own comment mislabelled "fixed". It is not: $$ is the
# shell's PID, which differs on every invocation even when nothing else
# changed. For any query that fails to plan, psql prefixes its error text
# with the `-f` filename it was given (`psql:<path>:<line>: ERROR: ...`), so
# the PID leaked into the capture and produced a spurious diff between two
# otherwise-identical runs — seen live in R122, R123, R124 and R128 (Q36,
# Q70, Q86 are the queries whose EXPLAIN fails and exposes it). The fix:
# derive the scratch path from the OUTPUT file's name, which is
# caller-chosen and constant across repeats, never from $$.
#
# Every capture is also machine-stamped (M0137-0002): engine-id, repo HEAD,
# planner-flags arm, pinned GUCs, serving-binary path/inode/PID and a
# stats-epoch fingerprint, written by scripts/lib/capture-stamp.sh instead of
# resting on the caller's hand-typed <header> the way R122 §10 found it. The
# binary/PID stamp needs the server's datadir; pass it as an optional 6th arg
# or it reads UNKNOWN(no datadir given) rather than guessing.
#
# usage: capture-tpch.sh <port> <db> <user> <outfile> <header> [datadir]
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
capture_verify_serving_binary "capture-tpch.sh" "$PORT" "$DATADIR" || exit 1

# NOT git-tracked (deferral ledger M0137-0001, 2026-09-15, resolved M0137-0003):
# the durable query source is the Go package internal/testutil/tpch (also what
# cmd/estimate-audit reads via tpch.Queries()/tpch.Q15ViewBody()), but this
# script predates that tool and is a lighter-weight EXPLAIN-only capture.
# Override both vars for a fresh checkout or CI where /tmp/parity-r0 was
# never seeded.
#
# M0137-0003: this script is NOT the TPC-H baseline-capture procedure — see
# docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md. Use
# `estimate-audit -plan-only` for a TPC-H baseline (it warm-ANALYZEs the one
# held session itself, so it never depends on prior state). This script stays
# useful for a quick raw-EXPLAIN structural capture against an
# already-stats-warm long-running cluster; it does not ANALYZE anything
# itself, so pointing it at a cold cluster silently measures the no-stats
# planner. It fails loudly (below) rather than emitting 22
# "MISSING QUERY FILE" sections when the query directory is missing, per the
# deferred item's own complaint ("no error surfaced beyond per-query
# 'MISSING QUERY FILE' lines").
Q="${TPCH_QUERY_DIR:-/tmp/parity-r0/queries/tpch}"
Q15A="${TPCH_Q15A_FILE:-/tmp/parity-r0/q15a.sql}"

if [ ! -d "$Q" ] || [ ! -f "$Q15A" ]; then
    echo "capture-tpch.sh: query source missing (TPCH_QUERY_DIR=$Q, TPCH_Q15A_FILE=$Q15A)." >&2
    echo "  Neither is git-tracked; seed them or export both vars. See" >&2
    echo "  docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md." >&2
    exit 1
fi

PIN=(-c "SET work_mem='64MB'" -c "SET max_parallel_workers_per_gather=4")
PIN_DESC="work_mem=64MB max_parallel_workers_per_gather=4"

: > "$OUT"
{
    echo "# ${HDR}"
    capture_stamp_block "$PORT" "$DB" "$USER" "$DATADIR" "$PIN_DESC" "$(basename "$OUT")"
} >> "$OUT"

run() {
    echo "=== $1" >> "$OUT"
    if ! timeout 180 psql -h 127.0.0.1 -p "$PORT" -U "$USER" -d "$DB" -X \
            "${PIN[@]}" -f "$2" 2>&1 | grep -vx SET >> "$OUT"; then
        echo "(capture failed)" >> "$OUT"
    fi
}

# Deterministic, not $$ — see the header comment above for why this matters.
# Two captures sharing the same $OUT use the same scratch path, so a query
# that fails to plan reports identical error text both times instead of
# leaking a PID into the diff.
tmp="${TMPDIR:-/tmp}/goopg-parity-capture-$(basename "$OUT").sql"

for n in $(seq 1 22); do
    if [ "$n" = "15" ]; then run "Q15a-VIEWBODY" "$Q15A"; continue; fi
    f="$Q/Q${n}.sql"
    if [ ! -f "$f" ]; then
        echo "=== Q${n}" >> "$OUT"
        echo "MISSING QUERY FILE" >> "$OUT"
        continue
    fi
    { echo -n "EXPLAIN "; cat "$f"; } > "$tmp"
    run "Q${n}" "$tmp"
done
rm -f "$tmp"
