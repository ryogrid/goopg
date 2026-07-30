#!/bin/bash
# S2 — TPC-H solo stage (ci/design/05-tpch-stage.md).
#
# ISOLATION FROM THE RALPH LOOP'S tpch-spotcheck.sh (user requirement): the
# loop's spotcheck unconditionally `goopg stop`s whatever server runs on the
# canonical bench data dir (bench/tpch/runtime_goopg/data, port 65433), so the
# batch must NOT hold that dir/port for its multi-hour stage. Instead the
# stage takes a SNAPSHOT COPY of the data dir (~2.2 GB, tens of seconds) and
# runs its own server on the copy at port ${NIGHTLY_TPCH_PORT:-65434}. The
# only touchpoint with the loop is the copy window, which requires the
# canonical dir to be server-free (a spotcheck run is ~3-4 min; we wait up to
# NIGHTLY_PORT_WAIT and verify no server appeared mid-copy).
#
#   step 0  data precondition + snapshot copy (skip-with-note, exit 0)
#   step 1  fresh capped server on the copy + spotcheck tripwire (runner
#           Q12/Q13 vs spotcheck_expected.env); mismatch => fail(spotcheck)
#   step 2  EXPLAIN capture (+ pinned plan-diff, informational)
#   step 3  22-query sweep under NIGHTLY_TPCH_BUDGET (7200s), per-query cap
#           min(1200s, remaining-120s), canonical power order
# Connection identity mirrors scripts/tpch-spotcheck.sh (superuser@postgres
# fallback: goopg roles/DBs are in-memory only; tables live in `postgres`).
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

TPCH_DIR="${RUN_DIR}/tpch"
mkdir -p "${TPCH_DIR}/explain"

# Shared bench config: PGDATA (canonical), PG_HOST/PG_PORT(65433), GOOPG_BIN,
# superuser creds, tpch creds, local_install/bin on PATH.
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"
# env_goopg.sh begins with `set -euo pipefail`, and `set` in a sourced file
# leaks into this shell — undo the errexit it turned on (this stage
# deliberately handles failures itself so it can always record a status).
set +e
SRC_DATA="${PGDATA}"            # canonical dir — loop-owned; NEVER stop/start a server on it
SRC_PORT="${PG_PORT}"           # 65433 — loop's spotcheck lane
RUN_PORT="${NIGHTLY_TPCH_PORT:-65434}"   # batch-reserved
RUN_DATA="${REPO_ROOT}/tmp/goopg-nightly-tpch-data"

BUDGET="${NIGHTLY_TPCH_BUDGET:-7200}"
RESERVE=120
PER_Q_CAP=1200
CG_UNIT="goopg-nightly-tpch"
RUNNER_BIN="${REPO_ROOT}/tmp/tpch-nightly-runner"
SERVER_LOG="${TPCH_DIR}/server.log"
# Canonical power-test order (matches tpch_power_test_20260526.md).
SWEEP_ORDER=(14 2 9 20 6 17 18 8 21 13 3 22 16 4 11 15 1 10 19 5 7 12)
if [[ -n "${NIGHTLY_TPCH_QUERIES:-}" ]]; then
    IFS=',' read -ra SWEEP_ORDER <<< "${NIGHTLY_TPCH_QUERIES}"
fi

skip_stage() {
    progress "S2" "tpch SKIP: $*"
    stage_status tpch "skip($1)"
    exit 0
}

# --- step 0a: source-data preconditions ---------------------------------------
[[ -s "${SRC_DATA}/PG_VERSION" ]] || skip_stage no-data "no initialised cluster at ${SRC_DATA}"
data_mb="$(du -sm "${SRC_DATA}" 2>/dev/null | awk '{print $1}')"
[[ -n "${data_mb}" && "${data_mb}" -ge 100 ]] || skip_stage no-data "data dir only ${data_mb:-0} MB"

EXPECTED_FILE="${REPO_ROOT}/bench/tpch/spotcheck_expected.env"
# shellcheck source=/dev/null
source "${EXPECTED_FILE}"
: "${Q12_EXPECTED:?}" "${Q13_EXPECTED:?}"

if port_busy 127.0.0.1 "${RUN_PORT}"; then
    skip_stage port-busy "batch-reserved port ${RUN_PORT} unexpectedly held — never killing the holder"
fi

