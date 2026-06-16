Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 79 COMPLETE
(committing this loop). NEXT loop starts on slice 80.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 79 (tid/xid/cid + _tid/_xid/_cid) ===
Gap: tid(27)/xid(28)/cid(29) seeded + analyzer-known; scalar NAMES already in
expr.go formatTypeOID (27/28/29), but NOT in codec.go TypeNameToOID/
OIDToTypeName nor oidToBuiltinTypeName, and the 3 array OIDs (_tid 1010,
_xid 1011, _cid 1012) were unwired everywhere → columns fell back to text(25).
Plain 3-site pattern, NO typmod.
Attrs: tid {len 6, byval f, align 's', storage 'p'};
xid/cid {len 4, byval t, align 'i', storage 'p'};
arrays all {len -1, byval f, align 'i', storage 'x'}.
FIX:
  1. catalog/codec.go: consts OIDTid/OIDXid/OIDCid + OIDArrayTid/Xid/Cid; cases
     in TypeNameToOID, OIDToTypeName, ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID 3 scalar + 3 array.
  3. executor/expr.go: oidToBuiltinTypeName +27/28/29 scalar +1010/1011/1012
     array; formatTypeOID +1010/1011/1012 array (scalars 27/28/29 pre-existed).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+tid/xid/cid array+typmod rows), internal/testport/pgdump_connsetup_test.go
(arr fixture +td/tds/xd/xds/cd/cds + asserts + doc), docs/design/0110-0001-...md.
Gates: gofmt clean; build ./... ok; catalog PASS; executor attr/typmod PASS;
TestPort_PgDumpConnectionSetup PASS (1.86s); pgbench CI-parity via pre-commit.

=== NEXT STEP — DU-002 slice 80 ===
Remaining scalar gaps to scan (VERIFY seeded in pg_type_seed_data.go FIRST):
  - regproc/regclass/reg* OID-alias types? check seeded + dumpable + array OIDs.
  - "char" (18, internal single-byte) / name (19) as declarable column types —
    check codec.go round-trip + array (_char 1002, _name 1003) seeded.
  - oidvector (30) / int2vector (22) — already partial? grep formatTypeOID.
Larger slices (defer): range/multirange (int4range 3904 has rngsubtype = LARGER);
IDENTITY cols + SEQUENCE objects (relkind 'S'); ENUM/composite/DOMAIN user types;
current_schemas() name[] wire format (slice 33 blocker, separate).
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding expr.go oidToBuiltinTypeName/formatTypeOID cases, grep
`case <oid>:` — some OIDs already have a scalar case from an older path.
GOTCHA: typmod types need a pgAttTypmod case AND typmod-aware formatTypeOID.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
