(idle — nothing in flight)

Loop #12 COMPLETE: M0119-0004 DU-002 slice 373 — a TABLE column typed as a
user-defined COMPOSITE type (`c public.addr`, `carr public.addr[]`) now
round-trips through pg_dump (PRODUCTION fix).

Bug: a composite type name is not a built-in, so the CREATE TABLE column path
folds it to the text fallback (catalog.TypeNameToOID / InMemory.ResolveColumnType
both preserve the bare name unchanged — it's not a domain). buildUserPGAttributeRow
(pg18_user_catalog_rows.go) had an enum branch (slice 88) and a domain branch
(slice 90) over the text fallback but NO composite branch, so pg_attribute.atttypid
stayed text(25) → pg_dump getTableAttrs→format_type rendered the column `text` /
`text[]` (UNRESTORABLE dump).

Fix: added the composite branch mirroring enum/domain — when typOID==OIDText and
no enum matched, `cat.LookupCompositeType(col.Type.Name)` → composite OID (scalar)
or ct.ArrayOID (`addr[]`, attndims=1). Layout = varlena/attlen=-1/attbyval=false/
attalign='d'/attstorage='x' (mirrors buildUserPGTypeRowForComposite). The parser
splits `public.addr` into Schema+Name so Type.Name is the bare registry key.
format_type already resolves composite OID/array OID → qualified name (slices
249/250), so no other site changed.

Files:
- internal/executor/pg18_user_catalog_rows.go (buildUserPGAttributeRow: composite
  branch + array-OID remap case + attrs layout case)
- internal/executor/pg18_user_catalog_rows_test.go (TestUserPGAttributeCompositeColumn)
- internal/testport/pgdump_connsetup_test.go (public.comptcol fixture + assertions)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 373)
- .ralph/fix_plan.md + .ralph/deferral_ledger.md (slice 373)

Gates: TestUserPGAttributeCompositeColumn + executor + catalog suites PASS;
TestPort_PgDumpConnectionSetup PASS (5.1s, byte-identical vs real pg_dump 18.3);
build/gofmt/vet clean; pgbench smoke = pre-commit hook. No TPC-H (metadata-only
catalog-row builder; composite types absent from TPC-H schema — precedent slices
370-372).

Deferred (ledger): composite-column VALUES (INSERT/COPY) not exercised (schema-dump
fidelity only); non-public-schema composite columns uncovered (registry is bare-name).

Next loop: pick a fresh M0119-0004 pg_dump slice. Empirical probe (throwaway
zz_probe_test.go dumping a feature-rich schema, then visually diff vs known PG
output) is the fastest way to find the next divergence given the deep existing
coverage.
