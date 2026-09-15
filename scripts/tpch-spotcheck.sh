#!/usr/bin/env bash
#
# tpch-spotcheck.sh — one-command pre-commit gate: TPC-H Q12/Q13 row-count
# spot-check against goopg, from a FRESH server start.
#
# Why: silent row-count regressions from executor/planner changes are this
# project's most expensive failure mode (m0071 Stage-B et al.). The mandated
# gate is "fresh server restart + Q12/Q13 canonical row-count check"; this
# script makes it a single command so loops cannot skip it.
#
# Usage:
#   scripts/tpch-spotcheck.sh
#
# Behaviour:
#   - Exits 0 with a loud SKIPPED message when no populated TPC-H data dir
#     exists (must not hard-block loops on machines without data).
#   - Otherwise (M0137-0007, online path added in the M0139 follow-up):
#     takes a snapshot clone of the shared bench cluster
#     (bench/tpch/runtime_goopg/data, :65433) into a PRIVATE data dir — via
#     `pg_basebackup -X fetch` when that cluster is RUNNING (the normal
#     case: it is a persistent cluster), via `cp -a` when it is at rest.
#     This gate never stops/starts a server on the shared cluster itself and
#     never requires it to be down, so it can neither kill nor be blocked by
#     a capture or another gate that is using it. Stops any stale
#     goopg left on ITS OWN private clone from a previous crashed run (via
#     the goopg control socket — NEVER pkill, which self-matches the
#     invoking shell), starts a fresh server on the clone under the
#     memory-cap wrapper (scripts/goopg-test-run.sh, scope goopg-spotcheck,
#     private port — see scripts/lib/tpch-private-clone.sh), waits for
#     readiness, runs Q12 + Q13 via cmd/tpch-runner, compares row counts
#     against bench/tpch/spotcheck_expected.env, stops the server.
#   - Exits 1 on any row-count mismatch or operational failure, 0 on PASS.
#
# Cost reporting (added by M0125-0005, the GOOPG_RELSIZE_FALLBACK default
# flip): the gate is not only a correctness check — every future commit pays
# its wall clock, and it runs S-cold, which is exactly the state the relation
# -size fallback changes. So the run also prints the planner-flag state it
# started the server with, the query-phase wall clock, and the peak memory of
# the capped scope (cgroup v2 memory.peak — the whole server tree, not one
# RSS sample). Those three lines are what makes a "did the default flip make
# the mandatory gate worse?" question answerable from an artefact instead of
# a re-run. See docs/design/0125-0005-relsize-fallback-default-flip.md.
#
# Expected counts live in bench/tpch/spotcheck_expected.env
# (Q12_EXPECTED / Q13_EXPECTED). Q13 is load-dependent: re-pin it after
# every fresh build_schema_goopg.sh (see comments in that file).
#
# Tunables:
#   TPCH_SPOTCHECK_TIMEOUT        per-query budget for tpch-runner        (default 600s)
#   TPCH_SPOTCHECK_MIN_MB         data-dir size below which we SKIP       (default 100)
#   TPCH_SPOTCHECK_READY_TIMEOUT  seconds to wait for readiness           (default 120)
#   TPCH_SPOTCHECK_PORT           this lane's PRIVATE port (M0137-0007)   (default 5580)
#   TPCH_SPOTCHECK_CLONE_WAIT     seconds to wait for :65433 to go quiet in
#                                 the `cp -a` fallback path only — the
#                                 default online pg_basebackup path never
#                                 waits for it                            (default 60)
#   TPCH_CLONE_MODE               auto|online|copy — see
#                                 scripts/lib/tpch-private-clone.sh       (default auto)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BENCH_DIR="${REPO_ROOT}/bench/tpch"

