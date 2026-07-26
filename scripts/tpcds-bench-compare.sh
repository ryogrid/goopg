#!/usr/bin/env bash
# tpcds-bench-compare.sh — Run TPC-DS queries on goopg + PG side by side,
# capturing status, elapsed time, row count, error message and EXPLAIN plan.
#
# Usage:
#   scripts/tpcds-bench-compare.sh                 # all 99 queries
#   scripts/tpcds-bench-compare.sh 47              # one query
#   scripts/tpcds-bench-compare.sh 8,39,47,50,76   # a list
#   scripts/tpcds-bench-compare.sh 5-14            # a range
#
# Env:
#   TIMEOUT_SEC        per-query timeout, default 600
#   EXPLAIN_TIMEOUT    per-query EXPLAIN timeout, default 30
#   SKIP_EXPLAIN=1     skip EXPLAIN capture (faster)
#   ENGINES            "goopg pg" (default), or just one of them
#
# Three defects fixed on 2026-07-27 (see
# docs/design/tpcds-round2-fixes/README.md §1.3), all of which corrupted
# the 2026-07-26 report:
#
#  1. `psql` was invoked bare, without sourcing bench/tpch/env_goopg.sh, so
#     it fell off PATH partway through the sweep. Every *_explain.txt and
#     *_result.txt from Q36 onward was the stub "timeout: failed to run
#     command 'psql'". Now PATH/LD_LIBRARY_PATH are set explicitly from
#     PG_PREFIX and the binary is probed before any query runs.
#  2. The row count was extracted with `tail -1` on the `(N rows)` marker,
#     which reports only the LAST result block. Q14/Q23/Q24/Q39 each hold
#     two statements, so their row counts were silently wrong. Now every
#     block is summed, and the per-block breakdown is recorded.
#  3. goopg and PG ran concurrently (`&` … `wait`), so each engine's
#     elapsed time included contention from the other. They now run
#     sequentially.
#
# Also: `EXPLAIN $(cat file)` could not work on a multi-statement file.
# EXPLAIN is now prefixed per statement.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# Fix (1): put the PG 18.3 client tools on PATH. env_goopg.sh sets
# `set -euo pipefail` and exports PG_PREFIX/PATH/LD_LIBRARY_PATH.
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"
export PATH="${PG_PREFIX}/bin:${PATH}"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib:${LD_LIBRARY_PATH:-}"

if ! command -v psql >/dev/null 2>&1; then
    echo "FATAL: psql not on PATH after sourcing env_goopg.sh (looked in ${PG_PREFIX}/bin)" >&2
    exit 2
fi

QDIR="${REPO_ROOT}/bench/tpch/runtime_goopg/tpcds-data/queries"
OUTDIR="${REPO_ROOT}/bench/tpch/runtime_goopg/tpcds-results"
mkdir -p "${OUTDIR}"

GOOPG_PSQL="psql -h 127.0.0.1 -p 65433 -U postgres -d postgres"
PG_PSQL="psql -h 127.0.0.1 -p 65432 -U ryo -d tpcds"
TIMEOUT_SEC="${TIMEOUT_SEC:-600}"
EXPLAIN_TIMEOUT="${EXPLAIN_TIMEOUT:-30}"
ENGINES="${ENGINES:-goopg pg}"

# PG queries to skip (dsqgen subquery-in-FROM generation artefacts;
# these fail on upstream PostgreSQL too — see design doc §1.2).
PG_SKIP="36 70 86"

