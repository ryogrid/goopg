# Design: WAL Group Commit (M0098-0002)

**Status**: accepted  
**Milestone**: M0098-0002 — WAL group commit  
**Expected gain**: 8–15× TPS for Simple Update; 5–10× for Standard

## Problem

Every transaction commit calls `walWriter.FlushUpTo(lsn)` which:
1. Sends one `op{kind: opFlush}` to the WAL writer's serialized `ops` channel
2. The writer goroutine handles ONE flush at a time: drain buffer + fdatasync + notify caller
3. At -c 100, 100 concurrent commits trigger 100 sequential fsyncs
4. Baseline (M0098-0001): Standard 229 TPS, Simple Update 228 TPS at -c 100

The baseline profile shows 424 ms average latency — consistent with per-transaction
fsync serialization. PostgreSQL batches N concurrent flush requests into one fdatasync,
delivering 7,882 TPS for Simple Update vs goopg's 228 TPS at -c 100.

## Design

### Core idea: shared flush queue + batching in writer goroutine

**Caller side** (`FlushUpTo`):
1. Append a `groupFlushReq{lsn, done chan struct{}}` to a shared `flushGroup.queue`
   (protected by `flushGroup.mu`)
2. Non-blocking send to `flushGroup.signal` (capacity-1 channel) to wake the writer
3. Block on `<-req.done`

**Writer goroutine**:
- `select` on both `ops` (appends/recycles/close) and `flushGroup.signal`
- When signal fires: snapshot and clear `flushGroup.queue` under lock, compute `maxLSN`
  across all requests, call `flushUpTo(maxLSN)` once, then `close(req.done)` for each

### Why a 1-buffered signal channel is safe

Invariant: the signal means "there's at least one entry in the queue."

- If writer is idle: signal wakes it, writer drains queue (may be N entries if N concurrent)
- If writer is processing: signal queues (capacity=1), writer loops and picks it up after
  finishing the current batch — which already includes any requests that arrived
  before the drain snapshot
- If writer already has a signal pending: non-blocking send drops the duplicate (safe —
  the pending signal will cause the writer to drain, including the new entry)

### LockOSThread

`runtime.LockOSThread()` in the writer goroutine pins it to one OS thread, eliminating
goroutine-migration overhead across the fdatasync hot path. This also prevents GC STW
preemption from delaying the fsync.

### Thread safety of req.err

The writer sets `req.err` before `close(req.done)`. The caller reads `req.err` after
`<-req.done`. `close` provides a Go happens-before guarantee, so this is race-free.

## Files changed

| File | Change |
|------|--------|
| `internal/wal/writer.go` | Add `groupFlushReq`, `flushGroup` types; modify `Writer`, `state`; replace `FlushUpTo`; extend `state.loop` with select on flush signal + `LockOSThread` |
| `docs/design/README.md` | Index entry |

## Backward compatibility

- `opFlush` case kept in ops channel loop (legacy path, not used by new FlushUpTo)
- `Append`, `RecordIterator`, checkpointer unaffected
- WAL segment file format unchanged
- Existing unit tests for WAL append/flush continue to pass
