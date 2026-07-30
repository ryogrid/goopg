#!/usr/bin/env bash
#
# tpch-relsize-arm.sh — one arm of M0125-0003's four-arm TPC-H measurement,
# run through the per-query-isolated harness design §D7 mandates.
#
#   arm | ANALYZE | GOOPG_RELSIZE_FALLBACK | what it measures
#   ----+---------+-----------------------+---------------------------------
#   c1  | no      | 0                     | pre-M0125-0003 S-cold: RowCount=0, DP seed 1
#   c2  | no      | 2                     | the change — the only interesting cell
#   w1  | yes     | 0                     | control; the regime every published number is in
#   w2  | yes     | 2                     | must equal w1 exactly — design §D3's invariant
#
# The flag values are set EXPLICITLY, never left unset (M0125-0031, 2026-07-30).
# Until M0125-0005 (`d4071df4`) an unset GOOPG_RELSIZE_FALLBACK meant stage 0, so
# the 2026-07-30 c1 arm's "(unset)" and an explicit "0" are the same measurement;
# after the flip an unset variable means stage 2, which would have silently turned
# c1 into a second c2 and w1 into a second w2. Explicit values keep every arm
# well-defined across future default changes — the arm is the independent variable, not
# the default.
#
# WHY NOT the ordinary power test (bench/tpch/run_power_test_goopg.sh):
#
#  1. It drives HammerDB, which runs all 22 queries in ONE long-lived session
#     against ONE long-lived server. Round-5 §6 measured a mis-ordered star
#     query that did NOT honour cancellation, with the server pinned at ~10 GB
#     RSS; in a shared session that wedges the host instead of reporting a
#     regression. Arm c2 is exactly the arm expected to produce such plans, so
#     the isolation is not optional here.
#  2. Server age is a confound of its own (CLAUDE.md "sweep-tail collapse"): a
#     server that just ran a timeout query sits at GOMEMLIMIT with GOGC=off and
#     thrashes GC, which mimics a regression in every later query. A restart per
#     query holds server age constant across queries AND across arms.
#  3. GOOPG_RELSIZE_FALLBACK is read once, in the planner package's init() from
#     the SERVER's environment (internal/planner/relsize.go:40). The arm can
#     therefore only be selected at server start — there is no GUC to flip
#     mid-session, so per-query restart is what makes the arm well-defined.
#
# The ONE deliberate divergence from round-5 §8's protocol (design §D7): its
# "full server restart + re-ANALYZE between queries" keeps the re-ANALYZE, and
# for the c1/c2 arms we must DROP it. ANALYZE pushes TableStats.RowCount above
# zero, and by design §D3's invariant the fallback then cannot fire at all —
# a re-ANALYZEd "c2" arm measures nothing and reports it as safe.
#
# Usage:
#   scripts/tpch-relsize-arm.sh c1
#   QUERIES=1,5,9 PER_Q=180 scripts/tpch-relsize-arm.sh c2
#   scripts/tpch-relsize-arm.sh probe-analyze     # is ANALYZE even reachable?
#
# Environment:
#   QUERIES   comma-separated query numbers (default 1..22)
#   PER_Q     per-query timeout in seconds (default 300; runner-enforced,
#             plus an external `timeout` clamp at PER_Q+60)
#   OUTDIR    results directory (default analysis/tpch-relsize-fallback-<date>)
#   GOOPG_BIN engine image to run (default tmp/goopg-relsize-bin, built here —
#             NEVER the shared tmp/goopg-bench-bin, which ci/batch and the SF=1
#             harness also run from: ledger row goopg_bench_bin_shared_lane)
#   NO_BUILD  1 = trust the existing GOOPG_BIN / runner images
#
# Output: ${OUTDIR}/<arm>.tsv (one row per query) + ${OUTDIR}/<arm>.log.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/bench-engine-id.sh
source "${REPO_ROOT}/scripts/lib/bench-engine-id.sh"

ARM="${1:?usage: $0 <c1|c2|w1|w2|probe-analyze>}"

PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

PG_HOST=127.0.0.1
PG_PORT=65433
PGDATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-relsize-bin}"
RUNNER_BIN="${REPO_ROOT}/tmp/tpch-relsize-runner"
CG_UNIT="goopg-relsize-arm"
PPROF_ADDR="${PPROF_ADDR:-127.0.0.1:6161}"

PER_Q="${PER_Q:-300}"
QUERIES="${QUERIES:-1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22}"
OUTDIR="${OUTDIR:-${REPO_ROOT}/analysis/tpch-relsize-fallback-$(date +%Y%m%d)}"

# Arm -> (flag, analyze). An unknown arm must not silently become c1.
ARM_FLAG=""; ARM_ANALYZE=0
case "${ARM}" in
    c1)            ARM_FLAG="0"; ARM_ANALYZE=0 ;;
    c2)            ARM_FLAG="2"; ARM_ANALYZE=0 ;;
    w1)            ARM_FLAG="0"; ARM_ANALYZE=1 ;;
    w2)            ARM_FLAG="2"; ARM_ANALYZE=1 ;;
    probe-analyze) ;;
    *) echo "unknown arm '${ARM}' (want c1|c2|w1|w2|probe-analyze)" >&2; exit 2 ;;
esac

mkdir -p "${OUTDIR}" "${REPO_ROOT}/tmp"
LOG="${OUTDIR}/${ARM}.log"
TSV="${OUTDIR}/${ARM}.tsv"
SERVER_LOG="${OUTDIR}/${ARM}.server.log"

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "${LOG}"; }

# --- pre-flight: we must OWN the port, and the data must be there ----------
# A foreign server on 65433 would be measured instead of ours (and killed by our
# stop ladder). The SF0.5 gate learned this the expensive way; refuse instead.
if [[ -f "${PGDATA}/postmaster.pid" ]] && kill -0 "$(head -1 "${PGDATA}/postmaster.pid")" 2>/dev/null; then
    echo "a goopg server is already running at ${PGDATA} (pid $(head -1 "${PGDATA}/postmaster.pid")) — stop it first:" >&2
    echo "  bench/tpch/stop_goopg.sh" >&2
    exit 3
fi
if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null; then
    echo "something is already listening on ${PG_HOST}:${PG_PORT} and it is not our cluster — refusing" >&2
    exit 3
fi
[[ -s "${PGDATA}/PG_VERSION" ]] || { echo "no loaded TPC-H cluster at ${PGDATA}" >&2; exit 3; }
if pgrep -f "ci/batch/run-nightly.sh" >/dev/null 2>&1; then
    echo "the nightly CI batch is running — every timing here would be void. Refusing." >&2
    exit 3
fi

if [[ "${NO_BUILD:-0}" != "1" ]]; then
    say "building engine -> ${GOOPG_BIN} and runner -> ${RUNNER_BIN}"
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || exit 4
    ( cd "${REPO_ROOT}" && go build -o "${RUNNER_BIN}" ./cmd/tpch-runner ) || exit 4
fi

# --- server lifecycle ------------------------------------------------------
# GOGC=100 + GOMEMLIMIT=12GiB, not the bench default GOGC=off: CLAUDE.md records
# that heavy TPC-H queries at S-cold need that headroom (Q21 drew a host-level
# OOM at 18GiB and completes at GOGC=100 + 12GiB), and S-cold is the whole point
# of the c-arms. GOOPG_MEM_HIGH must stay ABOVE GOMEMLIMIT or the scope parks in
# the kernel throttle band after one big query (cgroup_high_below_gomemlimit).
start_server() {
    # A SIGKILLed predecessor (Q21 in the 2026-07-30 c2 arm) leaves the listen
    # socket held and the scope half-alive for a few seconds; starting into that
    # fails and the query after the killed one is lost. Wait for the port to
    # clear, and retry the start once, before declaring server-fail.
    local attempt
    for attempt in 1 2; do
        rm -f "${PGDATA}/postmaster.pid"
        systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
        systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
        local i
        for i in $(seq 1 30); do
            pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null || break
            sleep 1
        done
        start_server_once && return 0
        say "  start attempt ${attempt} failed; see ${SERVER_LOG}"
        sleep 5
    done
    return 1
}

