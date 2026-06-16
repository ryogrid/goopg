Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 76 COMPLETE
(committing this loop). NEXT loop starts on slice 77.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 76 (pg_lsn, no typmod) ===
Gap: pg_lsn (3220)/_pg_lsn (3221) seeded in pg_type_seed_data.go (lines 137-138)
and supported by analyzer/executor for arithmetic, but never wired into the
pg_dump catalog-codec path, so a pg_lsn column fell back to text (25).
NOTE: oidToBuiltinTypeName ALREADY had scalar 3220 "pg_lsn" (older path) but no
array case; formatTypeOID had NEITHER. No typmod (plain 3-site pattern).
FIX:
  1. catalog/codec.go: consts OIDPgLsn(3220)/OIDArrayPgLsn(3221); cases in
     TypeNameToOID ("pg_lsn"), OIDToTypeName, ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID scalar
     {8,byval t,align 'd',storage 'p'} + array {-1,f,'d','x'}. NO pgAttTypmod case.
  3. executor/expr.go: oidToBuiltinTypeName +case 3221 "pg_lsn[]"; formatTypeOID
     +case 3220 "pg_lsn" +case 3221 "pg_lsn[]" (bare names, no typmod).
Parser accepts pg_lsn as a generic identifier (no multi-word/typmod handling).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+pg_lsn array row + typmod row), internal/testport/pgdump_connsetup_test.go
(arr fixture +lsn/lsns + asserts pg_lsn/pg_lsn[] + doc),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; parser PASS; executor attr/
typmod tests PASS; TestPort_PgDumpConnectionSetup PASS (1.78s); pgbench
CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 77 ===
Remaining scalar gaps (VERIFY seeded in pg_type_seed_data.go BEFORE wiring).
Candidates to scan:
  - txid_snapshot (2970)/_txid_snapshot (2949); pg_snapshot (5038)/_pg_snapshot
    (5039). No typmod. Check seeded.
  - range/multirange types (int4range, numrange, tsrange, ...) = LARGER (need
    rngsubtype handling); defer.
Larger slices (defer): IDENTITY cols + SEQUENCE objects (relkind 'S'),
ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding a case to expr.go oidToBuiltinTypeName/formatTypeOID, grep
`case <oid>:` across the file — some OIDs already have a case from an older path
(pg_lsn 3220 was already in oidToBuiltinTypeName but not formatTypeOID).
GOTCHA: typmod types need a pgAttTypmod case AND typmod-aware formatTypeOID.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
