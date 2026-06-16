Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 77 COMPLETE
(committing this loop). NEXT loop starts on slice 78.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 77 (txid_snapshot + pg_snapshot, no typmod) ===
Gap: txid_snapshot (2970)/_txid_snapshot (2949) and pg_snapshot (5038)/
_pg_snapshot (5039) seeded in pg_type_seed_data.go + supported by snapshot
SRFs, but never wired into the pg_dump catalog-codec path → snapshot columns
fell back to text (25). Both varlena, NO typmod (plain 3-site pattern).
FIX:
  1. catalog/codec.go: consts OIDTxidSnapshot(2970)/OIDPgSnapshot(5038) +
     OIDArrayTxidSnapshot(2949)/OIDArrayPgSnapshot(5039); cases in
     TypeNameToOID, OIDToTypeName, ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID scalar+array
     {-1, byval f, align 'd', storage 'x'} for all four. NO pgAttTypmod case.
  3. executor/expr.go: oidToBuiltinTypeName +2970/5038 scalar +2949/5039 array;
     formatTypeOID +2970/5038/2949/5039 (bare names, no typmod).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+txid_snapshot/pg_snapshot array+typmod rows), internal/testport/
pgdump_connsetup_test.go (arr fixture +txs/txss/pgs/pgss + asserts + doc),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; executor attr/typmod PASS;
TestPort_PgDumpConnectionSetup PASS (1.86s); pgbench CI-parity via pre-commit.

=== NEXT STEP — DU-002 slice 78 ===
Remaining scalar gaps (VERIFY seeded in pg_type_seed_data.go BEFORE wiring).
Candidates to scan:
  - range/multirange types (int4range 3904, numrange 3906, tsrange 3908, ...)
    = LARGER (need rngsubtype handling); defer.
  - xid8 (5069)/_xid8 (271)? check seeded + whether dumpable. tid/xid/cid (27/
    28/29) are in formatTypeOID already.
Larger slices (defer): IDENTITY cols + SEQUENCE objects (relkind 'S'),
ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding a case to expr.go oidToBuiltinTypeName/formatTypeOID, grep
`case <oid>:` across the file — some OIDs already have a case from an older path.
GOTCHA: typmod types need a pgAttTypmod case AND typmod-aware formatTypeOID.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