# M0137-0007: this gate no longer runs against the shared bench cluster
# (bench/tpch/runtime_goopg/data, :65433) — it takes a private snapshot clone
# and a private port instead, so it can never fight another lane (a
# concurrent M0137 baseline capture, one of the tpch-*-arm.sh scripts, or a
# peer Ralph loop) for the shared server. See scripts/lib/tpch-private-clone.sh
# for why, and ci/design/03-resources-and-parallelism.md §D /
# ci/design/05-tpch-stage.md for the analogous fix ci/batch already shipped
# for the nightly lane (this gate no longer needs that doc's "canonical 65433
# stays the loop's spotcheck lane" carve-out — it never touches 65433 now).
# Capture the caller's own GOOPG_BIN choice (if any) BEFORE env_goopg.sh's
# `${GOOPG_BIN:-default}` resolves it to the SHARED tmp/goopg-bench-bin —
# that shared path is itself a contention surface (env_goopg.sh's own
# comment: a spotcheck build used to clobber the nightly's binary mid-run).
CALLER_GOOPG_BIN="${GOOPG_BIN:-}"

# Shared bench config: PGDATA, PG_HOST/PG_PORT (65433), GOOPG_BIN,
# GOMEMLIMIT/GOGC, and postgres/local_install/bin on PATH (pg_isready).
# shellcheck source=../bench/tpch/env_goopg.sh
source "${BENCH_DIR}/env_goopg.sh"
# shellcheck source=lib/tpch-private-clone.sh
source "${SCRIPT_DIR}/lib/tpch-private-clone.sh"

SRC_DATA="${PGDATA}"   # canonical dir — lane-external; NEVER stop/start a server on it
SRC_PORT="${PG_PORT}"  # 65433
[[ -n "${CALLER_GOOPG_BIN}" ]] || GOOPG_BIN="${REPO_ROOT}/tmp/goopg-spotcheck-bin"
PGDATA="${REPO_ROOT}/tmp/goopg-spotcheck-tpch-data"       # this lane's private clone
PG_PORT="${TPCH_SPOTCHECK_PORT:-5580}"                     # this lane's private port

EXPECTED_FILE="${BENCH_DIR}/spotcheck_expected.env"
RUNNER_BIN="${REPO_ROOT}/tmp/tpch-spotcheck-runner"
SPOT_LOG="${REPO_ROOT}/tmp/goopg-spotcheck-tpch.log"
SPOT_PIDFILE="${REPO_ROOT}/tmp/goopg-spotcheck-tpch.pid"
CG_UNIT="goopg-spotcheck"
QUERY_TIMEOUT="${TPCH_SPOTCHECK_TIMEOUT:-600s}"
MIN_DATA_MB="${TPCH_SPOTCHECK_MIN_MB:-100}"
CLONE_WAIT="${TPCH_SPOTCHECK_CLONE_WAIT:-60}"

skip() {
    echo "=================================================================="
    echo "tpch-spotcheck: SKIPPED (no TPC-H data dir — see bench/tpch/README.md to set up)"
    echo "tpch-spotcheck: reason: $*"
    echo "=================================================================="
    exit 0
}

# ---------------------------------------------------------------------------
# Prerequisite checks — SKIP (exit 0), never hard-fail, when data is absent.
# Checked against the SOURCE dir: the private clone does not exist yet.
# ---------------------------------------------------------------------------
[[ -s "${SRC_DATA}/PG_VERSION" ]] || skip "no initialised cluster at ${SRC_DATA}"

data_mb="$(du -sm "${SRC_DATA}" 2>/dev/null | awk '{print $1}')"
if [[ -z "${data_mb}" ]] || (( data_mb < MIN_DATA_MB )); then
    skip "data dir is only ${data_mb:-0} MB (< ${MIN_DATA_MB} MB) — TPC-H tables not loaded; run bench/tpch/setup_goopg.sh + build_schema_goopg.sh"
fi

if [[ ! -f "${EXPECTED_FILE}" ]]; then
    echo "tpch-spotcheck: FATAL — missing ${EXPECTED_FILE} (it is committed to the repo; restore it)" >&2
    exit 1
