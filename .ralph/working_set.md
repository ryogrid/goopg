Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 80 COMPLETE
(committing this loop). NEXT loop starts on slice 81.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 80 (the reg* OID-reference family) ===
Gap: the 11 OID-alias scalars regproc(24)/regprocedure(2202)/regoper(2203)/
regoperator(2204)/regclass(2205)/regtype(2206)/regconfig(3734)/regdictionary
(3769)/regnamespace(4089)/regrole(4096)/regcollation(4191) + their _reg* arrays
(1008/2207/2208/2209/2210/2211/3735/3770/4090/4097/4192) were seeded in
pg_type_seed_data.go but never wired into the codec OID round-trip → a declared
`regclass` column fell back to text(25). Plain 3-site pattern, NO typmod.
Scalars all {len 4, byval t, align 'i', storage 'p'}; arrays {len -1, byval f,
align 'i', storage 'x'}.
NOTE: the ::regclass/::regproc value casts (expr.go) + catalog-column OID seeding
(initdb pgCatalogTypeOID/typeOIDForCatalogColumn already map regproc→24) are
SEPARATE paths — untouched.
FIX (same as prior slices):
  1. catalog/codec.go: 11 scalar + 11 array OID consts; cases in TypeNameToOID,
     OIDToTypeName, ArrayOIDForBase, BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go: userTypeAttrsForOID grouped scalar +
     grouped array cases.
  3. executor/expr.go: oidToBuiltinTypeName + formatTypeOID scalar+array cases.
Files: internal/catalog/codec.go, internal/catalog/codec_test.go (+reg* pairs in
TestTypeNameToOIDRoundTrip), internal/executor/pg18_user_catalog_rows.go,
internal/executor/pg18_user_catalog_rows_test.go (+reg* array+typmod rows),
internal/testport/pgdump_connsetup_test.go (arr fixture +rp..rcos + asserts +doc),
docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog round-trip PASS; executor attr/typmod
PASS; TestPort_PgDumpConnectionSetup PASS (1.86s); pgbench CI-parity via pre-commit.

=== NEXT STEP — DU-002 slice 81 ===
Remaining scalar gaps to scan (VERIFY seeded in pg_type_seed_data.go FIRST):
  - "char" (18) / name (19) as DECLARABLE column types: codec TypeNameToOID maps
    "char"→bpchar(1042) and has no name→19; OIDToTypeName has no 18/19. So a
    `c "char"` or `c name` column does NOT round-trip distinctly (name collision
    with bpchar). Tricky: needs disambiguation, maybe defer.
  - oidvector(30)/int2vector(22): partially wired but INCONSISTENT — formatTypeOID
    maps 30→"oid[]" and 22→"smallint[]" (WRONG; PG renders "oidvector"/"int2vector").
    oidToBuiltinTypeName 30→"oidvector". Fix to bare names? verify vs oracle.
Larger slices (defer): range/multirange (int4range 3904 has rngsubtype = LARGER);
IDENTITY cols + SEQUENCE objects (relkind 'S'); ENUM/composite/DOMAIN user types;
current_schemas() name[] wire format (slice 33 blocker, separate).
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v.
GOTCHA: before adding expr.go oidToBuiltinTypeName/formatTypeOID cases, grep
`case <oid>:` — some OIDs already have a case from an older path.
GOTCHA: server typeOIDFor (dispatch.go) is a SEPARATE 5th type→OID fn NOT touched
by these slices (RowDescription path); leaving reg* there as text matches the
established slice scope (tid/xid/cid also absent there).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
