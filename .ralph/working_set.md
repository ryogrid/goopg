(idle — nothing in flight)

Last landed: DU-002 slice 243 (loop #9) — composite type round-trips through pg_dump.
Slice 242 made the composite `pg_type` row visible but left typrelid=0, so
dumpCompositeType found no fields AND getTypes/selectDumpableType SKIPPED the type
(a typrelid!=0 type is dropped unless (SELECT relkind FROM pg_class WHERE oid=typrelid)='c').

KEY ROOT CAUSE (cost ~half the loop): goopg serves its OWN pg_class queries from the
VIRTUAL catalog builder (catalog.go pgClass.VirtualRows, iterates c.tables), NOT the heap.
A heap pg_class write is invisible to goopg's own pg_dump connection. BUT pg_attribute IS
heap-backed (catalogHeapSyncAvailable requires it non-virtual), so heap field rows ARE seen.
=> the fix had to add a relkind='c' row to the VIRTUAL pg_class builder, not just the heap.

Files touched:
- internal/catalog/catalog.go: CompositeType +RelOID; RegisterCompositeTypeWithFields allocs
  3 OIDs (type/array/relation, nextOID+=3); virtual pg_class builder emits a relkind='c' row
  per compositeTypes entry (reltype=OID, relnatts=#fields, relam=0/relfilenode=0).
- internal/executor/pg18_user_catalog_rows.go: buildUserPGTypeRowForComposite typrelid=RelOID;
  +buildUserPGClassRowForComposite, +buildUserPGAttributeRowForCompositeField, +parseCompositeFieldType.
- internal/executor/operators_ddl.go: syncCompositeTypeToCatalogHeap also writes heap
  pg_class+pg_attribute+index entries+mirrors (PG-standby parity); execDropType stamps via
  deleteCatalogRowsForOID.
- pg18_user_catalog_rows_test.go (+TestUserPGClassAndAttributeForComposite),
  pgdump_connsetup_test.go (addr fixture+assert), docs/design/0110-0001 (Slice 243), fix_plan.md.

Gates: gofmt + build clean; catalog+executor PASS; TestPort_PgDumpConnectionSetup +
TestPort_PgDump001Basic PASS (cgroup); live-verified round-trip + DROP cleanup; pgbench smoke on commit.

Next (slice 244+): composite fields of user-defined type (enum/domain/nested composite) —
parseCompositeFieldType resolves only built-ins (folds to text otherwise); ROLLBACK-undo
(PendingCreatedComposites); ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
