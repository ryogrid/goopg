Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 81 COMPLETE
(committing this loop). NEXT loop starts on slice 82.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 81 (int2vector / oidvector) ===
Gap: int2vector(22)/oidvector(30) + arrays _int2vector(1006)/_oidvector(1013)
were seeded in pg_type_seed_data.go but MIS-WIRED. Two bugs:
  (a) formatTypeOID rendered OID 22→"smallint[]" and 30→"oid[]" — those names
      belong to the GENUINE _int2(1005)/_oid(1028) array types. Real PG
      format_type(30,-1)="oidvector". Fixed 22→"int2vector", 30→"oidvector",
      added 1006→"int2vector[]", 1013→"oidvector[]".
  (b) codec had no name→OID entry → declared oidvector column fell back to text.
Scalars varlena {len -1, byval f, align 'i', storage 'p'}; arrays {storage 'x'}.
NOTE: oidToBuiltinTypeName already had 30→"oidvector" (correct); only added
missing 22→"int2vector" there. Verified against live PG 18.3: CREATE TABLE +
format_type + pg_dump all render bare "int2vector"/"oidvector"/"...[]".
FIX (3-site pattern + 2 formatTypeOID corrections):
  1. catalog/codec.go: OIDInt2vector(22)/OIDOidvector(30) +
     OIDArrayInt2vector(1006)/OIDArrayOidvector(1013) consts; cases in
     TypeNameToOID/OIDToTypeName/ArrayOIDForBase/BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID scalar+array cases.
  3. executor/expr.go: oidToBuiltinTypeName +22; formatTypeOID 22/30 FIXED +
     1006/1013 added.
Files: internal/catalog/codec.go, internal/catalog/codec_test.go (+vector pairs),
internal/executor/pg18_user_catalog_rows.go,
internal/executor/pg18_user_catalog_rows_test.go (+vector array+typmod rows),
internal/testport/pgdump_connsetup_test.go (fixture iv/ivs/ov/ovs + asserts +doc),
docs/design/0110-0001-pg-dump-tap-port.md (slice 81 entry).
Gates: gofmt clean; build ./... ok; catalog round-trip PASS; executor attr/typmod
PASS; initdb PASS (121s); TestPort_PgDumpConnectionSetup PASS (1.88s); pgbench
CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 82 ===
Remaining scalar gaps to scan (VERIFY seeded in pg_type_seed_data.go FIRST):
  - "char"(18) / name(19) as DECLARABLE column types: codec TypeNameToOID maps
    "char"→bpchar(1042) and has no name→19; so `c "char"`/`c name` does NOT
    round-trip distinctly (name collision with bpchar). Tricky disambiguation;
    parser may also fold "char"→bpchar. Investigate parser first, maybe defer.
Larger slices (defer): range/multirange (int4range 3904, rngsubtype = LARGER);
IDENTITY cols + SEQUENCE objects (relkind 'S'); ENUM/composite/DOMAIN user types;
current_schemas() name[] wire format (slice 33 blocker, separate).
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding expr.go oidToBuiltinTypeName/formatTypeOID cases, grep
`case <oid>:` — some OIDs already have a (sometimes WRONG) case from an older
path (slice 81 fixed exactly such a case: 22/30 → wrong array names).
GOTCHA: server typeOIDFor (dispatch.go) is a SEPARATE 5th type→OID fn NOT touched
by these slices (RowDescription path); leaving vectors there as-is matches scope.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres`/`cd /tmp` in a Bash compound PERSISTS;
use abs paths (bit me twice this loop).
