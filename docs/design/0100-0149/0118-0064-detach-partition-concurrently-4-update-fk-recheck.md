# 0118-0064 — `detach-partition-concurrently-4` PROMOTED: UPDATE fires RI parent-existence check + detach re-validates referencing rows after the wait

**Milestone:** M0118-0008 (Upstream Isolation Spec Suite Pass-Through)
**Status:** accepted
**Spec:** `postgres/src/test/isolation/specs/detach-partition-concurrently-4.spec`
**Test:** `TestPort_IsolationDetachPartitionConcurrently4` (`runIsoSpecStrict`, 21 permutations)
**Builds on:** 0118-0059/0060/0061 (detach-1/2/3), 0118-0062 (FK current-epoch), 0118-0063 (cursor pinning)

## Summary

This loop closes the last three permutations of `detach-partition-concurrently-4`
— the `WHERE CURRENT OF` positioned-update group — and **promotes the spec to
pass-required** (byte-for-byte across all 21 permutations).

The three remaining permutations all run

```
s1brr      begin isolation level repeatable read;
s1declare2 declare f cursor for select * from d4_fk where a = 2;
s1fetchone fetch 1 from f;
…
s1updcur   update d4_fk set a = 1 where current of f;
```

against a concurrent `alter table d4_primary detach partition d4_primary1
concurrently` (`d4_fk(a)` references the partitioned `d4_primary`, and value
`1` lives only in `d4_primary1`). They split into two behaviours that goopg did
not implement:

1. **UPDATE never fired the RI parent-existence check.** `update d4_fk set a = 1`
   changes the FK column to a value (`1`) that lives only in the
   concurrently-detaching partition. PostgreSQL's `RI_FKey_check` runs the
   instant the row is updated and, because the detach is already marked pending
   (invisible to the latest snapshot — design 0118-0062), the value is not found
   → `ERROR: insert or update on table "d4_fk" violates foreign key constraint
   "d4_fk_a_fkey"`. goopg only ran `checkFKInsert` from `insertOp`; `updateOp`
   performed no parent lookup, so `s1updcur` silently succeeded
   (permutations *s1updcur-after-detach*, with and without cancel).

2. **DETACH did not re-validate referencing rows after waiting.** In the
   *s1updcur-before-detach* permutation, `s1updcur` runs first (value `1` still
   present → succeeds), then the detacher blocks on s1's cursor-pinned snapshot.
   `detachPartitionFKRefCheck` (the `RI_PartitionRemove_Check` analog) ran
   **before** the wait under the statement-start snapshot, which could not see
   s1's still-uncommitted `a = 1` row, so it found no violation. After s1
   commits and the wait drains, goopg finalised the detach without re-checking,
   missing `ERROR: removing partition "d4_primary1" violates foreign key
   constraint "d4_fk_a_fkey_1"`.

## Fix 1 — UPDATE fires the RI parent-existence check on a key-column change

`internal/executor/operators_fk.go`: `checkFKInsert` is refactored to delegate
to a new `checkFKInsertForConstraints(ctx, owner, report, row, fks)` that runs
the parent-existence assertion over a caller-supplied subset of the owner
table's foreign keys (INSERT still passes every constraint).

`internal/executor/operators_storage.go`: `updateOp` gains two helpers:

- `childFKsToRecheck()` returns the FKs declared on the updated table whose
  referencing columns appear in this UPDATE's `SET` list. PostgreSQL fires the
  `RI_FKey_check` AFTER trigger only when a key column actually changes, so an
  UPDATE that touches no FK column performs no parent lookup. Mirroring that
  **bounds the new check's blast radius to FK-key UPDATEs on tables that have
  FKs** — every other UPDATE (pgbench, TPC-H, non-FK tables, non-key columns) is
  byte-identical to before. Computed once per UPDATE.
- `recheckChildFKs(fks, newRow, scanTbl)` runs `checkFKInsertForConstraints` on
  the post-trigger `newRow`, skipping rows that came from an inheritance child
  (different column layout; those children carry their own FKs and are not
  exercised here).

The check is wired into all three UPDATE write paths, immediately after BEFORE
triggers may have rewritten the row and before the heap write:
`updateOp.Next` (seqscan), `updateViaIndex` (B-tree), and `updateWithFrom`
(`UPDATE … FROM`). Sibling-paths discipline — the FK check must hold regardless
of which scan strategy the planner picked.

## Fix 2 — DETACH re-validates referencing rows after the wait

`internal/executor/operators_ddl.go`: after the hybrid detach wait
(`waitForRelationLockers` + `WaitForPinnedSnapshotsToCommit`) succeeds and
before Phase-2 finalize, the detacher re-runs `detachPartitionFKRefCheck`. Two
subtleties make the re-check see exactly what PostgreSQL does:

- **Fresh snapshot** (`TxnMgr.SnapshotFor(Tx).Clone()`): the first check used the
  statement-start snapshot, which predates the concurrent committer; the
  re-check must see the now-committed referencing row.
- **`PartitionDetachEpoch = 0` on that snapshot**: by now the child is marked
  detach-pending, so `routeToPartition` would otherwise filter it out (a fresh
  snapshot's epoch is `>=` the detach epoch). Forcing the epoch to `0` keeps the
  child in the routing set so a row whose key routes there is recognised as a
  violation. The child is still registered (`UnregisterPartitionChild` runs
  after), so the cloned-constraint suffix (`<fkname>_<N>`, N = 1-based child
  ordinal) is unchanged → `d4_fk_a_fkey_1`.

The original `ctx.Snap` is restored after the re-check.

## `WHERE CURRENT OF` positioned-DML (not required by this spec)

`d4_fk` holds exactly one row, so `update d4_fk set a = 1 where current of f`
and the all-rows `update d4_fk set a = 1` (the planner currently drops the
parsed `CurrentOf` and emits no WHERE) produce identical results — the spec
cannot distinguish them. Implementing positioned UPDATE/DELETE faithfully
(per-row CTID capture in the cursor + a CTID-restricted rewrite) is a separate,
project-wide feature that does not affect any `port` spec's output; it is
tracked as a bounded follow-up in the deferral ledger.

## Blast radius

- UPDATE FK check: fires only when an FK column is in the SET list of a table
  that has FKs. pgbench (`pgbench_*` have no FKs) and TPC-H load are unaffected;
  pgbench TPC-B smoke 0-failed.
- Detach re-check: only on the `DETACH … CONCURRENTLY` two-phase path, after the
  wait; superuser/non-FK detaches are unchanged (the scan finds no referencing
  row in O(referencing-rows)).

## Gates

- `TestPort_IsolationDetachPartitionConcurrently4` strict PASS (21 perms,
  byte-for-byte).
- Siblings: `…DetachPartitionConcurrently1/2/3`, `…Fk{Snapshot,Contention,
  Deadlock2}`, `…ReferentialIntegrity`, `…TemporalRangeIntegrity`,
  `…PartitionKeyUpdate1..4`, `…Merge{Update,Delete,InsertUpdate,MatchRecheck,
  Join}`, `…InsertConflictDoUpdate{,2,3,4}` PASS (no FK/UPDATE regression).
- `go test ./internal/executor/` PASS; regress-port `foreign_key`/`update`/
  `constraints`/`inherit` subtests no regression; `go build ./...` clean;
  pgbench TPC-B smoke 0-failed (pre-commit hook).