start_server_once() {
    GOOPG_CG_UNIT="${CG_UNIT}" GOOPG_MEM_HIGH=13G GOOPG_MEM_MAX=15G \
    GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=12GiB GOGC=100 \
    GOOPG_PPROF_ADDR="${PPROF_ADDR}" \
    GOOPG_RELSIZE_FALLBACK="${ARM_FLAG}" \
        "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${GOOPG_BIN}" start -D "${PGDATA}" \
        --listen "${PG_HOST}:${PG_PORT}" \
        --hba "${PGDATA}/pg_hba.conf" \
        >>"${SERVER_LOG}" 2>&1 &
    WRAPPER_PID=$!
    local i
    for i in $(seq 1 120); do
        kill -0 "${WRAPPER_PID}" 2>/dev/null || return 1
        pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null && return 0
        sleep 1
    done
    return 1
}

postmaster_pid() { [[ -f "${PGDATA}/postmaster.pid" ]] && head -1 "${PGDATA}/postmaster.pid" || true; }

# Peak RSS of the engine process, read BEFORE the stop ladder — VmHWM is the
# kernel's high-water mark, so it survives the query's own heap release and is
# the number round-5 §6's 10 GB pin would have shown.
peak_rss_kb() {
    local pid; pid="$(postmaster_pid)"
    [[ -n "${pid}" && -r "/proc/${pid}/status" ]] || { echo ""; return; }
    awk '/^VmHWM:/ {print $2}' "/proc/${pid}/status"
}

# Bounded ladder, never a bare `wait`: a leaked backend makes the graceful stop
# block forever, which wedged the nightly for 6h45m on 2026-07-29.
stop_server() {
    local pid; pid="$(postmaster_pid)"
    timeout 60 "${GOOPG_BIN}" stop -D "${PGDATA}" >>"${SERVER_LOG}" 2>&1
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
        kill -INT "${pid}" 2>/dev/null
        for _ in $(seq 1 30); do kill -0 "${pid}" 2>/dev/null || break; sleep 1; done
        kill -0 "${pid}" 2>/dev/null && { say "  stop: escalating to SIGKILL (pid ${pid})"; kill -KILL "${pid}" 2>/dev/null; }
    fi
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    rm -f "${PGDATA}/postmaster.pid"
}
trap 'stop_server; exit 130' INT TERM

# --- connection target -----------------------------------------------------
# Same fallback the nightly needs: roles/DBs are in-memory only on some builds,
# so tpch@tpch can vanish across a restart while the data stays reachable as
# postgres@postgres (memory tpch_spotcheck_slru_backfill_startup_hang).
resolve_target() {
    GATE_DB=tpch GATE_USER=tpch GATE_PASS=tpch
    if PGPASSWORD=tpch psql -h "${PG_HOST}" -p "${PG_PORT}" -U tpch -d tpch -tA \
        -c 'select 1 from lineitem limit 1' >/dev/null 2>&1; then return 0; fi
    GATE_DB=postgres GATE_USER=postgres GATE_PASS=postgres
    if PGPASSWORD=postgres psql -h "${PG_HOST}" -p "${PG_PORT}" -U postgres -d postgres -tA \
        -c 'select 1 from lineitem limit 1' >/dev/null 2>&1; then return 0; fi
    return 1
}

