Status: in progress — M0143-0002e (read side) landed 2026-09-17; M0143-0002f (write side) not started
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
