# 0118-0118 — `fk-partitioned-1` enabler: FK clone + existing-row validation on ATTACH PARTITION (M0118-0009)

**Status:** accepted
**Type:** enabler (NOT a spec promotion)
**Spec:** `postgres/src/test/isolation/specs/fk-partitioned-1.spec`
**Date:** 2026-06-25

## Summary

When a partition is attached to a partitioned table that carries a FOREIGN KEY,
PostgreSQL (`ATExecAttachPartition` → `CloneForeignKeyConstraints` →
`RI_Initial_Check`) clones the referencing FK onto the new partition and scans
the partition's **existing rows**, validating each against the referenced table
under `SELECT … FOR KEY SHARE`. goopg's ATTACH PARTITION executor previously set
up partition bounds and propagated unique/PK indexes but performed **no FK
clone and no validation**, so the spec's referenced-row checks never fired.

This enabler closes the **referencing-side validation** half: ATTACH now clones
the parent's FKs onto the child and validates the child's existing rows with the
wait-aware FK scan, so the permutations where the referenced row is deleted
before or during the attach now match PG 18.3 byte-for-byte — including the
`<waiting ...>` step when the delete is still in flight.

## What the spec exercises

```
ppk  (a int primary key) partition by list (a);  ppk1 partition of ppk for (1); -- has row (1)
pfk  (a int references ppk) partition by list (a);                              -- partitioned, FK pfk_a_fkey
pfk1 (a int not null);  insert into pfk1 values (1);                            -- standalone, row (1)
```

Sessions race `s1: delete from ppk1 where a = 1` against
`s2: alter table pfk attach partition pfk1 for values in (1)`. Two outcome
classes:

- **Class A — attach happens after / during the delete.** The attach's FK
  validation finds `ppk` no longer contains `1`, so it raises
  `ERROR: insert or update on table "pfk1" violates foreign key constraint
  "pfk_a_fkey"`. If the delete is still uncommitted at attach time the attach
  `<waiting ...>` on the in-flight `xmax`, then completes with the error once
  `s1` commits.
- **Class B — attach happens before the delete.** The attach succeeds (the
  referenced row still exists) and the cloned FK now makes `pfk1` reference
  `ppk`; the later `delete from ppk1` is restricted with
  `ERROR: update or delete on table "ppk1" violates foreign key constraint
  "pfk_a_fkey_1" on table "pfk"`.

This enabler implements **Class A**. Class B is deferred (see below).

## Change

`internal/executor/operators_fk.go`
- `fkConstraintName` now returns an explicitly-recorded `fk.Name` when present
  (falling back to the synthesised `<table>_<col>_fkey`). For inline/table FKs
  goopg already stores the auto-generated name at CREATE TABLE time, so this is
  identical to the synthesised result in the common case; it additionally
  preserves a user `CONSTRAINT` name and a **cloned parent-partition FK name**.
- New `cloneAndValidateAttachPartitionFKs(ctx, parentTbl, childTbl)`: for each
  FK on the partitioned parent, builds a clone carrying the parent constraint's
  resolved name, validates the partition's existing rows via the existing
  `fullTableFKCheck` (whose per-row `assertParentExists` goes through the
  wait-aware `scanTableForMatchFKWait` — descends the referenced table's
  partitions and blocks on an in-flight referenced-row delete), then records the
  clone on the child for ongoing enforcement (idempotent by name). No-op unless
  the parent actually has FKs.

`internal/executor/operators_ddl.go`
- The `AlterTableAttachPartition` case calls
  `cloneAndValidateAttachPartitionFKs` at **statement time** (before the
  explicit-txn defer of the catalog registration), so the FOR-KEY-SHARE wait and
  the eventual `23503` surface during the `ALTER`, exactly as upstream.

## Why this is correct / bounded

- The validation reuses the same `fullTableFKCheck` / `scanTableForMatchFKWait`
  machinery already trusted by `ADD CONSTRAINT … FOREIGN KEY` validation and the
  partition-routed INSERT FK check (so the partitioned referenced table `ppk` is
  scanned through its `ppk1` partition, and the FOR-KEY-SHARE wait on a
  concurrent uncommitted delete is the same path the `fk-deadlock` /
  `referential-integrity` specs already depend on).
- Blast radius is scoped to ATTACH PARTITION **of a partitioned parent that has
  FKs**; non-FK partition attaches (every other partition isolation spec) take
  the early `len(parentTbl.ForeignKeys)==0` return and are byte-unchanged.
- The `fkConstraintName` change returns the same string in every pre-existing
  call site (the stored `Name` already equals the synthesised name) and is
  strictly more correct for user-named constraints.

## Deferred (Class B — ledgered)

Class B permutations still diverge (first divergence moved from the very first
permutation to the first Class-B permutation):

1. **Referenced-side per-partition enforcement + naming.** The later
   `delete from ppk1` must be restricted reporting constraint `pfk_a_fkey_1`
   "on table pfk" — PG's cloned referenced-side constraint carries the `_N`
   disambiguation suffix and the leaf-partition (`ppk1`) name in the message;
   goopg's `assertNoChildRows` reports the declared `RefTable` and the
   referencing table's own name.
2. **Lock held to commit.** The attach's FOR-KEY-SHARE lock on the referenced
   rows must be **held until the attaching transaction commits**, so a
   concurrent `delete from ppk1` blocks (`<waiting ...>`) behind an uncommitted
   attach; today the validation waits on in-flight deletes but does not itself
   leave a lock that a later delete blocks on.
3. A secondary `table "pfk1" does not exist` error on the Class-B path
   (post-attach catalog visibility) needs investigation.

## Gates

- `fk-partitioned-1` probe: all Class-A permutations byte-identical to PG 18.3
  (first divergence now at the first Class-B permutation).
- Non-regression: `TestPort_IsolationReferentialIntegrity`, `…RiTrigger`,
  `…FkDeadlock`, `…FkDeadlock2`, `…FkContention`, `…FkSnapshot`,
  `…TemporalRangeIntegrity`, `…PartitionConcurrentAttach`,
  `…DetachPartitionConcurrently1`, `…AlterTable4`, `…InheritTemp`,
  `…PartitionDropIndexLocking` PASS.
- `go test ./internal/executor/` PASS; `go build ./...` clean; pgbench smoke =
  pre-commit hook.

Spec remains `defer` (not promoted) until Class B lands.
