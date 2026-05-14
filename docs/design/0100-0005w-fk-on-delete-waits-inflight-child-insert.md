# 0100-0005w — Parent DELETE waits for in-flight child INSERT under FK; RR/Ser raises 40001

**Status:** accepted (2026-05-15, loop 38).
**Scope:** `internal/executor/operators_fk.go`, `internal/mvcc/manager.go`.
**Test pin:** `TestFKDelete_RR_RaisesSerializationOnConcurrentChildInsert`,
`TestFKDelete_RC_CompletesAfterConcurrentChildInsertCommit`
(`internal/server/fk_delete_waits_inflight_child_insert_test.go`);
`TestManagerHasAbortedXID` (`internal/mvcc/has_aborted_xid_test.go`).
End-to-end: `TestPort_IsolationFkSnapshot` flips from `defer` (4 of 7
permutations green) to PASS (all 7 permutations green).

## Problem

The two remaining `fk-snapshot.spec` permutations exercised parent
DELETE under FK with a concurrent child INSERT in another session:

```
permutation s2ip2 s1brr s1ifp2 s2brr s2dp2 s1c s2c     # CASCADE leg
permutation s2ip2 s1brr s1ifn2 s2brr s2dp2 s1c s2c     # SET NULL leg
```

Order of events for the CASCADE leg:

1. `s2ip2`  — s2 (autocommit) inserts `pk_noparted(2)`.
2. `s1brr`  — s1 begins REPEATABLE READ.
3. `s1ifp2` — s1 inserts the referencing child row `fk_parted_pk(2)`. No
   commit yet; the child row is invisible to anyone with a snapshot
   that lists s1.xid in InProgress.
4. `s2brr`  — s2 begins REPEATABLE READ.  Its snapshot has s1.xid in
   InProgress.
5. `s2dp2`  — s2 deletes the parent row `pk_noparted` WHERE a=2.

Upstream PG: the RI_FKey_cascade_del trigger uses a crosscheck snapshot
which sees the in-flight s1 INSERT and acquires SELECT FOR KEY SHARE on
that child row. The lock blocks. When s1 commits, in RR mode the
crosscheck snapshot now sees the freshly-committed child row whose xmin
> our xact's snapshot → `40001 could not serialize access due to
concurrent update`.

goopg before this loop: `enforceFKOnDelete → fkCascadeDelete → scanRelForMatch`
filters all scanned tuples through `mvcc.TupleVisibleSubxact`, which
correctly rejects the in-flight tuple. The CASCADE loop finds zero
victims and returns nil. The DELETE completes silently with no wait
state and no 40001 — both observability gaps in `fk-snapshot.spec`'s
expected output.

The same gap exists for SET NULL (permutation L76) and for the
non-deferred NO ACTION / RESTRICT branches of `enforceFKOnDelete` —
they all defer to scans that filter out invisible tuples and therefore
miss the concurrent INSERT entirely.

## Fix

### Detection

A new helper `detectInFlightChildInsert(ctx, childTbl, fkCols, vals)`
scans the child relation (plus its inheritance / partition children)
for tuples whose:

- xmin != Invalid and xmin != ctx.Tx.XID (skip self-inserted rows that
  are already visible via the subxact-aware visibility check); AND
- xmin is in `ctx.Snap.InProgress` (the inserter started before us, or
  materialised its xid into the active set at a moment we still
  observe); AND
- ctx.TxnMgr.IsXIDActive(xmin) (the inserter is still in flight); AND
- the row's FK columns match `vals` via `fkRowMatches`.

The first such match returns `(xid, true)`. No match yields `(0, false)`.

We deliberately match against the top-level xid stamped on the heap row;
sub-xact resolution is unnecessary here because FK enforcement always
operates on the persisted xmin, not on a transient sub-xact assignment.

### Wait + post-wait classification

`fkChildWaitForInFlightInsert(ctx, childTbl, fkCols, vals)` wraps the
detection in a bounded loop (8 iterations) that:

1. `ctx.TxnMgr.WaitForXID(qctx, xid)` — blocks until the inserter
   commits or aborts. Context cancellation (connection close, query
   timeout) returns nil so the outer dispatcher can surface the ctx
   error.
2. RR / Serializable branch: if `!ctx.TxnMgr.HasAbortedXID(xid)`
   (the inserter committed), return `ExecError{Code: "40001",
   Message: "could not serialize access due to concurrent update"}`.
   The Manager already tracks aborts in `m.abortedXIDs` (set by
   `finish() → XactAbort`); we add `HasAbortedXID(xid)` to surface
   that under a lock — RR snapshots are frozen at BEGIN time and
   cannot tell us anything about aborts that happened after.
3. RC branch (or aborted inserter): refresh the snapshot via
   `SnapshotFor(ctx.Tx)` and loop. RC's `SnapshotFor` captures a
   fresh visibility horizon, so the next iteration (and the caller's
   own scans) will see the now-committed child row and process it
   normally.

The iteration cap bounds pathological chains where multiple short xacts
keep inserting fresh referencing rows; in practice 1–2 iterations
suffice (the WaitForXID rendezvous serialises everything else).

### Wiring into enforceFKOnDelete

`enforceFKOnDelete` calls `fkChildWaitForInFlightInsert` at the top of
each per-FK iteration, gated on:

```go
if !(fk.Deferrable && fk.InitiallyDeferred && ctx.Session != nil &&
     ctx.Session.InExplicitTransaction()) { ... }
```

Deferred FK checks (NO ACTION INITIALLY DEFERRED) run at COMMIT time
when no concurrent inserter against us can still be in-flight — they
need no wait. All four immediate branches (CASCADE, SET NULL,
SET DEFAULT, RESTRICT) and the immediate NO ACTION fallthrough get the
wait+40001 treatment uniformly.

## Why not patch the individual action paths

`fkCascadeDelete` and `fkSetNull` could each be made wait-aware
independently. Centralising the wait in `enforceFKOnDelete` keeps the
correctness gate in one place: every FK action runs against a snapshot
that has already serialised against any in-flight inserter or aborted
with 40001. It also keeps `fkCascadeDelete` / `fkSetNull` /
`assertNoChildRows` free of MVCC wait plumbing.

## Why detect xmin separately from the existing wait-aware xmax scan

`scanTableForMatchFKWait` (M0100-0005q) handles the **child-side FK
INSERT** waiting on an in-flight **parent-side UPDATE/DELETE** —
it watches `xmax` on matching parent rows.

This loop's case is the mirror: **parent-side DELETE** waiting on an
in-flight **child-side INSERT** — it watches `xmin` on matching child
rows. Sharing the helper would have entangled the two with conditional
flags; a dedicated `detectInFlightChildInsert` keeps each call site
focused on the single MVCC signal it cares about.

## Verification

- `TestPort_IsolationFkSnapshot` — PASS (was `defer`, all 7 permutations
  green end-to-end).
- `TestFKDelete_RR_RaisesSerializationOnConcurrentChildInsert` and
  `TestFKDelete_RC_CompletesAfterConcurrentChildInsertCommit` —
  PASS (new pins).
- `TestManagerHasAbortedXID` — PASS (new pin).
- `go test -count=1 -race -timeout 240s ./internal/executor/
  ./internal/storage/ ./internal/mvcc/ ./internal/server/` — PASS.
- Adjacent isolation tests `InsertConflictDoNothing`,
  `InsertConflictDoUpdate`, `LockCommittedUpdate`,
  `PartitionKeyUpdate1`, `PartitionKeyUpdate2` — all PASS (unchanged).
