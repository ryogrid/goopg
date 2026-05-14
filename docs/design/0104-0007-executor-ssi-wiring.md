# 0104-0007 — Executor SSI Hook Wiring

**Status:** accepted (M0104-0007 executor-wiring slice landed 2026-05-14)
**Date:** 2026-05-14
**Milestone:** M0104
**Tracks:** `.ralph/fix_plan.md` M0104-0007
**Upstream oracle:** `postgres/src/backend/storage/lmgr/predicate.c`
(`CheckForSerializableConflictOut`, `CheckForSerializableConflictIn`,
`PreCommit_CheckForSerializationFailure`),
`postgres/src/backend/access/heap/heapam.c` (call sites),
`postgres/src/backend/access/transam/xact.c`
(`CommitTransaction` ordering).

## Problem

M0104-0001..0006 built the SSI substrate at the `internal/mvcc` layer:

- `SerializableXact` lifecycle registry, dense `CommitSeqNo` allocation.
- Predicate-lock (SIREAD) targets with relation/page/tuple granularity
  and a holder→target inverted index that supports automatic coarsening.
- Read-path `CheckForSerializableConflictOut` and write-path
  `CheckForSerializableConflictIn` hooks that install symmetric rw-edges.
- `PreCommitCheckForSerializationFailure` walking the conflict graph,
  dooming pivots on 2-cycle write-skew and 3-cycle generic shapes, and
  returning `*SerializationFailureError` (SQLSTATE 40001) for the
  committing xact.

None of this fires unless the executor *calls* the hooks. Without
executor wiring a SERIALIZABLE workload runs identical to RR: the
registry is populated on `Begin`, the predicate-lock map stays empty,
no rw-edge is ever installed, and the pre-commit walk has nothing to
scan. M0104's DoD ("at least one known serializable anomaly pattern
deterministically rejected with SQLSTATE 40001") therefore requires
the executor-side wiring this slice lands.

## Goals

1. Add a thin executor-side abstraction layer over the mvcc hooks that:
   - Short-circuits to zero footprint for RC/RR readers/writers/commits.
   - Filters tag inputs so the underlying `mvcc.TupleLockTag` invariants
     never panic at the hook site.
   - Wraps `mvcc.SerializationFailureError` as `*ExecError{Code:"40001"}`
     so the wire layer surfaces the upstream SQLSTATE for the
     "could not serialize access due to read/write dependencies among
     transactions" phrasing.

2. Wire the read-path hook into every SERIALIZABLE tuple-emission site
   that goopg exposes as a user-facing scan:
   - `seqScanOp.Next` (post-visibility, before decode).
   - `indexScanOp.Next` (post-HOT-resolution, after release of the page
     RLock).

3. Wire the write-path hook into every SERIALIZABLE tuple-modification
   site:
   - `insertOp.Next` for both the non-partitioned path and the
     partition-routed path.
   - `updateOp.Next` for both the HOT-update fast-path and the
     non-HOT (`PageSetHeapTupleXmax` + `writeHeapRow`) path.
   - `deleteOp.Next` after the xmax stamp succeeds.

4. Wire the pre-commit walk into `transactionOp.execCommit`, BEFORE
   `TxnMgr.Commit`, so a doomed transaction never burns a commit
   record and the executor surfaces SQLSTATE 40001 with a rollback
   side-effect that releases the registry, predicate-lock holdings,
   and session explicit-transaction state.

## Non-Goals

- Visibility-check sites outside user-facing reads — FK enforcement,
  DDL scans, `ANALYZE`, MERGE, ON CONFLICT, apply-worker, TOAST chunk
  fetch. These call `mvcc.TupleVisible(Subxact)` but their purpose is
  out-of-band introspection; predicate-locking them would inflate the
  SIREAD set on every internal MVCC reader. Deferred to a follow-up
  loop if/when isolation-test promotion surfaces a missing edge.
- Index-target predicate locks (PG predicate-locks the index page when
  a btree scan crosses a range boundary, so phantoms inserted in the
  same key range trigger conflict-in even though no heap tuple was
  matched by the read). Without this, write-skew anomalies that hinge
  on phantom insertions are NOT caught. Tracked separately under
  M0104-0008 / future slice; the heap-grain wiring this slice lands is
  sufficient for the M0104 DoD pattern (2-cycle on shared rows).
- Executor-layer end-to-end isolation tests that drive the full SQL
  pipeline through two concurrent sessions. The wiring-contract tests
  this slice ships use direct `mvcc.Manager` + `executor.Context`
  construction; the multi-session SQL-driven tests are part of
  M0104-0008 (oracle isolation-test promotion).
