# M0100-0005r — Runtime unique-constraint violation on INSERT

Status: accepted
Filed: 2026-05-15 (loop 34)
Milestone: M0100 — RC isolation suite runtime correctness & 21-spec pass
Closes (in part): `read-write-unique.spec` L34/L36 (`ERROR:  duplicate key
value violates unique constraint "test_pkey"`).

## Problem

`internal/executor/operators_storage.go`'s
`maintainUniqueIndexesForInsert` previously called
`tree.Insert(key, ptr)` and `_ = ...`-discarded the result. The btree
insertion did not enforce uniqueness and there was no companion probe of
the heap, so a plain `INSERT` against an already-occupied PK / unique-key
silently succeeded:

```
CREATE TABLE test (i integer PRIMARY KEY);
INSERT INTO test VALUES (42);
INSERT INTO test VALUES (42);   -- expected: 23505. actual: succeeds.
```

Upstream PostgreSQL surfaces SQLSTATE 23505 with MESSAGE
`duplicate key value violates unique constraint "<idx>"` (see
`postgres/src/backend/access/nbtree/nbtinsert.c::_bt_check_unique`).
The 21-spec `read-write-unique.spec` of M0100-0005's pass goal pins
this exact diagnostic at L34/L36 of its second permutation
(`r1 w1 c1 r2 w2 c2`).

## Fix

`internal/executor/operators_storage.go` gains
`checkUniqueIndexesForInsert(ctx, tbl, cols, row, pos)` and
`isLiveForUniqueCheck(ctx, xmin, xmax)` helpers next to
`maintainUniqueIndexesForInsert`:

1. For every unique / primary btree index on `tbl`, encode the
   candidate key from `row` via the existing `encodeIndexKeyFromCols`.
2. `RangeScan(key, key)` the index for any matching entry. For each
   hit, probe the heap tuple at the recorded `(block, offset)` and
   pass `(xmin, xmax)` to `isLiveForUniqueCheck`.
3. If a live duplicate is found, return
   `*ExecError{Code: "23505", Message: "duplicate key value violates
   unique constraint %q"}`.

The plain `INSERT` operator (`insertOp.Next` —
`internal/executor/operators_storage.go`) calls the new helper *before*
`writeHeapRowReturning` in BOTH branches:

* The partition-routed branch (`if isPartitioned && routedPart != nil`)
  passes the routed leaf's `partTable` and `partRow` so per-partition
  unique indexes are enforced on the leaf.
* The non-partitioned branch (`else`) passes `o.plan.Table` and `row`.

`maintainUniqueIndexesForInsert` is unchanged — it remains the
post-write index-population step, called by the same INSERT sites and
by the apply worker. This preserves the M0103-0007 rung-1 fresh-session
visibility fix (apply-worker re-applies committed-on-publisher rows
that the subscriber may not yet see; skip-on-duplicate is the right
behaviour there, hence apply worker bypasses the new check).

The `upsertOp` path (`internal/executor/operators_upsert.go`,
ON CONFLICT) is also unaffected: it routes conflicts through
`probeArbiterWaiting` / `findInProgressConflict` and DO NOTHING /
DO UPDATE branches before any heap write, so the runtime check would
duplicate work that's already done.

## isLiveForUniqueCheck visibility rules

Conservative classification — false positives (rejecting a key that
PG would let in) are surfaced loudly via 23505, while false negatives
(letting a duplicate through) corrupt the unique invariant. The helper
errs on the rejection side:

| xmin status                       | xmax status                          | live? |
|-----------------------------------|--------------------------------------|-------|
| Invalid (zero)                    | -                                    | no    |
| Active in `Manager` (any session) | Invalid                              | yes   |
| Active in `Manager`               | Active                               | yes   |
| `Snap.SeesCommittedXID(xmin)`     | Invalid                              | yes   |
| `Snap.SeesCommittedXID(xmin)`     | Aborted                              | yes   |
| `Snap.SeesCommittedXID(xmin)`     | Committed (not in Aborted, < Xmin)   | no    |
| `Snap.HasAborted(xmin)`           | -                                    | no    |
| Unknown (committed before Xmin)   | Invalid                              | yes   |

This is a deliberate subset of upstream's `_bt_check_unique` →
`HeapTupleSatisfiesDirty`. The full XID-wait dance (re-check after
the conflicting xact commits / aborts, retry with refreshed snapshot,
release tuple lock) is out of scope for this slice — the existing
`epqWait` / EPQ machinery in `operators_storage.go` handles it for
UPDATE / DELETE / ON CONFLICT, and a separate sub-milestone owns its
extension to plain INSERT.

## Test pinning

`internal/executor/insert_unique_constraint_test.go`:

* `TestInsertRuntimeUniqueViolationRaises23505` — direct heap insert,
  same xact attempts a second insert with the same PK. Asserts
  `ExecError.Code == "23505"` and the upstream MESSAGE shape.
* `TestInsertRuntimeUniqueViolationAllowsAfterRolledBackInsert` —
  insert under xid X, rollback, start a fresh xact; assert
  `Snap.HasAborted(X) == true` and confirm the second insert (same key)
  succeeds. Pins the `HasAborted`-aware branch of
  `isLiveForUniqueCheck`.

End-to-end:
`go test -run TestPort_IsolationReadWriteUnique ./internal/testport/`
now produces the L34 ERROR line byte-identical to upstream
(`ERROR:  duplicate key value violates unique constraint "test_pkey"`);
remaining diffs in the spec are SERIALIZABLE first-read-snapshot
timing (separate scope) and SSI predicate-lock wait state (M0104
follow-up). The 4 currently-passing isolation tests
(`LockCommittedUpdate`, `InsertConflictDoUpdate`,
`InsertConflictDoNothing`, `PartitionKeyUpdate1`) are unaffected.

## Verification gates

* `go test -count=1 -race ./internal/executor/ ./internal/server/
  ./internal/mvcc/ ./internal/planner/ ./internal/parser/
  ./internal/analyzer/ ./internal/storage/ ./internal/wal/
  ./internal/initdb/ ./internal/access/btree/` — all PASS.
* `go test -count=1 -run TestInsertRuntimeUniqueViolation
  ./internal/executor/` — both new pins PASS.
* `go test -run 'TestPort_Isolation(LockCommittedUpdate|InsertConflictDo
  Update|InsertConflictDoNothing|PartitionKeyUpdate1)$' ./internal/test
  port/` — 4/4 PASS (no regression).
