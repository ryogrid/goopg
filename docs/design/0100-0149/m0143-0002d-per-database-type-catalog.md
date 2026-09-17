Status: in progress — M0143-0002e (read side) and M0143-0002f (write side) landed 2026-09-17; M0143-0002g (DROP DOMAIN + GRANT/REVOKE ACL sites) landed 2026-09-17; M0143-0002h (pg_range paired write+read fix) filed, not started
Date: 2026-09-17
Supersedes: none

# M0143-0002d — per-database `pg_type`/`pg_attribute` storage for user-defined types

## Problem

`pg_type`/`pg_attribute` rows for user-defined types (`CREATE TYPE ... AS
ENUM|(composite)|RANGE`, `CREATE DOMAIN`) always land in
`catalog.DefaultDBOid`'s catalog heap, regardless of which database's
connection issued the `CREATE`. Two databases can each declare a same-named
composite type without a collision error (correct — they are different
types), but a `SELECT * FROM pg_type`/`pg_dump` under either database sees the
**union** of both types' `pg_attribute` rows, because both types' physical
rows were written to the one shared file.

Live repro and the six-site write-side inventory are in the parent task
(`.ralph/fix_plan.md` M0143-0002d, filed by M0143-0002c). This doc adds the
missing piece M0143-0002d's own text flagged as unresolved: **the concrete
per-database fix shape**, scoped against the precedent that already made
ordinary user tables' `pg_class`/`pg_attribute` rows per-database
(`docs/design/0100-0149/0122-0018-per-database-catalog-namespace.md`, its
"Still deferred" section names this exact gap).

## Root cause, precisely

Two independent bugs, one on each side:

**Write side** (`internal/executor/operators_ddl.go`): `writeTypeHeapRowWithIndexes`
(`:18447-18458`), `updateTypeHeapRowWithIndexes` (`:18483-18504`), and
`syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel` pair (`:18539-18543`/
`:18555-18559`) all hardcode `storage.RelFileNode{DBOid: catalog.DefaultDBOid,
...}`. The six `deleteCatalogRowsForOID`-adjacent sites M0143-0002c
re-identified (`execAlterType` `:25242`/`:25282`/`:25325`/`:25372`,
`execAlterTypeAttrCmds` `:25617`, `execDropType`'s composite/enum/range
branches `~:25644-25689`) share the identical shape. The natural fix already
has a name in this file: `tableCatalogHeapDBOid(ctx)` (`:18697-18699`) already
returns `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` and is what the
**table** path uses for exactly this purpose — every type-catalog write site
above needs the same call in place of the `catalog.DefaultDBOid` literal.

**Read side** (`internal/initdb/open.go`): `pg_type` (OID 1247) and
`pg_attribute` (OID 1249) are registered as `catalog.Table` objects **once,
globally**, by `loadSystemCatalogsIfPresent` (`:2931-2972`), called from a
single call site (`:1376`) with no `DBOid` field set (Go zero value). Every
`SeqScan`/write against these two relations resolves via
`Catalog.InMemory.RelFileNode` (`internal/catalog/catalog.go:22354-22366`):
`dbOid := c.dbOid; if table.DBOid != 0 && table.DBOid != DefaultDBOid { dbOid =
table.DBOid }` — since these two `Table` structs' `DBOid` is always 0, the
condition never fires and **every connection**, regardless of
`ctx.CurrentDatabaseOid`, resolves to the one process-wide `c.dbOid` field.

**Contrast — why ordinary tables don't have this bug (investigated this
loop, no earlier doc stated it this explicitly):** `pg_class` (OID 1259) is
*also* a single global `catalog.Table`, registered once by
`registerSystemTables()` (`catalog.go:8647`, called once from the `InMemory`
constructor at `:4204`) — so naively it should have the identical bug. It
doesn't, because `SELECT * FROM pg_class` in goopg is **virtual** (per
`goopg_pg_class_virtual_pg_attribute_heap` memory: computed by iterating the
in-memory catalog's tables, already filtered by `ctx.CurrentDatabaseOid`
correctly) rather than a real heap `SeqScan` through `RelFileNode`. `pg_type`
and `pg_attribute`, by contrast, **are** real heap-backed relations
(`RegisterRealTable`, non-virtual) — a `SELECT` on them is a genuine `SeqScan`
that goes through the buggy `RelFileNode` resolution. Per-database routing for
ordinary tables' `pg_class`/`pg_attribute` **rows** works via a completely
different mechanism that never touches the singleton `Table.DBOid`:
recovery reads them with `storage.RelFileNode` constructed directly from an
explicit `heapDBOid` parameter (`loadUserTablesFromHeapForDB`, `open.go:3098+`),
and writes route via `tableCatalogHeapDBOid(ctx)` (the same helper the fix
above reuses). Types have no equivalent of this direct-construction reload
path, and worse, `pg_type`/`pg_attribute` are user-queryable relations in
their own right (unlike the pg_class rows recovery reads internally), so the
singleton-`Table` `RelFileNode` bug is directly observable from SQL.

