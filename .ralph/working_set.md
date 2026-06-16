Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 71 COMPLETE
(committing this loop). NEXT loop starts on slice 72.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 71 (network-address family) ===
Gap: inet (869)/cidr (650)/macaddr (829)/macaddr8 (774) + their arrays
(_inet 1041, _cidr 651, _macaddr 1040, _macaddr8 775) are ALL seeded in
pg_type_seed_data.go but were NEVER wired into TypeNameToOID/OIDToTypeName, so
each scalar fell back to text (25) → dumped as `text`; array paths had no OID.
FIX (proven 3-site additive pattern):
  1. catalog/codec.go: scalar consts OIDInet/OIDCidr/OIDMacaddr/OIDMacaddr8;
     array consts OIDArrayInet/Cidr/Macaddr/Macaddr8; cases in TypeNameToOID,
     OIDToTypeName, ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: scalar inet/cidr
     (typlen -1, align 'i', storage 'm'), macaddr (typlen 6, 'i','p'), macaddr8
     (typlen 8, 'i','p'); 4 array rows (typlen -1,'i','x'). Verified vs
     pg_type_seed_data.go lines 47/48/54/55/58/59/86/87.
  3. executor/expr.go formatTypeOID + oidToBuiltinTypeName: scalar 650/774/829/869
     + array 651/775/1040/1041 (all bare names, no typmod).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+4 array rows), internal/testport/pgdump_connsetup_test.go (arr fixture
+ip/ips/net/nets/mac/macs/mac8/mac8s + asserts + doc),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; executor array/typmod tests
PASS; TestPort_PgDumpConnectionSetup PASS (2.19s, no downstream logf under -v →
ExitCode==0 assert block ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 72 ===
Remaining likely-small scalar gaps (fall back to text now, VERIFY seeded in
pg_type_seed_data.go BEFORE wiring — must be seeded so a PG standby reads OID):
  - xml (142)/_xml (143); money (790)/_money (791); bit/varbit.
  - tsvector (3614)/_tsvector (3643), tsquery (3615)/_tsquery (3645).
  - point/line/lseg/box/path/polygon/circle geometric family.
Parser accepts ANY ident as a column type (parseColumnType); execCreateTable
does NOT reject unknown types — schema-only dump just needs the OID maps wired.
Larger slices (defer): IDENTITY cols + SEQUENCE objects (relkind 'S'),
ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v, confirm NO
downstream logf prints (proves the ExitCode==0 assert block ran).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