# --- probe-analyze: verify the warm-stats state by measurement -------------
# Historical context: `ANALYZE <table>` in database `tpch` used to error
# 42P01 (fixed by M0125-0028) and stats were believed per-connection and
# known non-durable (fixed by M0125-0029: pg_statistic + the goopg-private
# relstats sidecar persist per-DB and reload for every connection). The
# probe stays because it is the cheap way to re-verify all of that against
# the REAL bench cluster after any reload/rebuild.
if [[ "${ARM}" == "probe-analyze" ]]; then
    say "probe-analyze: starting server (no arm flag)"
    start_server || { say "server did not start; see ${SERVER_LOG}"; stop_server; exit 5; }
    resolve_target || { say "no reachable TPC-H data"; stop_server; exit 5; }
    say "target = ${GATE_USER}@${GATE_DB}"
    # One session: ANALYZE then read pg_class.reltuples, which renders
    # TableStats.RowCount directly (catalog.go:6946) — so it answers both
    # "does ANALYZE succeed" and "did it land where the planner reads".
    PGPASSWORD="${GATE_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${GATE_USER}" -d "${GATE_DB}" \
        -v ON_ERROR_STOP=0 \
        -c 'ANALYZE lineitem' \
        -c "select relname, reltuples from pg_class where relname in ('lineitem','orders')" \
        2>&1 | tee -a "${LOG}"
    say "probe-analyze: a second session must show reltuples back at 0 if stats are per-connection"
    PGPASSWORD="${GATE_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${GATE_USER}" -d "${GATE_DB}" -tA \
        -c "select relname, reltuples from pg_class where relname in ('lineitem','orders')" \
        2>&1 | tee -a "${LOG}"
    stop_server
    exit 0
fi

if [[ ${ARM_ANALYZE} -eq 1 && "${W_ARM_OK:-0}" != "1" ]]; then
    cat >&2 <<'EOF'
W_ARM_OK=1 is required for the w-arms. They are CONSTRUCTIBLE since
M0125-0028/-0029 (per-DB ANALYZE resolution; stats durable across restarts and
visible to every connection — this script's own warm-up phase issues the
one-time per-table ANALYZE), but a w-arm is still a timed multi-query
measurement on the shared bench cluster: the flag is the explicit opt-in that
you have a quiet host and mean to spend it. Run
`scripts/tpch-relsize-arm.sh probe-analyze` first if in doubt.
EOF
    exit 6
fi

# --- w-arms: one-time durable ANALYZE warm-up (M0125-0029) -----------------
# Stats persist across restarts (pg_statistic + the goopg-private relstats
# sidecar, both per-database) and are visible to every connection, so ONE
# warm-up pass before the loop suffices — the per-query restarts that
# isolate each measurement no longer wash the stats out. This replaces the
# retired plan of teaching cmd/tpch-runner an -analyze flag (same-session
# ANALYZE), which the durability work made unnecessary.
if [[ ${ARM_ANALYZE} -eq 1 ]]; then
    say "w-arm warm-up: one-time durable ANALYZE of the 8 TPC-H tables"
    start_server || { say "warm-up server did not start; see ${SERVER_LOG}"; stop_server; exit 5; }
    resolve_target || { say "warm-up: no reachable TPC-H data"; stop_server; exit 5; }
    for wtbl in lineitem orders partsupp part customer supplier nation region; do
        PGPASSWORD="${GATE_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${GATE_USER}" -d "${GATE_DB}" \
            -v ON_ERROR_STOP=1 -c "ANALYZE ${wtbl}" >>"${LOG}" 2>&1 \
            || { say "warm-up ANALYZE ${wtbl} FAILED"; stop_server; exit 5; }
    done
    say "w-arm warm-up: verifying reltuples survived into a fresh session"
    PGPASSWORD="${GATE_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${GATE_USER}" -d "${GATE_DB}" -tA \
        -c "select count(*) from pg_class where relname in ('lineitem','orders','partsupp','part','customer','supplier','nation','region') and reltuples > 0" \
        | grep -qx 8 || { say "warm-up verification FAILED (reltuples not visible)"; stop_server; exit 5; }
    stop_server
    say "w-arm warm-up done; stats persist across the per-query restarts"
fi

