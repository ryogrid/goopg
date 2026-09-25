# 0119-0006bs — Per-database extension registry (pg_extension write path)

Status: accepted (landed 2026-09-20, Loop #29)

Landed: `c.extensions` re-keyed to `database + "\x00" + lcname` with a
scope-overlap conflict rule; `DropExtension(name, database)` returns the
removed row's `(oid, scope)`; `ExtensionOID(name, database)` is
scope-resolved ("" = any row, matching the no-filter read convention);
`CreateExtension` returns `created bool` driving the upstream
`already exists, skipping` NOTICE + journal skip; `DropDatabase` purges
and `RenameDatabase` re-keys scoped rows; `CreateExtensionDuringRecovery`
dedups on exact key and bumps `nextOID`; `deleteExtensionCatalogRow`
stamps the scope's own heap and re-mirrors;
`reloadUserExtensionsFromHeap` hoisted out of the pg_collation error
branch (dead code at HEAD — the reload never ran on the success path)
and scans each registered database's `base/<oid>/3079`.

Verified live (private scratch cluster :5533): `CREATE EXTENSION
amcheck` succeeds inside a `CREATE DATABASE`'d database; `pg_amcheck -d
amcheckdb` (whole-database, no schema filter) exits 0 clean, including
`--heapallindexed`; per-database `pg_extension` scoping survives a
restart; `DROP EXTENSION` removes only the current database's row and
second-drop errors 42704; `IF NOT EXISTS` emits the NOTICE. This
resolves the 2026-09-02 ledger row (whole-database pg_amcheck on a
created database).
Milestone row: M0119-0006 (pg_amcheck server tier — deferral-ledger drain)
Parent finding: `.ralph/deferral_ledger.md` row ~2041 (2026-09-02):
whole-database `pg_amcheck -d <created-db>` fails.

## Finding

The ledger row's recorded repro is

```
CREATE DATABASE amcheckdb
CREATE EXTENSION amcheck          -- in amcheckdb
pg_amcheck -d amcheckdb           -- no --schema filter
```

and its recorded failure was `verify_heapam(1259)` → "could not open
relation" because system catalogs were registered only under
`DefaultDBOid`. That link in the chain is **already fixed at HEAD**:
`InMemory.tableByOID` gained a pg_catalog/information_schema fallback to
the `DefaultDBOid` namespace for OID lookups under a non-default dbOid
(M0119-0006bp), and `LookupTable` carries the matching name-based
fallback (M0122-0007 slice 4e).

At HEAD the repro does not reach that step at all — it stops earlier:

```
postgres=# CREATE DATABASE freshdb
freshdb=# CREATE EXTENSION amcheck
ERROR:  extension "amcheck" already exists        -- installed only in postgres!
pg_amcheck: warning: skipping database "freshdb": amcheck is not installed
```

`pg_amcheck`'s install probe is per-database
(`postgres/src/bin/pg_amcheck/pg_amcheck.c:174`):

```sql
SELECT n.nspname, x.extversion FROM pg_catalog.pg_extension x
JOIN pg_catalog.pg_namespace n ON x.extnamespace = n.oid
WHERE x.extname = 'amcheck'
```

The read side is already correct: `extensionRowsLocked` filters
`extensionRow.database` against the connecting database
(M0110-0003, AC-002 gap #7c), so the probe correctly reports "not
installed" in `freshdb`. The **write side is global**:

- `InMemory.CreateExtension` (`internal/catalog/catalog.go:13831`) keys
  `c.extensions` by lowercase name only — `if _, ok := c.extensions[lc]`
  — so an extension installed in one database blocks the same name in
  every other database with `42710 extension "..." already exists`.
- `DropExtension(name)` deletes the global key — a DROP in db A would
  remove db B's install.
- `ExtensionOID(name)` returns the global row — used by DROP EXTENSION's
  existence check, CREATE EXTENSION's journal step, and COMMENT ON
  EXTENSION; all three would resolve the wrong scope's row once
  same-name installs coexist.
- `CreateExtensionDuringRecovery` dedups globally — two per-db installs
  of the same name would collapse to one on restart replay.

Upstream, `pg_extension` is a per-database catalog (each database has its
own `base/<dboid>/3079` heap); the uniqueness constraint is
`pg_extension_name_index` (3081) *within one database*
(`postgres/src/bin/pg_amcheck/pg_amcheck.c:174` comment,
`postgres/src/backend/commands/extension.c` `get_extension_oid` — the
existence check scans the current database's `pg_extension` only). Same
`extname` in two databases is legal and independent.

## Durability gap (must land in the same slice)

`writeExtensionCatalogRow` (`internal/executor/sys_pg_extension.go`)
journals the row into `base/<NamespaceDBOid(currentDB)>/3079` — i.e. the
installing database's own heap — then `mirrorExtensionCatalogFiles`
copies **base/1 → base/5** (default → postgres alias). A row installed
in `amcheckdb` lands in `base/<amcheckdbOid>/3079`, which nothing ever
reads: `reloadUserExtensionsFromHeap`
(`internal/initdb/catalog_heap_reload.go:3351`) scans only
`base/<cat.DBOID()>/3079` and registers each row with `database=""`
(unscoped → visible everywhere).

**Worse (review finding): the reload is dead code at HEAD.** Its only
call site (`internal/initdb/open.go:~2390`) is nested *inside* the
`reloadUserCollationsFromHeap` **error branch**, after `pool`/
`walWriter`/`mgr` have already been `Close()`d — so on the success path
no extension row is reloaded in ANY database. `CREATE EXTENSION` has
never survived a restart (corroborated by a `pgamcheck004_port_test.go`
comment). This slice hoists the call onto the success path and makes it
per-database in one pass.

## Design

### Registry: key by (database, name)

`c.extensions` stays `map[string]*extensionRow` but the key becomes the
composite `database + "\x00" + lcname`. A helper resolves the row
visible in a given scope:

```go
// extensionVisibleInDB reports whether registry row e claims extname
// in database `db`. An unscoped row (e.database == "", legacy
// direct-call insert) is visible in every database, and an unscoped
// *new* install (database == "") is visible in — and therefore
// conflicts with — every database's scope.
func scopesOverlap(a, b string) bool { return a == "" || b == "" || a == b }
```

- `CreateExtension(name, schema, version, database, ifNotExists)`:
  conflict iff any existing row shares the lc-name AND
  `scopesOverlap(e.database, database)`. Same-name/different-db installs
  coexist, matching upstream's per-db `pg_extension_name_index`.
  Signature gains a `created bool` return (interface + impl): on a
  conflict with `ifNotExists` it returns `(false, nil)` so the executor
  can emit PG's `NOTICE: extension "x" already exists, skipping` AND —
  critically — skip `writeExtensionCatalogRow`, which today journals a
  duplicate heap row on every `IF NOT EXISTS` no-op (upstream
  `extension.c` `CreateExtension` returns early before any catalog
  insert; `get_extension_oid` + `ereport(NOTICE, …, "skipping")`).
- `DropExtension(name, database)`: delete the exact-scope row; if none,
  delete the unscoped row of that name (it claimed the name in this db).
  Returns the removed row's `(oid, scopeDatabase)` so the DROP caller
  can stamp xmax on the heap that actually holds the row — today
  `deleteExtensionCatalogRow` always stamps
  `base/<currentDB>/3079`, which is right only because the global
  registry made every install's scope the installer's; under per-db
  rows the stamp must target `NamespaceDBOid(ResolveDatabaseOid(scope))`
  (unscoped legacy rows → `DefaultDBOid`).
- `ExtensionOID(name, database)`: exact-scope match first, else the
  unscoped row, else — when `database == ""` — the first same-named row
  (matching `extensionRowsLocked("")`'s "no filter" convention, so
  embedded/test callers keep working), else 0.
- `CreateExtensionDuringRecovery(name, schema, version, database, oid)`:
  dedup on the exact composite key only — heap replay inserts verbatim —
  and bumps `nextOID` past `oid` (matching `RegisterCastDuringRecovery`/
  `RegisterDatabaseDuringRecovery`; without it a later runtime-minted
  extension oid can collide with a recovered one, and same-oid rows in
  different dbs make `deleteExtensionCatalogRow`'s oid-match stamp and
  `SetComment(3079, oid)` ambiguous).
- `DropDatabase(name)` purges `c.extensions` rows scoped to `name`;
  `RenameDatabase(old, new)` re-keys them — otherwise a recreated
  same-name db inherits phantom installs (pg_extension shows them, CREATE
  conflicts 42710, and DROP cannot clean the fresh heap), and a renamed
  db loses its extensions until restart re-attributes them.
- `extensionRowsLocked`/`ExtensionRowsForDB`: unchanged (they iterate
  map values; the composite key is invisible to them).

Executor call-site updates (all pass `o.ctx.CurrentDatabase`):
`execCreateExtension` (`operators_ddl.go:276` — plus the `created` flag
for notice/journal gating), DROP EXTENSION arm
(`operators_ddl.go:21734`/`:21742`), COMMENT ON EXTENSION arm
(`operators_ddl.go:24093`).

### Reload: scan every database's pg_extension heap

`reloadUserExtensionsFromHeap` follows the established per-db pattern
(`loadColumnDefaultsFromHeap`, same file ~line 250): scan
`base/<cat.DBOID()>/3079` plus `base/<DatabaseOid(name)>/3079` for each
name in `cat.ListDatabases()` — `reloadDatabasesFromHeap` (open.go:1546)
runs before this pass (open.go:2394), so user databases are registered.
Skip oids 0, `DefaultDBOid`, `PostgresDBOid`, and `cat.DBOID()` (the
bootstrap-scope dirs already scanned — base/1 and base/5 are mirrored
identical, and scanning both would double-register via the composite
key only if scopes differed; with identical `database` attribution the
exact-key dedup makes a rescan a no-op anyway).

Attribution: rows read from `base/<cat.DBOID()>` are scoped to the
canonical bootstrap database name — `"postgres"` (DBOID is postgres's
real on-disk oid; template1/template0 share the DefaultDBOid namespace
in goopg, see `ResolveDatabaseOid`/`NamespaceDBOid`). Rows read from
`base/<oid>` for a user db are scoped to that db's registered name
(reverse of `DatabaseOid`; a `databaseOid→name` walk over
`ListDatabases()` suffices — no new map needed).

The bootstrap-scope ambiguity is documented, not solved: an extension
installed while connected to `template1` (which aliases to the base/1
heap) reloads attributed `"postgres"`. Both connections shared one
namespace anyway; the pre-fix behaviour (unscoped/everywhere) was
strictly wronger.

`mirrorExtensionCatalogFiles` stays: without it, postgres-scope rows
written to base/1 would not reach base/5 where the reload reads. It is a
byte copy of base/1 → base/5 and never propagates user-db rows, so it is
harmless under per-db reload. (base/5/3079's only other consumer is a
real PG standby's relcache init — `relcache_init.go` — which FATALs if
the rel is absent, another reason the mirror must stay.)

### What this does NOT fix (recorded, not claimed)

- `pg_amcheck` whole-database mode against a created db may still hit
  further blockers downstream of the install probe (the original row's
  pg_class-walk concern). The live re-probe after this slice determines
  whether the ledger row resolves or needs a follow-up arm.
- template1/postgres share the DefaultDBOid namespace entirely — a
  template1 install appears in postgres after restart (above).
- template0 is connectable (datallowconn is not enforced server-side);
  a `CREATE EXTENSION` there writes base/4/3079 and
  `copyBootstrapCatalogImage` would clone it into future
  `CREATE DATABASE`s — a pre-existing template0-write poisoning issue
  this slice makes concrete for pg_extension but does not solve.
- Reload-time schema attribution: a user-db-local `extnamespace` won't
  resolve via the global `SchemaNameForOID` (the pg_namespace reload
  scans only `base/<DBOID()>`) and falls back to `"public"` — display
  only.
- `pg_amcheck --all`-style multi-db sweeps across template0/template1 are
  not exercised.

## Test plan

- `internal/catalog/extension_perdb_test.go` (exists since M0110-0003 —
  extend, not create): `CreateExtension` same name in two different DBs
  succeeds; each db's `ExtensionRowsForDB` shows its own row; duplicate
  *within* the same db still errors; `DropExtension(name, otherDB)` does
  not remove db A's row; `ExtensionOID(name, db)` is scope-correct;
  `DropDatabase` purges the scope's rows.
- Existing tests keep passing unchanged except signature updates
  (`DropExtension`, `ExtensionOID` gain `database`; `CreateExtension`
  gains the `created` return).
- Live scratch cluster (private port 5533, capped): `CREATE DATABASE`,
  `CREATE EXTENSION amcheck` inside it, `pg_amcheck -d <db>`
  whole-database; restart survival of the per-db install.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.

## Risks

- `database == ""` callers (embedded/test contexts) keep today's
  global-conflict semantics via `scopesOverlap`; `ExtensionOID("")`
  falls back to any same-named row, matching the read path's "no
  filter" convention.
- The composite map key is internal to `catalog.go`; exported signature
  changes: `CreateExtension` (+`created`), `DropExtension` (+`database`,
  returns), `ExtensionOID` (+`database`).
- `scanCatalogHeapRows` on a missing `base/<oid>/3079` creates the empty
  file via `O_CREATE` (smgr `relFile`) — harmless and matches upstream's
  always-present per-db catalogs.
