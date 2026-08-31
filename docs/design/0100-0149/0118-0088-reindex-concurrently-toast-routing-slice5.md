# 0118-0088 — REINDEX … CONCURRENTLY pg_toast.<name> routing, slice 5 (M0118-0008)

Status: accepted
Spec: `postgres/src/test/isolation/specs/reindex-concurrently-toast.spec`
Epic: TOAST-exposure (slices 1–4 = 0118-0084/0085/0086/0087); **this is the final slice.**

## Summary

`reindex-concurrently-toast` was the **last** unpromoted spec of the 25-spec
M0118-0008 isolation group (the other 24 already pass strict). Slices 1–4 exposed
a table's TOAST relation + its unique btree index as catalog objects, named them
through `::regclass`, and let `ALTER TABLE/INDEX … RENAME` under
`allow_system_table_mods` give them the deterministic names the spec's global
setup needs (`reind_con_toast` / `reind_con_toast_idx`). This slice routes the
`REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name>` steps and reproduces their
concurrency markers, promoting the spec to pass.

With this slice the **entire M0118-0008 isolation group is strict**.

## The spec's REINDEX behaviour

Session `s1` (BEGIN) takes a `LOCK TABLE reind_con_wide` (ROW EXCLUSIVE / SHARE /
EXCLUSIVE) and may then `ins1`/`upd1`/`del1` (writes large toasted `data`) or
`dro1` (DROP TABLE). Session `s2` issues `retab2`
(`REINDEX TABLE CONCURRENTLY pg_toast.reind_con_toast`) or `reind2`
(`REINDEX INDEX CONCURRENTLY pg_toast.reind_con_toast_idx`). Across 60
permutations the expected output shows three distinct outcomes:

1. **Wait then complete** — when `s1` performed a DML write or DROP *before* the
   REINDEX, the REINDEX reports `<waiting ...>` and completes only after `s1`
   commits/rolls back.
2. **No wait** — when the REINDEX is issued *immediately* after `s1`'s bare
   `LOCK TABLE` (no DML/DROP yet), it completes at once.
3. **does-not-exist** — when `s1` dropped the parent and committed while the
   REINDEX waited, the REINDEX then errors
   `relation "pg_toast.reind_con_toast[_idx]" does not exist`.

The load-bearing distinction between (1) and (2) is **what a transaction must
hold for REINDEX CONCURRENTLY to wait for it**: PG's REINDEX CONCURRENTLY on a
TOAST relation waits for lockers of the **TOAST relation**, not the parent table.
A transaction locks the TOAST relation only when it *toasts a value* (DML write)
or *drops the table* — a bare `LOCK TABLE parent` never propagates to the toast
rel. So `lrex1 ins1 retab2` waits (ins1 locked the toast rel) but
`lrex1 retab2 dro1` does not (at `retab2` only the parent was locked).

## Why the parent-table wait (first attempt) was wrong

The obvious routing — resolve the toast name to its parent and call
`waitForRelationLockers(parent)` — fails permutation (2): in goopg both the
explicit `LOCK TABLE … ROW EXCLUSIVE` and a DML write register the *same*
transaction-scoped `RowExclusiveLock` on the parent, so a parent-locker wait can't
tell `lrex1`-alone from `lrex1 ins1`. The wait must observe the **toast
relation's** lockers, which only DML/DROP take.

## Implementation

goopg stores TOAST inline (no physical toast heap), so the toast relation has no
real lock today. We model the lock that PG's toast path takes, confined to
toast-bearing tables and observed only by the toast REINDEX wait:

- **`catalog.(*InMemory).ToastRelFileNode(parentRel) (RelFileNode, bool)`** —
  returns the synthetic TOAST relation's lock node (parent `RelFileNode` with
  `RelOid` replaced by `parentOID + toastRelidOffset`) when the parent owns an
  auto-exposed TOAST relation; only `DB`+`RelOid` matter in a lock tag.
- **`catalog.(*InMemory).ToastParentTable(toastOID) (*Table, bool)`** — maps a
  synthetic TOAST relation/index OID back to its parent table.
- **`acquireWriteLockTxn` (context.go)** — after locking the parent
  `RowExclusiveLock`, also takes `RowExclusiveLock` on the toast rel (if any),
  mirroring PG opening the toast rel `RowExclusiveLock` when storing a toasted
  value. Held to end-of-transaction inside an explicit block; transient in
  autocommit (same lifecycle as the parent write lock).
- **`dropTableByRef` (operators_ddl.go)** — after the parent
  `AccessExclusiveLock`, also `AccessExclusiveLock`s the toast rel, mirroring
  `performDeletion` dropping the toast rel under the same lock.
- **`reindexOp.Next` (operators_reindex.go)** — a `pg_toast`-schema target is
  resolved via `LookupToastRel`; `CONCURRENTLY` then waits on the TOAST
  relation's lockers (`ToastParentTable` → `ToastRelFileNode` →
  `waitForRelationLockers`) for **both** the TABLE and INDEX object types (a toast
  index reindex waits for its toast table's lockers). After the wait it re-checks
  `LookupToastRel`; a gone parent (DROP committed) yields the 42P01 does-not-exist
  error. A non-existent toast name errors 42P01 up front.

A bare `LOCK TABLE` on the parent goes through neither the write nor the drop
path, so it never registers a toast-rel lock — exactly the (2)/(1) split.

## Blast radius

- The new toast-rel lock is taken on every DML write / DROP of a **toast-bearing
  user table**, but `RowExclusiveLock` is self-compatible and the only reader of
  toast-rel lockers is this spec's REINDEX wait, so no other spec's timing
  changes. (Verified: sibling concurrency/DDL specs below stay strict.)
- Inert for tables with no toastable column (`ToastRelFileNode` returns false),
  for system relations, and outside the catalog `*InMemory` implementation.
- No catalog-row/regclass/pg_dump/pg_amcheck output changes — this slice only
  adds lock acquisitions and REINDEX routing, no new virtual rows.

## Oracle

`postgres/src/backend/catalog/index.c` (`reindex_index` /
`ReindexRelationConcurrently` → `WaitForLockers` on the toast relation),
`postgres/src/backend/access/common/toast_internals.c` (toast rel opened
`RowExclusiveLock` on store), `postgres/src/backend/catalog/dependency.c`
(`performDeletion` drops the toast rel under the table's lock). Compared against
`./postgres/local_install` PG 18.3 expected output.

## Gates

- `TestPort_IsolationReindexConcurrentlyToast` **strict PASS** (all 60
  permutations byte-identical) — promoted soft→strict (pass-required).
- Sibling specs strict PASS (no regression): `IsolationReindexConcurrently`,
  `ReindexSchema`, `MultipleCic`, `DropIndexConcurrently1`, `AlterTable3`,
  `PlpgsqlToast`, `TruncateConflict`, `VacuumConcurrentDrop`, `ClusterConflict`,
  `DetachPartitionConcurrently1`, `MergeUpdate`, `InsertConflictDoUpdate2`,
  `VacuumNoCleanupLock`.
- TOAST-exposure slice tests PASS (`TestToastRelation*`,
  `TestReltoastrelidRegclassRendersToastName`).
- `go test ./internal/{catalog,executor}/` PASS; `go build ./...` clean.
- D-002 CSV rationale updated + `postgres-oracle-port-status.md` regenerated.
- pgbench smoke = pre-commit hook (mandatory).
