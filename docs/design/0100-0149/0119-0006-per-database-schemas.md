# M0119-0006 — user schemas are per-database

Status: landed 2026-10-12.
Parent: M0119-0006. This follows the template1 namespace collision fix
(`fba181f95`, Option A) and uses the keying convention of
`0122-0018-per-database-catalog-namespace.md`.

## Problem

`pg_namespace` is per-database in PostgreSQL. goopg kept user schemas in one
process-wide map (`InMemory.schemas`, keyed by name), so a schema created in
one database was effectively shared by all of them. Measured on a scratch
cluster with `CREATE DATABASE dba; \c dba; CREATE SCHEMA scoped_probe`:

- **Visibility leak.** `select nspname from pg_namespace` in `postgres`
  listed `scoped_probe`.
- **Shared name and OID.** `CREATE SCHEMA scoped_probe` in `postgres`
  "succeeded" (`RegisterSchema` was a no-op on an existing name). Both
  databases then shared one OID, so `relnamespace` stamps were ambiguous.
- **Cross-database DROP.** `DROP SCHEMA scoped_probe CASCADE` in `postgres`
  deleted the registry entry. `ALTER SCHEMA scoped_probe RENAME` in `dba`
  then failed with "does not exist", while `dba`'s tables in that schema
  lived on.
- **Single-database RENAME.** `RenameSchema` cascaded only into the default
  database's tables and indexes.

The heap side was already per-database: each database's `pg_namespace` rows
live in `base/<db>/2615`, and the startup reload reads each database's heap.
The reload then registered every row into the shared map, which recreated
the leak after a restart.

## Design

**Where schemas live.** The bootstrap schemas every database has
(`pg_catalog`, `public`, `pg_toast`, `information_schema`) stay in the
shared `InMemory.schemas`. User schemas move to
`tableNamespace.schemas`, next to that database's tables. The per-database
`tableNamespace` also gains `schemaOwners` and `schemaHeapTIDs`. These hold
the owner and heap TID of every schema in that database, bootstrap ones
included, because each database has its own `pg_namespace` row for `public`
too.

**API.** Every schema method takes the trailing `dbOid ...uint32` used by
the table API (`resolveDBOid`). The value is normalised with
`NamespaceDBOid`, so a connection's real `postgres` OID reads the default
namespace.

- **Lookups** (`SchemaExists`, `SchemaOID`, `SchemaOwnerOID`):
  - With a `dbOid`: the bootstrap schemas, then that database's user schemas.
  - Without one: every database, the default first and then the others in
    OID order. This is the old process-wide answer, kept for resolvers that
    have no database in hand (recovery replays, catalog-internal
    `…namespace` columns, the pg\_proc view), so an unmigrated caller cannot
    lose a schema.
- **Writes** (`RegisterSchema`, `UnregisterSchema`, `RenameSchema`,
  `SetSchemaOwner`, heap-TID setters, recovery variants) default to
  `DefaultDBOid`. `RenameSchema` now cascades into the target database's own
  tables and indexes.
- **Reverse lookup.** `SchemaNameForOID` scans the bootstrap schemas and
  then every database. OIDs come from one cluster-wide counter, so an OID
  names one schema wherever it is found.
