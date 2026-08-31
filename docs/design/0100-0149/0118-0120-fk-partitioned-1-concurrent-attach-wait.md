# 0118-0120 — `fk-partitioned-1` PROMOTED: concurrent ATTACH PARTITION held-to-commit wait

Status: accepted
Milestone: M0118-0005 / M0118-0009 (Upstream Isolation Spec Suite Pass-Through)
Spec: `postgres/src/test/isolation/specs/fk-partitioned-1.spec`
Predecessors: [0118-0118](0118-0118-fk-partitioned-attach-validation.md) (Class A:
referencing-side clone + validation on ATTACH), [0118-0119](0118-0119-fk-partitioned-1-referenced-side.md)
(committed Class B: referenced-side leaf-partition delete check)

## Summary

**Promotion.** This closes the final slice of `fk-partitioned-1` — the **concurrent
Class B** permutations, where `DELETE FROM ppk1` runs *while the* `ALTER TABLE pfk
ATTACH PARTITION pfk1` *is still uncommitted*. PostgreSQL renders:

```
step s2a: alter table pfk attach partition pfk1 for values in (1);
step s1d: delete from ppk1 where a = 1; <waiting ...>
step s2c: commit;
step s1d: <... completed>
ERROR:  update or delete on table "ppk1" violates foreign key constraint "pfk_a_fkey_1" on table "pfk"
```

The DELETE must **block** behind the uncommitted attach and only then error.
`TestPort_IsolationFkPartitioned1` is now strict (`runIsoSpecStrict`) and
byte-identical to PG 18.3 across all 18 active permutations.

## Why goopg previously diverged (two coupled symptoms)

`cloneAndValidateAttachPartitionFKs` runs at *statement* time and appends the
cloned FK to `pfk1.ForeignKeys` immediately (shared catalog), but the partition
*registration* (`RegisterPartitionChild`) is deferred to the attaching
transaction's COMMIT (M0118-0008 transactional-DDL visibility). So during the
uncommitted window:

1. **No wait.** Nothing held a lock the concurrent `DELETE FROM ppk1` could block
   on, so it fired immediately instead of `<waiting ...>`.
2. **Mis-named table.** `enforceFKOnDeletePartitionAncestor` skips per-partition
   FK clones via `IsPartitionChild(ref.Child.OID)` so the ROOT `pfk` names the
   violation — but pre-commit `IsPartitionChild(pfk1)` is still `false`, so the
   clone was *not* skipped and the error wrongly named the leaf `"pfk1"`.

PostgreSQL's `RI_Initial_Check` (run by `CloneForeignKeyConstraints` →
`validateForeignKeyConstraint`) executes `SELECT … FOR KEY SHARE` on the
referenced rows and **holds those KEY SHARE locks to commit**. A concurrent
DELETE of a referenced row conflicts with KEY SHARE and waits; once the attach
commits, the now-registered clone makes the referenced-side check fire on the
root. Both goopg symptoms are the same missing piece: the held-to-commit lock.

## Mechanism

goopg's isolation runner detects blocking purely by timing, and cross-statement
row-lock blocking rides `WaitForXID`, not heavyweight locks (statement-scoped).
Rather than synthesise a heap KEY SHARE lock from the DDL path, we model the
held-to-commit lock with the attaching **transaction id** as the wait target —
the DELETE blocks on the attach's XID exactly as it would block on a KEY SHARE
lock held by that transaction.

### 1. Record the in-flight attach XID (`internal/catalog/catalog.go`)

New map `pendingAttachXID map[uint32]uint32` (partition child OID → attaching XID)
with `SetPendingAttachXID` / `PendingAttachXID` / `ClearPendingAttachXID` (mutex
guarded). It marks the window in which a clone FK is visible but the partition is
not yet registered.

### 2. Set / clear the marker (`internal/executor/operators_ddl.go`, `operators_tx.go`)

- **Set** in the deferred-ATTACH branch, only when the parent has FKs (a clone was
  placed): `MaterializeWriterXID()` then `SetPendingAttachXID(childOID, xid)`.
- **Clear on COMMIT** in `ApplyPendingPartitionAttaches`, right after
  `RegisterPartitionChild` (the partition is now visible; the normal committed
  path takes over).
- **Clear on ROLLBACK** in `execRollback`, draining the pending attaches.

### 3. Wait then re-evaluate (`internal/executor/operators_fk.go`)

`enforceFKOnDeletePartitionAncestor` now drives a bounded retry loop over a new
`fkDeleteAncestorPass`. When the pass finds a referencing row for the deleted key
in a relation that is **not yet a registered partition child** but **has a
`PendingAttachXID` of another still-active transaction**, it `WaitForXID`s that
transaction, refreshes the snapshot, and signals retry. After the attach commits,
the re-run sees `IsPartitionChild(pfk1) == true`, skips the clone, scans the ROOT
`pfk` (which descends into the now-registered `pfk1`), and raises the 23503 naming
`"pfk_a_fkey_1"` on table `"pfk"`. If the attach instead aborts, `IsXIDActive`
is false on re-run and the delete proceeds (no stale reference remains for the
deleted key on the committed side).

## Result

All three permutation classes of `fk-partitioned-1` are byte-identical to PG 18.3:
Class A (attach validation fails referencing-side), committed Class B (immediate
referenced-side error), and concurrent Class B (`<waiting ...>` then error).
Spec promoted `defer` → `pass`.

## Blast radius

- `pendingAttachXID` is written only by a deferred ATTACH PARTITION of a parent
  that carries a foreign key (exactly the partitioned-FK scenario) and read only
  inside `enforceFKOnDeletePartitionAncestor`, itself a no-op unless the deleted
  table is a partition of a referenced table. Ordinary deletes/attaches are
  byte-unchanged.
- The wait is gated on `IsXIDActive(axid) && axid != self`, so a committed/aborted
  or self attach never blocks. The retry loop is capped at 8.
- No other `port` spec exercises a concurrent attach + referenced-side partition
  delete.

## Gates

- `TestPort_IsolationFkPartitioned1` strict PASS (18 perms, byte-exact).
- Non-regression strict: `ReferentialIntegrity`, `RiTrigger`, `FkDeadlock`,
  `FkDeadlock2`, `FkContention`, `FkSnapshot`, `TemporalRangeIntegrity`,
  `PartitionConcurrentAttach`, `DetachPartitionConcurrently{1,2,3,4}`,
  `AlterTable4`, `InheritTemp`, `PartitionDropIndexLocking` — PASS.
- `go test ./internal/executor/ ./internal/catalog/` PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.
