# 0118-0119 — `fk-partitioned-1` enabler: referenced-side FK enforcement when deleting from a leaf partition

Status: accepted
Milestone: M0118-0009 (Upstream Isolation Spec Suite Pass-Through)
Spec: `postgres/src/test/isolation/specs/fk-partitioned-1.spec`
Predecessor: [0118-0118](0118-0118-fk-partitioned-attach-validation.md) (Class A: referencing-side clone + validation on ATTACH)

## Summary

**Enabler, NOT a promotion.** Closes the **committed-attach Class B** permutations
of `fk-partitioned-1`: after `pfk1` is attached as a partition of the FK-owning
`pfk` (which `references ppk`), a subsequent `DELETE FROM ppk1` (a leaf partition
of the *referenced* `ppk`) must be rejected on the **referenced side** with

```
ERROR:  update or delete on table "ppk1" violates foreign key constraint "pfk_a_fkey_1" on table "pfk"
```

goopg previously let that delete through silently: `enforceFKOnDelete` looks up
FKs via `FindFKsReferencingTable("ppk1")`, but the FK's `RefTable` is the
partitioned parent `"ppk"`, not the leaf `"ppk1"`, so nothing matched and no
referenced-side check fired.

## Background — the three names PostgreSQL reports

PG clones a foreign key's **referenced-side action triggers** down to every
partition of the referenced table. Deleting a row from a leaf partition therefore
still fires the FK's `RI_FKey_noaction_del` check. The violation names:

- **referenced table** = the leaf actually deleted from → `"ppk1"` (not `"ppk"`),
- **constraint** = the per-partition cloned constraint `<fkname>_<N>` →
  `"pfk_a_fkey_1"` (`N` = the leaf's 1-based ordinal among the referenced
  parent's partition children — `ChooseConstraintName`'s dedup suffix), and
- **referencing table** = the relation where the FK was declared → `"pfk"` (the
  root partitioned FK owner, **not** the per-partition clone `pfk1`).

This is the same `<fkname>_<N>` scheme already implemented for the *detach* side
in `detachPartitionFKRefCheck` (design 0118-0060).

## Changes

### 1. Referenced-side check for a deleted leaf partition (`internal/executor/operators_fk.go`)

New `enforceFKOnDeletePartitionAncestor(ctx, leafTbl, leafRow)`, called at the
end of `enforceFKOnDelete`. It walks `leafTbl`'s partition-ancestor chain
(`ppk1 → ppk`) and, for each FK that references an ancestor, scans the
referencing table for a still-present matching row; on a hit it raises the 23503
with the three-name message above.

- **Skip per-partition FK clones.** `FindFKsReferencingTable("ppk")` returns both
  the root `pfk`'s FK *and* the copy ATTACH placed on `pfk1`. The clone is
  skipped (`im.IsPartitionChild(ref.Child.OID)`), so the violation names the root
  `pfk`; scanning `pfk` already descends into every partition (`scanTableForMatch`
  → `allDescendants`), so the clone is redundant.
- **Ordinal suffix** = the leaf's 1-based index among the referenced parent's
  `PartitionChildren` (here `ppk1` → `_1`).

### 2. Reliable partition-parent lookup (`internal/catalog/catalog.go`)

`Table.PartitionParentOID` is **only** populated on the `CREATE TABLE … PARTITION
OF` path; `ALTER TABLE … ATTACH PARTITION` updates the `partitionChildren` map but
leaves the field `0`. Two new map-backed helpers make partition identity reliable
for both paths:

- `IsPartitionChild(oid) bool` — used to skip FK clones.
- `PartitionParentOf(childOID) (uint32, bool)` — used by the new executor helper
  `partitionParentTable` to walk leaf → parent (field first, map fallback).

### 3. Teardown: `DROP TABLE` of a parent + its partition in one statement (`internal/executor/operators_ddl.go`)

The spec's `teardown { drop table ppk, pfk, pfk1; }` failed with
`ERROR: table "pfk1" does not exist`: dropping the partitioned `pfk` cascade-drops
its partition `pfk1`, then the *explicit* `pfk1` name was looked up again and
missed. `dropPartitionDescendants` now records each cascade-dropped partition in
the statement's `cascadeDropped` set (both qualified and bare name), so a
partition listed alongside its parent is dropped once — matching PG, which
resolves all `DROP` targets as one deduplicated set. (The sibling inheritance
cascade already did this.)

## Result

- All **Class A** perms (0118-0118) **and** the four **committed-attach Class B**
  perms (`… s2a s2c … s1d …` → immediate 23503 naming `pfk_a_fkey_1` on table
  `pfk`) are byte-identical to PG 18.3.
- First divergence moved to the first **concurrent Class B** perm
  (`s1b s2b s2a s1d s2c s1c`): probe `defer`, actual 130/expected 133 lines —
  the 3 missing lines are the `<waiting ...>` / `<... completed>` of the three
  perms where `s1d` deletes **while the attach is still uncommitted**.

## Deferred — the concurrent Class B slice (lock held to commit)

The remaining 3 perms need the attach's FOR-KEY-SHARE lock on the referenced rows
to be **held to commit**, so a concurrent `DELETE FROM ppk1` blocks `<waiting ...>`
behind an uncommitted attach and then errors once the attach commits. Today the
attach validation (`scanTableForMatchFKWait`) *waits on* an in-flight delete but
leaves **no lock** for a later delete to block on, and during the uncommitted
window the referenced-side check fires eagerly (and would mis-name the clone,
since `IsPartitionChild(pfk1)` is still false pre-commit). Implementing the
held-to-commit referenced-row lock is the next slice — spec stays `defer`.

## Blast radius

`enforceFKOnDeletePartitionAncestor` is a no-op unless the deleted table is a
partition of a table that some FK references — i.e. exactly the partitioned-FK
scenario; ordinary deletes are byte-unchanged. The `DROP TABLE` fix only suppresses
a duplicate drop of a partition already removed in the same statement. No other
`port` spec exercises concurrent attach + referenced-side partition delete.

## Gates

- Probe: Class A + committed Class B byte-exact (first divergence = concurrent
  Class B perm; 130/133 lines).
- Non-regression strict: `ReferentialIntegrity`, `RiTrigger`, `FkDeadlock`,
  `FkDeadlock2`, `FkContention`, `FkSnapshot`, `TemporalRangeIntegrity`,
  `PartitionConcurrentAttach`, `DetachPartitionConcurrently{1,2,3,4}`,
  `AlterTable4`, `InheritTemp`, `PartitionDropIndexLocking` — PASS.
- `go test ./internal/executor/ ./internal/catalog/` PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.
