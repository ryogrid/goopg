Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 86 COMPLETE,
about to commit + push. NOTHING in flight. Next loop starts slice 87.

=== DONE (this loop) — DU-002 slice 86 (aclitem / aclitem[]) ===
Gap: aclitem(1033)/_aclitem(1034) used internally for catalog *acl cols but had
NO codec name→OID entry, so a declared `aclitem` col fell back to text(25); neither
display fn rendered 1033/1034 for the user-DDL path. Both already seeded in
pg_type_seed_data.go (verified vs upstream pg_type.dat: typlen 16, byval f,
align 'd', storage 'p'). Added:
  1. catalog/codec.go: OIDAclitem(1033)/OIDArrayAclitem(1034) consts + 4-site
     (TypeNameToOID "aclitem", OIDToTypeName→"aclitem", ArrayOIDForBase,
     BaseOIDForArray).
  2. executor/expr.go: formatTypeOID scalar 1033→"aclitem" + array 1034→
     "aclitem[]"; oidToBuiltinTypeName scalar 1033 + array 1034 (both synced).
  3. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID scalar 1033
     {16,f,'d','p'} + array 1034 {-1,f,'d','x'}.
Files: codec.go, codec_test.go, expr.go, pg18_user_catalog_rows.go,
pg18_user_catalog_rows_test.go, pgdump_connsetup_test.go (fixture acl/acls +
asserts), design doc 0110-0001 slice 86 section.
Gates: gofmt clean; build ./... ok; TestTypeNameToOIDRoundTrip PASS;
TestUserPGAttributeArrayColumn PASS; TestPort_PgDumpConnectionSetup PASS (1.88s);
pgbench CI-parity smoke via pre-commit hook (pending commit).

=== NEXT STEP — DU-002 slice 87 ===
Pattern per scalar+array type: VERIFY seeded in pg_type_seed_data.go FIRST (check
typlen/align/storage vs postgres/src/include/catalog/pg_type.dat — bootstrap.go can
be STALE, seed_data is source of truth); then check BOTH display fns
(oidToBuiltinTypeName scalar ~L195/array ~L313 AND formatTypeOID ~L10841 in expr.go)
— sibling-paths gotcha; add 4-site codec wiring + userTypeAttrsForOID scalar+array;
add fixture col(s) to `arr` table (~L626) + asserts (~L1146) in
pgdump_connsetup_test.go; add codec_test round-trip + pg18 attr-test row; run -v.
Remaining un-wired simple scalar+array candidates: getting thin. Survey pg_type_seed
_data.go for any base type 'b' still missing from TypeNameToOID.
BLOCKED/LARGER (defer w/ ledger): "char"(18)/_char(1002) needs parser fold work
(BUT _char IS already in oidToBuiltinTypeName case 1002 → "\"char\"[]"); range/
multirange (int4range 3904, has rngsubtype = LARGER); IDENTITY+SEQUENCE (relkind
'S'); ENUM/composite/DOMAIN user types; gtsvector(3642) GiST-internal skip.
GOTCHA: server typeOIDFor (dispatch.go) is a SEPARATE 5th type→OID fn (RowDescription
path) NOT touched by these slices; leaving types there as-is matches scope.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd` in a Bash compound PERSISTS; use abs paths.
