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
#   - Otherwise: stops any stale goopg on the bench data dir (via the goopg
#     control socket — NEVER pkill, which self-matches the invoking shell),
#     starts a fresh server under the memory-cap wrapper
#     (scripts/goopg-test-run.sh, scope goopg-spotcheck), waits for
#     readiness, runs Q12 + Q13 via cmd/tpch-runner, compares row counts
#     against bench/tpch/spotcheck_expected.env, stops the server.
#   - Exits 1 on any row-count mismatch or operational failure, 0 on PASS.
#
# Expected counts live in bench/tpch/spotcheck_expected.env
# (Q12_EXPECTED / Q13_EXPECTED). Q13 is load-dependent: re-pin it after
# every fresh build_schema_goopg.sh (see comments in that file).
#
# Tunables:
#   TPCH_SPOTCHECK_TIMEOUT        per-query budget for tpch-runner        (default 600s)
#   TPCH_SPOTCHECK_MIN_MB         data-dir size below which we SKIP       (default 100)
#   TPCH_SPOTCHECK_READY_TIMEOUT  seconds to wait for readiness           (default 120)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BENCH_DIR="${REPO_ROOT}/bench/tpch"

# Shared bench config: PGDATA, PG_HOST/PG_PORT (65433), GOOPG_BIN,
# GOMEMLIMIT/GOGC, and postgres/local_install/bin on PATH (pg_isready).
# shellcheck source=../bench/tpch/env_goopg.sh
source "${BENCH_DIR}/env_goopg.sh"

EXPECTED_FILE="${BENCH_DIR}/spotcheck_expected.env"
RUNNER_BIN="${REPO_ROOT}/tmp/tpch-spotcheck-runner"
SPOT_LOG="${RUNTIME_DIR}/goopg.spotcheck.log"
SPOT_PIDFILE="${RUNTIME_DIR}/spotcheck.pid"
CG_UNIT="goopg-spotcheck"
QUERY_TIMEOUT="${TPCH_SPOTCHECK_TIMEOUT:-600s}"
MIN_DATA_MB="${TPCH_SPOTCHECK_MIN_MB:-100}"

skip() {
    echo "=================================================================="
    echo "tpch-spotcheck: SKIPPED (no TPC-H data dir — see bench/tpch/README.md to set up)"
    echo "tpch-spotcheck: reason: $*"
    echo "=================================================================="
    exit 0
}

# ---------------------------------------------------------------------------
# Prerequisite checks — SKIP (exit 0), never hard-fail, when data is absent.
# ---------------------------------------------------------------------------
[[ -s "${PGDATA}/PG_VERSION" ]] || skip "no initialised cluster at ${PGDATA}"

data_mb="$(du -sm "${PGDATA}" 2>/dev/null | awk '{print $1}')"
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
# Stop any stale instance on this data dir. Use the goopg control socket
# (same mechanism as bench/tpch/stop_goopg.sh). NEVER `pkill -f goopg`:
# it self-matches the invoking shell (memory: goopg_manual_server_test_workflow).
# ---------------------------------------------------------------------------
if "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1; then
    echo "tpch-spotcheck: stopped stale goopg instance"
else
    rm -f "${PGDATA}/postmaster.pid"   # clean stale pidfile so start doesn't refuse
fi
# Also clear a lingering spotcheck scope from a previous crashed run.
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
rm -f "${SPOT_PIDFILE}"

# ---------------------------------------------------------------------------
# Start a FRESH server under the memory-cap wrapper (MANDATORY for any
# goopg server start) with a distinct cgroup scope name so it cannot
# collide with Ralph / manual test scopes.
# ---------------------------------------------------------------------------
echo "tpch-spotcheck: starting fresh goopg on ${PG_HOST}:${PG_PORT} (scope ${CG_UNIT}, log ${SPOT_LOG})"
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
echo "tpch-spotcheck: running Q12 + Q13 (per-query timeout ${QUERY_TIMEOUT})"
runner_out="$("${RUNNER_BIN}" \
    --host="${PG_HOST}" --port="${PG_PORT}" \
    --db="${GATE_DB}" --user="${GATE_USER}" --password="${GATE_PASS}" \
    --queries=12,13 --per-query-timeout="${QUERY_TIMEOUT}" 2>&1)" || {
        echo "tpch-spotcheck: FATAL — tpch-runner failed:" >&2
        echo "${runner_out}" >&2
        exit 1
    }
echo "${runner_out}"

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
