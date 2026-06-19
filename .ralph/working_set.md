(idle — nothing in flight)

Last landed: DU-002 slice 252 (loop #18) — composite FIELD whose type is an ARRAY
of a user-defined DOMAIN. `CREATE TYPE rec AS (zips zipcode[])` now dumps the field
as `public.zipcode[]`, not the base type's array (`text[]`). Closes the
composite-field type matrix (built-in / enum / domain / composite × scalar / array)
for CREATE TYPE.

Mechanism (mirrors composite-array field slice 250 + domain-array column slice 251):
- buildUserPGAttributeRowForCompositeField: the domain re-resolve (was gated
  `!isArray`) now also handles isArray → domainArrayOID = d.ArrayOID; element layout
  from domain base (d.BaseOID else TypeNameToOID(d.Base.Name)); domain-over-enum
  forces int align. New domainArrayOID cases in array-OID switch (atttypid→ArrayOID,
  attndims=1) + attrs switch (typlen=-1, storage 'x', base align/collation).
- NO expr.go change: format_type LookupDomainByArrayOID branch (251) + synced
  domain-array pg_type row (syncDomainTypeToCatalogHeap, 251) already exist.

Files: internal/executor/pg18_user_catalog_rows.go,
internal/executor/pg18_user_catalog_rows_test.go (+TestUserPGAttributeCompositeFieldDomainArray),
internal/testport/pgdump_connsetup_test.go (CREATE TYPE public.dom_arr_comp + asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 252), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; executor+catalog unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.5s, real pg_dump round-trips `zips public.zipcode[]`);
pgbench pre-commit smoke on commit (.githooks/pre-commit).

Next (slice 253+): ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE (composite-field type
matrix complete for CREATE TYPE).