- **Callers that know their database pass it.**
  - The `Catalog` interface's four schema methods take the same trailing
    `dbOid`.
  - `SearchPathCatalog` (the planner's catalog) injects the connection's
    database.
  - The executor's `ctx.Catalog` is the bare `*InMemory`, so its schema
    calls pass `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` explicitly.
    That covers CREATE/DROP/ALTER SCHEMA, COMMENT ON SCHEMA, the
    `namespaceOIDForSchema` relnamespace/pronamespace stamps through the
    pg\_class/pg\_proc row builders, default privileges, reg\* input and the
    `pg_namespace` heap-TID bookkeeping.
- **pg_namespace rendering.**
  - `PGNamespaceRowsForDBOid(db)` returns the bootstrap schemas, that
    database's user schemas and owners, and the temporary namespaces.
  - The values operator swaps it in for `pg_catalog.pg_namespace` on the
    connection's database, in the same way pg\_extension and pg\_class
    are swapped.
  - The static `VirtualRows` is the default database's.
- **Restart.** `reloadUserSchemasFromHeapForDB` registers each row in its own
  database, `NamespaceDBOid(heapDBOid)`, including owner and TID.
- **CREATE DATABASE.** The new database's catalog image is template0's, so a
  template's user schemas used to reach the new database only through the
  shared map. `CopyUserSchemas(src, dst)` now gives the new database the
  template's user schemas, keeping their OIDs and owners. Each one is
  journalled as a `pg_namespace` heap row of the new database
  (`SyncCompatSchemaToCatalogHeap`) before the template's relations, which
  may live in those schemas, are copied.
- **DROP DATABASE** empties the dropped database's schema maps, so the
  unscoped lookup cannot find them.

## Verification

- `TestSchemasArePerDatabase` (catalog) covers scoping, bootstrap
  visibility, distinct OIDs, the unscoped fallback, owner, drop, rename,
  rows and template copy.
- `TestPort_SchemasArePerDatabase` checks every measured symptom before and
  after a restart. On HEAD it fails three ways: visibility, the shared OID,
  and ALTER after a foreign DROP. It passes now.
- The live probe matches PG 18.3 apart from the one-object CASCADE NOTICE
  wording, which is a pre-existing difference.
- The pg\_amcheck / pg\_dump / schema-restart / template / isolation
  schema-family testport tests pass.
- Regress `namespace`, `create_schema`, `object_address`, `dependency`,
  `temp`, `create_view` and `alter_table` diffs are byte-identical to HEAD.
- Gates: units, initdb, executor, postmaster, tpch-spotcheck, acceptance
  arm and sf025 PASS=99.

## Residuals (ledgered)

- **Bootstrap schemas are still shared.** `DROP SCHEMA public` or
  `ALTER SCHEMA public RENAME` affects every database. In PG each database
  owns its copy.
- **Callers that still use the unscoped any-database lookup:**
  - `grant_ddl` (`GRANT ... ON SCHEMA`, postmaster path).
  - `pg_proc_view` pronamespace, because `Routine` has no database.
  - Catalog-internal recovery resolvers.
  - The `opClassSchemas` / `statisticsObjs` rename cascade, which is
    keyed by name only.
- **Compat path.** The CREATE SCHEMA parse-recovery path always targets the
  default database (`currentDatabaseOidForCompat`).
- **Found by the probe, not scoping:**
  - CREATE TABLE in a nonexistent schema succeeds and stamps `public`'s OID.
    PG raises 3F000.
  - A one-object `DROP ... CASCADE` NOTICE is worded "drop cascades to 1
    other objects" with a DETAIL, where PG prints "drop cascades to table
    sa.t".

## Follow-up (2026-10-12): the creation namespace must exist

Probing the per-database change showed that every statement creating a
schema-qualified object accepted a schema that does not exist, and filed the
object under `public`'s OID. Affected: CREATE TABLE (also TEMP and IF NOT
EXISTS), CREATE TABLE AS, SELECT INTO, VIEW, SEQUENCE, MATERIALIZED VIEW,
TYPE, DOMAIN, FUNCTION and PROCEDURE, plus ALTER TABLE ... SET SCHEMA. In PG
they all go through `RangeVarGetCreationNamespace` /
`QualifiedNameGetCreationNamespace` → `get_namespace_oid`, which raises
3F000 `schema "x" does not exist`.

- **Creation check.** `ddlOp.checkCreationNamespace` runs before the
  per-kind dispatch, so it comes before every other check. A TEMP table in a
  missing schema therefore reports the schema, not 0A000, as PG does.
  - It looks the schema up in the connection's database.
  - `pg_temp` / `pg_temp_N` are skipped.
  - Only a plain CREATE TABLE carries a position (the relation name, which is
    PG's caret). CTAS, SELECT INTO and the other kinds carry none.
- **ALTER TABLE ... SET SCHEMA** checks the target only after resolving the
  relation (`AlterTableNamespace` order), so a missing table still reports
  42P01.
- **Quoted schema names were never registered.** Turning on the check exposed
  this: the compat scanner for CREATE SCHEMA (`parser/ddl.go`) dropped a
  quoted name (`"my.schema"`, `"Mixed"`), and objects created in such a
  schema had only "worked" because nothing was checked. The scanner now:
  - accepts a quoted identifier as the name;
  - parses `IF NOT EXISTS`, which it used to read as a schema named `if`.
- **Duplicates.** The executor raises NamespaceCreate's 42P06 for an
  existing schema, or under IF NOT EXISTS the NOTICE `schema "x" already
  exists, skipping`. `RegisterSchema` had made a duplicate a silent no-op.

Verification:

- `TestCreateInMissingSchemaRaises3F000` and
  `TestCreateSchemaQuotedIfNotExistsDuplicate`.
- The probe is byte-identical to PG 18.3 apart from the residuals below.
- Regress A/B:
  - namespace now matches PG on the duplicate error and the IF NOT EXISTS
    notice, but its diff grows by 5 lines. In PG the first
    `CREATE SCHEMA ... CREATE VIEW` fails on its sub-element. goopg does not
    run sub-elements (M0134-0009), so it creates the schema, and the second
    CREATE SCHEMA now correctly hits 42P06.
  - alter_generic gains a matching line.
  - inherit / privileges / rowsecurity show only OID and pointer noise.
  - The other 16 cases are byte-identical.

Residuals (ledgered):

- `CREATE TABLE pg_catalog.x` is accepted. PG refuses it with 42501 unless
  allow_system_table_mods is on.
- The schema registry folds names to lower case, so `"Mixed"` shows in
  pg_namespace as `mixed` and `"Mixed"` / `mixed` collide.
- goopg NOTICEs carry SQLSTATE 00000. PG sends 42P06 / 42P07 on the
  "already exists, skipping" notices.
- An unqualified CREATE whose search_path holds no existing schema is not
  checked. PG raises 3F000 `no schema has been selected to create in`.
- The dispatch parse-recovery path (`schemaNameFromCreate`) lowercases a
  quoted name.
