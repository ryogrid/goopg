Task: M0110-0003 AC-003 — user-schema TABLE durability across restart +
schema-scoped 003_check tier (loop #21). COMPLETE + committed. (idle — next loop
starts clean unless resuming candidates below.)

=== WHAT LANDED (loop #21) ===
A table created in a user schema now reloads under its schema after a restart
(completing loop #20's CREATE SCHEMA durability). Root cause was a sibling
encode/decode disagreement on pg_class.relnamespace:
- WRITE side namespaceOIDForSchema collapsed every non-public schema to the
  pg_catalog OID (11) → s1.t written relnamespace=11.
- READ side loadUserTablesFromHeap reloaded relnamespace=11 as pg_catalog.t →
  pg_amcheck --schema s1 found no relations post-restart.
Fix: both halves agree on the REAL schema OID from the registry 0110-0012 restores.

Files:
- internal/catalog/catalog.go (SchemaOID added to Catalog interface; new
  SchemaNameForOID reverse lookup on InMemory)
- internal/executor/operators_ddl.go (namespaceOIDForSchema(cat, schema) resolves
  registered user schema via cat.SchemaOID; 4 call sites threaded)
- internal/executor/pg18_user_catalog_rows.go (buildUserPGClassRow/...ForIndex
  take cat)
- internal/initdb/open.go (moved replaySchemaDDLRecords BEFORE
  loadUserTablesFromHeap; reverse-map relnamespace via cat.SchemaNameForOID in
  loadUserTablesFromHeap + loadUserIndexesFromHeap)
- tests: internal/catalog/schema_oid_roundtrip_test.go,
  internal/executor/namespace_oid_schema_test.go,
  internal/testport/pgamcheck003_schemascoped_test.go
  (TestPort_PgAmcheck003SchemaScoped), updated stale comment in
  pgamcheck003_combined_test.go
- docs/design/0110-0013-user-schema-table-durability.md + README index
- docs/test-port/postgres-oracle-port-status.{csv,md} (AC-003)
- .ralph/fix_plan.md (loop #21 PROGRESS)

Key symbols: namespaceOIDForSchema, SchemaOID (interface), SchemaNameForOID,
loadUserTablesFromHeap/loadUserIndexesFromHeap schema reverse-map,
replaySchemaDDLRecords ordering.

Gates: build/vet/gofmt clean; PASS catalog + initdb + executor + server + full
pg_amcheck testport + TestPort_CreateSchemaSurvivesRestart (no regression).
TPC-H spotcheck SKIP (no data dir; change touches only non-public namespace
resolution — public tables get byte-identical pg_class rows).

=== OUT OF SCOPE ===
- PG-standby user-schema visibility (no pg_namespace heap row / 2684/2685 index
  for user schemas) — follow-up would mirror syncTableToCatalogHeap for
  pg_namespace if a PG standby ever needs user-schema visibility.

=== NEXT STEP (resume candidates, all larger) ===
Remaining AC-003 003_check tiers need feature work (hash/gist/gin/brin/spgist
AMs, box/int4range/int4[] types, STORAGE EXTERNAL TOAST, multi-DB orchestration);
005_opclass_damage (CREATE OPERATOR CLASS + pg_amproc parity); M0095-0003
recvlogical (030/040 logical decoding); M0110-0001 pg_dump 002 / M0110-0002
pg_waldump 002 (catalog parity).
