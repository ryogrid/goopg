Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 75 COMPLETE
(committing this loop). NEXT loop starts on slice 76.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 75 (bit + varbit, typmod-bearing) ===
Gap: bit (1560)/varbit (1562) + arrays (_bit 1561, _varbit 1563) all seeded in
pg_type_seed_data.go (lines 104-107) but never wired into TypeNameToOID/
OIDToTypeName, so each scalar fell back to text (25). UNLIKE recent slices, bit
& varbit CARRY a typmod = raw bit length, NO VARHDRSZ (anybit_typmodin/out).
FIX (proven 3-site pattern + 4th typmod site):
  1. catalog/codec.go: consts OIDBit/OIDVarbit + OIDArrayBit/OIDArrayVarbit;
     cases in TypeNameToOID ("bit"; "varbit"/"bit varying"), OIDToTypeName,
     ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID scalar+array rows
     (all typlen -1, byval f, align 'i', storage 'x'); AND pgAttTypmod case
     1560,1562 → return args[0] verbatim (NO +4, unlike char/varchar).
  3. executor/expr.go: formatTypeOID typmod cases (bit(%d)/bit varying(%d);
     arrays = formatTypeOID(base,typmod)+"[]"); oidToBuiltinTypeName bare
     "bit"/"bit varying" + "[]" forms.
Parser already maps `bit varying`→varbit + parses (n) typmod + [] (ddl.go ~2562).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+3 array rows, +3 typmod rows), internal/testport/pgdump_connsetup_test.go
(arr fixture +bv/bvs/vb/vbs + asserts bit(8)/bit varying(16) + doc),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; parser PASS; executor attr/
typmod tests PASS; TestPort_PgDumpConnectionSetup PASS (1.84s); pgbench
CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 76 ===
Remaining likely-small scalar gaps (VERIFY seeded in pg_type_seed_data.go BEFORE
wiring — must be seeded so a PG standby reads OID). Candidates to scan:
  - pg_lsn (3220)/_pg_lsn (3221) — no typmod; oidToBuiltinTypeName already has
    scalar 3220 "pg_lsn" (grep `case 3220` before re-adding). Check seeded.
  - txid_snapshot / pg_snapshot, tsrange/numrange etc. (range types = larger).
Larger slices (defer): IDENTITY cols + SEQUENCE objects (relkind 'S'),
ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding a case to expr.go oidToBuiltinTypeName, grep `case <oid>:`
across the file — some OIDs already have a case from an older path.
GOTCHA: typmod types need a pgAttTypmod case AND typmod-aware formatTypeOID.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