# --- the arm ---------------------------------------------------------------
{
    echo "# arm: ${ARM}  (GOOPG_RELSIZE_FALLBACK='${ARM_FLAG:-unset}'  analyze=${ARM_ANALYZE})"
    echo "# started: $(date -Is)"
    echo "# engine-id: $(bench_engine_id)"
    echo "# engine-binary: on-disk=$(bench_engine_bin_sha "${GOOPG_BIN}") (${GOOPG_BIN#"${REPO_ROOT}/"})"
    echo "# per-query cap: ${PER_Q}s (external clamp $((PER_Q + 60))s); restart per query; NO re-ANALYZE"
    echo "# host: load$(cut -d' ' -f1-3 /proc/loadavg)"
    printf 'query\tsecs\trows\tstatus\tpeak_rss_kb\trunning_engine\n'
} > "${TSV}"

say "arm ${ARM}: flag='${ARM_FLAG:-unset}' queries=${QUERIES} per_q=${PER_Q}s -> ${TSV}"
IFS=',' read -r -a QLIST <<< "${QUERIES}"
arm_t0=${SECONDS}

for qn in "${QLIST[@]}"; do
    if ! start_server; then
        say "Q${qn}: server did NOT start — recording as server-fail"
        printf 'Q%s\t\t\tserver-fail\t\t\n' "${qn}" >> "${TSV}"
        stop_server; continue
    fi
    running="$(bench_running_engine_sha "${PGDATA}")"
    if ! resolve_target; then
        say "Q${qn}: no reachable TPC-H data — aborting arm"
        printf 'Q%s\t\t\tno-data\t\t%s\n' "${qn}" "${running}" >> "${TSV}"
        stop_server; break
    fi

    q_t0=${SECONDS}
    out="$(timeout -k 30 --signal=INT "$((PER_Q + 60))" \
        "${RUNNER_BIN}" -host "${PG_HOST}" -port "${PG_PORT}" \
        -db "${GATE_DB}" -user "${GATE_USER}" -password "${GATE_PASS}" \
        -queries "${qn}" -per-query-timeout "${PER_Q}s" 2>&1)"
    q_elapsed=$(( SECONDS - q_t0 ))
    printf '===== Q%s =====\n%s\n' "${qn}" "${out}" >> "${LOG}"

    rss="$(peak_rss_kb)"
    # Q15 reports as Q15-CREATEVIEW / Q15a-VIEWBODY / Q15b-MAIN; the MAIN select
    # is the query's result of record (same convention as ci/batch stage-tpch).
    label="Q${qn}"; [[ "${qn}" == "15" ]] && label="Q15b-MAIN"
    ok_line="$(grep -E "^${label}: OK elapsed=" <<<"${out}" | tail -1)"
    if [[ -n "${ok_line}" ]]; then
        rows="$(sed -n 's/.*rows=\([0-9]\+\)$/\1/p' <<<"${ok_line}")"
        secs="$(sed -n 's/.*elapsed=\([0-9.]\+\)s.*/\1/p' <<<"${ok_line}")"
        printf 'Q%s\t%s\t%s\tok\t%s\t%s\n' "${qn}" "${secs}" "${rows}" "${rss}" "${running}" >> "${TSV}"
        say "Q${qn} ok ${secs}s rows=${rows} peakRSS=${rss}kB"
    else
        status=error
        if grep -qiE '57014|canceling statement|context deadline' <<<"${out}" || (( q_elapsed >= PER_Q )); then
            status=timeout
        fi
        printf 'Q%s\t%s\t\t%s\t%s\t%s\n' "${qn}" "${q_elapsed}" "${status}" "${rss}" "${running}" >> "${TSV}"
        say "Q${qn} ${status} after ${q_elapsed}s peakRSS=${rss}kB"
    fi
    stop_server
done

total=$(( SECONDS - arm_t0 ))
{
    echo "# finished: $(date -Is)  wall=${total}s"
    echo "# engine-id at end: $(bench_engine_id)"
} >> "${TSV}"
say "arm ${ARM} done in ${total}s -> ${TSV}"
