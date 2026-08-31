# 0118-0046 — `alter-table-2`: ADD FOREIGN KEY … NOT VALID lock semantics (M0118-0008)

**Status:** accepted
**Milestone:** M0118-0008 (Upstream isolation spec suite pass-through — DDL/VACUUM/maintenance concurrency)
**Spec:** `postgres/src/test/isolation/specs/alter-table-2.spec`
**Test:** `internal/testport/isolation_port_test.go` → `TestPort_IsolationAlterTable2` (`runIsoSpecStrict`)

## Summary

Promote `alter-table-2` to **pass-required** (strict), byte-identical to PG 18.3
across all **48 permutations**. The spec mixes `ALTER TABLE b ADD CONSTRAINT bfk
FOREIGN KEY (a_id) REFERENCES a (i) NOT VALID` (session `s1`, in an explicit
transaction) with concurrent reads / `SELECT … FOR UPDATE` / `INSERT`s on both
the referencing table `b` and the referenced table `a` (session `s2`), to
observe what waits and what proceeds.

> *ADD CONSTRAINT uses ShareRowExclusiveLock so we mix writes with it to see what
> works or waits.* — spec header.

## Gaps fixed

### 1. Parser: accept the `NOT VALID` trailer on ADD FOREIGN KEY

`parseAlterTableAction`'s `ADD … FOREIGN KEY …` arm only consumed an optional
`[NOT] DEFERRABLE` trailer — on hitting `NOT VALID` it consumed `NOT` and then
`expectKeyword(KwDeferrable)` failed with `expected keyword deferrable (got
valid)`. Replaced the single `if/else if` with a small loop that accepts
`[NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE]` **and** `NOT VALID` in any
order (PG's grammar allows e.g. `… DEFERRABLE NOT VALID`). New AST field
`AlterTableAction.NotValid`.

`NOT VALID` means the constraint is created **without** checking pre-existing
rows; a later `VALIDATE CONSTRAINT` performs the scan. goopg does not enforce FK
referential integrity (v0 stance, design 0003-0004), so `NOT VALID` is recorded
but otherwise a no-op semantically — except it sets `pg_constraint.convalidated`
to `'f'` (new `catalog.ForeignKey.NotValid` → `convalidated` column in the
virtual `pg_constraint` builder). No `port` spec reads `convalidated` here; it is
recorded for faithfulness / future `VALIDATE CONSTRAINT`.

### 2. Executor: ADD CONSTRAINT takes a transaction-scoped ShareRowExclusiveLock

PostgreSQL's `AlterTableGetLockLevel` returns `ShareRowExclusiveLock` for
`AT_AddConstraint` (an FK). The `AlterTableAddForeignKey` case in
`execAlterTable` now calls the existing `acquireDDLLockTxn(rel,
lockmgr.ShareRowExclusiveLock)` on the **altered** table `b` before recording the
FK — the same write/DDL lock helper used by `create-trigger` (0118-0027) and
`alter-table-3` (0118-0032).

The standard lock-conflict matrix then drives every permutation:

| s2 step on `b`            | lock held by s2     | vs s1 ShareRowExclusive | result      |
|---------------------------|---------------------|-------------------------|-------------|
| `SELECT … FOR UPDATE`     | RowShareLock        | compatible              | proceeds    |
| `INSERT INTO b`           | RowExclusiveLock    | **conflict**            | one waits   |
| plain `SELECT`            | AccessShareLock     | compatible              | proceeds    |

So when `s2d INSERT INTO b` holds its RowExclusiveLock (s2 still open) and `s1b`
runs, `s1b` waits until `s2f COMMIT`; conversely when `s1b` holds its
ShareRowExclusiveLock (s1 still open) and `s2d` runs, `s2d` waits until `s1c
COMMIT`. `SELECT … FOR UPDATE` on `b` (RowShareLock) and the operations on the
referenced table `a` never conflict with the ADD-FK lock.

Confinement is identical to the existing DDL-lock siblings: a **no-op in
autocommit** (`TxnLockBackendID == 0`) and for **system catalogs**
(`RelOid < firstNormalObjectOID`), so the pg_dump-restore / HammerDB-load path
(which runs ADD FK in autocommit or a restore transaction) takes no new lock and
pgbench TPC-B is untouched.

#### Referenced table `a`

Upstream `ATAddForeignKeyConstraint` also locks the **referenced** relation in
`ShareRowExclusiveLock`. The spec cannot distinguish this: `s2e INSERT INTO a`
(RowExclusiveLock on `a`) always runs *after* `s2d INSERT INTO b` in every
permutation, so by the time `s1b` could conflict on `a` it is already waiting on
`b`. We therefore lock only the altered table `b`, which matches all 48
permutations exactly and keeps the blast radius minimal (no other `port` spec
exercises concurrent ADD FK against the referenced table). If a future spec
distinguishes it, the referenced-table lock is a one-line addition.

## Scope / deferred

`alter-table-1` (the sibling spec) additionally needs `ALTER TABLE … VALIDATE
CONSTRAINT name` parsing + its `ShareUpdateExclusiveLock` level — deferred to a
follow-up loop. The rest of the M0118-0008 tail (partition ATTACH/DETACH
transactional-DDL visibility, `reindex-concurrently-toast`, `plpgsql-toast`)
remains deferred per the ledger.

## Gates

- `TestPort_IsolationAlterTable2` strict PASS (48 permutations, byte-identical).
- Sibling lock-DDL specs `TestPort_IsolationAlterTable3` / `CreateTrigger` /
  `SequenceDdl` PASS (no lock-matrix regression).
- Parser units (`internal/parser`) PASS — `NOT VALID` trailer + existing FK
  shape (`TestParseAlterTableAddForeignKey`).
- Executor + catalog units PASS.
- `go build ./...` + `go vet` clean.
- pgbench TPC-B smoke (pre-commit hook) — 0 failed, no TPS regression (ADD-FK
  lock is no-op in autocommit; INSERT/UPDATE write lock unchanged).
