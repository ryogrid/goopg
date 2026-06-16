Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 70 COMPLETE
(committing this loop). NEXT loop starts on slice 71.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 70 (interval scalar + array) ===
Gap: interval (1186) was rendered by formatTypeOID AND oidToBuiltinTypeName
(latter already had 1186→interval, 1187→interval[]) but was NEVER wired into
TypeNameToOID/OIDToTypeName, so an interval column fell back to text (25) and
dumped as `text`; the array path had no _interval (1187) OID at all.
FIX (proven 3-site additive pattern):
  1. catalog/codec.go: OIDInterval=1186 const; OIDArrayInterval=1187 const;
     cases in TypeNameToOID, OIDToTypeName, ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: 2 rows — scalar
     (typlen 16, byval f, align 'd', storage 'p') + array (typlen -1, align 'd',
     storage 'x'); verified vs pg_type_seed_data.go lines 98/99 + pg_type.dat.
  3. executor/expr.go formatTypeOID: array case 1187→"interval[]" (scalar 1186
     already existed). oidToBuiltinTypeName twin ALREADY had both — no change.
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+1 row interval), internal/testport/pgdump_connsetup_test.go (arr fixture
+span/spans + asserts + doc), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; executor array/typmod/coll
tests PASS; TestPort_PgDumpConnectionSetup PASS (1.83s, no downstream logf
under -v → ExitCode==0 assert block ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 71 ===
Scalar+array column-type gaps now closed: int2/int4/int8/text/bool/numeric/
float8/date/timestamp/float4/time/timestamptz/uuid/bytea/varchar/bpchar/oid/
json/jsonb/interval. Remaining likely-small scalar gaps (fall back to text now):
  - inet (869)/_inet (1041), cidr (650)/_cidr (651), macaddr (829)/_macaddr (1040),
    macaddr8 (774)/_macaddr8 (775) — network types (NOT in pg_type seed? verify
    via initdb/pg_type_seed_data.go before wiring — must be seeded so a PG standby
    can read the OID).
  - xml (142)/_xml (143); money (790)/_money (791); bit/varbit.
Parser accepts ANY ident as a column type (parseColumnType), and execCreateTable
does NOT reject unknown types — so schema-only dump just needs the OID maps wired.
Larger slices (defer): IDENTITY columns + SEQUENCE objects (relkind 'S'),
ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v, confirm NO
downstream logf prints (proves the ExitCode==0 assert block ran).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
