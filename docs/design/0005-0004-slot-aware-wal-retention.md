# 0005-0004 — Slot-aware WAL Retention (M0005)

Status: accepted (2026-04-29)

Milestone: [`0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md)

Predecessors:
- [`0002-0001-checkpointing.md`](0002-0001-checkpointing.md) — owns the
  checkpointer that triggers retention.
- [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md)
  §"Replication slot retention semantics" — declared the design before
  the implementation existed.

## Why

Before this loop, the WAL writer never deleted a segment. `pg_wal/`
grew unbounded across the lifetime of any goopg cluster, regardless
of checkpoint cadence. The `min_wal_size` GUC was registered but
inert. Replication slots tracked `RestartLSN` but nothing consumed
that value: a connected standby couldn't actually rely on the primary
keeping WAL around, because there was no recycling path *to* keep
WAL away from in the first place.

This loop closes the loop end-to-end. After every successful
checkpoint, the primary now:

1. Invalidates any replication slot whose lag exceeds
   `max_slot_wal_keep_size` (so a single laggy standby cannot pin
   WAL forever).
2. Computes a keep horizon as `min(checkpointLSN, min(RestartLSN ∀
   live slots))`.
3. Unlinks every WAL segment whose final byte sits strictly before
   the segment containing the keep horizon.

## Components

### `internal/wal/writer.go` — `RemoveOldSegments(keepLSN)`

A new op (`opRecycle`) is queued onto the writer's serialised loop
goroutine. The loop scans `pg_wal/`, drops in-memory file handles for
soon-to-be-unlinked segments, removes them, and reports the count of
deletions. Running on the loop goroutine guarantees no race with
`Append` or `FlushUpTo` — those touch `s.files` and `s.dirty` from
the same goroutine.

The segment that **contains** `keepLSN` is preserved. Callers can
therefore pass the LSN of the just-flushed checkpoint marker without
worrying that recovery will lose the marker itself. `keepLSN == 0` is
a no-op (interpreted as "no records yet"), so the early-startup
window where no checkpoint or slot has ever advanced won't accidentally
nuke a fresh `pg_wal/`.

### `internal/wal/slots.go` — `Slot.Invalidated` + `InvalidateLagging`

`Slot` grows an `Invalidated` flag (persisted in the slot's JSON
state file) that tracks whether the slot was forcibly evicted because
its lag exceeded `max_slot_wal_keep_size`. An invalidated slot
remains visible (so operators can `pg_drop_replication_slot` it
deliberately) but is skipped by `MinRestartLSN`, so its stale
`RestartLSN` no longer pins WAL.

`InvalidateLagging(currentLSN, maxKeepBytes)` walks every live slot,
flips any whose `currentLSN - RestartLSN > maxKeepBytes` to
`Invalidated = true`, persists the new state via the existing
tempfile+rename, and returns the names that flipped. `maxKeepBytes
<= 0` short-circuits to no-op (matches upstream's
`max_slot_wal_keep_size = -1` unlimited sentinel). Lag comparison is
strict (`>`), matching upstream; a slot that lags exactly the bound
is preserved.

Active slots are still invalidated — upstream behaves the same way:
the next standby-status reply from the laggy walsender will fail
because the slot is no longer usable, and the operator must rebuild
the standby from a fresh base backup.

### `internal/wal/retention.go` — `SlotAwareRetainer`

Implements a small `Retainer` interface (`Retain(checkpointLSN)
error`). The production type holds references to the writer + slot
registry + the `MaxSlotKeepBytes` knob, and runs the four-step pipeline:

1. Invalidate lagging slots against the writer's `WrittenLSN()`
   (matching upstream's `KeepLogSeg`, which uses the live write
   head, not the checkpoint redo).
2. Compute the keep horizon — `min(checkpointLSN,
   min(RestartLSN ∀ live slots))`.
3. Call `Writer.RemoveOldSegments(keepLSN)`.
4. Log a one-line summary at INFO when anything happened.

A retainer error is logged but does **not** fail the checkpoint —
the marker is already durable, and retention is best-effort. This
matches the spirit of upstream's `RemoveOldXlogFiles`: cleanup
failures don't unwind the checkpoint that just succeeded.

### `internal/wal/checkpointer.go` — post-marker hook

`Checkpointer` grows a `retainer Retainer` field and a
`SetRetainer(r)` method. Inside `runCheckpoint`, after the marker is
durable and the FPI epoch is reset, the retainer (if any) is invoked
with `endLSN`. nil disables pruning entirely (the v0 default for
tests that construct a checkpointer in isolation).

### `internal/initdb/open.go` — `Slots` on `Runtime`

`OpenSlots` is now driven from `initdb.Open` so the production binary
sees the same registry the wire-layer replication handlers do. The
registry was previously only opened ad-hoc in tests; replication
slots were therefore unusable in the production binary. This loop
fixes that drive-by.

`Runtime` carries the new `Slots *wal.Slots` handle so callers (the
server config, the retainer in `cmd/goopg start`) can grab it without
re-opening.

### `cmd/goopg/main.go` — wire-up

After the existing GUC fan-out (checkpoint timeout, max_wal_size,
completion target, FPW), the start command now:

- Threads `rt.Slots` and `rt.WAL` into `server.Config` so
  `START_REPLICATION` and friends actually work in the production
  binary, and so `IDENTIFY_SYSTEM` reports a real `xlogpos`.
- Builds a `SlotAwareRetainer` keyed off the `max_slot_wal_keep_size`
  GUC (stored in MB; -1 sentinel disables the cap) and installs it on
  the checkpointer.

## Tests

`internal/wal/retention_test.go` covers:

- `TestRemoveOldSegmentsKeepsContainingSegment` — the segment holding
  the keep-LSN survives.
- `TestRemoveOldSegmentsZeroLSNIsNoop` — fresh-startup guard: zero
  keep-LSN doesn't drop everything.
- `TestRemoveOldSegmentsClosesCachedHandle` — open file handles are
  released and the inode is really unlinked.
- `TestSlotsInvalidateLagging` — lag-based eviction flips multiple
  slots, drops them from `MinRestartLSN`, and round-trips through
  `OpenSlots`.
- `TestSlotsInvalidateLaggingHonoursBoundary` — strict-`>` semantics:
  lag exactly equal to the bound is preserved.
- `TestSlotsInvalidateLaggingDisabled` — `max_slot_wal_keep_size <= 0`
  truly disables the cap.
- `TestSlotAwareRetainerPrunesBelowSlotHorizon` — end-to-end: a slot
  pins segments at and after its `RestartLSN`; everything older is
  removed.
- `TestSlotAwareRetainerInvalidatesAndPrunes` — a slot lagged past
  the cap is flipped, then retention falls back to the checkpoint
  horizon and prunes through where the slot used to anchor.

## Out of scope

- **Recycling-by-rename** (the `min_wal_size` "preallocated reserve"
  upstream maintains). v0 deletes the file. The next time the writer
  needs space it `O_CREATE`s fresh. Adding the rename pool is a
  size-tuning optimisation, not a correctness one — `min_wal_size`
  remains in the registry as a SHOW/SET-only GUC.
- **`pg_wal/archive_status/`** integration. There's no archive_command
  yet; nothing creates `.ready` markers, nothing requires `.done`
  presence before unlink. Adds in M0005 follow-ups.
- **In-flight walsender awareness.** A walsender currently streaming
  the very segment we're about to unlink would be reading from an
  already-open file descriptor; on Linux the inode survives until the
  fd closes, so the in-flight stream completes. But a *reconnect*
  against an invalidated slot will fail. Surfacing this as a clean
  protocol-level error message (with hint: "rebuild standby from
  base backup") is left for the observability loop alongside
  `pg_stat_replication`.
- **Slot-name validation on Invalidated slots.** Today an invalidated
  slot can still be advanced via `AdvanceConfirmedFlushLSN`; the
  advance succeeds in-memory but the slot stays invalidated. A future
  walsender startup-time check should refuse to bind a walsender to
  an invalidated slot. Tracked in
  `0005-0003-replication-observability.md`.

## Cross-references

- Milestone:
  [`docs/milestones/0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md).
- Streaming-replication architecture:
  [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md)
  (this doc fills in the "WAL retention" hook the architecture doc
  declared).
- Checkpointing:
  [`0002-0001-checkpointing.md`](0002-0001-checkpointing.md).
