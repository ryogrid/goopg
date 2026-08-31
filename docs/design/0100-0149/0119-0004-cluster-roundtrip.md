# 0119-0004 — Clustered-index round-trip in pg_dump (DU-002 slice 320)

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity, DU-002)

## Problem

`CLUSTER <table> USING <index>` selects a *clustering index* for a table: the
index whose order PostgreSQL physically rewrites the heap into. The selection is
recorded in `pg_index.indisclustered` (one index per table). `pg_dump` re-emits
it as a trailing statement after the index's `CREATE INDEX` (plain secondary
index — `dumpIndex`, pg_dump.c:18141) or after the constraint's
`ALTER TABLE … ADD CONSTRAINT` (constraint-backed index — `dumpConstraint`,
pg_dump.c:18483):

```
ALTER TABLE public.clus_t CLUSTER ON clus_t_b_idx;
```

(The index name is intentionally **unqualified** in this syntax.)

goopg's `CLUSTER` executor (`operators_cluster.go`, M0095-0008) was a pure no-op:
it validated the table exists and took the AccessExclusiveLock (for the
`cluster-conflict` isolation spec) but did nothing with the `USING <index>`
clause. Both `pg_index` row builders hardcoded `indisclustered='f'`. So the
clustering selection was **silently dropped** on dump/restore — a real
round-trip fidelity gap, not a guard.

## Fix

Mirror the `REPLICA IDENTITY USING INDEX` path (slice 306), which records a
per-index flag that `pg_dump` reads back from the `pg_index` heap.

1. **Catalog field** — new `catalog.Index.IsClustered bool` (defaults false).
2. **Both `pg_index` builders project it** — `boolStr(idx.IsClustered)` in the
   virtual `pgIndexCatalog` builder (`catalog.go`, the one `pg_dump`'s
   `getIndexes` SELECT reads through) and `NewBoolDatum(idx.IsClustered)` in the
   heap row builder `buildUserPGIndexRow` (`pg18_user_catalog_rows.go`). Kept in
   sync so a restarting backend and `pg_dump` agree.
3. **Executor records the selection** — `clusterOp.Next()`, when
   `stmt.IndexName != ""`, resolves the named index among
   `Catalog.IndexesOnTable(tbl)` (case-insensitive; `42704
   index "x" for table "y" does not exist` when absent), sets `IsClustered=true`
   on it and `false` on every other index of the table
   (`mark_index_clustered`, cluster.c), and re-syncs each *changed* index's
   `pg_index` heap row.
4. **Generic heap-row resync** — `resyncIndexReplicaIdentHeap` was renamed to
   `resyncIndexHeapRow` (it already rewrites the full row from
   `buildUserPGIndexRow`, so it is flag-agnostic) and is now shared by the
   replica-identity and clustering paths.

goopg performs **no physical index-ordered heap rewrite** — this is dump
fidelity only, exactly as `REPLICA IDENTITY` is (goopg has no logical
replication). The `CLUSTER … USING` lock/validation behaviour is unchanged; the
no-`USING` and no-table forms keep their prior no-op behaviour (so the
`cluster-conflict` isolation spec is untouched).

## Blast radius

- `catalog.Index.IsClustered` defaults false → every existing index (TPC-H /
  pgbench / all PKs) projects `indisclustered='f'` exactly as before.
- The new executor branch is gated on `stmt.IndexName != ""`; `CLUSTER tbl`
  (no `USING`) and bare `CLUSTER` are byte-identical to before.
- `resyncIndexHeapRow` is the prior `resyncIndexReplicaIdentHeap` body verbatim;
  the replica-identity caller is unchanged in behaviour.

## Oracle

`postgres/src/bin/pg_dump/pg_dump.c` — `dumpIndex` (CLUSTER ON after a plain
index, line 18141) and `dumpConstraint` (after `ADD CONSTRAINT`, line 18483),
both keyed on `indxinfo->indisclustered` read from the `getIndexes`
`i.indisclustered` projection (line 7763). Backend semantics:
`postgres/src/backend/commands/cluster.c` `mark_index_clustered` (sets the flag
on the chosen index, clears all others on the relation).

## Gates

- **DU-002 slice 320** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`): `public.clus_t` clustered on a
  plain secondary index `clus_t_b_idx` (dumpIndex path) and `public.clus_pk`
  clustered on its PRIMARY KEY index `clus_pk_pkey` (dumpConstraint path); both
  `ALTER TABLE … CLUSTER ON …;` lines asserted present in the dump, verified vs
  real pg_dump 18.3 (~4.5 s). PASS.
- `internal/catalog` + `internal/executor` unit suites PASS.
- `go build ./...` clean.
- pgbench TPC-B smoke = pre-commit hook (`.githooks/pre-commit`).

## Still open under M0119-0004

pg_dump 002–010 catalog-view parity (further getter-battery slices: GRANT/ACL
`relacl`, `CREATE RULE`/`pg_get_ruledef`, `CREATE POLICY`/RLS `pg_policy`);
richer `CLUSTER`/`ALTER TABLE … CLUSTER ON`/`SET WITHOUT CLUSTER` parse+restore
support (only `CLUSTER … USING` marks the flag today); extended-protocol
commit-time deferral.
