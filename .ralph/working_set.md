(idle — nothing in flight)

Last landed: DU-002 slice 242 (loop #8) — composite type `pg_type` heap row.
`CREATE TYPE x AS (...)` was already parsed + registered in-memory but INVISIBLE
to pg_type (no heap row). This slice synthesizes the two pg_type rows PG allocates
per composite type (typtype='c' + auto `_name` array typtype='b'/typcategory='A'),
mirroring the enum precedent (slices 89/90).

Files touched:
- internal/catalog/catalog.go: new `CompositeType{Name,OID,ArrayOID,Fields}` struct +
  `compositeTypes` map; `RegisterCompositeTypeWithFields` allocates 2 OIDs (stable on
  re-register) and returns `*CompositeType`; new `LookupCompositeType`;
  `DropCompositeType` clears new+field maps.
- internal/executor/operators_ddl.go: `syncCompositeTypeToCatalogHeap` (mirrors
  `syncEnumTypeToCatalogHeap`); wired into execCreateType composite branch + execDropType
  xmax-stamp (parallel to enum branch).
- internal/executor/pg18_user_catalog_rows.go: `buildUserPGTypeRowFor{Composite,CompositeArray}`.
- internal/executor/pg18_user_catalog_rows_test.go: `TestUserPGTypeRowForComposite`.
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 242); .ralph/fix_plan.md.

Gates: gofmt OK; `go build ./internal/...` clean; full executor + catalog suites PASS;
`TestPort_PgDumpConnectionSetup` PASS (cgroup wrapper, -count=1, 3.65s — no composite
fixture added, so emitting rows cannot regress the round-trip); pgbench pre-commit smoke
on commit.

KEY: typrelid left 0 — the implicit pg_class relation (relkind='c') is NOT seeded yet.

Next (slice 243+): synthesize the implicit pg_class relation (relkind='c',
reltype=type OID) + one pg_attribute row per field (atttypid resolved from each field's
type name — reuse buildUserPGAttributeRow's type resolution) + pg_class OID/relname-nsp
index entries; set typrelid to the relation OID; THEN add `CREATE TYPE x AS (a int, b text)`
to the pgdump_connsetup_test.go fixture and assert round-tripped
`CREATE TYPE public.x AS (a integer, b text);`. dumpCompositeType (pg_dump.c:13083) walks
typrelid→pg_attribute via the query in the design doc. Also deferred: ROLLBACK-undo for
composite heap rows (PendingCreatedComposites, mirror PendingCreatedEnums).