# parse_qlist "1,3,5-8" -> "1 3 5 6 7 8"; empty argument -> 1..99
parse_qlist() {
    local spec="${1:-}"
    if [[ -z "$spec" ]]; then seq 1 99; return; fi
    local part lo hi
    for part in ${spec//,/ }; do
        if [[ "$part" == *-* ]]; then
            lo="${part%%-*}"; hi="${part##*-}"
            seq "$lo" "$hi"
        else
            echo "$part"
        fi
    done
}

# explain_file_for <query.sql> — emit each statement prefixed with EXPLAIN.
# Fix (4): `EXPLAIN $(cat file)` cannot handle a file with two statements.
explain_script() {
    awk '
        { stmt = stmt $0 "\n" }
        /;[[:space:]]*$/ { printf "EXPLAIN %s", stmt; stmt = "" }
        END { if (stmt ~ /[^[:space:]]/) printf "EXPLAIN %s;\n", stmt }
    ' "$1"
}

run_one() {
    local label="$1" psql_cmd="$2" q="$3"
    local qf="${QDIR}/query${q}.sql"
    local out_prefix="${OUTDIR}/${label}_q${q}"
    local explain_file="${out_prefix}_explain.txt"
    local result_file="${out_prefix}_result.txt"

    [[ -f "$qf" ]] || return 0

    if [[ -z "${SKIP_EXPLAIN:-}" ]]; then
        { echo "# EXPLAIN for query${q}.sql on ${label} — $(date -Iseconds)"; } > "${explain_file}"
        explain_script "$qf" > "${OUTDIR}/.explain_${label}_${q}.sql"
        timeout "${EXPLAIN_TIMEOUT}" ${psql_cmd} -f "${OUTDIR}/.explain_${label}_${q}.sql" \
            >> "${explain_file}" 2>&1 || true
        rm -f "${OUTDIR}/.explain_${label}_${q}.sql"
    fi

    local start=$SECONDS qout qex
    qout=$(timeout "${TIMEOUT_SEC}" ${psql_cmd} -f "${qf}" 2>&1) && qex=0 || qex=$?
    local elapsed=$((SECONDS - start))

    local status rows err blocks
    if [[ $qex -eq 124 ]]; then
        status="TIMEOUT"; rows=0; blocks=""; err="timeout after ${TIMEOUT_SEC}s"
    elif grep -qi "ERROR" <<<"$qout"; then
        status="ERROR"; rows=0; blocks=""
        err=$(grep -i "ERROR" <<<"$qout" | head -1 | tr -d '|' | cut -c1-200)
    elif grep -qP '\(\d+ rows?\)' <<<"$qout"; then
        status="OK"; err=""
        # Fix (2): sum EVERY result block, not just the last one, so a
        # multi-statement template (Q14/Q23/Q24/Q39) reports its true total.
        blocks=$(grep -oP '\(\d+ rows?\)' <<<"$qout" | grep -oP '\d+' | paste -sd+ -)
        rows=$(( $(grep -oP '\(\d+ rows?\)' <<<"$qout" | grep -oP '\d+' | paste -sd+ -) ))
        # Show the breakdown only when there is more than one block.
        [[ "$blocks" == *+* ]] || blocks=""
    else
        status="OK"; rows=$(wc -l <<<"$qout"); blocks=""; err=""
    fi

    printf '%s\n' "${qout}" > "${result_file}"
    printf "%-5s %-4s %-7s %6ss %8s %s%s\n" \
        "${label}" "Q${q}" "${status}" "${elapsed}" "${rows}" \
        "${blocks:+[${blocks}] }" "${err:0:80}"
}

echo "# TPC-DS SF=1 Benchmark — $(date -Iseconds)"
echo "# goopg: $(git log --oneline -1)  PG: 18.3"
echo "# timeout: ${TIMEOUT_SEC}s per query, engines: ${ENGINES}, PG skip: ${PG_SKIP}"
echo ""

for q in $(parse_qlist "${1:-}"); do
    # Fix (3): sequential, never concurrent — a concurrent peer
    # contaminates the elapsed time of both engines.
    for eng in ${ENGINES}; do
        case "$eng" in
        goopg) run_one "goopg" "${GOOPG_PSQL}" "${q}" ;;
        pg)
            if ! grep -qw "${q}" <<<"${PG_SKIP}"; then
                run_one "pg" "${PG_PSQL}" "${q}"
            else
                printf "%-5s %-4s %-7s %s\n" "pg" "Q${q}" "SKIP" "known dsqgen artefact (fails on PG too)"
            fi
            ;;
        esac
    done
done

echo ""
echo "=== DONE ==="
echo "Results:  ${OUTDIR}/<engine>_q<N>_result.txt"
echo "EXPLAINs: ${OUTDIR}/<engine>_q<N>_explain.txt"
