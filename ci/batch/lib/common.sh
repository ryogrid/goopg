# ci/batch/lib/common.sh — shared helpers for the nightly batch.
# Sourced by run-nightly.sh and every stages/stage-*.sh.
# Contract: the orchestrator exports REPO_ROOT and RUN_DIR before any stage runs.
# Design: ci/design/01-architecture.md, 04-logging-and-reporting.md.

# progress <TAG> <msg...> — one line to the run's real-time progress log AND
# the caller's stdout (which stages redirect into their own logs).
progress() {
    local tag="$1"; shift
    printf '%s [%s]\t%s\n' "$(date -Is)" "$tag" "$*" | tee -a "${RUN_DIR}/progress.log"
}

# stage_status <stage> <status> — record a stage's outcome word
# (pass / fail / fail(build) / skip(no-data) / skip(port-busy) / ...).
# The orchestrator appends the elapsed seconds after the stage returns.
stage_status() {
    mkdir -p "${RUN_DIR}/stages"
    printf '%s\n' "$2" > "${RUN_DIR}/stages/$1.status"
}

# port_busy <host> <port> — 0 when something accepts on host:port.
port_busy() {
    ( exec 3<>"/dev/tcp/$1/$2" ) 2>/dev/null
}

# wait_port_free <host> <port> [max_seconds] — poll every 15s until the port
# is free; 1 on timeout. Never kills the holder (ci/design/03 §D).
wait_port_free() {
    local host="$1" port="$2" max="${3:-${NIGHTLY_PORT_WAIT:-900}}" waited=0
    while port_busy "${host}" "${port}"; do
        if (( waited >= max )); then
            return 1
        fi
        sleep 15
        waited=$(( waited + 15 ))
    done
    return 0
}

# stop_scope <unit> — stop + reset-failed a transient cgroup scope (idempotent).
stop_scope() {
    systemctl --user stop "$1.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "$1.scope" >/dev/null 2>&1 || true
}

# proc_alive <pid> — 0 while the pid is a real running process. A background
# child that has exited but not yet been reaped shows up as a zombie and still
# answers `kill -0`, so state-based detection is the only correct test here.
proc_alive() {
    local st
    st="$(ps -o stat= -p "$1" 2>/dev/null | tr -d ' ')"
    [[ -n "${st}" && "${st}" != Z* ]]
}

# stop_goopg_server <goopg_bin> <data_dir> <pid> [graceful_s] [immediate_s]
# Bounded, escalating shutdown of a stage's goopg server. Sets STOP_RUNG to the
# rung that actually worked (already-exited|graceful|immediate|sigterm|sigkill)
# and always returns 0 — the caller decides whether an escalation is newsworthy.
# STOP_RUNG is a global rather than an echoed value on purpose: `$(...)` would
# run this in a subshell, where `wait` cannot reap the CALLER's background child
# and the server would be left a zombie.
#
# Why this is not a plain `wait ${pid}`: a graceful stop only returns once every
# backend goroutine has finished, and a backend can leak. On 2026-07-29 the
# nightly's TPC-H Q13 hit the 1200s per-query cap, the harness killed the psql
# client, and the server logged "client connection lost mid-query; cancelling
# statement" for backend pid=40 — which then never closed. `goopg stop` took its
# shutdown checkpoint, cancelled the run context, drained the accept loop, and
# blocked forever on that one backend. The stage's untimed `wait` inherited the
# hang: 6h45m of a 16-core host consumed after the sweep had already finished,
# the run never reached its TPC-DS stage, and every concurrent timing was voided
# (the host-quiet guards then refuse to start, so this also blocks the next day's
# measurements). The engine leak is tracked separately; this ladder makes sure it
# can only ever cost minutes. Each stage's data dir is a disposable clone that is
# rm -rf'd immediately after, so nothing of value rides on the final checkpoint.
stop_goopg_server() {
    local bin="$1" datadir="$2" pid="$3"
    local graceful="${4:-${NIGHTLY_STOP_GRACEFUL:-120}}"
    local immediate="${5:-${NIGHTLY_STOP_IMMEDIATE:-60}}"
    local waited

    STOP_RUNG=""
    if [[ -z "${pid}" ]] || ! proc_alive "${pid}"; then
        wait "${pid}" 2>/dev/null || true
        STOP_RUNG="already-exited"; return 0
    fi

    # Rung 1 — graceful: shutdown checkpoint, pg_control State=DB_SHUTDOWNED.
    "${bin}" stop -D "${datadir}" >/dev/null 2>&1 || true
    for (( waited = 0; waited < graceful; waited++ )); do
        proc_alive "${pid}" || { STOP_RUNG="graceful"; break; }
        sleep 1
    done

    # The graceful stop missing its budget is the ONLY moment the wedged server
    # still exists to be inspected — every rung below destroys the evidence. Grab
    # a goroutine dump here so a leaked backend is diagnosable from the run's
    # artifacts alone. (On 2026-07-29 no dump existed: the server's pprof bind
    # had silently lost 127.0.0.1:6060 to another instance, and reproducing the
    # hang costs a 20-minute query. Callers now pass a private STOP_PPROF_ADDR.)
    if [[ -z "${STOP_RUNG}" && -n "${STOP_PPROF_ADDR:-}" && -n "${STOP_DUMP_FILE:-}" ]]; then
        curl -s --max-time 30 \
            "http://${STOP_PPROF_ADDR}/debug/pprof/goroutine?debug=2" \
            -o "${STOP_DUMP_FILE}" 2>/dev/null \
            && [[ -s "${STOP_DUMP_FILE}" ]] \
            && printf 'goroutine dump written to %s\n' "${STOP_DUMP_FILE}" \
            || printf 'goroutine dump UNAVAILABLE (pprof %s unreachable)\n' "${STOP_PPROF_ADDR}"
    fi

    # Rung 2 — immediate: skips both checkpoints (State stays DB_IN_PRODUCTION,
    # recovered by WAL replay on the next start). Mirrors upstream SIGQUIT.
    if [[ -z "${STOP_RUNG}" ]]; then
        "${bin}" stop -D "${datadir}" -mode immediate >/dev/null 2>&1 || true
        for (( waited = 0; waited < immediate; waited++ )); do
            proc_alive "${pid}" || { STOP_RUNG="immediate"; break; }
            sleep 1
        done
    fi

    # Rung 3/4 — signals. SIGTERM is the same graceful path the control socket
    # just failed to complete, so it gets a short leash before SIGKILL.
    if [[ -z "${STOP_RUNG}" ]]; then
        kill -TERM "${pid}" 2>/dev/null || true
        for (( waited = 0; waited < 30; waited++ )); do
            proc_alive "${pid}" || { STOP_RUNG="sigterm"; break; }
            sleep 1
        done
    fi
    if [[ -z "${STOP_RUNG}" ]]; then
        kill -KILL "${pid}" 2>/dev/null || true
        STOP_RUNG="sigkill"
    fi
    wait "${pid}" 2>/dev/null || true
    return 0
}
