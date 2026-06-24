# 0118-0030 — `reindex-schema` isolation spec: REINDEX SCHEMA per-relation locking

Status: accepted
Milestone: M0118-0008 (Upstream isolation-spec suite pass-through — DDL / VACUUM /
maintenance concurrency)
Date: 2026-06-22

## Summary

Promotes the upstream `reindex-schema.spec` isolation spec to **pass-required**
(`runIsoSpecStrict`, byte-identical to PostgreSQL 18.3 across both permutations).
This is the **fourth** M0118-0008 promotion, after `create-trigger` (0118-0027),
`sequence-ddl` (0118-0028) and `reindex-concurrently` (0118-0029).

## The spec

`reindex-schema` checks that a concurrent `DROP TABLE` of one relation in a
schema succeeds while a `REINDEX SCHEMA` of that schema is *waiting* on another,
locked relation:

```
setup: CREATE SCHEMA reindex_schema;
       CREATE TABLE reindex_schema.tab_locked  (a int PRIMARY KEY);
       CREATE TABLE reindex_schema.tab_dropped (a int PRIMARY KEY);

s1: begin1 { BEGIN; }
    lock1  { LOCK reindex_schema.tab_locked IN SHARE UPDATE EXCLUSIVE MODE; }
    end1   { COMMIT; }
s2: reindex2      { REINDEX SCHEMA reindex_schema; }
    reindex_conc2 { REINDEX SCHEMA CONCURRENTLY reindex_schema; }
s3: drop3  { DROP TABLE reindex_schema.tab_dropped; }

permutation begin1 lock1 reindex2      drop3 end1
permutation begin1 lock1 reindex_conc2 drop3 end1
```

Expected: `reindex2` / `reindex_conc2` reports `<waiting ...>`, `drop3` completes
immediately, then the reindex completes once `end1` commits.

## Root cause

`reindexOp.Next` had **no `SCHEMA` case** — `REINDEX SCHEMA` parsed and returned
EOF immediately, taking no lock, so it never waited and the two permutations
diverged from PG at the very first step (`reindex2` did not report waiting).

## Behaviour reproduced

The physical index rebuild is a no-op in goopg v0, but the **lock behaviour** is
observable. Upstream `REINDEX SCHEMA`:

- collects the relations in the schema, then processes them one at a time;
- a **plain** reindex takes a `ShareLock` on each table;
- **`CONCURRENTLY`** takes a `ShareUpdateExclusiveLock` and waits for existing
  lockers to drain.

Both `ShareLock` and `ShareUpdateExclusiveLock` conflict with the
`ShareUpdateExclusive` that `lock1` holds (standard lock conflict matrix), so the
reindex stalls on `tab_locked`. Because relations are processed in **OID
(creation) order**, the stall lands on the earliest-created table (`tab_locked`,
created before `tab_dropped`) first — so the reindex never reaches/locks
`tab_dropped`, letting `drop3` proceed. After `end1` commits, the lock drains and
the reindex completes.

## Implementation

Two files (`internal/executor/`):

1. **`context.go` — generalised the autocommit-transient lock helper.** Extracted
   the body of `acquireSequenceLockTxn` (added in 0118-0028) into a new
   mode-parameterised `(*Context).acquireRelLockMaybeTransient(rel, mode)`:
   - inside an explicit transaction (`TxnLockBackendID != 0`) the lock is held to
     end-of-transaction via `acquireRelLockTxn`;
   - in autocommit it is acquired transiently under the globally-unique
     per-statement `BackendID` and **released the instant it is granted**, so the
     *wait* on a conflicting holder still happens during acquisition but no lock
     lingers past the statement (PostgreSQL's single-statement implicit
     transaction).

   `acquireSequenceLockTxn` now delegates to it with `RowExclusiveLock` (no
   behaviour change for `sequence-ddl`).

2. **`operators_reindex.go` — added the `SCHEMA` case.** Enumerates the schema's
   non-virtual user tables via `Catalog.TablesInSchema`, sorts them by OID
   (`schemaRelsByOID` helper), and per relation either:
   - **plain**: `acquireRelLockMaybeTransient(rel, lockmgr.ShareLock)`; or
   - **CONCURRENTLY**: `waitForRelationLockers(rel)` (the 0118-0029 primitive,
     reused — waits for lockers without taking a conflicting lock).

   The reindex of `reindex_schema` runs autocommit, so the plain path takes the
   transient `ShareLock` (blocks on `tab_locked`, then no-op rebuild).

## Blast radius

Narrow. `acquireRelLockMaybeTransient` only adds a lock to `REINDEX SCHEMA`
(previously a complete no-op) and to the already-locking `nextval()` path
(unchanged). `ShareLock`/`ShareUpdateExclusive` is acquired only per
REINDEX-SCHEMA relation; system catalogs (`OID < firstNormalObjectOID`) are
skipped. No hot-path (DML/scan) change. `REINDEX TABLE`/`INDEX`/`DATABASE`
behaviour is unchanged.

## Gates

- `TestPort_IsolationReindexSchema` strict PASS (both permutations).
- Lock-sibling regression PASS: `TestPort_IsolationReindexConcurrently`,
  `TestPort_IsolationCreateTrigger`, `TestPort_IsolationSequenceDdl`,
  `TestPort_IsolationDropIndexConcurrently1`, `TestPort_TimeoutsTableLevel`.
- `-race` lockmgr; executor unit (`TestReindex*`/`TestSequence*`/`TestLock*`).
- pgbench smoke (pre-commit hook): 0 failed.

## Remaining (M0118-0008 stays open)

`reindex-concurrently-toast` (`allow_system_table_mods` GUC + `pg_toast`
reindex), `multiple-cic` (CREATE INDEX CONCURRENTLY must evaluate an immutable
partial-index predicate function during build — constant-folding + advisory-lock
block), `alter-table-*` (ADD/VALIDATE CONSTRAINT lock semantics), the
`*-conflict` family (truncate/vacuum/cluster — need CREATE ROLE/GRANT/SET ROLE
privilege infra), partition specs, `inherit-temp`, `plpgsql-toast`.
`REINDEX SCHEMA CONCURRENTLY` parsing/waiting is now covered (this loop).