# --- step 0b: snapshot copy of the canonical data dir --------------------------
# The copy is only consistent while NO server runs on the source. A loop
# spotcheck may start one at any moment, so: wait for 65433 free, copy, then
# verify it stayed free — retry a few times on interference.
copy_ok=0
for attempt in 1 2 3; do
    progress "S2" "tpch: waiting for canonical ${SRC_PORT} to be server-free for the snapshot copy (attempt ${attempt}/3)"
    if ! wait_port_free "${PG_HOST}" "${SRC_PORT}"; then
        skip_stage port-busy "canonical port ${SRC_PORT} busy for ${NIGHTLY_PORT_WAIT:-900}s — no consistent copy window"
    fi
    progress "S2" "tpch: copying ${SRC_DATA} -> ${RUN_DATA} (${data_mb} MB)"
    rm -rf "${RUN_DATA}"
    if ! cp -a "${SRC_DATA}" "${RUN_DATA}"; then
        rm -rf "${RUN_DATA}"
        skip_stage copy-failed "cp -a failed (disk space?)"
    fi
    if ! port_busy 127.0.0.1 "${SRC_PORT}"; then
        copy_ok=1
        break
    fi
    progress "S2" "tpch: a server appeared on ${SRC_PORT} mid-copy — copy may be inconsistent; retrying"
done
[[ ${copy_ok} -eq 1 ]] || skip_stage port-busy "could not get an interference-free copy in 3 attempts"
rm -f "${RUN_DATA}/postmaster.pid"   # stale pidfile from the copy would block start

# --- build server + runner ------------------------------------------------------
progress "S2" "tpch: building goopg + tpch-runner"
mkdir -p "$(dirname "${GOOPG_BIN}")"
( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || { stage_status tpch "fail(build)"; exit 1; }
( cd "${REPO_ROOT}" && go build -o "${RUNNER_BIN}" ./cmd/tpch-runner ) || { stage_status tpch "fail(build)"; exit 1; }

stop_scope "${CG_UNIT}"   # clear a lingering scope from a crashed previous run

PPROF_ADDR="${NIGHTLY_TPCH_PPROF_ADDR:-127.0.0.1:6160}"
progress "S2" "tpch: fresh server start on ${PG_HOST}:${RUN_PORT} (clone dir, unit=${CG_UNIT} high=10G max=12G pprof=${PPROF_ADDR})"
# 8>&- : the detached server must NOT inherit the orchestrator's run-lock fd —
# an orphaned server would otherwise pin the run lock and every later firing
# would exit 5 (ci/design/06 §C).
# GOOPG_PPROF_ADDR: a private pprof port. The default 127.0.0.1:6060 is taken by
# whichever goopg started first on this host, and losing that race is logged only
# at Debug — which is why the 2026-07-29 wedge left no goroutine dump behind.
GOOPG_CG_UNIT="${CG_UNIT}" GOOPG_MEM_HIGH=10G GOOPG_MEM_MAX=12G \
GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=9GiB GOOPG_PPROF_ADDR="${PPROF_ADDR}" \
    "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${RUN_DATA}" \
    --listen "${PG_HOST}:${RUN_PORT}" \
    --hba "${RUN_DATA}/pg_hba.conf" \
    > "${SERVER_LOG}" 2>&1 8>&- &
server_pid=$!

cleanup() {
    # Bounded ladder, never a bare `wait` — a leaked backend makes the graceful
    # stop block forever and would wedge this stage, the run, and the host
    # (2026-07-29: 6h45m). See stop_goopg_server in lib/common.sh.
    STOP_PPROF_ADDR="${PPROF_ADDR}"
    STOP_DUMP_FILE="${TPCH_DIR}/server-goroutines.txt"
    stop_goopg_server "${GOOPG_BIN}" "${RUN_DATA}" "${server_pid}"
    case "${STOP_RUNG}" in
        graceful|already-exited) ;;
        *) progress "S2" "tpch: server would NOT stop gracefully — escalated to ${STOP_RUNG} (leaked backend? see tpch/server.log)" ;;
    esac
    stop_scope "${CG_UNIT}"
    rm -rf "${RUN_DATA}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM   # route signals through the EXIT trap (cleanup)

ready=0
for _ in $(seq 1 120); do
    kill -0 "${server_pid}" 2>/dev/null || break
    if pg_isready -h "${PG_HOST}" -p "${RUN_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        ready=1; break
    fi
    sleep 1
done
if [[ ${ready} -ne 1 ]]; then
    progress "S2" "tpch FAIL: server not ready in 120s (see tpch/server.log)"
    tail -n 20 "${SERVER_LOG}" || true
    stage_status tpch "fail(startup)"
    exit 1
fi

