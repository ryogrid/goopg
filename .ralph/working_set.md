(idle — nothing in flight)

Last landed: DU-002 slice 249 (loop #15) — composite type FIELD whose type is
itself another user-defined COMPOSITE type (nested composite) now dumps as the
schema-qualified composite name (public.addr), not text. Mirrors enum/domain
field paths (slices 245/248).

Mechanism:
- buildUserPGAttributeRowForCompositeField (internal/executor/pg18_user_catalog_rows.go):
  after the domain re-resolve, a still-text scalar field calls cat.LookupCompositeType(base)
  → inner composite OID (atttypid). attrs = pass-by-ref varlena (-1, byval=false,
  align 'd', storage 'x', matching buildUserPGTypeRowForComposite); atttypmod -1.
- catalog: LookupCompositeType promoted onto the Catalog interface (row builder
  takes the interface) + new LookupCompositeTypeByOID so format_type's fallback
  (expr.go ~L8828) renders the OID as public.<inner> (mirrors enum/domain branches).
- Inner composite's pg_type rows already exist (slice 242) → no new OID alloc.

Files: internal/catalog/catalog.go (interface + LookupCompositeTypeByOID),
internal/executor/expr.go (format_type composite branch),
internal/executor/pg18_user_catalog_rows.go (re-resolve + attrs case),
internal/executor/pg18_user_catalog_rows_test.go (+TestUserPGAttributeCompositeFieldNestedComposite),
internal/testport/pgdump_connsetup_test.go (nested_comp fixture + compositeDefs assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 249).

Gates: gofmt clean; go build ./... clean; catalog+executor unit tests PASS;
TestPort_PgDumpConnectionSetup PASS (3.8s, real pg_dump round-trips
`location public.addr`); pgbench pre-commit smoke on commit.

REORDER NOTE: domain-array field (previously listed as slice 249) deferred — it
blocks on domain array types, which goopg does not synthesize anywhere yet (a
separate feature, not a dump slice).

Next (slice 250+): composite-typed ARRAY field (addr[]) — element OID via
ct.ArrayOID (already synced, slice 242), attndims=1, format_type via a new
LookupCompositeTypeByArrayOID branch. Then domain array types as their own
feature. Then ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
