# M0131-S30.4 — why no checkpoint fired during the crash probe, and the two max_wal_size deviations it exposed

Status: implemented (2026-08-12)
Task: `M0131-S30.4` (fix_plan.md)
Code: `internal/wal/checkpointer.go`
Tests: `internal/wal/checkpointer_test.go`
Oracle: `postgres/src/backend/access/transam/xlog.c`

## The question S30.4 asked

The S30 crash probe (`analysis/crashprobe.sh`) generated ~180 MiB of WAL and
`checkpoint` appeared **zero** times in the pre-crash log, so every run replayed
from `redo=16777256`. S30.4 required an explicit decision: does goopg's
checkpointer simply never trigger on WAL volume, or do the probe's settings
disable it?

## The decision: the probe is behaving correctly; the trigger works

Neither. The checkpointer does trigger on volume, and the probe disables
nothing — it writes no `postgresql.conf` overrides at all, so it runs on the
PG-default GUCs:

| GUC | goopg BootVal (`internal/config/defaults.go`) | PG 18 default |
|---|---|---|
| `checkpoint_timeout` | 300 s | 300 s |
| `max_wal_size` | 1024 MB | 1 GB |
| `checkpoint_completion_target` | 0.9 | 0.9 |

The probe kills the server 30 s in (`KILLAT`), well inside the 300 s timeout, and
~180 MiB is far below any volume threshold either engine would use. **A real PG
under the same workload and settings would also produce zero checkpoints.** Zero
checkpoints is therefore an artifact of the probe's duration, not a defect — it
is an aggravating factor for S30's row-loss measurements (nothing advances redo,
so a truncated replay costs the whole run) exactly as the item suspected, and
nothing more.

Two genuine deviations from the oracle surfaced while establishing that, and both
are fixed here.

## Deviation 1 — `max_wal_size` was used as the trigger distance

goopg compared `WrittenLSN - lastCheckpointLSN >= MaxWALBytes` — i.e. it treated
`max_wal_size` itself as the distance between checkpoints. Upstream does not:
`CalculateCheckpointSegments` (xlog.c:2170-2198) derives a *segment count* from
it, because `max_wal_size` is a ceiling on the WAL retained for one cycle and a
spread checkpoint consumes `checkpoint_completion_target` more segments while it
is running:

```c
target = (double) ConvertToXSegs(max_wal_size_mb, wal_segment_size) /
    (1.0 + CheckPointCompletionTarget);
CheckPointSegments = (int) target;          /* round down, floor at 1 */
```

`XLogCheckpointNeeded` (xlog.c:2279-2289) then fires at
`new_segno >= old_segno + (CheckPointSegments - 1)`.

At the shared defaults that is `64 / 1.9 = 33` segments, so PG checkpoints
**32 segments = 512 MiB** past redo. goopg waited the full **1024 MiB** — half as
often as the oracle under identical settings. `checkpointSegments()` now ports
the formula and the trigger compares segment numbers.

## Deviation 2 — the anchor was the checkpoint RECORD, not its redo point

The old comparison anchored on `lastCheckpointLSN`, the end LSN of the checkpoint
*record*. Upstream anchors on `RedoRecPtr`. The record trails redo by the entire
dirty-page flush phase plus the `XLOG_RUNNING_XACTS` record that precedes every
online checkpoint, so anchoring on it silently shortened every window after the
first by that gap. The trigger now reads `lastCheckpointRedoLSN` (stored 1-based,
converted back to the 0-based position segment numbers use).

