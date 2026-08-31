# M0100-0005u — `isLiveForUniqueCheck` honours self-xact deletion

Status: accepted (2026-05-15)
Owner: Ralph (loop 37)

## Problem

`fk-snapshot.spec`'s permutations
`s1brr s1dfp s1ifp1 s1c s1sfn` and `s1brc s1dfp s1ifp1 s1c s1sfn`
issue, in a single transaction:

```
BEGIN ISOLATION LEVEL REPEATABLE READ;  -- (or READ COMMITTED)
DELETE FROM fk_parted_pk WHERE a = 1;
INSERT INTO fk_parted_pk VALUES (1);
COMMIT;
```

Upstream PostgreSQL accepts the INSERT — the row was deleted by the
same xact, so the unique index has no live conflict. goopg raised

```
ERROR:  duplicate key value violates unique constraint "fk_parted_pk_1_pkey"
```

…driven by the M0100-0005r runtime unique-check
(`internal/executor/operators_storage.go::checkUniqueIndexesForInsert`).

## Root cause

`isLiveForUniqueCheck(ctx, xmin, xmax)` classifies whether a heap tuple
is a live duplicate.  After M0100-0005r the predicate handled

* `xmax = InvalidTransactionID` ⇒ live
* `xmax` aborted ⇒ live (deletion didn't stick)
* `xmax` active by some xact ⇒ live (concurrent delete, not yet committed)
* `xmax` committed ⇒ dead

…but it never asked the question "is xmax our **own** xid?".  In the
delete-then-insert-in-same-xact shape the deleter is the current
session; `IsXIDActive(xmax)` returns `true` (we are still running) and
the helper voted "live", so the follow-up INSERT failed at the
runtime unique check.

This mirrors PostgreSQL's `HeapTupleSatisfiesDirty`
(`postgres/src/backend/access/heap/heapam_visibility.c`), which
short-circuits self-xact xmax to "deleted" before consulting the
clog/proc-array — same invariant, same reason.

## Fix

Single-site change in `isLiveForUniqueCheck`
(`internal/executor/operators_storage.go`): before falling through to
`TxnMgr.IsXIDActive(xmax)`, check `xmax == ctx.Tx.XID` and return
`false` (dead).  The xmin arm gets a parallel guard so that a row
whose xmin is our own xid is treated as live without consulting the
manager (same as the existing default-true branch, but explicit).

## Tests

- `TestIsLiveForUniqueCheck_SelfXactDeleteIsDead`
  (`internal/executor/insert_unique_constraint_test.go`) drives the
  helper directly with a synthesised `(xmin = prior-committed,
  xmax = self-xid)` pair and asserts the verdict is "dead".  A second
  arm constructs `(xmin = prior-committed, xmax = other-active-xid)`
  and asserts "live" so the concurrent-delete semantics M0100-0005r
  protects do not regress.

End-to-end: `TestPort_IsolationFkSnapshot` advances past L106 / L114
diffs (the spurious 23505 lines).  Remaining diffs are
`<waiting ...>` for concurrent FK INSERT + RR `40001` for the
"could not serialize access due to concurrent update" — separate
scope (FK wait-state + SSI conflict materialisation).

## Verification

```
go test -count=1 -race -timeout 240s \
  ./internal/executor/ ./internal/storage/ \
  ./internal/server/ ./internal/mvcc/
```

All green.  Adjacent isolation tests
(`InsertConflictDoNothing`, `InsertConflictDoUpdate`,
`LockCommittedUpdate`, `PartitionKeyUpdate1`,
`PartitionKeyUpdate2`) — 5/5 still PASS.

## Out of scope

- The remaining `fk-snapshot` diffs (concurrent FK INSERT
  `<waiting ...>` and RR serialise-access error) require FK
  wait-state plumbing on the `pk_noparted` DELETE path and a
  serialise-access error materialisation under RR — both standalone
  follow-ups.
- Upsert / ON CONFLICT paths use `probeArbiterWaiting` first and never
  reach this helper, so the change has no effect on the M0100-0005s/t
  arbiter machinery.
