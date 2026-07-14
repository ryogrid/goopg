# Design: WAL Group Commit Batching Policy (M0099-0003)

**Status**: superseded (2026-07-12) by
[`docs/design/wal-backend-flush/`](wal-backend-flush/) — the hardcoded
1000 µs / 5-sibling batching delay on the writer loop was replaced by the real
`commit_delay` / `commit_siblings` GUCs (PG defaults 0 / 5), applied as a
holder-only sleep on the backend flush path.  
**Milestone**: M0099-0003  
**Filed**: 2026-05-12

## Background

M0098-0002 landed WAL group commit: `FlushUpTo` appends a `groupFlushReq` to
`flushGroup.queue`, signals via a 1-buffered channel, and the writer goroutine
drains the entire queue in one `fdatasync`. This delivered ~2× TPS improvement
for write workloads (229 → 443 TPS standard; 228 → 420 TPS simple update).

The targets (1,500 / 1,500 TPS) are still 3.4× / 3.6× away. The remaining gap
is partly that current group commit does not accumulate enough concurrent
requestors per `fdatasync` under the arrival pattern of 100 pgbench clients.

## Problem

The current `handleGroupFlush` implementation wakes up immediately when the
first `FlushUpTo` call signals `flushGroup.signal`. If only 1–3 transactions
have queued their requests at that instant, the `fdatasync` is wasted on a small
batch. PostgreSQL addresses this via `commit_delay` (µs sleep before flush) +
`commit_siblings` (minimum concurrent backends before sleeping), defined in
`postgres/src/backend/access/transam/xact.c` (`TransactionGroupUpdateXidStatus`).

## Analysis of arrival pattern

At 420 TPS and a 1ms average transaction latency, the average inter-arrival
gap between `FlushUpTo` calls is ~2.4 ms. A single `fdatasync` on NVMe takes
~0.1–0.5 ms. Without a batching delay, each `fdatasync` serves 1–2 concurrent
requestors on average, providing limited group-commit benefit.

A commit_delay of 0.5–2 ms would allow 5–20 additional transactions to arrive
and queue before the flush, multiplying batch size without adding meaningful
latency for the median transaction.

## Proposed Design

### Batching policy parameters (GUC-aligned)

| Parameter | Default | Meaning |
|-----------|---------|---------|
| `commit_delay_us` | 1000 µs | How long to sleep after the first waiter before flushing |
| `commit_siblings` | 5 | Minimum concurrent waiters required before sleeping; if fewer, flush immediately |

These mirror PostgreSQL's `commit_delay` / `commit_siblings` semantics
(`postgres/src/backend/utils/misc/guc.c`).

### handleGroupFlush modification

```go
func (s *state) handleGroupFlush() {
    s.fg.mu.Lock()
    queue := s.fg.queue
    s.fg.queue = s.fg.queue[:0]
    s.fg.mu.Unlock()

    if len(queue) == 0 {
        return
    }

    // Batching: if enough concurrent waiters, sleep briefly to accumulate more
    if len(queue) >= commitSiblings && commitDelayUs > 0 {
        time.Sleep(time.Duration(commitDelayUs) * time.Microsecond)
        // Drain any new arrivals during sleep
        s.fg.mu.Lock()
        queue = append(queue, s.fg.queue...)
        s.fg.queue = s.fg.queue[:0]
        s.fg.mu.Unlock()
    }

    // Find max LSN and flush once
    maxLSN := LSN(0)
    for _, req := range queue {
        if req.lsn > maxLSN { maxLSN = req.lsn }
    }
    err := s.flushUpTo(maxLSN)
    for _, req := range queue {
        req.err = err
        close(req.done)
    }
}
```

### Default values rationale

- `commit_delay_us = 1000 µs` (1 ms): At 420 TPS a 1 ms wait should accumulate
  ~0.4 additional transactions per µs of delay. 1 ms yields ~20 extra concurrent
  requestors per flush, dramatically increasing batch size.
- `commit_siblings = 5`: Only apply the delay when at least 5 concurrent
  transactions are already waiting; avoids adding latency to bursty but sparse
  workloads.

### Configuration surface

Initially hard-coded constants `commitSiblings` and `commitDelayUs` in
`internal/wal/writer.go`. Can be promoted to GUCs in a later loop if needed
for operator tuning.

## Correctness

- `fdatasync` is still called for every batch (durability preserved).
- A batch that fails `fdatasync` propagates the error to all queued requestors
  (same as the current single-requestor path).
- The sleep is implemented via `time.Sleep` (not `select`), which is acceptable
  since `handleGroupFlush` runs on the writer goroutine pinned to an OS thread
  (`runtime.LockOSThread` — M0098-0002).

## Interaction with evictMu fix (M0099-0002)

The WAL batching improvement is orthogonal to the buffer pool fix. Both should
be implemented independently and their combined effect measured in M0099-0005.

## Expected Impact

- `fdatasync` calls per second: from ~420/s → ~21/s (if batching 20 per flush).
- Write TPS: expected 2–4× additional gain on top of M0098-0002 levels.
  Target: reach 1,500 TPS standard / 1,500 TPS simple update.
- Latency: median transaction latency increases by at most commit_delay_us (1 ms)
  for write transactions; read-only transactions unaffected (no WAL flush).

## Files to Modify

| File | Change |
|------|--------|
| `internal/wal/writer.go` | `handleGroupFlush` + batching constants |
| `internal/wal/writer_test.go` | Test batch accumulation |
| `docs/design/README.md` | Index entry |
