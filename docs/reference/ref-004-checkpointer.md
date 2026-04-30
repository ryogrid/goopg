# REF-004: Checkpointer

…(existing content through "Key Differences" unchanged)…

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
