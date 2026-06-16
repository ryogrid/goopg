Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 64 COMPLETE
(committing this loop). NEXT loop starts on slice 65.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 64 (float8[]/date[]/timestamp[] round-trip) ===
Gap: slice 63 mapped only bool/numeric/int2/int4/int8/text element types, so a
`ratios double precision[]` / `days date[]` / `moments timestamp[]` column fell
back to its SCALAR element OID and dumped without the array dimension. DDL
(buildUserPGAttributeRow) + heap-loader (initdb/open.go) already route through
catalog.ArrayOIDForBase/BaseOIDForArray generically, so only the 3
element-type-keyed tables needed new entries.
FIX (3 sites, additive, identical to slice 63):
  1. catalog/codec.go: OIDArrayFloat8=1022, OIDArrayDate=1182,
     OIDArrayTimestamp=1115 consts + float8↔_float8 / date↔_date /
     timestamp↔_timestamp cases in ArrayOIDForBase & BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: _float8 (align 'd'),
     _date (align 'i'), _timestamp (align 'd') rows — all typlen=-1, typstorage 'x'.
  3. executor/expr.go formatTypeOID: 1022→"double precision[]", 1182→"date[]",
     1115→"timestamp without time zone[]".
Element scalar OIDs (701/1082/1114) already existed; only array maps were missing.
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(TestUserPGAttributeArrayColumn +float8/date/timestamp cases),
internal/testport/pgdump_connsetup_test.go (arr fixture +ratios double
precision[], days date[], moments timestamp[] + slice-64 asserts),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog+parser+initdb+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (exit-0 path — NO "remaining gap" log, so the
new arr asserts genuinely ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 65 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates still open:
  - uuid[]: BLOCKED — uuid lacks a scalar OID in catalog.TypeNameToOID/
    OIDToTypeName (falls back to OIDText). Wire scalar uuid (OID 2950) FIRST,
    then array _uuid (2951) via the same 3-site pattern.
  - float4[] (_float4 1021), timestamptz[] (_timestamptz 1185), time[] (_time
    1183) — same 3-site pattern, all have existing scalar OIDs.
  - IDENTITY column / SEQUENCE / serial — blocked: sequences skipped from pg_class
    virtual view (Virtual && no View). Larger slice (relkind='S' first).
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
  - ENUM / composite / domain user type column.
ALWAYS: add ONE+ fixture element, run TestPort_PgDumpConnectionSetup, confirm the
exit-0 path runs (no "remaining DU-002 catalog-parity gap" log), inspect the dump.
Known-working array element types: int2/int4/int8/text/bool/numeric(typmod)/
float8/date/timestamp[].
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