# --- connection-target resolution (spotcheck-script fallback) --------------------
GATE_DB="${TPCH_DB}"; GATE_USER="${TPCH_USER}"; GATE_PASS="${TPCH_PASS}"
probe_lineitem() {
    PGDATABASE="$1" PGUSER="$2" PGPASSWORD="$3" \
        psql -h "${PG_HOST}" -p "${RUN_PORT}" -tA -c 'select 1 from lineitem limit 1' 2>&1 >/dev/null
}
if ! probe_err="$(probe_lineitem "${GATE_DB}" "${GATE_USER}" "${GATE_PASS}")"; then
    if grep -qiE '(role|database).*does not exist' <<<"${probe_err}"; then
        progress "S2" "tpch: tpch role/db absent post-restart (in-memory only); falling back to ${PG_SUPERUSER}@postgres"
        GATE_DB="postgres"; GATE_USER="${PG_SUPERUSER}"; GATE_PASS="${PG_SUPERUSER_PASS}"
        if ! probe_err="$(probe_lineitem "${GATE_DB}" "${GATE_USER}" "${GATE_PASS}")"; then
            grep -qiE 'does not exist' <<<"${probe_err}" && skip_stage no-data "lineitem not loaded (${probe_err})"
            progress "S2" "tpch FAIL: schema probe failed: ${probe_err}"
            stage_status tpch "fail(probe)"; exit 1
        fi
    elif grep -qiE 'does not exist' <<<"${probe_err}"; then
        skip_stage no-data "tpch schema not loaded (${probe_err})"
    else
        progress "S2" "tpch FAIL: schema probe failed: ${probe_err}"
        stage_status tpch "fail(probe)"; exit 1
    fi
fi
progress "S2" "tpch: data target = ${GATE_USER}@${GATE_DB} on :${RUN_PORT}"

run_runner() {  # run_runner <queries> <per-query-timeout> <outer-clamp-s> [extra-flags...]
    # The outer `timeout` is the absolute clamp (doc 05 §D): a runner that
    # hangs past its own per-query budget is INT'd, then KILLed 30s later.
    local q="$1" to="$2" clamp="$3"; shift 3
    timeout -k 30 --signal=INT "${clamp}" \
        "${RUNNER_BIN}" -host "${PG_HOST}" -port "${RUN_PORT}" \
        -db "${GATE_DB}" -user "${GATE_USER}" -password "${GATE_PASS}" \
        -queries "${q}" -per-query-timeout "${to}" "$@" 2>&1
}

# --- step 1: spotcheck tripwire (Q12/Q13 against THIS server) ---------------------
progress "S2" "tpch: spotcheck Q12/Q13 (expected ${Q12_EXPECTED}/${Q13_EXPECTED})"
spot_out="$(run_runner "12,13" 600s $(( 2 * 600 + 120 )) || true)"
printf '%s\n' "${spot_out}" > "${TPCH_DIR}/spotcheck.log"
q12=$(sed -n 's/^Q12: OK .*rows=\([0-9]\+\)$/\1/p' <<<"${spot_out}")
q13=$(sed -n 's/^Q13: OK .*rows=\([0-9]\+\)$/\1/p' <<<"${spot_out}")
spot_ok=1
[[ "${q12}" == "${Q12_EXPECTED}" ]] || spot_ok=0
[[ "${q13}" == "${Q13_EXPECTED}" ]] || spot_ok=0
if [[ ${spot_ok} -eq 1 ]]; then
    echo "SPOTCHECK: PASS q12=${q12} q13=${q13}" >> "${TPCH_DIR}/spotcheck.log"
    progress "S2" "tpch: spotcheck PASS (q12=${q12} q13=${q13})"
else
    echo "SPOTCHECK: FAIL q12=${q12:-ERR} q13=${q13:-ERR} expected=${Q12_EXPECTED}/${Q13_EXPECTED}" >> "${TPCH_DIR}/spotcheck.log"
    progress "S2" "tpch: spotcheck FAIL (q12=${q12:-ERR} q13=${q13:-ERR}, expected ${Q12_EXPECTED}/${Q13_EXPECTED}) — sweep will be SKIPPED"
fi

