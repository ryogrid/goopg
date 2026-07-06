# 06 — Scheduler: Resident Daemon + ralph_loop.sh Hook

Requirement 7: a resident process, launched from `~/.ralph/ralph_loop.sh`,
fires the batch daily at ~00:00 local time, with protection against multiple
resident instances.

## A. Why a self-sleeping daemon (not cron / systemd timers)

- WSL2 user sessions don't reliably provide cron or per-user systemd timers
  across restarts, and the project already has the precedent of loop-launched
  sidecars (`mem_guard.py`).
- Launching from `ralph_loop.sh` means the scheduler exists exactly on the
  machine/tree where the loop develops — no separate provisioning step.
- `flock` gives airtight single-instance semantics that survive loop
  restarts, crashes, and manual launches.

## B. `ci/batch/nightly-scheduler.sh` (the daemon)

Behavior specification:

```bash
#!/bin/bash
# Resident nightly-batch scheduler. Safe to launch any number of times:
# only one instance survives (flock). Logs to ci/logs/scheduler.log.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK_DIR="$HOME/.ralph/locks"; mkdir -p "$LOCK_DIR"
SCHED_LOCK="$LOCK_DIR/goopg-nightly-scheduler.lock"
LOG="$REPO_ROOT/ci/logs/scheduler.log"; mkdir -p "$REPO_ROOT/ci/logs"
FIRE_HOUR="${NIGHTLY_FIRE_HOUR:-00}"        # 2-digit local hour, see §E

exec 9>"$SCHED_LOCK"
if ! flock -n 9; then
    # Another scheduler is already resident — this is the NORMAL path on
    # every ralph_loop.sh restart. Exit 0 silently (idempotent spawn).
    exit 0
fi
echo "$(date -Is) scheduler start pid=$$ repo=$REPO_ROOT fire_hour=$FIRE_HOUR" >>"$LOG"

while :; do
    # Sleep until the next local FIRE_HOUR:00 (recompute each cycle —
    # tolerant of clock jumps / suspend, which WSL2 does on host sleep).
    now=$(date +%s)
    next=$(date -d "today ${FIRE_HOUR}:00" +%s)
    (( next <= now )) && next=$(date -d "tomorrow ${FIRE_HOUR}:00" +%s)
    sleep $(( next - now ))

    # Re-check after wake: if we woke early/late, only fire inside the
    # [FIRE_HOUR:00, FIRE_HOUR+1:00) window; otherwise loop and recompute.
    [[ $(date +%H) == "$FIRE_HOUR" ]] || continue

    echo "$(date -Is) firing nightly batch" >>"$LOG"
    setsid bash "$REPO_ROOT/ci/batch/run-nightly.sh" \
        >>"$REPO_ROOT/ci/logs/launch.log" 2>&1 9>&- &
    # 9>&-  : the child must NOT inherit the scheduler-lock fd — otherwise a
    #         dead scheduler's lock stays pinned by a still-running batch
    #         (blocking any replacement scheduler), and `fuser -k` on the
    #         lock file would kill the batch too.
    # launch.log : bootstrap stderr only; once run-nightly.sh creates its
    #         run dir, all its output goes there (doc 04). scheduler.log
    #         stays daemon-lines-only.
    # Detached: the scheduler keeps ticking regardless of batch duration.
    # If yesterday's batch somehow still runs, run-nightly.sh's own run
    # lock makes this firing a no-op (see §C).
done
```

Design points:

- **Repo-anchored, not cwd-anchored**: `REPO_ROOT` derives from the script's
  own path, so it works no matter where it was spawned from (the
  worktree-cwd-hazard lesson).
- **Fire window** `[FIRE_HOUR:00, +1h)`, default hour `00` — a late wake
  (host was asleep at midnight) inside the window still fires; a wake at
  03:00 skips to the next midnight. **No catch-up runs** — a missed night is
  simply missed; on-demand coverage is `make nightly-batch`.
- The scheduler is **not** killed when the loop exits (no EXIT trap in the
  loop pointing at it) — it outlives loop restarts, and `flock` makes the
  next loop start's spawn attempt a clean no-op. Stopping it deliberately:
  `fuser -k "$HOME/.ralph/locks/goopg-nightly-scheduler.lock"` (safe against
  the batch because the spawn closes fd 9 in the child) or kill the PID
  logged in `scheduler.log` (never `pkill -f`).
