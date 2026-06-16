Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 65 COMPLETE
(committing this loop). NEXT loop starts on slice 66.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 65 (float4[]/time[]/timestamptz[] round-trip) ===
Gap: slice 64 mapped float8/date/timestamp element types; a `speeds real[]` /
`times time[]` / `zoned timestamptz[]` column still fell back to its SCALAR
element OID and dumped without the array dimension. Scalar OIDs (700/1083/1184)
already existed; only the array maps were missing.
FIX (3 sites, additive, identical to slice 64):
  1. catalog/codec.go: OIDArrayFloat4=1021, OIDArrayTime=1183,
     OIDArrayTimestampTZ=1185 consts + float4↔_float4 / time↔_time /
     timestamptz↔_timestamptz cases in ArrayOIDForBase & BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: _float4 (align 'i'),
     _time (align 'd'), _timestamptz (align 'd') rows — all typlen=-1, typstorage 'x'.
  3. executor/expr.go formatTypeOID: 1021→"real[]", 1183→"time without time
     zone[]", 1185→"timestamp with time zone[]". Also added 1183 to
     oidToBuiltinTypeName (1021/1185 already present there).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+float4/time/timestamptz cases), internal/testport/pgdump_connsetup_test.go
(arr fixture +speeds real[], times time[], zoned timestamptz[] + slice-65 asserts),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog+executor unit suites PASS;
TestPort_PgDumpConnectionSetup PASS (exit-0 path — new arr asserts genuinely ran
inside ExitCode==0 block); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 66 ===
Known-working array element types now: int2/int4/int8/text/bool/numeric(typmod)/
float8/date/timestamp/float4/time/timestamptz. All scalar-OID-backed array types
are now covered. Remaining candidates (all LARGER than the 3-site array pattern):
  - uuid[]: BLOCKED — wire scalar uuid (OID 2950) into catalog.TypeNameToOID/
    OIDToTypeName FIRST, then array _uuid (2951) via the 3-site pattern.
  - IDENTITY column / SEQUENCE / serial — sequences skipped from pg_class virtual
    view (Virtual && no View); needs relkind='S' support first.
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
  - ENUM / composite / domain user type column.
ALWAYS: add ONE+ fixture element, run TestPort_PgDumpConnectionSetup, confirm the
exit-0 path runs (no "remaining DU-002 catalog-parity gap" log), inspect the dump.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
