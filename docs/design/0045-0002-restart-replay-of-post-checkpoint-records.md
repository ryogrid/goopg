# 0045-0002 — Restart Replay of Post-Checkpoint WAL Records

**Status:** accepted
**Parent milestone:** M0045
**Date:** 2026-05-04

## 1. Objective

After M0045-0001 unblocks the segment-0 guard, define the recovery
model that fires between **goopg startup** and **server-ready**:
post-last-checkpoint WAL records may or may not have made it to
the data files before the kill, so they must be replayed against
the buffer pool to bring the cluster back to a consistent state.

## 2. The recovery model

Goopg's WAL-before-data invariant (see
`internal/wal/checkpointer.go` and the
WAL-write rules in `internal/storage/bufpool.go`) gives us:

- **Pre-checkpoint records** (LSN < lastCheckpointLSN):
  every page they touch has been flushed to disk by the
  checkpoint that closed that LSN range. Replaying them is a
  no-op (every flushed page already has an LSN ≥ the record's
  LSN, so the replayer skips them) — but it's also wasteful, so
  we **don't replay them**. We start from `lastCheckpointLSN`.
- **Post-checkpoint records** (LSN ≥ lastCheckpointLSN):
  may or may not have reached disk depending on whether the
  buffer pool flushed those individual pages before the kill.
  We **must replay them** to ensure the buffer pool's view is
  consistent with the WAL stream's view. Idempotency of
  `StreamReplayer.Run` guarantees this is safe even if some
  records' effects already landed (they become no-ops).

## 3. Reuse `StreamReplayer.Run`

`internal/wal/stream_replayer.go::StreamReplayer.Run` is the
existing replay driver — it powers replication standby
catch-up. Properties we rely on:

- **Idempotent** — applying the same record twice produces the
  same buffer-pool state. Replication standbys regularly re-apply
  the trailing WAL after a restart; this property is well-tested.
- **Skip-if-newer** — records targeting a page whose on-disk LSN
  is ≥ the record's LSN are skipped. (Concrete behaviour lives in
  the per-record-type `apply*` handlers; the replayer trusts each
  handler to do the right thing.)
- **Lossless when caught up** — when the iterator returns no more
  records, `Run` returns nil and `ApplyLSN()` reports the highest
  applied LSN.

The recovery driver is a thin wrapper:

```go
func recoverFromWAL(mgr *storage.Manager, walDir string,
    segSize int64, lastCkptLSN uint64) (uint64, error) {
    iter, err := wal.NewRecordIterator(nil, walDir, segSize, lastCkptLSN)
    if err != nil {
        return 0, err
    }
    defer iter.Close()
    sr := wal.NewStreamReplayer(mgr, lastCkptLSN)
    if err := sr.Run(context.Background(), iter); err != nil {
        return 0, err
    }
    return sr.ApplyLSN(), nil
}
```

`NewRecordIterator` already accepts an arbitrary `startLSN`; the
existing replication path proves it works against partially-
truncated WAL streams (segments before `startLSN` are not read).

## 4. Where the recovery driver runs

`recoverFromWAL` runs in `cmd/goopg/main.go::runStart` (or
`internal/server/server.go::startBackends` — wherever the WAL
writer is wired up at startup) **after** `detectWritePos` returns
and **before** the listener accepts connections. Order:

1. `detectWritePos` reports `writePos` and `prevRecPtr` from the
   on-disk segment tail (M0045-0001 fix in effect).
2. `discoverLastCheckpointLSN` (M0045-0003) walks the retained
   WAL backwards to find the most recent checkpoint marker.
3. `recoverFromWAL` (this doc) replays
   `[lastCkptLSN, writePos)` against the buffer pool.
4. Once the listener binds, the cluster is ready.

If step 3 fails the binary exits with a clear error pointing at
the affected segment; the operator's recourse is `--reset` (data
loss) or manual investigation.

## 5. What replay actually does for the run-007 case

In the run-007 hard-kill, no checkpoint had completed since the
last retention pass (the kill occurred mid-Q9). What's on disk:

- Data files: contain everything up to the last checkpoint LSN
  (which is the same `keepLSN` retention used).
- WAL segments: contain everything from the last checkpoint LSN
  up to the kill point.

After the M0045-0001 fix, `writePos` reflects the kill point.
`recoverFromWAL` runs over `[lastCkptLSN, writePos)`. Because
HammerDB's power test was a read-only workload at the moment of
the kill, the WAL between `lastCkptLSN` and `writePos` is mostly
checkpoint markers, replication-slot bookkeeping, and any
auto-VACUUM / `commitGCEvery`-tagged transactions that fired
during the kill window. None of these change user data, so the
replay is effectively a no-op — but the *correctness* of letting
the server start back up is what matters.

## 6. Edge cases

- **No checkpoint marker in retained WAL**: M0045-0003 handles
  this. If the operator deleted segments by hand or the
  cluster was killed in the very first checkpoint cycle (so no
  checkpoint marker has ever been emitted), recovery fails with
  a clear error message rather than silently losing data.
- **Replay encounters a bad record**: `StreamReplayer.Run`
  surfaces the error; recovery aborts with the affected LSN. No
  partial-replay state is committed because the buffer pool's
  individual page-LSN tracking ensures every applied record
  bumps a page-LSN atomically with the page mutation.
- **WAL extends beyond `writePos`**: shouldn't happen after
  M0045-0001 (writePos is computed from `scanLastSegmentEnd`'s
  EOS sentinel) but `RecordIterator` already stops at EOS, so
  the iterator naturally bounds the replay range.

## 7. Verification

- Unit test in `internal/wal/recovery_test.go` (new file): seed a
  temp data dir with a synthesised WAL stream containing a
  checkpoint marker followed by N records; call
  `recoverFromWAL`; assert the buffer pool's reported
  `ApplyLSN()` matches the last record's end-LSN.
- Integration test (M0045-0004) covers the end-to-end kill path.
- `TestTPCHResultParity` — regression gate.

## 8. Out of scope for 0045-0002

- Discovering the last-checkpoint LSN (M0045-0003).
- The integration test (M0045-0004).
- Recovery from a partial / torn last record (existing
  `scanLastSegmentEnd` handles it; nothing changes).
- Any new WAL record types or on-disk format changes.
