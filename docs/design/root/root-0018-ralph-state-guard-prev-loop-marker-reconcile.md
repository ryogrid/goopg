# root-0018 — Ralph state-guard: reconcile the previous loop's clean-exit marker

Status: accepted (loop #11, 2026-06-14)
Scope: `cmd/validate-ralph-state` (Ralph loop state-consistency guard)

## Problem

`make ralph-state-guard` failed at the **start of every loop** for loops #5–#11,
each time reporting:

```
status:   status="running" last_action="executing" loop_count=N timestamp=<loop N start>
progress: status="completed" timestamp=<loop N-1 end>
1. status.status is running while progress.status is completed
2. status.last_action is executing but progress.status is completed
```

Earlier loops mis-diagnosed this as **concurrent-loop corruption** and "repaired"
it by hand-editing `.ralph/progress.json` back to `in_progress`. That restore was
stomped again on the next clean exit, so the failure recurred every loop — pure toil,
no root-cause fix.

## Actual root cause

There is no concurrent loop. ppid analysis confirms a single `--live` loop: the
second `ralph_loop.sh` process is the `portable_timeout` pipeline subshell whose
ppid is the first loop (see memory `concurrent_ralph_loops_corrupt_tree`).

The real mechanism is the driver's own bookkeeping. `~/.ralph/ralph_loop.sh`
line ~1832 (comment *"Clear progress file"*) writes:

```sh
echo '{"status": "completed", ...}' > "$PROGRESS_FILE"
```

after **every** successful `claude` exit. Here `completed` means *"this loop's
claude call finished cleanly"* — **not** *"project done"*. The next loop then sets
`status.json` to `running/executing` (new `loop_count`) without touching
`progress.json`. So the steady state while any loop N runs is necessarily:

- `status`   = `running/executing`, loop N, timestamp ≈ loop N start
- `progress` = `completed`, timestamp = loop N-1's clean-exit

The guard flagged that normal transient as corruption.

## Fix

A new auto-repair rule in `autoRepair` (`cmd/validate-ralph-state/main.go`), the
exact complement of the existing stale-status rule:

| condition | meaning | repair |
|---|---|---|
| `progressTS` **newer** than `statusTS` by > max-skew | the **status** is stale (loop died, something completed later) | mark status `completed/idle` (pre-existing rule) |
| `progressTS` **not newer** than `statusTS` by > max-skew | the **progress** `completed` is the previous loop's exit marker; the live `running` status is authoritative | reconcile `progress → in_progress`, timestamp = `statusTS` (new rule) |

The two predicates partition on `progressTS.After(statusTS + maxSkew)`, so they
never both fire. The new rule preserves the live `status` (the running loop is
genuinely doing work) and moves only the stale `progress` field forward, clearing
both the "running while completed" issue and any "status newer than progress" skew
issue in one shot. `make ralph-state-guard` now self-heals via its `-fix` pass with
no manual edit, every loop.

Genuine project completion is unaffected: at real completion the driver sets
`status.status` to `completed`/`graceful_exit` (not `running`), so `isStatusRunning`
is false and the new rule does not fire.

## Tests

`cmd/validate-ralph-state/main_test.go`:
- `TestAutoRepairReconcilesPrevLoopCompletedMarker` — completed marker 5s older than
  a running status → progress reconciled to in_progress; status untouched; validate clean.
- `TestAutoRepairReconcilesLongRunningLoop` — loop running > max-skew past the marker;
  both the mismatch and skew issues clear.
- `TestAutoRepairNoopWhenConsistent` — running + in_progress left untouched.
- `TestAutoRepairStaleRunningStatus` (unchanged) — confirms the complement rule
  (progress newer by > max-skew → status completed) still fires.
