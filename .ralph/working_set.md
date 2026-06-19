(idle — nothing in flight)

Last landed: DU-002 slice 248 (loop #14) — composite type FIELD whose type is a
user-defined DOMAIN now dumps as the schema-qualified domain name (public.zipcode),
not the base type. Mirrors table-column slice 90.

Mechanism (executor-only):
- buildUserPGAttributeRowForCompositeField (internal/executor/pg18_user_catalog_rows.go
  ~L1466): after the enum re-resolve, when a scalar field still folds to the text
  fallback, cat.LookupDomain(base) → domain pg_type OID for atttypid. Physical attrs
  follow the domain's BASE, resolved exactly like buildUserPGTypeRowForDomain
  (d.BaseOID, else TypeNameToOID(d.Base.Name); BaseIsEnum → fixed enum shape).
  atttypmod stays -1 (typmod belongs to the domain def, not the use site).
- KEY DIFFERENCE from table columns: composite fields are NOT resolved to the base
  type at CREATE TYPE — the parser records the raw domain name in ColType. So the
  re-resolve keys on `base` (the raw name), and the base attrs must be looked up from
  the catalog, not read off typOID (which is the text fallback). Scalar only.

Files: internal/executor/pg18_user_catalog_rows.go,
internal/executor/pg18_user_catalog_rows_test.go (+TestUserPGAttributeCompositeFieldDomain),
internal/testport/pgdump_connsetup_test.go (dom_comp fixture + compositeDefs assertions),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 248).

Gates: gofmt clean; go build ./... clean; executor unit tests PASS;
TestPort_PgDumpConnectionSetup PASS (3.25s, real pg_dump round-trips
`z public.zipcode` / `n public.numd`); pgbench pre-commit smoke on commit.

Next (slice 249+): composite field whose type is a DOMAIN ARRAY (zipcode[]), then
nested-composite field (composite whose field is itself a composite), then
ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
