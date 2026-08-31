# DU-002 slice 321 — `ALTER TABLE … CLUSTER ON` / `SET WITHOUT CLUSTER` (restore side)

Status: accepted

## Problem

Slice 320 made a clustered index round-trip *out* of goopg: `CLUSTER <t> USING
<idx>` records the selection on `pg_index.indisclustered`, and pg_dump re-emits it
as a trailing

```sql
ALTER TABLE <t> CLUSTER ON <idx>;
```

after the index's `CREATE INDEX` (dumpIndex, pg_dump.c:18141) or constraint's
`ADD CONSTRAINT` (dumpConstraint, pg_dump.c:18483).

But goopg could not **parse or execute** that `ALTER TABLE … CLUSTER ON …` clause
— `parseAlterTableAction` had no `CLUSTER ON` arm and the executor had no
matching action kind. So goopg produced a dump it could not restore into itself:
the clustering selection survived the dump but was rejected on reload (syntax
error). The companion `SET WITHOUT CLUSTER` form (which clears the selection) was
likewise unparseable.

## Fix

Mirror the existing `REPLICA IDENTITY` action plumbing.

### Parser (`internal/parser/`)

- `ast.go`: two new `AlterTableActionKind` values — `AlterTableClusterOn` and
  `AlterTableSetWithoutCluster` — plus a `ClusterIndexName string` field on
  `AlterTableAction`.
- `ddl.go` `parseAlterTableAction`:
  - `CLUSTER ON <ident>` → `AlterTableClusterOn{ClusterIndexName: <ident>}`
    (errors if `ON` or the index name is missing).
  - `SET WITHOUT CLUSTER` → `AlterTableSetWithoutCluster`. This branch is gated on
    the next token after `SET` being the identifier `without`, so it does **not**
    shadow the pre-existing `SET (reloptions)` parenthesized form (which keeps
    matching via `isAlterReloptVerb` + `(`).

### Executor (`internal/executor/`)

- `operators_cluster.go`: extracted two shared helpers from `clusterOp.Next()`:
  - `markTableClusterIndex(ctx, tbl, idxName, pos)` — resolve the named index in
    `Catalog.IndexesOnTable(tbl)` (`42704` if absent), set its `IsClustered` and
    clear the flag on every other index of the table (`mark_index_clustered`,
    cluster.c), re-syncing each changed index's pg_index heap row via
    `resyncIndexHeapRow`.
  - `clearTableClusterIndex(ctx, tbl)` — clear `IsClustered` on every index of the
    table (`ATExecSetWithoutCluster` → `mark_index_clustered(rel, InvalidOid)`).
  - `clusterOp.Next()` (the `CLUSTER <t> USING <idx>` statement, slice 320) now
    calls `markTableClusterIndex`, so the statement and ALTER paths stay in sync.
- `operators_ddl.go` ALTER-TABLE action loop: `AlterTableClusterOn` calls
  `markTableClusterIndex`; `AlterTableSetWithoutCluster` calls
  `clearTableClusterIndex`.

## Scope / blast radius

Dump-fidelity only — no physical index-ordered heap reorder (goopg has none).
The `CLUSTER ON` action sets exactly the same `catalog.Index.IsClustered` /
`pg_index.indisclustered` state the `CLUSTER … USING` statement already set in
slice 320, so the dump output is unchanged; this slice only widens the SQL
surface goopg accepts. No new catalog field. `SET WITHOUT CLUSTER` only clears a
flag that defaults false. Zero impact on any object that never clusters.

## Oracle

- pg_dump emits `ALTER TABLE <t> CLUSTER ON <idx>` (dumpIndex / dumpConstraint).
- `tablecmds.c` `ATExecClusterOn` → `mark_index_clustered(rel, indexOid, false)`;
  `ATExecSetWithoutCluster` → `mark_index_clustered(rel, InvalidOid, false)`.

## Gates

- `internal/parser` `TestParseAlterTableClusterOn` (CLUSTER ON captures the index
  name; SET WITHOUT CLUSTER is its own kind; `SET (reloptions)` is unaffected) —
  PASS.
- `internal/executor` `TestDDLAlterTableClusterOnRoundTrip` (CLUSTER ON marks the
  index; re-pointing clears the previous one; unknown index → 42704; SET WITHOUT
  CLUSTER clears the selection) — PASS.
- `internal/testport` `TestPort_PgDumpConnectionSetup` (slice 320 round-trip,
  exercising the refactored `clusterOp`) — PASS vs real pg_dump 18.3.
- `internal/parser` + `internal/executor` + `internal/catalog` suites — PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

pg_dump 002–010 catalog parity (GRANT/ACL `relacl`, `CREATE RULE`, `CREATE
POLICY`/RLS); extended-protocol commit-time deferral (architecturally entangled).
The CLUSTER round-trip (out via slice 320, back in via this slice) is now closed.
