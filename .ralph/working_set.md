(idle — nothing in flight)

Last landed: DU-002 slice 250 (loop #16) — composite type FIELD whose type is an
ARRAY of another user-defined COMPOSITE type (`stops addr[]`) now dumps as the
schema-qualified composite array name (`public.addr[]`), not `text[]`. Mirrors the
enum-array field path (slice 246/89).

Mechanism:
- buildUserPGAttributeRowForCompositeField (internal/executor/pg18_user_catalog_rows.go):
  the nested-composite re-resolve now handles isArray — cat.LookupCompositeType(base)
  → inner composite ArrayOID (compositeArrayOID) when isArray, scalar OID otherwise.
  Array switch: `case compositeArrayOID != 0` (atttypid=ArrayOID, attndims=1).
  Attrs switch: matching varlena-array layout (-1, byval=false, align 'd', storage 'x').
- catalog: new LookupCompositeTypeByArrayOID (Catalog iface + InMemory), mirrors
  LookupEnumByArrayOID. expr.go format_type fallback adds an else-if branch rendering
  the array OID as public.<inner>[].
- Inner composite's _name array pg_type row already exists (slice 242) → no OID alloc.

Files: internal/catalog/catalog.go (iface + LookupCompositeTypeByArrayOID),
internal/executor/expr.go (format_type composite-array branch),
internal/executor/pg18_user_catalog_rows.go (re-resolve + array switch + attrs case),
internal/executor/pg18_user_catalog_rows_test.go (+TestUserPGAttributeCompositeFieldCompositeArray),
internal/testport/pgdump_connsetup_test.go (route fixture + compositeDefs + text[] negative assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 250), .ralph/fix_plan.md (progress note).

Gates: gofmt clean; go build ./... clean; catalog+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.6s, real pg_dump round-trips `stops public.addr[]`);
pgbench pre-commit smoke on commit.

Next (slice 251+): domain ARRAY types as their own feature — allocate the `_name`
array OID at CREATE DOMAIN, sync its pg_type row, render domain-array columns/fields.
Then ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
