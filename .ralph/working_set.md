Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 66 COMPLETE
(committing this loop). NEXT loop starts on slice 67.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 66 (scalar uuid + uuid[] round-trip) ===
Gap: uuid was the first scalar element type NOT wired into the type-name maps,
so a `uuid` column fell back to text (OID 25) and dumped as `text`; there was
no _uuid array OID at all.
FIX (scalar half + array half, additive):
  1. catalog/codec.go: OIDUUID=2950 const; OIDArrayUUID=2951 const; uuid↔_uuid
     in ArrayOIDForBase & BaseOIDForArray; "uuid" case in TypeNameToOID &
     OIDToTypeName.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: uuid (typlen 16,
     byval f, align 'c', storage 'p' per pg_type.dat) + _uuid (typlen -1,
     align 'i' [element-'c'→array 'i', matching _bool], storage 'x').
  3. executor/expr.go formatTypeOID: added `case 2951: return "uuid[]"` (2950→
     "uuid" was already present). NOTE: formatTypeOID is at expr.go:10344 — the
     REAL one pg_dump format_type uses; a SEPARATE name-based fn at expr.go:~100
     already had 2950/2951 but is NOT the one the test hits. Don't confuse them.
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+uuid case), internal/catalog/codec_test.go (+{"uuid",OIDUUID} roundtrip),
internal/testport/pgdump_connsetup_test.go (arr fixture +tok uuid, ids uuid[] +
asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog pkg PASS; executor TestUserPG* PASS;
TestPort_PgDumpConnectionSetup PASS (exit-0 path — uuid asserts are inside the
ExitCode==0 return block); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 67 ===
Known-working array element types: int2/int4/int8/text/bool/numeric(typmod)/
float8/date/timestamp/float4/time/timestamptz/uuid. ALL scalar-OID-backed
array types now covered. Remaining candidates (all LARGER than 3-site pattern):
  - IDENTITY column / SEQUENCE / serial — sequences skipped from pg_class
    virtual view (Virtual && no View); needs relkind='S' support first.
  - ALTER TABLE ... SET DEFAULT / column-level non-trivial DEFAULT.
  - ENUM / composite / domain user type column (needs user pg_type rows).
ALWAYS: add ONE+ fixture element, run TestPort_PgDumpConnectionSetup, confirm
the ExitCode==0 path runs (asserts inside that block actually execute).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; always cd
back to /home/ryo/work/goopg/goopg or use absolute paths.