fi
# shellcheck source=../bench/tpch/spotcheck_expected.env
source "${EXPECTED_FILE}"
: "${Q12_EXPECTED:?Q12_EXPECTED not set in ${EXPECTED_FILE}}"
: "${Q13_EXPECTED:?Q13_EXPECTED not set in ${EXPECTED_FILE}}"

# ---------------------------------------------------------------------------
# Build binaries (cached rebuilds are cheap; guarantees we test HEAD).
# ---------------------------------------------------------------------------
echo "tpch-spotcheck: building goopg + tpch-runner"
mkdir -p "$(dirname "${GOOPG_BIN}")"
( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg )
( cd "${REPO_ROOT}" && go build -o "${RUNNER_BIN}" ./cmd/tpch-runner )

# ---------------------------------------------------------------------------
# Stop any stale instance left on THIS LANE's private clone (a previous
# spotcheck run that crashed before its own cleanup ran). Use the goopg
# control socket (same mechanism as bench/tpch/stop_goopg.sh). NEVER
# `pkill -f goopg`: it self-matches the invoking shell (memory:
# goopg_manual_server_test_workflow). This never touches SRC_DATA/SRC_PORT.
# ---------------------------------------------------------------------------
if "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1; then
    echo "tpch-spotcheck: stopped stale goopg instance on the private clone"
else
    rm -f "${PGDATA}/postmaster.pid"   # clean stale pidfile so start doesn't refuse
fi
# Also clear a lingering spotcheck scope from a previous crashed run.
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
rm -f "${SPOT_PIDFILE}"

