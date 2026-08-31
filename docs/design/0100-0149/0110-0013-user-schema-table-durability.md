# 0110-0013 — User-schema table durability across restart (relnamespace OID round-trip)

Status: accepted

## Problem

Loop #20 ([0110-0012](0110-0012-create-schema-wal-durability.md)) made
`CREATE/DROP SCHEMA` itself durable across a server restart (the schema name +
OID are restored into the in-memory registry via a WAL-record replay driver).
But a *table created inside a user schema* still did not reload under its schema
after a restart.

Two sibling code paths disagreed about how a user table's schema is encoded in
its `pg_class.relnamespace`:

- **Write side** — `syncTableToCatalogHeap` (and the `pg_class` virtual-catalog
  row builders) stamp `relnamespace` via `namespaceOIDForSchema(schema)`. That
  helper mapped *any* schema other than `public` to the **`pg_catalog` OID (11)**:

  ```go
  func namespaceOIDForSchema(schema string) uint32 {
      if schema == "" || schema == "public" { return PublicNamespaceOID }
      return PGCatalogNamespaceOID            // ← s1, s2, … all collapse to 11
  }
  ```

- **Read side** — `loadUserTablesFromHeap` (restart recovery, the sole catalog
  recovery path) reconstructs `Table.Schema` from the recovered `relnamespace`:

  ```go
  if tr.RelNamespace == PGCatalogNamespaceOID { schema = "pg_catalog" }
  else if recovered.physical && tr.RelNamespace == PublicNamespaceOID { schema = "public" }
  ```

So a table created as `s1.t` was written with `relnamespace = 11` and reloaded
as `pg_catalog.t`. After a restart `pg_amcheck --schema s1` (and any
schema-qualified resolution) found *no relations in `s1`* — the recurring
003_check symptom logged across loops #15/#19, which forced every AC-003
surrogate to place its fixtures in `public`.

The fast-start catalog cache (`pg_goopg_catalog_cache.json`) stores the schema
as a *string* and so was correct — but it is unlinked on every DDL commit and is
only an optimization; the authoritative recovery path is the `pg_class` heap
scan, which was lossy for user schemas. A crash (or any restart after DDL) lost
the schema association.

## Decision

Make the two sibling paths agree on the **real schema OID** as the durable
encoding, reusing the schema name↔OID registry that [0110-0012](0110-0012-create-schema-wal-durability.md)
already restores on restart.

1. **Write side** — `namespaceOIDForSchema(cat, schema)` now resolves a
   registered user schema to the OID the catalog assigned it
   (`cat.SchemaOID(schema)`). System schemas keep their fixed OIDs
   (`public`→2200, `pg_catalog`→11); an unregistered schema or a nil catalog
   falls back to `pg_catalog` (preserving pre-fix behaviour). The OID written is
   the *same* value `CREATE SCHEMA` assigned and that `RegisterSchemaDuringRecovery`
   restores from the WAL on restart, so the encoding round-trips.

2. **Read side** — `loadUserTablesFromHeap` and `loadUserIndexesFromHeap` add a
   final branch: if `relnamespace` is neither `pg_catalog` nor `public` but
   matches a registered schema (`cat.SchemaNameForOID(oid) != ""`), reload the
   relation under that schema name.

3. **Ordering** — `replaySchemaDDLRecords` (the schema-registry WAL replay) is
   moved to run **before** `loadUserTablesFromHeap`/`loadUserIndexesFromHeap` in
   `open.go`. Previously it ran after table load, so the registry was empty when
   the reverse-map was needed.

4. **Catalog primitives** — `SchemaOID(name)` is promoted onto the `Catalog`
   interface (it was already an `InMemory` method), and a new
   `SchemaNameForOID(oid)` provides the reverse lookup.

This deliberately keeps the existing `pg_class` heap-append mechanism (no new
durability machinery); it only corrects the *value* written to an existing
column so the existing recovery scan is lossless. Writing the real schema OID is
strictly more correct than the prior `pg_catalog` collapse.

## Sibling-path note

This is a textbook [[pattern_sibling_paths_must_agree]] fix: the encode
(`namespaceOIDForSchema`) and decode (`loadUserTablesFromHeap` /
`loadUserIndexesFromHeap`) halves must use the same OID convention. They are
changed together here, with unit tests pinning each half plus an end-to-end test
spanning the restart.

## Scope / non-goals

- **PG-standby user-schema visibility** remains out of scope (as in 0110-0012):
  goopg still writes no `pg_namespace` heap row / 2684/2685 index entries for a
  user schema. A user table's `relnamespace` now points at the real schema OID,
  which an attaching PG standby cannot resolve without that `pg_namespace` row —
  but user-schema tables were already standby-invisible, so this is no
  regression (and is the natural follow-up if standby user-schema visibility is
  ever required: mirror a `pg_namespace` row the way `syncTableToCatalogHeap`
  mirrors `pg_class`).
- Non-transactional, consistent with the CREATE SCHEMA / CREATE DATABASE
  precedents.

## Tests

- `internal/catalog/schema_oid_roundtrip_test.go` —
  `TestSchemaOIDNameRoundTrip`: `SchemaOID`↔`SchemaNameForOID` inverse, including
  the recovery-carried-OID and drop cases.
- `internal/executor/namespace_oid_schema_test.go` —
  `TestNamespaceOIDForSchemaResolvesUserSchema`: write side resolves a registered
  user schema to its OID; system schemas keep fixed OIDs; unregistered/nil-catalog
  falls back to `pg_catalog`.
- `internal/testport/pgamcheck003_schemascoped_test.go` —
  `TestPort_PgAmcheck003SchemaScoped`: creates `s1.t003sc`, removes its heap file
  across a stop→corrupt→restart cycle, and asserts a `pg_amcheck --schema s1` run
  reports the missing file (exit 2) — proving the schema *and* its table survived
  the restart with the correct schema association, end-to-end through the real
  pg_amcheck binary's schema resolution.

## Result

`CREATE SCHEMA s1; CREATE TABLE s1.t; …` now reloads `s1.t` under schema `s1`
after a restart, so `pg_amcheck --schema s1` (and schema-qualified resolution
generally) works post-restart. This is the AC-003 enabler that lets the
schema-scoped tier of `003_check.pl` be ported faithfully (user schemas, not the
`public` workaround). The remaining AC-003 tiers still need feature work
(hash/gist/gin/brin/spgist AMs, box/int4range/int4[] types, STORAGE EXTERNAL
TOAST, multi-DB orchestration; 005_opclass_damage).