Before the first checkpoint of a process there is no redo pointer to read.
Upstream always has one — recovery seeds `RedoRecPtr` from the checkpoint it
started from — so `Run` seeds `volumeAnchor` with the writer position observed at
loop start. Anchoring at LSN 0 instead (the old fallback's effective behaviour)
would make every restart of a cluster whose WAL has already passed `max_wal_size`
checkpoint on the very first poll.

## Deviation 2b — the 1 s poll turns `CheckPointSegments == 1` into a storm

Found by measurement, not by reading: a first cut of the above, run against a
48 MB `max_wal_size` cluster (`CheckPointSegments = 1`, so the literal
`elapsed >= CheckPointSegments - 1` is `elapsed >= 0`), produced **42 checkpoints
in 40 s** of pgbench — most against WAL that had not advanced a single segment.

The cause is *where* the two engines evaluate the test. Upstream calls
`XLogCheckpointNeeded` from `XLogWrite` only when it has just opened a new
segment, so `CheckPointSegments == 1` still means "once per segment". goopg polls
on `VolumeCheckInterval` (1 s), where a zero distance is satisfied on every tick.
`elapsedSegmentsNeeded()` therefore floors the distance at one segment, which
reproduces upstream's behaviour for `CheckPointSegments == 1` and is identical to
it for every larger value. Re-measured on the same cluster: 14 checkpoints over
~223 MiB of WAL, one per ~16 MiB segment.

Polling rather than hooking the segment switch remains a deviation in *when* the
condition is noticed (up to `VolumeCheckInterval` late); see the deferral ledger.

## Verification

- `go test ./internal/wal/` (whole package) and `-race` on `TestCheckpointer*`.
- New `TestCheckpointerCheckPointSegments` pins the `CalculateCheckpointSegments`
  port including the PG-default row (33 segments / 512 MiB).
- `TestCheckpointerVolumeTriggerThreshold` rewritten to the segment-number
  contract and to prove the anchor is redo, not the record LSN (it sets
  `lastCheckpointLSN` deliberately far past redo).
- `TestCheckpointerVolumeTriggerDisabled` pins `max_wal_size = 0` as off.
- E2E on a throwaway 5534 cluster with `max_wal_size = 48MB`,
  `checkpoint_timeout = 3600s`: checkpoints land ~16 MiB apart, none from the
  timer.

## Follow-up (2026-08-13, AI-20260813-005117-008): the anchor-seeding race

The nightly race lane failed `TestCheckpointerVolumeTrigger` with
`volume trigger did not fire within 2s`. It was filed as a load-sensitive
2 s-deadline flake — the shape of the already-fixed `internal/mctx
TestMultipleChunks` item — but it is not one: no deadline would have saved it.

`Run` seeds `volumeAnchor` from `vr.WrittenLSN()` **inside its own goroutine**,
while the caller that spawned it keeps appending. The test spawns `Run`, then
immediately appends 16 records to cross a segment boundary. If `Run` is scheduled
late — which is what a co-loaded nightly host under `-race` at `-p=4` does — the
anchor is taken *after* those appends, so the writer is level with the anchor,
the trigger can never fire, and the test waits out any deadline.

Reproduced deterministically at `-cpu=1`: **6 of 20** runs failed with the exact
nightly message, each at 2.02 s.

Two changes:

1. `Run` now invokes `OnLoopStart`/`OnLoopEnd` **after** the volume ticker is
   armed and the anchor stored, rather than as the first statement. "The loop has
   started" is only a useful observable once the loop is armed; a waiter released
   earlier can still race its appends ahead of the seed. Both hooks still bracket
   the whole timer/volume loop, so the production consumer
   (`initdb.Open` → `activity.SetCurrentGoroutine`) is unaffected.
2. The test takes the handshake: `OnLoopStart` closes an `armed` channel and the
   append loop waits on it. Same `-cpu=1` probe after the fix: **20 of 20** pass,
   1.5 s total against 40 s of timeouts before.

Still deferred (ledger row): in production `NewCheckpointer` runs inside
`initdb.Open` while `Run` is not started until `cmd/goopg/main.go`, so WAL
appended in between — including the end-of-recovery checkpoint — is absorbed into
the anchor and widens the first `max_wal_size` window. It self-corrects at the
first checkpoint, after which `lastCheckpointRedoLSN` is the anchor.
