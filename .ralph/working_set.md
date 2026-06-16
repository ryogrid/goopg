Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 62 COMPLETE
(committing this loop). NEXT loop starts on slice 63.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 62 (array-typed column round-trip) ===
Gap (confirmed via code read + real pg_dump exit-0 run): parser captured the SQL
`[]` suffix (ColumnType.IsArray, M0097-0071) but CREATE TABLE's addCol DROPPED it
→ buildUserPGAttributeRow wrote the SCALAR element OID → pg_dump's
format_type(atttypid,atttypmod) rendered `tags text` not `tags text[]`. catalog.Type
had NO array flag at all.
FIX (5 sites, additive, runtime-safe — Type.Name still holds element type):
  1. catalog/catalog.go: catalog.Type gains `IsArray bool`.
  2. catalog/codec.go: array OID consts (OIDArrayInt2=1005/Int4=1007/Text=1009/
     Int8=1016) + ArrayOIDForBase + BaseOIDForArray (only these 4; the ones
     formatTypeOID already renders as arrays).
  3. executor/operators_ddl.go addCol (~line 703): propagate IsArray into Type.
  4. executor/pg18_user_catalog_rows.go buildUserPGAttributeRow: compute typmod
     from BASE oid first, then remap typOID→array OID + attndims=1 when IsArray;
     userTypeAttrsForOID gains 4 array cases (_int8 align 'd', _text default coll).
  5. initdb/open.go loadUserTablesFromHeap (~1957): reverse-map persisted array OID
     → element + re-flag IsArray (heap-write↔heap-read sibling paths in sync).
Files: internal/catalog/catalog.go, internal/catalog/codec.go,
internal/executor/operators_ddl.go, internal/executor/pg18_user_catalog_rows.go,
internal/initdb/open.go, internal/executor/pg18_user_catalog_rows_test.go
(TestUserPGAttributeArrayColumn), internal/testport/pgdump_connsetup_test.go
(arr fixture + slice-62 asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog+parser+initdb+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS first run (exit-0 path — NO "remaining gap"
log line, so the arr asserts genuinely ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 63 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates still open:
  - numeric[]/bool[] array columns — would now fall back to scalar OID (slice 62
    only mapped int2/int4/int8/text). Needs array OID + formatTypeOID rendering.
  - IDENTITY column / SEQUENCE / serial — blocked: sequences skipped from pg_class
    virtual view (Virtual && no View). Larger slice (relkind='S' first).
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
  - ENUM / composite / domain user type column.
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, confirm the
exit-0 path runs (no "remaining DU-002 catalog-parity gap" log), inspect the dump.
Known-working: CHECK, DEFAULT now(), typmods, FKs, comments, ordered indexes,
plain+renamed VIEWs, GENERATED STORED cols, MATERIALIZED/RECURSIVE VIEW,
int2/int4/int8/text[] array columns.
Known orthogonal: plpgsql user funcs can't dump (plpgsql absent from pg_language).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
