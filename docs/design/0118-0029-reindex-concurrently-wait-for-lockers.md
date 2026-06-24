# 0118-0029 — `reindex-concurrently` isolation spec: REINDEX TABLE CONCURRENTLY wait-for-lockers

- **Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
- **Status:** accepted
- **Spec promoted:** `reindex-concurrently` (6 permutations, byte-identical to PG 18.3)
- **Test:** `TestPort_IsolationReindexConcurrently` (`runIsoSpecStrict`)

## Problem

`REINDEX TABLE CONCURRENTLY reind_con_tab` failed two ways against the spec:

1. **Parser gap.** goopg's `parseReindex` accepted `CONCURRENTLY` only *before*
   the object-type keyword (`REINDEX CONCURRENTLY TABLE …`, the legacy spelling).
   The modern — and the only standard — PostgreSQL position is *after* the type
   keyword: `REINDEX TABLE CONCURRENTLY name`. The spec uses that form, so the
   statement raised a syntax error at the relation name.

2. **Missing wait.** In PostgreSQL, `REINDEX … CONCURRENTLY` runs in several
   phases, each of which calls `WaitForLockers` to wait for every transaction
   holding a lock on the table to finish before it swaps in the rebuilt index —
   *without* taking a lock that blocks those transactions (it holds only
   `ShareUpdateExclusive`, which does not conflict with `AccessShare`/
   `RowExclusive`). The spec observes this: a `reindex` issued while another
   session has the table open reports `<waiting ...>` and completes only after
   that transaction commits. goopg's REINDEX is a no-op stub, so it returned
   immediately and never showed `<waiting ...>`.

## Fix

### Parser (`internal/parser/parser.go`)

After the object-type switch in `parseReindex`, accept an optional
`CONCURRENTLY` keyword in the post-type position (only if it was not already
seen in the legacy pre-type position). Both spellings now set
`ReindexStmt.Concurrently`.

### Wait-for-lockers (`internal/executor/context.go`)

New `(*Context).waitForRelationLockers(rel storage.RelFileNode) error` — the
WaitForLockers analog on the dedicated `tableLockMgr`:

- It polls `tableLockMgr.Holders(tag)` and returns as soon as no **other**
  backend (`!= TxnLockBackendID`, `!= BackendID`) holds a lock on the relation.
- It takes **no lock of its own**, so concurrent reads (`AccessShare`) and
  writes (`RowExclusive`) proceed unimpeded — the CONCURRENTLY contract.
- System catalogs (`RelOid < firstNormalObjectOID`) are skipped; a context
  cancellation/timeout (statement_timeout / client cancel) aborts the wait with
  the matching SQLSTATE-57014 error, exactly like the lock-wait helpers.
- A `pg_stat_activity` lock wait-event is reported for the duration of the wait.

The heavyweight table locks it observes are the transaction-scoped
`acquireScanReadLockTxn` (reads, `AccessShare`) / `acquireWriteLockTxn`
(writes, `RowExclusive`) registered by prior M0118-0008 slices under
`TxnLockBackendID`. Crucially these are only registered **inside an explicit
transaction**, so a bare `BEGIN` with no table access registers nothing — which
is why a `reindex` started before any concurrent session has touched the table
(permutation 1) returns immediately, matching PostgreSQL.

### Executor (`internal/executor/operators_reindex.go`)

`reindexOp.Next` now captures the looked-up table in the `TABLE` case and, when
`Concurrently` is set, calls `waitForRelationLockers` on its `RelFileNode`
before the (no-op) physical rebuild.

## Why a poll loop reproduces the output byte-for-byte

The isolation runner (`framework/isolation_runner.go`) detects a blocked step
purely by **timing**: a step that does not complete within `blockDetectWait`
(300 ms) is reported `<waiting ...>`, and its completion surfaces when the query
returns. No `pg_locks`/is-blocked probe is used. So a REINDEX that simply does
not return while it polls is correctly rendered as blocked, and completes the
instant the lockers drain.

A single "wait until no other holder" pass reproduces all six permutations:

| permutation | at reindex start | drains after |
|---|---|---|
| `reindex sel1 …` | no holder (only bare `BEGIN`s) | — (completes immediately) |
| `sel1 reindex …` | `{s1}`, then `{s1,s2}` as s2 acts | last commit (`end2`) |
| `sel1 upd2 reindex …` | `{s1,s2}` | `end2` |
| `sel1 upd2 ins2 reindex …` | `{s1,s2}` | `end2` |
| `sel1 upd2 ins2 del2 reindex …` | `{s1,s2}` | `end2` |
| `sel1 upd2 ins2 del2 end1 reindex end2` | `{s2}` (s1 already committed) | `end2` |

Because the runner serialises step *starts*, every concurrent holder that will
exist is present (or arrives while polling) before the set empties, so the
drain-until-empty wait lands on the last concurrent commit every time.

## Blast radius

- The wait is gated on `Concurrently && ObjectType == "TABLE"`; plain `REINDEX`
  stays a no-op. REINDEX is not on any hot path.
- `waitForRelationLockers` only **reads** the lock manager (`Holders`) and takes
  no lock, so it cannot introduce new blocking on pgbench/TPC-H.
- Same confinement as the sibling lock helpers (system-catalog + autocommit
  skip).

## Gates

- `TestPort_IsolationReindexConcurrently` strict PASS (6 permutations).
- Lock-sibling regression: `create-trigger`, `sequence-ddl`,
  `drop-index-concurrently-1` strict PASS.
- `go test -race ./internal/lockmgr/... ./internal/executor/...` PASS.
- `internal/parser` unit tests PASS.
- pgbench TPC-B smoke via the pre-commit hook.

## Remaining M0118-0008 tail (still deferred)

`reindex-concurrently-toast` (needs `allow_system_table_mods` GUC),
`reindex-schema` (`REINDEX SCHEMA CONCURRENTLY` parsing), `multiple-cic` and the
partition CONCURRENTLY specs (CIC waiting + `ALTER TABLE … DETACH PARTITION
CONCURRENTLY` parsing + `pg_backend_pid`), `alter-table-*` (ADD/VALIDATE
CONSTRAINT lock semantics), the `*-conflict` family (CREATE ROLE/GRANT/SET ROLE
privilege infra), `inherit-temp`, `plpgsql-toast`. The `waitForRelationLockers`
primitive added here is reusable by the other CONCURRENTLY specs once their
parsing/feature blockers land.
