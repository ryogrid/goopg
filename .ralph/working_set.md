Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 63 COMPLETE
(committing this loop). NEXT loop starts on slice 64.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 63 (bool[]/numeric[] array round-trip) ===
Gap: slice 62 only mapped int2/int4/int8/text element types, so a `flags
boolean[]` / `prices numeric(10,2)[]` column fell back to its SCALAR element OID
→ dumped as `boolean`/`numeric(10,2)`, losing the array dimension. The DDL
(addCol) + heap-loader (loadUserTablesFromHeap) paths were ALREADY generic (they
route through catalog.ArrayOIDForBase/BaseOIDForArray), so only the 3
element-type-keyed tables needed new entries.
FIX (3 sites, additive):
  1. catalog/codec.go: OIDArrayBool=1000, OIDArrayNumeric=1231 consts +
     bool↔_bool / numeric↔_numeric cases in ArrayOIDForBase & BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: _bool (1000) and
     _numeric (1231) rows — both typlen=-1, typalign 'i', typstorage 'x'.
  3. executor/expr.go formatTypeOID: case 1000→"boolean[]"; case 1231→
     formatTypeOID(1700,typmod)+"[]" so element typmod yields numeric(10,2)[].
atttypmod already carries ELEMENT typmod (computed before array remap, slice 62)
— no new typmod plumbing needed.
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(TestUserPGAttributeArrayColumn +bool/numeric cases, struct gained `args`),
internal/testport/pgdump_connsetup_test.go (arr fixture +flags boolean[],
prices numeric(10,2)[] + slice-63 asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog+parser+initdb+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (exit-0 path — NO "remaining gap" log, so the
new arr asserts genuinely ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 64 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates still open:
  - more array element types: date[]/timestamp[]/uuid[]/float8[] — same 3-site
    pattern (array OID const + ArrayOID maps + userTypeAttrsForOID + formatTypeOID).
  - IDENTITY column / SEQUENCE / serial — blocked: sequences skipped from pg_class
    virtual view (Virtual && no View). Larger slice (relkind='S' first).
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
  - ENUM / composite / domain user type column.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, confirm the
exit-0 path runs (no "remaining DU-002 catalog-parity gap" log), inspect the dump.
Known-working: CHECK, DEFAULT now(), typmods, FKs, comments, ordered indexes,
plain+renamed VIEWs, GENERATED STORED cols, MATERIALIZED/RECURSIVE VIEW,
int2/int4/int8/text/bool/numeric[] array columns (numeric carries typmod).
Known orthogonal: plpgsql user funcs can't dump (plpgsql absent from pg_language).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