**Provisioning is NOT the gap — confirmed this loop.** M0143-0002d's own
filing worried the fix needed new per-database physical files ("this is
materially bigger... genuinely no small tweak"). That fear is refuted:
`internal/initdb/initdb.go`'s `CreatePerDatabaseScaffolding` (`:387-410`)
calls `copyBootstrapCatalogImage` (`:426-473`) for every `CREATE DATABASE`
other than the three built-ins, which copies **every** regular file byte-for-
byte from `base/4` (`template0`) into `base/<newOid>/` — this already
includes `1247` (pg_type) and `1249` (pg_attribute), since they're ordinary
catalog OIDs to that copy loop. **Every database already has its own
physical pg_type/pg_attribute heap file the moment it's created.** The gap
is purely in-memory registration/routing, not disk layout.

## Fix shape

Mirror the precedent this codebase already used to fix the identical shape
for indexes (`M0127-P5.6-f-pre`,
`internal/catalog/catalog.go:6584-6698` `RegisterIndexDuringRecoveryForDB`,
driven by the per-database loop at `internal/initdb/open.go:3576-3585`):

1. **New catalog method** `RegisterSystemCatalogForDB(dbOid uint32, oid
   uint32, schema, name string, cols []catalog.Column) error` (or fold into
   an existing per-DB registration helper if one better fits) on
   `catalog.InMemory`: registers a `Table` object into `c.ns(dbOid)` with
   `Table.DBOid = dbOid` set, for a **system** relation (pg_type/pg_attribute
   shape), as opposed to `RegisterIndexDuringRecoveryForDB`'s user-object
   registration. `RelFileNode`'s existing `table.DBOid != 0` branch then does
   the rest — no change needed to `RelFileNode` itself.
2. **Startup reload**: after `loadSystemCatalogsIfPresent`'s existing
   `DefaultDBOid` call (`open.go:1376`), add a loop over `cat.ListDatabases()`
   (skipping `DefaultDBOid`/`PostgresDBOid`/`cat.DBOID()` exactly like
   `open.go:3576-3585`'s index loop) calling the new registration function for
   each already-existing distinct-dbOid database, using
   `heapFilePresent(base/<dbOid>/1247|1249)` the same way the existing
   function does for the default database.
3. **CREATE DATABASE time**: call the same registration function for the
   new database's own oid, next to `copyTemplateTables`'s existing
   `im.TryRegisterUserTable(newTbl, newOid)` call (`database_ddl.go:995`) —
   the physical files are already there (provisioning point above), so this
   step is registration-only, no new heap write.
4. **Write side**: once (1)-(3) land and are verified (a live two-database
   `CREATE TYPE`/restart round trip, see Test below), change the write-side
   hardcodes (`writeTypeHeapRowWithIndexes`, `updateTypeHeapRowWithIndexes`,
   `syncCompositeTypeToCatalogHeap`'s two `RelFileNode`s, and the ~9 delete-
   side sites M0143-0002c inventoried) from `catalog.DefaultDBOid` to
   `tableCatalogHeapDBOid(ctx)` (`operators_ddl.go:18697-18699`, already
   `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`) — **all sites in the same
   commit**, per M0143-0002d's own sequencing note: a partial change would
   leave some paths targeting the connection's real dbOid while others still
   write to Default, worse than today's uniform (wrong) behavior.

Step 4 must not land before steps 1-3: fixing the write side alone would make
a distinct-dbOid connection write its type row into its own physical file
correctly while every *read* of `pg_type`/`pg_attribute` (steps 1-3 unfixed)
still resolves to the single process-wide `c.dbOid` — the row becomes
invisible to its own connection immediately after `CREATE TYPE`, a worse
regression than today's silent cross-database union.

**Index entries need no change** — `insertCanonicalSysBtreeLeaf`
(`sys_catalog_index_insert.go:423-428`) already routes via
`tableCatalogHeapDBOid(ctx)`, so `pg_type_oid_index` /
`pg_type_typname_nsp_index` / `pg_class_relname_nsp_index` /
`pg_attribute_relid_attnum_index` inserts are already correct; only the heap
rows they point at were wrong.

## Decomposition (two loop-sized tasks, filed in `.ralph/fix_plan.md`)

- **M0143-0002e** (read side: fix-shape steps 1-3 above). No behavior change
  observable from write-side SQL yet (the write side still hardcodes
  DefaultDBOid until 0002f lands) — this task's own gate is a unit test that
  registers a second database and confirms `pg_type`/`pg_attribute`
  `RelFileNode` resolution now differs per `ctx.CurrentDatabaseOid`, plus the
  existing regress/unit suite staying green (no behavior moves for the
  DefaultDBOid path, which is every existing test).
  - **Done 2026-09-17 — implementation note, one deviation from step 1's
    literal wording:** step 1 above says "new catalog method"; the landed
    fix instead extended the EXISTING `RegisterRealTable(t *Table, dbOid
    ...uint32)`, which already carried an unused variadic `dbOid` parameter
    (dead code nobody had wired up). `RegisterRealTable` now sets `t.DBOid =
    resolved` whenever the resolved dbOid names a genuine non-default
    database — a separate method would have duplicated the idempotency/
    namespace logic already in `RegisterRealTable` for no benefit, and every
    existing call site (both callers pass no dbOid arg) is unaffected since
    `resolveDBOid(nil) == DefaultDBOid` short-circuits the new branch. Steps
    2-3 landed as literally specified: `loadSystemCatalogsIfPresentForDB`
    (the per-DB body factored out of `loadSystemCatalogsIfPresent`) and the
    new exported `initdb.RegisterSystemCatalogsForDB` for CREATE DATABASE
    time. See `.ralph/fix_plan.md`'s M0143-0002e Done note for the full
    file:line list and the two new tests
    (`internal/catalog/register_real_table_dbid_test.go`,
    `internal/initdb/system_catalog_dbid_test.go`).
- **M0143-0002f** (write side: fix-shape step 4). Depends on M0143-0002e
  `[x]`. Gate: the live two-database repro from M0143-0002c's Done note
  (`.ralph/fix_plan.md` M0143-0002c) now returns each database's own type
  shape only, plus a restart round trip (see Test below) confirming the fix
  survives `Close`/`Open` — not just live-session visibility.
  - **Done 2026-09-17.** All named write sites
    (`writeTypeHeapRowWithIndexes`, `updateTypeHeapRowWithIndexes`,
    `syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel`, and every
    composite/enum/range delete-side `catalog.DefaultDBOid` literal
    M0143-0002c inventoried) now route through `tableCatalogHeapDBOid(ctx)`.
    Live (pre-restart) verification matched the design exactly: a composite
    type declared identically-named in two databases returned each
    database's own field list, not the union. **The restart half surfaced a
    second, independent bug** in M0143-0002e's own read-side landing: its
    per-database registration loop lived inside `loadSystemCatalogsIfPresent`
    (`internal/initdb/open.go`), called at the function's line 1376 — well
    *before* `reloadDatabasesFromHeap` (line 1546) populates
    `cat.ListDatabases()`, the very list the loop iterates. Every restart
    therefore silently registered zero non-default databases' pg_type/
    pg_attribute Tables, so a distinct-dbOid database's own types vanished
    (0 rows, not the union) after `Close`/`Open` even with this task's
    write-side fix landed. Fixed by moving the per-DB loop out of
    `loadSystemCatalogsIfPresent` (now just the DefaultDBOid pass again) to
    run immediately after the pre-existing `loadUserTablesFromHeapForDB`
    per-DB loop (both iterate the same post-reload `cat.ListDatabases()`).
    New test `TestDatabaseDDLTypeCatalogReloadAcrossRestart`
    (`internal/postmaster/database_ddl_type_reload_test.go`) mirrors
    `TestDatabaseDDLReloadAcrossRestart`'s shape and caught this live (empty
    result, not union, was the tell) before the ordering fix; it covers
    composite (pg_attribute join, the M0143-0002c repro exactly), domain
    (typbasetype identity via `format_type`), and range (pg_type isolation
    only — see "What this does NOT do" below for why the range assertion
    stops short of `pg_range`). Two write sites intentionally NOT touched
    because they were never in this task's enumerated scope —
    `pgRangeRel` (`internal/executor/sys_pg_range.go`, still hardcodes
    DefaultDBOid) and `execDropDomain`/the ACL-resync functions
    (`resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`, `operators_ddl.go`) —
    are recorded as a `.ralph/deferral_ledger.md` row dated 2026-09-17
    rather than folded in here. Gates: `go build ./...` clean; `go test
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...
    ./internal/catalog/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green;
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0,
    plan-shapes 99/99 identical (gate-stamp PASS against the staged tree).
- **M0143-0002g** (the two write sites M0143-0002f left out of scope).
  Parent: M0143-0002d.
  - **Done 2026-09-17 (item 2 of 2 — DROP DOMAIN + GRANT/REVOKE ACL resync
    only).** Routed `execDropDomain`'s two `deleteTypeFromCatalogHeap` calls
    and `resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`'s delete+attrRel
    construction through `tableCatalogHeapDBOid(ctx)`
    (`operators_ddl.go:23537,23667-23670,26366,26369`). These sites are safe
    to fix write-side-only because their read side — the per-database
    pg_type/pg_attribute reload loop M0143-0002e/f already landed
    (`loadSystemCatalogsIfPresentForDB`, `internal/initdb/open.go:1586`) —
    is generic to whichever executor code wrote the row; it doesn't care
    which function did the writing. New tests
    (`internal/postmaster/database_ddl_type_acl_domain_reload_test.go`):
    `TestDatabaseDDLTypeGrantOnTypeNonDefaultDBReload` (caught a duplicate
    pg_type row pre-fix — the ACL resync's stale-row xmax stamp missed the
    right heap while the already-fixed insert correctly landed the new row
    there, so both stayed live) and
    `TestDatabaseDDLTypeDropDomainNonDefaultDBReload` (caught a surviving
    "ghost" row pre-fix — the xmax stamp had nothing to compensate for).
    **`pgRangeRel`'s own `DefaultDBOid` hardcode (item 1) was explicitly NOT
    fixed** — before assuming it was as safe as item 2, checked whether an
    equivalent per-database read side exists for `pg_range` and found there
    isn't one: `reloadUserRangeTypesFromHeap`
    (`internal/initdb/catalog_heap_reload.go:1685`) runs as a single
    unconditional pass keyed on `cat.DBOID()` (not looped over
    `cat.ListDatabases()` the way the pg_type/pg_attribute loop is), and
    `RegisterRangeTypeDuringRecovery` hardcodes `DBOid: cat.DBOID()` on
    every reloaded `RangeType`. A write-only `pgRangeRel` fix would
    therefore make a non-default database's range type silently lose its
    pg_range row (and `rngsubtype`) on the very next restart — an actual
    data-loss regression traded for today's merely-cosmetic wrong-file
    placement. Re-filed as **M0143-0002h** (paired write+read fix, one
    commit) with a `.ralph/deferral_ledger.md` row dated 2026-09-17
    (task-id M0143-0002g). Gates: `go build ./...` clean; `go test
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...
    ./internal/catalog/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green;
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0
    TIMEOUT=0, plan-shapes 99/99 identical.
- **M0143-0002h** (pg_range paired write+read per-database fix). Parent:
  M0143-0002d. Filed 2026-09-17, not started. See the `.ralph/fix_plan.md`
  task text for the full three-part fix shape (write-side `pgRangeRel` swap
  + a new per-database `reloadUserRangeTypesFromHeap` loop mirroring
  `loadSystemCatalogsIfPresentForDB` + the `rngsubtype`-join test
  extension). Both halves land in the same commit — the read-side loop must
  exist before or alongside the write-side swap, never after, or a restart
  in between would lose data (the exact trap M0143-0002g's own audit
  surfaced and declined to walk into).

## Test

Mirror `internal/postmaster/database_ddl_reload_test.go`'s
`TestDatabaseDDLReloadAcrossRestart` shape (in-process, no subprocess/socket,
`postmaster.Server` used only as a `tryHandleDatabaseDDL`/`wireExtensionRows`
method-holder, real `initdb.Open`→`Close`→`Open` restart): two databases each
declare a same-named enum, domain, composite, and range type; after restart,
each database's `SELECT typname, ... FROM pg_type`/`pg_attribute` (and
`pg_dump`, live-checked separately) shows only its own type's shape, matching
the repro M0143-0002c already ran manually.

## What this does NOT do

Does not touch `pg_constraint` (already per-database, different mechanism —
`tableCatalogHeapDBOid(ctx)`-style routing was already correct there per the
0122-0018 doc's "Still deferred" note, which is what's now stale — constraint
routing was fixed by M0143-0002/-0002b, both about `dropIndexByName`/
`DropForeignKeyConstraint`'s in-memory index maps, not pg_type/pg_attribute).
Does not touch sequences (`internal/catalog/catalog.go` sequence maps are
already `catalog.NamespaceDBOid`-keyed per the 0122-0018 doc §4e, unrelated
mechanism). Does not add a `RelFileNode`-level per-connection dbOid
parameter — the chosen fix keeps `RelFileNode`'s existing `table.DBOid`
dispatch and instead makes sure a `Table` object with the right `DBOid`
exists in every database's namespace, matching the index precedent exactly
rather than introducing a second per-database-catalog mechanism.
