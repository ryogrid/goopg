(idle — nothing in flight)

Last landed: DU-002 slice 251 (loop #17) — domain ARRAY types as their own feature.
A `d[]` column (array of a user-defined DOMAIN) now round-trips through pg_dump as
`public.d[]`, not the base type's array (`text[]`). Domains were the last
user-defined type with no auto-generated `_name` array type.

Mechanism (mirrors enum slice 89 / composite slice 242/250):
- catalog: Domain gains `ArrayOID`; RegisterDomain allocates TWO OIDs (domain then
  `_name` array). New LookupDomainByArrayOID (Catalog iface + InMemory).
- pg18_user_catalog_rows.go: buildUserPGTypeRowForDomain.typarray now = d.ArrayOID
  (so pg_dump isarray subquery suppresses `_zipcode`); new
  buildUserPGTypeRowForDomainArray (typtype='b', typcategory='A', typelem=d.OID,
  varlena layout, typalign=base element). buildUserPGAttributeRow domain re-resolve
  handles IsArray -> domainArrayOID (atttypid=ArrayOID, attndims=1, varlena attrs).
- operators_ddl.go: syncDomainTypeToCatalogHeap writes the array row; execDropDomain
  stamps both rows' xmax.
- expr.go: format_type adds `else if LookupDomainByArrayOID` -> public.<domain>[].

Files: internal/catalog/catalog.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/operators_ddl.go, internal/executor/expr.go,
internal/executor/pg18_user_catalog_rows_test.go (+TestUserPGAttributeDomainArrayColumn),
internal/testport/pgdump_connsetup_test.go (zips zipcode[] column + asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 251), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; catalog+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.6s, real pg_dump round-trips `zips public.zipcode[]`);
pgbench pre-commit smoke on commit (.githooks/pre-commit).

Next (slice 252+): composite FIELD whose type is a domain array (`f zipcode[]`,
buildUserPGAttributeRowForCompositeField analog). Then ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
