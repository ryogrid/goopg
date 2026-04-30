# REF-004: Checkpointer

## Overview

The checkpointer periodically writes dirty buffers to disk and advances the redo point so that crash recovery does not replay WAL from the beginning of time. It also manages WAL segment retention (removing segments that are no longer needed for recovery or replication).

## goopg Implementation

**Package:** `internal/wal/checkpointer.go`

### Key Types

- `Checkpointer` — controls checkpoint timing (interval, volume-based triggers) and coordinates with the WAL writer.
- `Config` — `Interval`, `MaxWALBytes`, `CompletionTarget`.

### Checkpoint Cycle

```
Checkpointer.Run(ctx)
  ├─ ticker fires (every Interval)
  ├─ runCheckpoint(ctx, scheduled)
  │    ├─ FlushAllPaced — write all dirty buffers
  │    ├─ FlushUpTo — flush WAL
  │    ├─ WriteCheckpointMarker — append XLOG_CHECKPOINT marker to WAL
  │    ├─ RetainSegments — remove old WAL segments
  │    └─ Update redo pointer
  ├─ volume-based trigger (when MaxWALBytes is exceeded)
  └─ sleep until next tick
```

### WAL Segment Retention

`RetainSegments` keeps WAL segments that are still needed:
- Segments after the last checkpoint LSN.
- Segments pinned by replication slots (`slot.RestartLSN`).
- Segments within a safety margin (`MaxSlotWALKeepBytes`).

Segments that are no longer needed are unlinked.

### Checkpoint Marker

The checkpoint marker is a WAL record (`RmgrCheckpoint`) written after all dirty buffers and WAL have been flushed. It records the redo point (the LSN at which recovery should start replaying). On restart, `wal.ReplayFromDir` replays from the last checkpoint marker forward.

## PostgreSQL Implementation

PostgreSQL's checkpointer (`checkpointer.c`) is a dedicated process:

- **Bgwriter separation** — PostgreSQL has a separate `bgwriter`
  process that flushes dirty buffers continuously, so the
  checkpointer's I/O burst is smaller. goopg combines both roles
  in `FlushAllPaced` (called during checkpoint and during buffer
  eviction).
- **Checkpoint frequency** — controlled by `checkpoint_timeout`
  (default 5 min) and `max_wal_size` (default 1 GB). goopg uses
  a configurable `Interval` (default 5 min) and `MaxWALBytes`.
- **Restart point** — on standbys, the checkpointer (called the
  "restart point") is driven by the WAL receiver's progress.
  goopg does not have a standby-mode checkpointer.
- **Checkpoint WAL record** — PostgreSQL writes
  `XLOG_CHECKPOINT_SHUTDOWN` (clean shutdown) or
  `XLOG_CHECKPOINT_ONLINE` (normal checkpoint). goopg writes a
  single marker type.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Dirty buffer write | `FlushAllPaced` in-band | bgwriter + checkpointer |
| WAL retention | Slot-aware | Slot-aware + wal_keep_segments GUC |
| Checkpoint marker | Single type | SHUTDOWN / ONLINE |
| Standby checkpoints | Not implemented | Restart points on standby |

## References

- goopg: `internal/wal/checkpointer.go`
- PG checkpointer: `postgres/src/backend/postmaster/checkpointer.c`
- PG bgwriter: `postgres/src/backend/postmaster/bgwriter.c`
