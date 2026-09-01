# M0119-0006bn — a genuinely-empty heap table has no on-disk main-fork file

Status: accepted
Milestone: M0119-0006 (pg_amcheck server tier)

## Problem

While re-verifying the `[[0119-0006bm]]` `pg_class.relam` fix (previous slice),
driving the real `pg_amcheck` binary against a fresh fixture with an empty
`PARTITION OF` child surfaced a second, unrelated failure:

```
ERROR:  could not open file "base/5/16430": No such file or directory
```

on `verify_heapam()` / `pg_amcheck` for a table that was **never written to**
— not a removed file, a table with zero rows since `CREATE TABLE`.

Reproduced with the real `pg_amcheck` binary and confirmed **general**, not
partition-specific: a bare `CREATE TABLE bare_empty (a int);` with zero
inserts hits the identical error.

## Root cause

`storage.Manager.relFile` (`internal/storage/smgr.go`) opens every relation
fork with `os.O_RDWR | os.O_CREATE` — the file is created lazily, the first
time something touches the relation (typically `Pool.Extend` on the first
`INSERT`). Nothing in goopg's `CREATE TABLE` path ever calls into storage at
all: `internal/executor/operators_ddl.go`'s `createTableHonoringPendingDrop`
(the choke point for ordinary `CREATE TABLE` and CTAS) and
`execCreatePartitionChild`'s direct `Catalog.CreateTable` call both only
touch the in-memory/heap-persisted **catalog** — they never call anything on
`ctx.Pool`.

Upstream PostgreSQL does not do this: `heap_create_with_catalog` →
`heap_create` → `RelationCreateStorage` calls `smgrcreate(rd_smgr,
MAIN_FORKNUM, false)` (`postgres/src/backend/catalog/heap.c`), which creates
a **zero-block but present** file on disk unconditionally, whether or not
any row is ever inserted. So on real PG, a never-written table's main fork
file always exists; on goopg it does not.

This collided with the `[[goopg_smgr_ocreate_recreates_removed_files]]`-driven
fix in `verifyHeapamOp.Open` (`internal/executor/operators_verify_heapam.go`),
which correctly uses the stat-only `ctx.Pool.Exists` (not `NBlocks`/`Pin`,
which would silently *recreate* a removed file as empty) to detect a missing
main fork and raise PG's `58030`/`could not open file` error — exactly
mirroring `mdopenfork`'s `ENOENT` path. `Exists` cannot itself distinguish
"removed after having data" from "never had a file because nothing ever
wrote to it" — both are, correctly, "no file at this path" from a pure
`os.Stat`. The fix has to be upstream of that check: make a genuinely-empty
table have a file, matching PG.

## Fix

Add an eager, idempotent file-touch primitive and call it once from CREATE
TABLE:

- `storage.Manager.CreateFile(rel RelFileNode) error` (`internal/storage/
  smgr.go`) — thin wrapper around the existing `relFile(rel)` (its
  `O_CREATE` open already creates the file as a side effect; calling it with
  no read/write is exactly `smgrcreate`'s zero-block file). Idempotent:
  `relFile` caches by `RelFileNode` and a pre-existing file's `O_CREATE` open
  is a no-op.
- `storage.Pool.CreateFile(rel RelFileNode) error` (`internal/storage/
  bufpool.go`) — the standard `Pool` → `Manager` delegation, matching
  `Exists`/`NBlocks`/`RelPath`.
- `executor.(*ddlOp).touchHeapMainForkFile(tbl *catalog.Table) error`
  (`internal/executor/operators_ddl.go`) — resolves `RelFileNode(tbl)` and
  calls `ctx.Pool.CreateFile`. No-op when `ctx.Pool == nil` (the storage-less
  CTAS probe branch in `execCreateTableAs`, which never touches physical
  storage) or `tbl == nil`.

Call sites (the two places a *new* heap table's `catalog.Table` is minted):

1. `createTableHonoringPendingDrop` — covers ordinary `CREATE TABLE` and
   `CREATE TABLE ... AS SELECT` (both call sites route through this one
   helper), including the temp-table and pending-drop-shadow branches.
2. `execCreatePartitionChild` — `CREATE TABLE ... PARTITION OF ...` calls
   `Catalog.CreateTable` directly rather than through
   `createTableHonoringPendingDrop`, so it needs its own call.

Both call sites fail the whole DDL statement if the touch errors (disk full,
permission issue, etc.) — matching PG's behavior when `smgrcreate` itself
fails.

## Verification

- `TestVerifyHeapam_DetectsMissingRelationFile` (the constraint this slice
  was scoped not to break) still passes — a file removed *after* having
  data is still reported missing; `CreateFile` is only ever called once, at
  `CREATE TABLE` time, never as part of the verify path.
- Manual end-to-end against a fresh capped server: `verify_heapam()` now
  returns 0 rows (clean) instead of erroring for a bare empty table, an
  empty `PARTITION OF` child, an empty `CREATE TABLE ... AS SELECT ... WHERE
  false`, and an empty temp table; a subsequent `INSERT` + re-check stays
  clean.
- Real `pg_amcheck --schema=public` against a partitioned table with an
  empty leaf carrying a `USING gist` index: exit 2 (`could not open file`) →
  exit 0, matching real PG.
- `go build ./...` clean; `go test ./internal/executor/ -run TestVerifyHeapam`
  full pass.

## Not in scope / deferred

- Materialized views, `ALTER TABLE` rewrites that mint a new relfilenode, and
  any other storage-creating DDL path besides the two above are not audited
  here — they inherit the same lazy-creation risk if they exist as separate
  choke points. Not ledgered individually (no concrete repro found this
  loop); flag if `pg_amcheck` surfaces one.
- A newly-discovered, unrelated bug found while re-verifying: `CREATE SCHEMA
  s1;` followed by `CREATE TABLE s1.t (...)` does not register a
  `pg_namespace` row for `s1` and the table lands in `relnamespace=2200`
  (`public`) instead — schema qualification is silently dropped across a
  fresh connection. See `.ralph/deferral_ledger.md` (M0119-0006, 2026-09-02,
  "no relations to check in schemas matching s1"). Separate bug, not
  addressed here.
