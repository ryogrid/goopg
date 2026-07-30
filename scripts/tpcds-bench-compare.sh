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

# Fix (1): put the PG 18.3 client tools on PATH (env_tpcds.sh exports
# PG_PREFIX/PATH/LD_LIBRARY_PATH and every TPC-DS dir/port).
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

if ! command -v psql >/dev/null 2>&1; then
    echo "FATAL: psql not on PATH after sourcing env_tpcds.sh (looked in ${PG_PREFIX}/bin)" >&2
    exit 2
fi

QDIR="${TPCDS_QUERY_DIR}"
OUTDIR="${TPCDS_RESULTS_DIR}"
mkdir -p "${OUTDIR}"

GOOPG_PSQL="psql -h ${TPCDS_HOST} -p ${TPCDS_PORT} -U ${TPCDS_SUPERUSER} -d postgres"
PG_PSQL="psql -h ${TPCDS_HOST} -p ${TPCDS_PG_PORT} -U ${TPCDS_PG_USER} -d ${TPCDS_PG_DB}"
TIMEOUT_SEC="${TIMEOUT_SEC:-600}"
EXPLAIN_TIMEOUT="${EXPLAIN_TIMEOUT:-30}"
# Which bench/tpcds/server.sh lane restart_goopg bounces. It was hardcoded to
# sf1, so pointing this harness at another cluster (TPCDS_PORT=65437) still
# restarted the SF=1 server — i.e. it bounced the wrong cluster and left the
# measured one untouched. Default is unchanged.
SF_LANE="${SF_LANE:-sf1}"
ENGINES="${ENGINES:-goopg pg}"
# Set by run_one so the caller can react to a TIMEOUT (see restart_goopg).
LAST_STATUS=""

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
    # Match psql's own error prefix, not a bare "error" anywhere in the
    # output. The previous form (`grep -qi ERROR` over the whole result)
    # turned any query returning a row whose text contains "error" into a
    # false ERROR with rows=0 — and it ran BEFORE the row-count branch, so
    # the misclassification was silent.
    elif grep -qE '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):' <<<"$qout"; then
        status="ERROR"; rows=0; blocks=""
        err=$(grep -E '^(psql:[^ ]*:[0-9]+: )?(ERROR|FATAL|PANIC):' <<<"$qout" | head -1 | tr -d '|' | cut -c1-200)
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
    LAST_STATUS="${status}"
    printf "%-5s %-4s %-7s %6ss %8s %s%s\n" \
        "${label}" "Q${q}" "${status}" "${elapsed}" "${rows}" \
        "${blocks:+[${blocks}] }" "${err:0:80}"
}

# Sweep-integrity provenance (M0124-0001, design doc 0124-0001 D1).
#
# A multi-loop sweep is chunked into several invocations and every chunk
# header must describe the SAME engine. `git log -1` alone cannot carry that
# invariant for two measured reasons:
#
#  * it changes when a docs/tracker commit lands between chunks, which is not
#    a code change and must not read as one; and
#  * it does NOT change when the engine differs — `bench/tpcds/server.sh
#    start` runs `go build` from the WORKING TREE, so an uncommitted engine
#    edit enters the sweep at the next RESTART_AFTER_TIMEOUT bounce. That is
#    exactly the mis-provenance trap the design doc opens with
#    (`sweep-20260727-214619.txt` labelled with RC-1b's parent).
#
# So the header also carries the tree hashes of the engine source and the
# sha256 of the binary actually serving the cluster. Chunks are comparable
# when engine-tree AND engine-binary match; restart_goopg re-checks the
# binary after every rebuild.
# The three helpers themselves moved to bench/tpcds/env_tpcds.sh on 2026-07-30
# (M0125-0011's gate-integrity follow-up): the SF0.5 gate needs the SAME fields
# with the SAME meaning, and two copies of a provenance rule drift. The reasons
# behind each definition are documented at that single site; these wrappers keep
# this script's call sites and its "SWEEP VOID" policy unchanged.
engine_id() { bench_engine_id; }
engine_bin_sha() { bench_engine_bin_sha "${GOOPG_BIN}"; }
running_engine_sha() { bench_running_engine_sha "${TPCDS_PGDATA}"; }
ENGINE_BIN_SHA_AT_START="$(running_engine_sha)"
ENGINE_ID_AT_START="$(engine_id)"

echo "# TPC-DS SF=1 Benchmark — $(date -Iseconds)"
echo "# goopg: $(git log --oneline -1)  PG: 18.3"
echo "# engine-id: ${ENGINE_ID_AT_START}"
echo "# engine-binary: running=${ENGINE_BIN_SHA_AT_START} on-disk=$(engine_bin_sha) (${GOOPG_BIN})"
if [[ "${ENGINE_BIN_SHA_AT_START}" != "$(engine_bin_sha)" ]]; then
    echo "# WARNING: the serving goopg predates the current build of ${GOOPG_BIN}."
    echo "#          Restart it (bench/tpcds/server.sh stop sf1 && … start sf1) before"
    echo "#          a chunk you intend to publish, or the chunk measures an unknown engine."
fi
echo "# timeout: ${TIMEOUT_SEC}s per query, engines: ${ENGINES}, PG skip: ${PG_SKIP}"
echo ""

