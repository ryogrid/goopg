# WAL Performance Analysis and Optimisation Path

## Executive Summary

This report documents the investigation into goopg's write-path
performance (`UPDATE pgbench_accounts` accounts for 98% of
transaction time at ~74ms per call) and the experiments performed
to identify the bottleneck.

**Key finding after M0026 implementation:** Eliminating the WAL
state-loop round-trip via concurrent `tryAppend` did NOT improve
throughput (~14 TPS unchanged). The bottleneck is NOT WAL
serialisation — it is likely in the executor's heap tuple
modification or B-tree index path.

## Current Architecture (after M0026)

```
Client goroutine                     state.loop goroutine
─────────────────                    ──────────────────────
Writer.Append(payload)
  encode record
  lock(appendMu)
    buffer to walBuf (memcpy)
    update writeLSN
  unlock(appendMu)
  return LSN                          (no round-trip in common case)

Writer.FlushUpTo(lsn) ─────────────►  lock(appendMu)
                                        drain walBuf -> segments
                                      unlock(appendMu)
                                      fdatasync
  <- ok ---------------------------  reply
```

The fast path (`tryAppend`) eliminates the state-loop round-trip
for the common case (buffer not full). Despite this, throughput
remained at ~14 TPS — conclusively proving the bottleneck is
**not** the WAL Append serialisation.

## Bottleneck Reassessment

With WAL serialisation eliminated, the remaining suspect is the
executor's UPDATE implementation. The `UPDATE pgbench_accounts`
query goes through:

1. **Parse + Analyze + Plan** — < 0.01 ms
2. **B-tree index scan** for `aid = :aid` — reads 3-4 index pages
   (all buffer-pool hits after warm-up)
3. **Heap fetch** — read the tuple from the heap page
4. **Lock tuple** — acquire tuple-level lock
5. **Mark old tuple dead** — MVCC visibility update
6. **Insert new tuple** — new tuple version on the heap page
7. **WAL Append (2x)** — heap + index WAL records
   (fast path: ~0.01 ms)
8. **Transaction commit** — WAL Append for commit marker
   (fast path: ~0.01 ms)

Steps 2-3 are the same as SELECT (which runs in 0.3 ms).
Steps 5-6 are write-specific. At ~74ms total, one of these steps
accounts for ~73ms of unexpected latency.

### Hypotheses

1. **B-tree insert overhead** — The UPDATE modifies the index
   entry's pointer. If the B-tree insertion path has a bug or
   O(n) behaviour, it could dominate.

2. **Heap page slot management** — The heap modification functions
   might do full-page scans for free slots.

3. **Lock acquisition** — `LockTuple` might be serialising or
   doing I/O.

4. **WAL record encoding overhead** — The `encodeRecord` /
   `encodeRecordXLog` functions compute a CRC-32 over the payload.
   While fast, repeated calls add up.

### Next Steps

- Profile `UPDATE pgbench_accounts` with `go tool pprof` to see
  where the CPU time is spent.
- Add temporary latency logging around the executor's heap
  modification functions.

## Measurement Data (post-M0026)

| Workload | Clients | TPS | Latency (ms) |
|----------|---------|-----|-------------|
| Select-only | 1 | 3 224 | 0.31 |
| Select-only | 4 | 6 403 | 0.63 |
| Select-only | 16 | 5 900 | 2.71 |
| Simple update (-N) | 1 | 13.5 | 74.0 |
| Simple update (-N) | 4 | 13.8 | 290 |
| Default TPC-B | 1 | 13.1 | 76.4 |
| Default TPC-B | 4 | 13.9 | 288 |

The M0026 change (concurrent Append) did not affect throughput,
confirming the bottleneck is elsewhere.

## References

- Milestone M0025: `docs/milestones/0025-oltp-performance-analysis.md`
- Milestone M0026: `docs/milestones/0026-concurrent-wal-append.md`
- PostgreSQL WAL: `postgres/src/backend/access/transam/xlog.c`
- goopg WAL implementation: `internal/wal/writer.go`