- `OnConflict_CheckForSerializationFailure` per-edge synchronous
  check (PG fires it inline from `FlagRWConflict`). The pre-commit
  scan is sufficient for M0104 DoD; per-edge can be layered on later
  via the polarity-agnostic `registerRWConflictLocked` helper.

## API surface

A new file `internal/executor/ssi.go` exports three helpers:

```go
func ssiActive(ctx *Context) bool
func ssiRecordTupleRead(ctx *Context, rel storage.RelFileNode,
                        block storage.BlockNumber, slot uint16,
                        writerXmin storage.TransactionID)
func ssiRecordTupleWrite(ctx *Context, rel storage.RelFileNode,
                         block storage.BlockNumber, slot uint16)
func ssiPreCommitCheck(ctx *Context, tx mvcc.Transaction) error
```

`ssiActive` is the central gate — all hooks short-circuit on it. The
guard is `ctx != nil && ctx.TxnMgr != nil && ctx.Tx.Isolation ==
mvcc.IsolationSerializable && ctx.Tx.Handle != 0`. RC, RR, bootstrap
contexts that lack a `TxnMgr`, and SERIALIZABLE contexts whose
`Begin`-time handle hasn't been propagated (e.g. ANALYZE running
outside an explicit transaction) all exit in the inline comparison.

`ssiRecordTupleRead` performs two `mvcc.Manager` calls:
1. `AcquirePredicateLock(handle, TupleLockTag(...))` — installs the
   SIREAD lock for this read at tuple grain. Future SERIALIZABLE
   writers touching the same tuple, the covering page, or the
   covering relation see this read.
2. `CheckForSerializableConflictOut(handle, writerXmin)` — installs
   an R→W rw-edge if `writerXmin` identifies a concurrent SERIALIZABLE
   writer. No-op for invalid/bootstrap/frozen, self, or finished
   writer XIDs (the mvcc layer filters internally).

`ssiRecordTupleWrite` performs one `mvcc.Manager` call:
- `CheckForSerializableConflictIn(handle, TupleLockTag(...))` — walks
  the predicate-lock holder set on the exact tag and on every covering
  ancestor (`tuple → page → relation`) and installs R→W rw-edges
  against every concurrent SERIALIZABLE reader that holds a covering
  lock. Same edge orientation, same idempotence semantics, same
  in/outConflicts slices as the read-path discovery — a 2-cycle
  detected from either side surfaces the same graph to the pre-commit
  walker.

Both helpers filter `block == InvalidBlockNumber || slot == 0`
*before* `mvcc.TupleLockTag` is reached, because that constructor
panics on those invariants by design (the SIREAD contract requires
that callers commit to a granularity at construction). The executor
hook sites can produce `slot == 0` in the seqScan `curSlot - 1` path
when the very first emission of a page is skipped, so the filter is
load-bearing, not defensive.

`ssiPreCommitCheck` calls
`mvcc.Manager.PreCommitCheckForSerializationFailure(handle)`. The
returned error (a `*mvcc.SerializationFailureError`) is wrapped as:

```go
&ExecError{
    Code:    "40001",
    Message: "could not serialize access due to read/write " +
             "dependencies among transactions: " + err.Error(),
}
```

The upstream message prefix is reused verbatim so test scaffolding
written against PG error output (`errordetail`) can recognise the
goopg variant. The mvcc layer's `Reason` field is appended so the
canonical "Canceled on identification as a pivot, during commit
attempt" detail string surfaces through the wire layer.

## Wiring details by site

### Read path: `seqScanOp.Next`

After `TupleVisibleSubxact` passes, `curSlot` has already been
post-incremented past the just-fetched slot (`o.curSlot - 1` is the
slot that produced this tuple). The hook fires before decode so the
SIREAD lock is installed even on tuples whose decode fails (we still
read the tuple bytes; PG predicate-locks every visible tuple touched
by a scan, decode failure is post-read).

### Read path: `indexScanOp.Next`

`followHOTChainNoCopy` already resolves the HOT chain under the page
RLock; the returned `actualSlot` is the live slot the predicate lock
must target (not the index-pointed slot, which may have been pruned).
The hook fires after the RLock is released and the page is unpinned
so the mvcc lock-acquisition does not pile on top of the buffer-pool
latch — the lock graph stays flat.

### Write path: `insertOp.Next`

After `writeHeapRowReturning` returns the new tuple's `ItemPointer`,
`ssiRecordTupleWrite(ctx, rel, ptr.Block, ptr.Offset)` runs before
`maintainUniqueIndexesForInsert`. Both the partition-routed and the
non-partitioned branches are covered. For an INSERT the per-tuple
holder set is guaranteed empty (the tuple is brand new) but covering
page/relation holders are still found, which is the canonical
phantom-insertion catch.