# guard_host_quiet — refuse to sweep while the nightly CI batch owns the host.
#
# This harness had NO such guard until 2026-07-29 (M0124-0004). The nightly
# batch fires at 00:00 and its TPC-H stage keeps a capped goopg server on :65434
# alive for hours (measured: 112% CPU, 7.5 GiB RSS at 5 h). It collides with
# this harness on neither port nor data dir, so a sweep started in that window
# looked clean and produced timings that cannot be compared against a
# quiet-host baseline — the property the whole SF=1 sweep exists to establish.
#
# The M0124-0001 re-sweep itself was lucky, not careful: it ran 17:06–23:49 on
# 2026-07-28 and finished 34 min before the 00:23:44 fire. The guard makes that
# a checked precondition instead of a coincidence. FORCE=1 overrides, and the
# SF0.5 harness carries the mirror-image check.
guard_host_quiet() {
    [[ "${FORCE:-0}" == "1" ]] && return 0
    if bench_foreign_procs | grep -qE 'ci/batch/(run-nightly\.sh|stages/)'; then
        echo "FATAL: the nightly CI batch is running (ci/batch) — its TPC-H stage would" >&2
        echo "       contaminate every timing in this sweep. Wait for it, or FORCE=1." >&2
        exit 2
    fi
}
guard_host_quiet

# restart_goopg — bounce the goopg server to drop accumulated heap.
#
# Why this exists (2026-07-27): the bench server runs with GOGC=off and
# GOMEMLIMIT=12GiB, so the Go GC only fires as the heap nears the limit.
# A 600 s timeout query builds an enormous intermediate; once one or two
# of those have run, the heap sits at the limit and every SUBSEQUENT
# query in the same process thrashes GC. The effect is a "sweep-tail
# collapse" that looks exactly like a code regression:
#
#   Q6  70s -> TIMEOUT, Q7 67s -> TIMEOUT   (measured, immediately after
#   Q4 and Q5 both timed out at 600 s)
#
# but the same binary on a freshly started server returns Q6 in 62 s and
# Q7 in 64 s, matching both the baseline and PostgreSQL's row counts.
# Restarting after each goopg TIMEOUT keeps a heap bomb from poisoning
# the rest of the sweep. Set RESTART_AFTER_TIMEOUT=0 to disable.
restart_goopg() {
    [[ "${RESTART_AFTER_TIMEOUT:-1}" == "1" ]] || return 0
    echo "      (restarting goopg to drop accumulated heap)"
    "${REPO_ROOT}/bench/tpcds/server.sh" stop  "${SF_LANE}" >/dev/null 2>&1 || true
    "${REPO_ROOT}/bench/tpcds/server.sh" start "${SF_LANE}" >/dev/null 2>&1 || {
        echo "      WARNING: goopg restart failed — remaining results are suspect"
        return 1
    }
    # server.sh start rebuilds from the working tree, so this restart is the
    # one place a mid-sweep engine change can enter unnoticed. Say so loudly.
    local now; now="$(engine_id)"
    if [[ "${now}" != "${ENGINE_ID_AT_START}" ]]; then
        echo "      *** SWEEP VOID: engine source changed mid-sweep" \
             "(${ENGINE_ID_AT_START} -> ${now}) — re-run this chunk ***"
        ENGINE_ID_AT_START="${now}"
    else
        # Same source; the image sha still moves because of the VCS stamp.
        # Report it so the chunk records which image answered.
        echo "      (engine source unchanged; image now $(running_engine_sha))"
    fi
}

# reap_pg_orphans — kill PG backends left running by a client-side timeout.
#
# Ported from scripts/tpcds-sf05-regression.sh for M0124-0001 (design doc
# 0124-0001 D4): `timeout N psql` kills only the CLIENT; the PostgreSQL
# backend keeps executing the query and contaminates every later timing in
# the sweep. The SF0.5 harness codified this hazard; the SF=1 harness had no
# equivalent, so a PG-side TIMEOUT silently left a hot backend behind.
#
# Two properties are load-bearing and must not be "simplified":
#  * the victim set is MATERIALIZED — SQL does not guarantee evaluation
#    order, and a bare `WHERE … AND pg_terminate_backend(pid)` has already
#    killed a healthy backend in this programme (the Q6 incident);
#  * the predicate is `backend_type='client backend' AND state='active'`,
#    matching SF0.5 exactly. A looser `state <> 'idle'` would also match
#    `idle in transaction` — a silent widening of a statement that kills
#    backends.
reap_pg_orphans() {
    ${PG_PSQL} -t -A -c "
        with victims as materialized (
            select pid from pg_stat_activity
            where backend_type='client backend'
              and pid <> pg_backend_pid()
              and datname='${TPCDS_PG_DB}'
              and state='active'
              and now()-query_start > interval '${TIMEOUT_SEC} seconds'
        )
        select count(*) from victims where pg_terminate_backend(pid)" 2>/dev/null || true
}

for q in $(parse_qlist "${1:-}"); do
    # Fix (3): sequential, never concurrent — a concurrent peer
    # contaminates the elapsed time of both engines.
    for eng in ${ENGINES}; do
        case "$eng" in
        goopg)
            run_one "goopg" "${GOOPG_PSQL}" "${q}"
            # A goopg TIMEOUT means a 600 s query just built an enormous
            # intermediate; bounce the server before it poisons the rest.
            [[ "${LAST_STATUS}" == "TIMEOUT" ]] && restart_goopg
            ;;
        pg)
            if ! grep -qw "${q}" <<<"${PG_SKIP}"; then
                run_one "pg" "${PG_PSQL}" "${q}"
                # A PG TIMEOUT killed only psql; reap the orphaned backend
                # before it steals CPU from the next query (D4).
                if [[ "${LAST_STATUS}" == "TIMEOUT" ]]; then
                    echo "      (reaping orphaned PG backends: $(reap_pg_orphans | tr -d '[:space:]') terminated)"
                fi
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
