#!/bin/bash
# nightly-scheduler.sh — resident daemon: fire run-nightly.sh daily at
# ~NIGHTLY_FIRE_HOUR:00 local (default 00). Safe to launch any number of
# times: flock dedups residents (second instance exits 0 — the normal path on
# every ralph_loop.sh restart). Design: ci/design/06-scheduler.md.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK_DIR="${HOME}/.ralph/locks"; mkdir -p "${LOCK_DIR}"
SCHED_LOCK="${LOCK_DIR}/goopg-nightly-scheduler.lock"
# Persisted "last fired for this calendar day" marker (YYYY-MM-DD). Dedups the
# daily fire across scheduler restarts so a mid-day (re)start does not re-run a
# batch that already fired today, while still allowing a catch-up fire when the
# host was suspended across the fire window. Scheduler-owned; manual run dirs do
# not touch it.
SCHED_STATE="${LOCK_DIR}/goopg-nightly-last-fire-date"
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
    # Recompute each cycle — tolerant of clock jumps / WSL2 suspend.
    now=$(date +%s)
    today_fire=$(date -d "today ${FIRE_HOUR}:00" +%s 2>/dev/null)
    if [[ -z "${today_fire}" ]]; then
        log "date computation failed (FIRE_HOUR=${FIRE_HOUR}); sleeping 3600s"
        sleep 3600
        continue
    fi
    today_date=$(date +%Y-%m-%d)
    last_fire_date="$(cat "${SCHED_STATE}" 2>/dev/null || true)"

    # Catch-up fire: fire as soon as today's FIRE_HOUR:00 has passed and we
    # have not fired yet for today. Unlike the old strict [FIRE_HOUR:00, +1h)
    # window guard, a wake that lands PAST the window (e.g. WSL2 was suspended
    # across midnight, so `sleep` returns late) still runs the daily batch
    # instead of skipping the whole day. The per-day SCHED_STATE marker keeps
    # this to at most one fire per calendar day, even across restarts.
    if (( now >= today_fire )) && [[ "${last_fire_date}" != "${today_date}" ]]; then
        printf '%s' "${today_date}" > "${SCHED_STATE}"
        log "firing nightly batch (scheduled ${today_date}T${FIRE_HOUR}:00; woke $(date -Is))"
        # 9>&- : the batch must NOT inherit the scheduler-lock fd — otherwise a
        #        dead scheduler's lock stays pinned by a running batch, and
        #        `fuser -k` on the lock file would kill the batch too.
        # launch.log gets bootstrap output only; once run-nightly.sh creates
        # its run dir, everything goes there. If yesterday's batch still runs,
        # the run lock makes this firing a no-op (exit 5).
        setsid bash "${REPO_ROOT}/ci/batch/run-nightly.sh" \
            >> "${REPO_ROOT}/ci/logs/launch.log" 2>&1 9>&- &
        # Re-loop to recompute the wait; last_fire_date now == today, so the
        # next pass falls through to sleeping until tomorrow's fire.
        continue
    fi

    # Sleep until the next fire we still owe: today's if it is still ahead,
    # otherwise tomorrow's.
    if (( now >= today_fire )); then
        next=$(date -d "tomorrow ${FIRE_HOUR}:00" +%s)
    else
        next=${today_fire}
    fi
    log "next fire at $(date -Is -d "@${next}") (sleeping $(( next - now ))s)"
    sleep $(( next - now ))
done
