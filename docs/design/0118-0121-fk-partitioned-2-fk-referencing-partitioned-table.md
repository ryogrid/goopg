# 0118-0121 — fk-partitioned-2: FK referencing a partitioned table

**Milestone:** M0118-0005 (FK / referential-integrity isolation output-parity)
**Spec:** `postgres/src/test/isolation/specs/fk-partitioned-2.spec`
**Test:** `TestPort_IsolationFkPartitioned2` (`internal/testport/isolation_port_test.go`)
**Status:** PROMOTED — byte-identical to PG 18.3 across all six permutations.

## What the spec exercises

Both sides of the FK are list-partitioned:

```
ppk (a int primary key) partition by list (a);   ppk1 partition of ppk for values in (1);  insert 1
pfk (a int references ppk) partition by list (a); pfk1 partition of pfk for values in (1);
```

Two concurrent races, each run under READ COMMITTED (`s2b`) and SERIALIZABLE
(`s2bs`):

1. **INSERT vs concurrent DELETE of the referenced row.** `s1` deletes the
   referenced `ppk` row (`a=1`); `s2` inserts the referencing `pfk` row. The
   INSERT's FK existence check (`SELECT 1 FROM ppk WHERE a=1 FOR KEY SHARE`)
   blocks `<waiting ...>` behind `s1`'s in-flight delete. When `s1` commits:
   - READ COMMITTED re-evaluates, finds the row gone → `23503` FK violation.
   - REPEATABLE READ / SERIALIZABLE cannot follow the update chain past their
     snapshot → `40001 could not serialize access due to concurrent update`.

2. **DELETE of the referenced row vs concurrent INSERT.** `s2` inserts the
   referencing `pfk` row (uncommitted); `s1` issues `DELETE FROM ppk WHERE a=1`
   (the **partitioned parent**, routed to leaf `ppk1`). The DELETE blocks on the
   in-flight insert; once it commits, the referenced-side check rejects with:
   `update or delete on table "ppk1" violates foreign key constraint
   "pfk_a_fkey_1" on table "pfk"` — naming the **leaf partition** and the
   **per-partition cloned constraint**. (`s1` is plain READ COMMITTED in both
   permutations, so this is `23503`, never `40001`.)

## Divergences and fixes

The READ COMMITTED INSERT case (1) and the `<waiting ...>` blocking already
worked from the fk-partitioned-1 / fk-deadlock machinery. Two gaps remained.

### Gap A — RR/SSI INSERT-side FOR KEY SHARE must raise 40001

`scanTableForMatchFKWait` (the INSERT-side existence scan, `operators_fk.go`)
waits on the matched parent row's in-flight **key-changing** xmax (recorded as
`pending` only for a DELETE or key UPDATE — never a no-key update or pure
locker), then refreshes the snapshot and re-scans. Under READ COMMITTED that
re-scan correctly finds the row gone and the caller emits `23503`. But under
REPEATABLE READ / SERIALIZABLE, PostgreSQL's `heap_lock_tuple` returns
`HeapTupleUpdated` → `ERRCODE_T_R_SERIALIZATION_FAILURE` rather than walking the
update chain past the transaction's snapshot.

Fix: after the wait + snapshot refresh (and after the existing cross-partition
move `0A000` check), if `ctx.Tx.Isolation != ReadCommitted` and the pending
updater committed (`!HasAbortedXID`), return `40001 could not serialize access
due to concurrent update`. Mirrors the referenced-DELETE side's
`fkChildWaitForInFlightInsert`.

### Gap B — partitioned-parent DELETE must name the leaf clone constraint

`DELETE FROM ppk` reaches `enforceFKOnDelete` with `parentTbl = ppk` (the
partitioned parent). goopg's first loop scans `FindFKsReferencingTable("ppk")`
and fired `assertNoChildRows`, which names the parent (`ppk`) and the base
constraint (`pfk_a_fkey`). PostgreSQL instead fires the RI trigger cloned onto
the **leaf** partition where the tuple physically lives, naming `ppk1` /
`pfk_a_fkey_1`.

The repo already had `enforceFKOnDeletePartitionAncestor` /
`fkDeleteAncestorPass` (from fk-partitioned-1's leaf-direct `DELETE FROM ppk1`)
that produces exactly the leaf-named, ordinal-suffixed error — but it walks
**leaf → ancestor**, so when called with the partitioned parent it breaks
immediately (`ppk` has no partition parent) and the parent-named
`assertNoChildRows` won the race.

Fix in `enforceFKOnDelete`:
- Route the deleted row to its leaf with `routeToPartition` when `parentTbl` is
  partitioned (no-op when the DELETE already targets a leaf, as in
  fk-partitioned-1).
- When the row lives in a leaf partition (`leafIsPartition`), **skip** the
  parent-named `assertNoChildRows` for the NO ACTION / RESTRICT branches — the
  unconditional `fkChildWaitForInFlightInsert` above still serialises against any
  concurrent referencing INSERT, so the `<waiting ...>` behaviour is preserved.
- Run `enforceFKOnDeletePartitionAncestor` from the **leaf** so the violation
  names the per-partition clone.

### Gap B follow-up — deferral correctness (fk-snapshot regression)

Routing the ancestor pass for partitioned-parent deletes exposed that
`fkDeleteAncestorPass` raised immediately, ignoring DEFERRABLE INITIALLY
DEFERRED FKs. `fk-snapshot` deletes and re-inserts the referenced row in one
transaction (legal under a deferred FK, checked at COMMIT). Fix:
`fkDeleteAncestorPass` now queues a deferred check
(`BasicSession.AddDeferredFKCheck`, deduped against the parent-keyed first-loop
queue) and skips the immediate raise when the FK is deferrable + initially
deferred inside an explicit transaction — mirroring the first loop's NO ACTION
branch.

## Files

- `internal/executor/operators_fk.go` — `scanTableForMatchFKWait` 40001;
  `enforceFKOnDelete` leaf routing + skip; `fkDeleteAncestorPass` deferral.
- `internal/testport/isolation_port_test.go` — `TestPort_IsolationFkPartitioned2`.
- `docs/test-port/postgres-oracle-port-status.{csv,md}` — D-002 rationale.

## Oracle

`postgres/src/backend/utils/adt/ri_triggers.c` (RI_FKey_check FOR KEY SHARE),
`postgres/src/backend/access/heap/heapam.c` (`heap_lock_tuple` →
`HeapTupleUpdated` serialization failure). Verified against
`./postgres/local_install` PG 18.3 expected output.

## Regression gates

`TestPort_IsolationFkPartitioned1/2`, `FkSnapshot`, `FkContention`,
`FkDeadlock`, `FkDeadlock2`, `ReferentialIntegrity` all green; full
`internal/executor` unit suite green.
