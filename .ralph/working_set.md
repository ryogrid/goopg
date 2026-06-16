Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 74 COMPLETE
(committing this loop). NEXT loop starts on slice 75.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 74 (xml + money) ===
Gap: xml (142)/money (790) + arrays (_xml 143, _money 791) are ALL seeded in
pg_type_seed_data.go (lines 29/30 xml, 56/57 money) but were NEVER wired into
TypeNameToOID/OIDToTypeName, so each scalar fell back to text (25) → dumped as
`text`; array paths had no OID. (oidToBuiltinTypeName already had scalar xml 142
at line ~10436 from an earlier path — DO NOT re-add it; caused a duplicate-case
compile error that I removed.)
FIX (proven 3-site additive pattern):
  1. catalog/codec.go: scalar consts OIDXML/OIDMoney; array consts
     OIDArrayXML/OIDArrayMoney; cases in TypeNameToOID, OIDToTypeName,
     ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: scalar xml (typlen
     -1, byval f, align 'i', storage 'x'), money (typlen 8, byval t, align 'd',
     storage 'p'); 2 array rows (typlen -1, byval f; _xml align 'i', _money
     align 'd'; storage 'x'). Verified vs pg_type_seed_data.go.
  3. executor/expr.go: formatTypeOID scalars 142/790 + arrays 143/791;
     oidToBuiltinTypeName money 790 + arrays 143/791 (xml 142 ALREADY present).
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+2 array rows), internal/testport/pgdump_connsetup_test.go (arr fixture
+xm/xms/mny/mnys + asserts + doc), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; executor attr tests PASS;
TestPort_PgDumpConnectionSetup PASS (1.90s); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 75 ===
Remaining likely-small scalar gaps (VERIFY seeded in pg_type_seed_data.go BEFORE
wiring — must be seeded so a PG standby reads OID):
  - bit (1560)/varbit (1562) + _bit (1561)/_varbit (1563) — SEEDED (lines
    104-107); these CARRY typmod (bit(n)/varbit(n)) → format_type needs typmod
    decode like bpchar (NOT the no-typmod fast pattern used here).
Larger slices (defer): IDENTITY cols + SEQUENCE objects (relkind 'S'),
ENUM/composite/DOMAIN user types.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding a case to expr.go oidToBuiltinTypeName, grep `case <oid>:`
across the file — some OIDs already have a case from an older path.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
