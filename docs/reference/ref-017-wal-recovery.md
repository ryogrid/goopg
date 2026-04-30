# REF-017: WAL Redo / Crash Recovery

…(existing content through "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Redo Point vs Checkpoint LSN

PostgreSQL's checkpoint record contains a `Redo` pointer that
indicates the LSN where recovery must start replaying. The
checkpoint itself may have been written at a later LSN. This
allows the checkpointer to skip redo for pages that were flushed
before the Redo point.

goopg does not distinguish between the checkpoint LSN and the
redo start point.

### FPI-Based Idempotency

PostgreSQL's `pd_lsn` field on each data page contains the LSN
of the most recent WAL record that modified the page. During
recovery, `ApplyRedoRecord` checks `pd_lsn` against the WAL
record's LSN. If `pd_lsn ≥ rec.lsn`, the page is already up
to date and the record is skipped.

This makes recovery fully idempotent: replaying the same record
twice is harmless because the second replay finds `pd_lsn ≥
rec.lsn` and skips.

goopg does not check `pd_lsn` during replay. Idempotency relies
on the record-level CRC and the assumption that each record is
applied exactly once.

### Recovery Conflicts

On a hot standby, WAL replay can conflict with active queries:
- **Snapshot conflict** — a query's snapshot is too old to see
  the tuples being removed by VACUUM on the primary.
- **Tablespace conflict** — a tablespace is being dropped.
- **Lock conflict** — a query holds a lock that replay needs.

PostgreSQL resolves conflicts by cancelling the conflicting
query with a message like "terminating connection due to
conflict with recovery".

goopg does not have a hot standby query path, so recovery
conflicts do not occur.

### Parallel Redo (PG 18+)

PostgreSQL 18 adds parallel redo: multiple worker processes can
replay WAL records in parallel for different tablespaces. This
significantly reduces recovery time for large databases.

goopg replays records serially.

### WAL Summariser (PG 17+)

The WAL summariser tracks which WAL segments are no longer needed
for recovery by monitoring the gap between the checkpoint redo
LSN and the oldest outstanding WAL record. It generates summary
files that allow recovery to skip over unused WAL segments.

goopg does not summarise WAL; it replays from the checkpoint
marker forward, scanning all segments.

## goopg Improvement Analysis

### P1: pd_lsn-Based Replay Idempotency

Check `pd_lsn` before applying a WAL record. If `pd_lsn ≥
rec.lsn`, skip the record.

**Impact:** Correctness for crash-recovery idempotency. Also
enables recovery to restart from an arbitrary point.

### P2: Parallel Redo

Process WAL records in parallel when they touch different
relations. Use a shared hash table of in-progress relation IDs
to detect conflicts.

**Impact:** Faster recovery for multi-table workloads.

## References

- goopg: `internal/wal/recovery.go`
- goopg: `internal/wal/stream_replayer.go`
- PG recovery: `postgres/src/backend/access/transam/xlogrecovery.c`
- PG redo: `postgres/src/backend/access/transam/README`
- PG FPI: `postgres/src/backend/access/transam/xlog.c`
  (`XLogRecordPageWithLock`)