# ---------------------------------------------------------------------------
# Snapshot-clone the shared, lane-external cluster into this lane's private
# data dir (M0137-0007; online path added in the M0139 follow-up). When a
# server is live on SRC_PORT this is `pg_basebackup -X fetch` against it —
# consistent by PG's own online-backup contract (forced checkpoint + the WAL
# back to its redo point + a pg_control naming it), and needing NO stop and
# NO wait. When SRC_PORT answers nothing it is the cheaper `cp -a`, guarded
# by a re-check that nothing started mid-copy. See
# scripts/lib/tpch-private-clone.sh. This is the only touchpoint with the
# shared cluster in this whole script, and it never stops/starts anything
# there.
# ---------------------------------------------------------------------------
echo "tpch-spotcheck: snapshot-cloning ${SRC_DATA} -> ${PGDATA} (${data_mb} MB)"
if ! tpch_private_clone_snapshot "${SRC_DATA}" "${PGDATA}" "${PG_HOST}" "${SRC_PORT}" "${CLONE_WAIT}"; then
    echo "tpch-spotcheck: FATAL — could not snapshot the shared TPC-H cluster (see above); do NOT commit, retry the gate" >&2
    echo "tpch-spotcheck: hint — the clone no longer needs :65433 to be DOWN; if the online pg_basebackup path failed, check that pg_basebackup is on PATH and that :65433 accepts a replication connection" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Start a FRESH server under the memory-cap wrapper (MANDATORY for any
# goopg server start) with a distinct cgroup scope name so it cannot
# collide with Ralph / manual test scopes.
# ---------------------------------------------------------------------------
echo "tpch-spotcheck: starting fresh goopg on ${PG_HOST}:${PG_PORT} (scope ${CG_UNIT}, log ${SPOT_LOG})"
# Record the planner-flag state IN the artefact. A timing/RSS number whose arm
# is only known from the shell that produced it is not reproducible evidence —
# the same omission was repaired in scripts/tpcds-sf025-regression.sh (M0125-0011).
#
# M0127-P5.9-q (2026-08-06): the flag list and its unset-labels now come from
# scripts/planner-flags.sh, whose table is GENERATED from the Go defaults. This
# line used to hedge with "unset(build default)" for GOOPG_RELSIZE_FALLBACK —
# honest, but not actually the default, so it could not be diffed — while
# naming GOOPG_COST_DRIVEN_JOINORDER, which no code reads any more, and not
# naming GOOPG_PGSHAPED_DP, which since M0127-P5.9 selects the ENUMERATOR. A
# spot-check timing whose artefact cannot say which enumerator produced it is
# the failure this line exists to prevent, and this gate is the one every
# planner commit pays.
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/planner-flags.sh"
echo "tpch-spotcheck: planner-flags: $(planner_flags_body) GOMEMLIMIT=${GOMEMLIMIT:-unset} GOGC=${GOGC:-unset}"
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${PGDATA}" \
    --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" \
    >"${SPOT_LOG}" 2>&1 &
server_pid=$!
echo "${server_pid}" >"${SPOT_PIDFILE}"

cleanup() {
    # Stop via control socket (blocks until process exit), then reap.
    "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    rm -f "${SPOT_PIDFILE}"
}
trap cleanup EXIT

ready=0
for _ in $(seq 1 "${TPCH_SPOTCHECK_READY_TIMEOUT:-120}"); do
    if ! kill -0 "${server_pid}" 2>/dev/null; then
        break   # server process died — fall through to the error below
    fi
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 1
done
if [[ "${ready}" -ne 1 ]]; then
    echo "tpch-spotcheck: FATAL — goopg did not become ready; tail of ${SPOT_LOG}:" >&2
    tail -n 20 "${SPOT_LOG}" >&2 || true
    exit 1
fi

# Quick schema probe: a started cluster without the tpch schema is "no data",
# not a regression — SKIP rather than fail.
#
# Target resolution (goopg-specific): goopg persists user-created ROLEs and
# DATABASEs only in memory (internal/server/role_ddl.go; CREATE DATABASE is not
# durably replayed for the bench dir), so the `tpch` role and `tpch` database
# created during the HammerDB load DO NOT survive the fresh restart this script
# performs. The loaded tables themselves persist — HammerDB writes them into the
# `postgres` database (the only database goopg keeps across restart). So we probe
# the configured tpch target first (forward-compatible if role/db persistence
# ever lands), and on a "role/database does not exist" error fall back to the
# superuser + `postgres` database where the data actually lives. A genuine
# "relation lineitem does not exist" is the real not-loaded case → SKIP.
GATE_DB="${TPCH_DB}"; GATE_USER="${TPCH_USER}"; GATE_PASS="${TPCH_PASS}"
probe_lineitem() {  # args: db user pass — prints psql stderr on failure, returns rc
    PGDATABASE="$1" PGUSER="$2" PGPASSWORD="$3" \
        psql -h "${PG_HOST}" -p "${PG_PORT}" -tA -c 'select 1 from lineitem limit 1' 2>&1 >/dev/null
}
if ! probe_err="$(probe_lineitem "${GATE_DB}" "${GATE_USER}" "${GATE_PASS}")"; then
    if grep -qiE '(role|database).*does not exist' <<<"${probe_err}"; then
        # tpch role/db didn't survive the restart — fall back to the persistent
        # superuser/postgres target before deciding "no data".
        echo "tpch-spotcheck: tpch role/db absent post-restart (in-memory only); falling back to ${PG_SUPERUSER}@postgres"
        GATE_DB="postgres"; GATE_USER="${PG_SUPERUSER}"; GATE_PASS="${PG_SUPERUSER_PASS}"
        if ! probe_err="$(probe_lineitem "${GATE_DB}" "${GATE_USER}" "${GATE_PASS}")"; then
            if grep -qiE 'does not exist' <<<"${probe_err}"; then
                skip "cluster is up but lineitem is not loaded in any persistent database (${probe_err})"
            fi
            echo "tpch-spotcheck: FATAL — schema probe failed: ${probe_err}" >&2
            exit 1
        fi
    elif grep -qiE 'does not exist' <<<"${probe_err}"; then
        skip "cluster is up but the tpch schema is not loaded (${probe_err})"
    else
        echo "tpch-spotcheck: FATAL — schema probe failed: ${probe_err}" >&2
        exit 1
    fi
fi
echo "tpch-spotcheck: data target = ${GATE_USER}@${GATE_DB}"

# ---------------------------------------------------------------------------
# Run Q12 + Q13 via the canonical runner (same HammerDB SQL text as the
# power test: internal/testutil/tpch.Queries()).
# ---------------------------------------------------------------------------

# Peak memory of the capped scope, in bytes. cgroup v2 `memory.peak` is a
# high-water mark maintained by the kernel over the scope's whole lifetime,
# so it needs no sampling loop and cannot miss a spike between polls — the
# way a /proc/<pid>/status VmHWM read would if the server had already been
# stopped. Prints nothing when the run is UNCAPPED (no systemd delegation).
scope_mem_peak_mb() {
    local cg peak
    cg="$(systemctl --user show -p ControlGroup --value "${CG_UNIT}.scope" 2>/dev/null || true)"
    [[ -n "${cg}" ]] || return 1
    peak="$(cat "/sys/fs/cgroup${cg}/memory.peak" 2>/dev/null || true)"
    [[ "${peak}" =~ ^[0-9]+$ ]] || return 1
    awk -v b="${peak}" 'BEGIN { printf "%.0f", b / 1048576 }'
}
report_cost() {  # args: phase-wall-clock-seconds
    local peak_mb
    echo "tpch-spotcheck: cost: query-phase wall clock ${1}s"
    if peak_mb="$(scope_mem_peak_mb)"; then
        echo "tpch-spotcheck: cost: peak scope memory ${peak_mb} MB (cgroup memory.peak, scope ${CG_UNIT})"
    else
        echo "tpch-spotcheck: cost: peak scope memory UNAVAILABLE (uncapped run or no cgroup v2 memory.peak)"
    fi
}

echo "tpch-spotcheck: running Q12 + Q13 (per-query timeout ${QUERY_TIMEOUT})"
phase_start_ms="$(date +%s%3N)"
runner_rc=0
runner_out="$("${RUNNER_BIN}" \
    --host="${PG_HOST}" --port="${PG_PORT}" \
    --db="${GATE_DB}" --user="${GATE_USER}" --password="${GATE_PASS}" \
    --queries=12,13 --per-query-timeout="${QUERY_TIMEOUT}" 2>&1)" || runner_rc=$?
phase_secs="$(awk -v a="${phase_start_ms}" -v b="$(date +%s%3N)" 'BEGIN { printf "%.1f", (b - a) / 1000 }')"
if (( runner_rc != 0 )); then
    echo "tpch-spotcheck: FATAL — tpch-runner failed:" >&2
    echo "${runner_out}" >&2
    report_cost "${phase_secs}" >&2
    exit 1
fi
echo "${runner_out}"
report_cost "${phase_secs}"

# Lines look like: "Q12: OK elapsed=78.93s rows=2" / "Q12: ERROR after ..."
extract_rows() {
    local label="$1"
    sed -n "s/^${label}: OK .*rows=\([0-9]\+\)$/\1/p" <<<"${runner_out}"
}

q12_actual="$(extract_rows Q12)"
q13_actual="$(extract_rows Q13)"

fail=0
check() {
    local label="$1" actual="$2" expected="$3"
    if [[ -z "${actual}" ]]; then
        echo "tpch-spotcheck: ${label} FAIL — query errored or row count unparsable (expected ${expected} rows)"
        fail=1
    elif [[ "${actual}" == "${expected}" ]]; then
        echo "tpch-spotcheck: ${label} PASS — rows=${actual} (expected ${expected})"
    else
        echo "tpch-spotcheck: ${label} FAIL — rows=${actual}, expected ${expected}"
        fail=1
    fi
}
check Q12 "${q12_actual}" "${Q12_EXPECTED}"
check Q13 "${q13_actual}" "${Q13_EXPECTED}"

if [[ "${fail}" -ne 0 ]]; then
    echo "tpch-spotcheck: RESULT=FAIL — silent row-count regression signature; do NOT commit (revert the change)."
    echo "tpch-spotcheck: note: if you just reloaded TPC-H data, Q13 may have legitimately shifted; see ${EXPECTED_FILE}."
    exit 1
fi
echo "tpch-spotcheck: RESULT=PASS"
exit 0
