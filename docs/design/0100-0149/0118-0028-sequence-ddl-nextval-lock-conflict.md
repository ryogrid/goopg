# 0118-0028 — `sequence-ddl` isolation spec: nextval / ALTER SEQUENCE table-lock conflict (M0118-0008)

**Status:** accepted
**Date:** 2026-06-22
**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency isolation specs)
**Spec:** `postgres/src/test/isolation/specs/sequence-ddl.spec`

## Summary

Promotes `sequence-ddl` to pass-required (byte-identical to PostgreSQL 18.3) by
giving sequence access the heavyweight relation locks PostgreSQL holds:

- `nextval()` now takes a **`RowExclusiveLock`** on the sequence relation
  (mirrors upstream `lock_and_open_sequence` in `sequence.c`, which calls
  `LockRelationOid(seqrelid, RowExclusiveLock)`).
- `ALTER SEQUENCE` now takes an **`AccessExclusiveLock`** on the sequence
  relation (`acquireDDLLockTxn`, the same DDL-lock helper that landed for
  `create-trigger` in 0118-0027).

`RowExclusiveLock` conflicts with `AccessExclusiveLock` in the standard
lock-conflict matrix, so a concurrent `nextval` blocks while another session is
mid-`ALTER SEQUENCE` (inside an open transaction), and a later `ALTER SEQUENCE`
waits for an in-progress `nextval` to commit — exactly the behavior the spec
pins.

## Background — what the spec exercises

`sequence-ddl.spec` (PG10+ comment: *"the s2nv step would see the uncommitted
s1alter change, but now it waits"*) interleaves two sessions over a single
`seq1`:

- **s1** runs inside an explicit transaction (`setup { BEGIN; }`): `ALTER
  SEQUENCE seq1 MAXVALUE 10` / `MAXVALUE 20` / `RESTART WITH 5`, then `COMMIT`.
- **s2** calls `SELECT nextval('seq1') FROM generate_series(1, 15)` — sometimes
  in autocommit, sometimes inside its own explicit transaction (`s2begin`).

The five permutations pin two facts:

| permutation | pinned behavior |
|-------------|-----------------|
| `s1alter s1commit s2nv` | s1 already committed → s2nv does **not** wait (errors at MAXVALUE 10) |
| `s1alter s2nv s1commit` | s2nv (**autocommit**) **waits** for s1's lock, then completes after s1 commits |
| `s1restart s2nv s1commit` | same, for `RESTART WITH 5` |
| `s2begin s2nv s1alter2 s2commit s1commit` | s2nv holds its lock to end-of-txn → `s1alter2` **waits** for `s2commit` |

All the sequence *semantics* (MAXVALUE clamping, the "reached maximum value"
error, RESTART WITH) already matched PG byte-for-byte; the sole divergence was
the missing `<waiting ...>` annotations — goopg let `nextval` and `ALTER
SEQUENCE` run without taking any cross-statement relation lock.

## Design

### The lock identities

Sequences are virtual catalog relations: `CREATE SEQUENCE` (and SERIAL/IDENTITY
registration) calls `createSeqCatalogTable`, which registers a `catalog.Table`
with `IsSequence = true` and a user OID (≥ `firstNormalObjectOID`). So a
sequence has a `RelFileNode` reachable via `Catalog.LookupTable(name)` →
`Catalog.RelFileNode(tbl)`, and the same `tableLockMgr` that backs `LOCK TABLE`
and the create-trigger DDL/DML locks can lock it.

### nextval — `acquireSequenceLockTxn`

PostgreSQL's `nextval` holds `RowExclusiveLock` until end-of-transaction. goopg
splits the two cases the isolation tester produces on a persistent per-session
connection:

- **Explicit transaction** (`TxnLockBackendID != 0`): acquire
  `RowExclusiveLock` under the transaction backend identity via
  `acquireRelLockTxn` — held until `connTxState.End()` drops it at
  COMMIT/ROLLBACK. This is what makes `s1alter2` wait for `s2commit` in
  permutation 5, and it is idempotent (the 15 `generate_series` iterations
  re-acquire the same backend+tag+mode as a cheap mask check).
- **Autocommit** (`TxnLockBackendID == 0`): a single autocommit statement *is*
  its own implicit transaction, so the lock only needs to live for the duration
  of the acquire. We acquire `RowExclusiveLock` under the globally-unique
  per-statement `BackendID` and `Release` it as soon as it is granted. The
  **wait still happens during acquisition** — an autocommit `nextval` blocks
  while another session holds a conflicting `ALTER SEQUENCE` lock — which is
  exactly what permutations 2/3 require. Releasing immediately avoids leaking a
  dead lock entry under a per-statement identity that nothing else would ever
  release.

The per-statement `BackendID` (minted in `dispatch.go`) and the per-connection
`LockBackendID` (minted in `server.go`) both come from the same
`s.nextBackendID` atomic counter, so they are globally unique and never collide;
the targeted `Release(BackendID, tag, RowExclusiveLock)` therefore drops exactly
the lock this `nextval` took, with no risk to any other backend.

`RowExclusiveLock` is self-compatible, so concurrent `nextval`s — including
SERIAL/IDENTITY inserts on the hot path — never block each other at the table
level; only a concurrent `ALTER SEQUENCE` (or other AccessExclusive acquirer)
conflicts, matching PostgreSQL. System catalogs (OID < `firstNormalObjectOID`)
are skipped.

### ALTER SEQUENCE — `acquireDDLLockTxn`

`execAlterSequence` resolves the sequence's `catalog.Table` and takes a
transaction-scoped `AccessExclusiveLock` via the existing `acquireDDLLockTxn`
(no-op in autocommit and for system catalogs, held until COMMIT otherwise),
before mutating any sequence parameters. In the spec ALTER always runs inside
s1's explicit transaction, so the lock is held until `s1commit`, blocking a
concurrent `nextval` (permutations 2/3) and being blocked by an in-progress
`nextval`'s held lock (permutation 5).

## Confinement / blast radius

This reuses the create-trigger machinery's confinement: the lock helpers are
no-ops for system catalogs, and `acquireDDLLockTxn` is a no-op outside an
explicit transaction. The only new always-on cost is the autocommit `nextval`
acquire+release, which is bounded to user sequences and never blocks against
another `nextval` (self-compatible mode). pgbench's default TPC-B schema uses no
sequences on its hot UPDATE path, so the smoke test is unaffected (0 failed
transactions).

## Testing / gates

- `TestPort_IsolationSequenceDdl` (`runIsoSpecStrict`) — all five permutations
  byte-identical to PG 18.3.
- `go test -race ./internal/lockmgr/... ./internal/executor/...` (lock-path
  change).
- pgbench TPC-B smoke — 0 failed transactions.
- D-002 CSV promoted (`sequence-ddl` row → `pass`), and
  `postgres-oracle-port-status.md` / `upstream-isolation-coverage.md` /
  `postgres-oracle-target-inventory.md` regenerated.

## Oracle

`postgres/src/backend/commands/sequence.c` — `nextval_internal` /
`lock_and_open_sequence` (RowExclusiveLock); `AlterSequence`
(AccessExclusiveLock via `relation_open`). Compared against
`./postgres/local_install` PG 18.3.

## Remaining M0118-0008

`alter-table-*` (ADD/VALIDATE CONSTRAINT lock semantics), the `*-conflict`
family (truncate/vacuum/cluster — need CREATE ROLE/GRANT/SET ROLE privilege
infrastructure), `reindex-*`, `multiple-cic`/partition specs, `inherit-temp`,
and `plpgsql-toast` stay deferred. The group remains open.