### Write path: `updateOp.Next`

The HOT-update fast-path (`tryApplyHOTUpdate` returns `used == true`)
overwrites the tuple in place; `pu.slot` is the SLOT that held the
prior visible version. The non-HOT path stamps xmax on `pu.slot` then
inserts a new tuple via `writeHeapRow`. In both cases the rw-conflict
target is the OLD slot a concurrent SERIALIZABLE reader would have
predicate-locked.

The hook fires from a single site at the end of the per-pending-update
iteration (`if !epqSkipSeq { ssiRecordTupleWrite(...) }`) — *after*
the EPQ retry loop has converged on a final `pu.slot`. That ordering
matters because EPQ chain-following may swap `pu.slot` to a newer HOT
descendant; firing inside the loop would target a stale slot and
trigger a false rw-edge.

### Write path: `deleteOp.Next`

Mirrors `updateOp`: the hook fires after the `epqSkipDel` filter so
EPQ-skipped victims (concurrent abort + RC chain-follow lost) do not
register a rw-edge for a write that never actually happened.

### Commit path: `transactionOp.execCommit`

The pre-commit check fires before `TxnMgr.Commit` and after the
deferred-FK check. The ordering matters:

1. **Deferred FK first** — if the FK check fails, the executor rolls
   back via `TxnMgr.Rollback` and the SerializableXact is released
   normally (no spurious 40001).
2. **SSI pre-commit second** — if a dangerous structure is detected,
   the executor performs an immediate `TxnMgr.Rollback`,
   `EndExplicitTransaction`, `clearCtxTransaction`, and surfaces
   `*ExecError{Code:"40001"}`. The session exits the explicit-tx state
   with the same semantics as a regular ROLLBACK.
3. **TxnMgr.Commit third** — runs only when no abort condition holds.

The SSI check is gated on `tx.Isolation == IsolationSerializable` and
`tx.Handle != 0` so the RC/RR commit cost is one inline comparison.

## Tests

`internal/executor/ssi_test.go` (8 pins):

| Test | Pins |
|---|---|
| `TestSSI_RecordTupleRead_NoOpForRC` | Zero footprint for RC: SerializableXact lookup returns nil after a synthetic read |
| `TestSSI_RecordTupleRead_NoOpForRR` | Zero footprint for RR (read + write helpers both inert) |
| `TestSSI_RecordTupleRead_AcquiresPredicateLockForSerializable` | Reader → Writer rw-edge surfaces after the read-path acquire + a peer SERIALIZABLE write through the write-path helper |
| `TestSSI_RecordTupleRead_InvalidTagFiltered` | (InvalidBlockNumber,*) and (*,0) inputs absorbed pre-`TupleLockTag` (no panic) |
| `TestSSI_RecordTupleRead_ZeroHandleSkipped` | `Handle == 0` (no-explicit-tx) skips the registry probe even when isolation is SERIALIZABLE |
| `TestSSI_ExecCommit_ReturnsSerializationFailureWhenDoomed` | End-to-end execCommit path: `MarkDoomedForTest` + COMMIT → `ExecError{Code:"40001"}` + session state cleared + ActiveCount == 0 |
| `TestSSI_ExecCommit_NoOpForRC` | RC commit unaffected by the SSI wiring |
| `TestSSI_ExecCommit_HappyPathForSerializable` | SERIALIZABLE without conflicts commits cleanly (pre-commit walk does not spuriously fail) |

The end-to-end SQL-driven write-skew test (two concurrent sessions,
both SELECT/INSERT, one's COMMIT raises 40001) is staged for the
M0104-0008 oracle isolation-test promotion slice — that's where the
isolation harness already lives.

## Risks & follow-ups

- **Index-target predicate locks** — the heap-grain wiring this slice
  ships does not catch phantom-insertion anomalies on btree range
  scans. Filed for M0104-0008 / index-scan slice; the M0104 DoD
  pattern (2-cycle write-skew on shared heap rows) is covered.
- **FK / DDL / ANALYZE / MERGE / TOAST visibility paths** — these
  call `TupleVisible(Subxact)` but are not user-facing reads. If a
  promoted isolation test surfaces a missing rw-edge, the helper
  pattern from `ssi.go` lifts cleanly into those sites.
- **Per-edge dooming** — upstream's
  `OnConflict_CheckForSerializationFailure` fires inline from
  `FlagRWConflict`. The pre-commit-only path goopg uses today catches
  every dangerous structure the M0104 DoD requires, but pays a higher
  abort cost (the doomed xact runs to completion before noticing).
  The `registerRWConflictLocked` helper is the natural injection
  point if/when latency-sensitive workloads need the earlier abort.
