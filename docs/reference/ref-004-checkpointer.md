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

## PostgreSQL Implementation (Deep Dive)

### Checkpoint WAL Record Format

PostgreSQL writes two types of checkpoint records:

- **XLOG_CHECKPOINT_SHUTDOWN** — written during a clean shutdown.
  Indicates that no recovery is needed.
- **XLOG_CHECKPOINT_ONLINE** — written during normal checkpoints.
  Contains `nextXid`, `nextOid`, `nextMulti`, `nextMultiOffset`,
  `oldestXid`, `oldestXidDB`, `oldestMulti`, `oldestMultiDB`,
  `time`, `oldestActiveXid`.

The checkpoint record size is ~120 bytes. goopg writes a single
marker type with xid + timestamp.

### Restart Points on Standby

On a hot standby, the checkpointer is replaced by the **restart
point** mechanism. The startup process (which performs WAL
replay) periodically creates restart points — analogous to
checkpoints on the primary. Restart points allow the standby to
truncate WAL that has been fully replayed.

goopg does not implement restart points on standbys.

### XLOG_RESTORE_POINT

PostgreSQL supports `pg_create_restore_point('name')` which
writes an `XLOG_RESTORE_POINT` WAL record. This can be used for
point-in-time recovery.

goopg does not support restore points.

### Checkpoint Speed Control

PostgreSQL controls checkpoint I/O via:
- `checkpoint_completion_target` (default 0.9) — spread checkpoint
  writes over 90% of the checkpoint interval.
- `CheckpointerWriteDelay` — sleep between writes during
  checkpoint (default 200 ms).

goopg's checkpointer uses `FlushAllPaced` which writes all dirty
buffers without pacing.

## goopg Improvement Analysis

### P2: Checkpoint Write Pacing

Add a pacing delay between buffer flushes during checkpoints.
Spread the I/O over `completionTarget × interval` seconds.

**Impact:** Reduces I/O spikes during checkpoints.

### P3: XLOG_RESTORE_POINT

Implement `pg_create_restore_point` for point-in-time recovery.

## References

- goopg: `internal/wal/checkpointer.go`
- PG checkpointer: `postgres/src/backend/postmaster/checkpointer.c`
- PG checkpoint WAL: `postgres/src/include/catalog/pg_control.h`
