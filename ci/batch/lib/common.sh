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
