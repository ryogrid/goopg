#!/bin/bash
# nightly-scheduler.sh — resident daemon: fire run-nightly.sh daily at
# ~NIGHTLY_FIRE_HOUR:00 local (default 00). Safe to launch any number of
# times: flock dedups residents (second instance exits 0 — the normal path on
# every ralph_loop.sh restart). Design: ci/design/06-scheduler.md.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK_DIR="${HOME}/.ralph/locks"; mkdir -p "${LOCK_DIR}"
SCHED_LOCK="${LOCK_DIR}/goopg-nightly-scheduler.lock"
LOG="${REPO_ROOT}/ci/logs/scheduler.log"; mkdir -p "${REPO_ROOT}/ci/logs"
# Normalize NIGHTLY_FIRE_HOUR to a valid 2-digit local hour: "7" would never
# match `date +%H` ("07") in the window recheck, and garbage would break the
# date arithmetic into a tight error loop. Invalid values fall back to 00.
FIRE_HOUR="$(printf '%02d' "$(( 10#${NIGHTLY_FIRE_HOUR:-0} ))" 2>/dev/null || echo 00)"
if ! [[ "${FIRE_HOUR}" =~ ^([01][0-9]|2[0-3])$ ]]; then
    FIRE_HOUR="00"
fi

exec 9>"${SCHED_LOCK}"
if ! flock -n 9; then
    # Another scheduler is already resident — idempotent spawn, exit quietly.
    exit 0
fi

log() { printf '%s %s\n' "$(date -Is)" "$*" >> "${LOG}"; }
log "scheduler start pid=$$ repo=${REPO_ROOT} fire_hour=${FIRE_HOUR}"

while :; do
    # Sleep until the next local FIRE_HOUR:00 (recompute each cycle —
    # tolerant of clock jumps / WSL2 suspend).
    now=$(date +%s)
    next=$(date -d "today ${FIRE_HOUR}:00" +%s 2>/dev/null)
    if [[ -z "${next}" ]]; then
        log "date computation failed (FIRE_HOUR=${FIRE_HOUR}); sleeping 3600s"
        sleep 3600
        continue
    fi
    if (( next <= now )); then
        next=$(date -d "tomorrow ${FIRE_HOUR}:00" +%s)
    fi
    log "next fire at $(date -Is -d "@${next}") (sleeping $(( next - now ))s)"
    sleep $(( next - now ))

    # Only fire inside the [FIRE_HOUR:00, +1h) window; a wake far outside it
    # (host slept through the night) skips to the next computation.
    [[ "$(date +%H)" == "${FIRE_HOUR}" ]] || continue

    log "firing nightly batch"
    # 9>&- : the batch must NOT inherit the scheduler-lock fd — otherwise a
    #        dead scheduler's lock stays pinned by a running batch, and
    #        `fuser -k` on the lock file would kill the batch too.
    # launch.log gets bootstrap output only; once run-nightly.sh creates its
    # run dir, everything goes there. If yesterday's batch still runs, the
    # run lock makes this firing a no-op (exit 5).
    setsid bash "${REPO_ROOT}/ci/batch/run-nightly.sh" \
        >> "${REPO_ROOT}/ci/logs/launch.log" 2>&1 9>&- &
done
