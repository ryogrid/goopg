Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 87 COMPLETE,
about to commit + push. NOTHING in flight. Next loop starts slice 88.

=== DONE (this loop) — DU-002 slice 87 (single-byte "char" / "char"[]) ===
Gap: "char"(18)/_char(1002) is a DECLARABLE 1-byte type (quoted `"char"`),
distinct from bpchar(1042). Both arrive at the catalog as type name "char", so
the name-only TypeNameToOID CANNOT disambiguate — it folds "char"→bpchar. The
PARSER already encodes the difference in args: unquoted `char` → bpchar(1)
(arg [1], ddl.go:2635); quoted `"char"` → no arg. Fix lives at catalog-row layer:
  1. catalog/codec.go: OIDChar(18)/OIDArrayChar(1002) consts; ArrayOIDForBase &
     BaseOIDForArray char↔_char cases; OIDToTypeName(18)→"char". TypeNameToOID
     LEFT returning bpchar (value codec keeps treating len-1 char as 1-byte str —
     NO encode/decode change).
  2. executor/pg18_user_catalog_rows.go: buildUserPGAttributeRow remaps
     bpchar→OIDChar when name=="char" && len(Args)==0 (right after the
     TypeNameToOID call, BEFORE typmod calc); userTypeAttrsForOID _char(1002)
     case {-1,f,'i','x'} (scalar 18 already present).
  3. executor/expr.go: formatTypeOID array case 1002 → `"char"[]` (scalar 18 +
     both oidToBuiltinTypeName cases already present).
Files: codec.go, codec_test.go, expr.go, pg18_user_catalog_rows.go,
pg18_user_catalog_rows_test.go (array row + scalar "char"-vs-char(1) assert),
pgdump_connsetup_test.go (fixture `ch "char"`/`chs "char"[]` + asserts),
design doc 0110-0001 slice 87 section.
Gates: gofmt+vet clean; build ./... ok; TestTypeNameToOIDRoundTrip PASS;
TestUserPGAttributeArrayColumn PASS; catalog+executor full suites PASS;
TestPort_PgDumpConnectionSetup PASS (1.98s, real pg_dump round-trip);
pgbench CI-parity smoke via pre-commit hook (pending commit).

=== NEXT STEP — DU-002 slice 88 ===
SIMPLE SCALAR+ARRAY TYPES ARE EXHAUSTED. Confirmed by surveying
pg_type_seed_data.go: every base type 'b' with category != 'A' is now wired
EXCEPT the category-'Z'/internal types (gtsvector 3642 GiST-skip;
pg_node_tree 194; pg_ndistinct 3361; pg_dependencies 3402; pg_mcv_list 5017;
pg_brin_bloom_summary 4600; pg_brin_minmax_multi_summary 4601 — none are
user-facing column types pg_dump would emit, and most have no array peer).
Next slices must move to OBJECT types, NOT column types:
  - SEQUENCE round-trip (CREATE SEQUENCE / relkind 'S') — high value, common.
  - ENUM / composite / DOMAIN user types (CREATE TYPE … AS …).
  - range / multirange (int4range 3904, needs pg_range rngsubtype wiring).
  - IDENTITY columns (GENERATED … AS IDENTITY, ties to sequences).
Pick ONE; each is LARGER than a type-wiring slice — scope to a landable sub-slice
and use the deferral ledger if it can't fully land.
GOTCHA: server typeOIDFor (dispatch.go) is a SEPARATE 5th type→OID fn
(RowDescription path), NOT touched by these slices — matches scope.
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd` in a Bash compound PERSISTS; use abs paths.