# --- step 2: EXPLAIN capture (before the sweep — forensics survive a dead q1) -----
sweep_csv=$(IFS=','; echo "${SWEEP_ORDER[*]}")
progress "S2" "tpch: EXPLAIN capture (${sweep_csv})"
run_runner "${sweep_csv}" 60s $(( ${#SWEEP_ORDER[@]} * 60 + 120 )) -explain > "${TPCH_DIR}/explain-run.log" 2>&1 || true
# Split per query: "QN...: EXPLAIN plan:" opens a block; the trailing
# "QN...: OK elapsed=..." line closes it. Q15a/Q15b both land in q15.txt.
current=""
while IFS= read -r line; do
    case "${line}" in
        Q*": EXPLAIN plan:")
            label="${line%%:*}"
            num="$(sed -E 's/^Q([0-9]+).*/\1/' <<<"${label}")"
            current="$(printf '%s/explain/q%02d.txt' "${TPCH_DIR}" "${num}")"
            printf '== %s ==\n' "${label}" >> "${current}"
            ;;
        Q*": OK elapsed="*|Q*": ERROR "*)
            current=""
            ;;
        *)
            [[ -n "${current}" ]] && printf '%s\n' "${line}" >> "${current}"
            ;;
    esac
done < "${TPCH_DIR}/explain-run.log"

# Pinned plan diff (informational; ls -t mtime-tie gotcha — ci/design/05 §C).
# PLAN_PORT points at OUR clone server; PLAN_DB/USER/PASS must be the resolved
# gate target — the Makefile defaults (tpch/tpch) don't survive the fresh
# restart (that's why the probe above fell back to superuser@postgres).
make -C "${REPO_ROOT}" plan-diff LABEL=m0077-final MODE=structural \
    PLAN_PORT="${RUN_PORT}" PLAN_DB="${GATE_DB}" PLAN_USER="${GATE_USER}" PLAN_PASS="${GATE_PASS}" \
    > "${TPCH_DIR}/plan-gate.log" 2>&1 || true

if [[ ${spot_ok} -ne 1 ]]; then
    stage_status tpch "fail(spotcheck)"
    exit 1
fi

# --- step 3: budget-bounded sweep --------------------------------------------------
TIMINGS="${TPCH_DIR}/timings.csv"
echo "query,elapsed_s,rows,status" > "${TIMINGS}"
: > "${TPCH_DIR}/run.log"
sweep_t0=${SECONDS}
sweep_failed=0
i=0
for qn in "${SWEEP_ORDER[@]}"; do
    i=$(( i + 1 ))
    elapsed_sweep=$(( SECONDS - sweep_t0 ))
    remaining=$(( BUDGET - elapsed_sweep ))
    if (( remaining <= RESERVE )); then
        progress "S2" "tpch: BUDGET EXHAUSTED (${elapsed_sweep}s) — Q${qn} and later marked not-run"
        for rest in "${SWEEP_ORDER[@]:$(( i - 1 ))}"; do
            echo "Q${rest},,,not-run(budget)" >> "${TIMINGS}"
        done
        sweep_failed=1
        break
    fi
    per_q=$(( remaining - RESERVE ))
    (( per_q > PER_Q_CAP )) && per_q=${PER_Q_CAP}

    q_t0=${SECONDS}
    out="$(run_runner "${qn}" "${per_q}s" $(( per_q + 60 )) || true)"
    q_elapsed=$(( SECONDS - q_t0 ))
    printf '%s\n' "${out}" >> "${TPCH_DIR}/run.log"

    # Q15 reports as Q15-CREATEVIEW / Q15a-VIEWBODY / Q15b-MAIN — the MAIN
    # select is the query's result of record.
    label="Q${qn}"; [[ "${qn}" == "15" ]] && label="Q15b-MAIN"
    ok_line="$(grep -E "^${label}: OK elapsed=" <<<"${out}" | tail -1 || true)"
    if [[ -n "${ok_line}" ]]; then
        rows="$(sed -n 's/.*rows=\([0-9]\+\)$/\1/p' <<<"${ok_line}")"
        secs="$(sed -n 's/.*elapsed=\([0-9.]\+\)s.*/\1/p' <<<"${ok_line}")"
        echo "Q${qn},${secs},${rows},ok" >> "${TIMINGS}"
        progress "S2" "tpch q${qn} ok ${secs}s rows=${rows} (budget left $(( BUDGET - (SECONDS - sweep_t0) ))s)"
    else
        status="error"
        if grep -qiE '57014|canceling statement|context deadline' <<<"${out}" || (( q_elapsed >= per_q )); then
            status="timeout"
        fi
        echo "Q${qn},${q_elapsed},,${status}" >> "${TIMINGS}"
        progress "S2" "tpch q${qn} ${status^^} after ${q_elapsed}s (per-query cap ${per_q}s)"
        sweep_failed=1
    fi
done

total=$(( SECONDS - sweep_t0 ))
progress "S2" "tpch: sweep done in ${total}s (baseline 1469s)"
if [[ ${sweep_failed} -eq 0 ]]; then
    stage_status tpch "pass"
    exit 0
fi
stage_status tpch "fail(sweep)"
exit 1
