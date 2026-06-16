Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 69 COMPLETE
(committing this loop). NEXT loop starts on slice 70.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 69 (json/jsonb scalar + array) ===
Gap: json (114) / jsonb (3802) were absent from TypeNameToOID/OIDToTypeName, so
a json/jsonb column fell back to text (25) and dumped as `text`; the array path
had no _json (199) / _jsonb (3807) OID at all.
FIX (proven 3-site additive pattern):
  1. catalog/codec.go: OIDJSON=114, OIDJsonb=3802, OIDArrayJSON=199,
     OIDArrayJsonb=3807 consts; cases in TypeNameToOID, OIDToTypeName,
     ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: 4 rows (scalar +
     array; typlen -1, byval f, align 'i', storage 'x', NO collation — verified
     against pg_type.dat lines 138/445).
  3. executor/expr.go formatTypeOID: array cases 199→"json[]", 3807→"jsonb[]"
     (no typmod). Scalar 114/3802 cases already existed. Name-based twin (~11057)
     already had json/jsonb scalar; arrays go via OID path, no change.
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+2 rows json/jsonb), internal/testport/pgdump_connsetup_test.go (arr fixture
+doc/docs/jdoc/jdocs + asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; TestUserPGAttributeArrayColumn
PASS; TestPort_PgDumpConnectionSetup PASS (1.88s, no downstream logf under -v →
ExitCode==0 assert block ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 70 ===
Scalar+array column-type gaps now closed: int2/int4/int8/text/bool/numeric/
float8/date/timestamp/float4/time/timestamptz/uuid/bytea/varchar/bpchar/oid/
json/jsonb. Remaining likely-small scalar gaps to probe (fall back to text now):
  - inet (869)/_inet (1041), cidr (650)/_cidr (651), macaddr (829)/_macaddr (1040),
    macaddr8 (774)/_macaddr8 (775) — network types.
  - xml (142)/_xml (143); bit/varbit; interval[] (_interval 1187); money (790).
Verify scalar round-trips before the array form. Larger slices (defer): IDENTITY
columns + SEQUENCE objects (relkind 'S'), ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v, confirm NO
downstream logf prints (proves the ExitCode==0 assert block ran).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