- One scheduler per **repo path** would need per-repo lock names if a second
  checkout ever runs a loop; current reality is one tree, one lock name is
  fine (note left for the implementer).

## C. Run-level lock (scheduled vs manual overlap)

`ci/batch/run-nightly.sh` opens its own lock first thing:

```bash
RUN_LOCK="$HOME/.ralph/locks/goopg-nightly-run.lock"
exec 8>"$RUN_LOCK"
if ! flock -n 8; then
    echo "nightly batch already running (lock: $RUN_LOCK) — exiting" >&2
    exit 5
fi
```

Guarantees:

- the 00:00 firing and a human's `make nightly-batch` can never overlap
  (whoever is second exits immediately with distinct code 5);
- a >24 h runaway batch causes the next night's firing to no-op rather than
  stack (and the previous run's own stage timeouts bound the runaway anyway).

Two locks, two jobs: the scheduler lock deduplicates *residents*; the run
lock serializes *executions*. Neither uses PID files (stale-PID races); both
are `flock` on fds, auto-released on process death.

**fd-inheritance rule for stage scripts:** any long-lived process a stage
spawns detached (a background goopg server, a monitor) must be started with
`8>&-` so an orphan can't keep the run lock held after the orchestrator
dies. Ordinary foreground stage children inheriting fd 8 is fine (they die
with the run).

## D. The `~/.ralph/ralph_loop.sh` hook (patch to apply at implementation)

`ralph_loop.sh` is outside this repo, so the design records the exact patch;
the implementer (or the user) applies it manually. It mirrors the existing
`start_mem_guard()` sidecar pattern (defined ~line 2066, invoked from
`main()` at ~line 2122; `trap stop_mem_guard EXIT` at ~line 2114):

```bash
# --- add near start_mem_guard() (~line 2066) ---------------------------
# Nightly regression batch scheduler (goopg kaizen, 2026-07): if the
# project provides ci/batch/nightly-scheduler.sh, spawn it detached.
# The script self-deduplicates via flock, so calling this on every loop
# start is safe. Intentionally NOT stopped on loop exit — the scheduler
# outlives the loop; see <repo>/ci/design/06-scheduler.md.
start_nightly_scheduler() {
    local sched="./ci/batch/nightly-scheduler.sh"
    [[ -f "$sched" ]] || return 0          # no-op on repos without ci/
    setsid bash "$sched" </dev/null >/dev/null 2>&1 &
    log_status "INFO" "Nightly-batch scheduler spawn attempted (flock dedups)" 2>/dev/null || true
}

# --- add inside main(), next to start_mem_guard (~line 2122) -----------
    start_nightly_scheduler
```

Why this shape:

- **`[[ -f ]]` guard** — the hook is a no-op for any other project the loop
  runs on; zero coupling.
- **`setsid` + detached** — the scheduler leaves the loop's process group, so
  loop shutdown/`kill`-trees and the mem-guard's descendant accounting don't
  touch it (`mem_guard.py` walks the *loop's* descendants; a `setsid`
  scheduler that has been reparented is outside that tree — and its own
  footprint is a sleeping bash, negligible either way).
- **No stop function / no trap change** — deliberate (§B); the only lifecycle
  the loop owns is "ensure one exists".
- Relative `./ci/...` is correct here because `ralph_loop.sh` runs with cwd =
  project root (it addresses `.ralph/` relatively); the scheduler immediately
  re-anchors itself to its own absolute path (§B), so the cwd dependence
  lasts one line.

## E. Interaction with the loop's nightly activity

The batch does not pause the loop, and must not: requirement 1 says design
*around* it. At 00:00 the loop may be mid-gate (its own capped test run).
The consequences are already handled by the resource design:

- port/data-dir collisions ⇒ bounded-wait-then-skip (doc 03 §D);
- memory co-pressure ⇒ budget headroom + `resource-kill` classification
  (doc 03 §A/§C);
- timing noise ⇒ the perf-tolerance policy (doc 04 §C).

If nightly contention proves chronic (repeated `port-busy` skips in
`history.jsonl`), the escalation path is to move the firing window (e.g.
`NIGHTLY_FIRE_HOUR=04`) — the §B spec already parameterizes the hour, so
this is a restart-with-env change, no code edit.
