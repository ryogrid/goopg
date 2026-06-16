Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 67 COMPLETE
(committing this loop). NEXT loop starts on slice 68.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 67 (bytea[] array round-trip) ===
Gap: scalar `bytea` (OID 17) already round-tripped (wired in all 4 sites), but
its array form `_bytea` (OID 1001) was missing, so a `bytea[]` column fell back
to scalar `bytea`.
FIX (proven 3-site pattern, array half only — additive):
  1. catalog/codec.go: OIDArrayBytea=1001 const; bytea↔_bytea in ArrayOIDForBase
     & BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: _bytea row (typlen
     -1, byval f, align 'i', storage 'x' — matches _bool).
  3. executor/expr.go formatTypeOID (the REAL one at ~10344): added
     `case 1001: return "bytea[]"` (bytea has no typmod → bare name). The
     name-based twin at expr.go:~121 already had 1001→"bytea[]".
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+{"bytea",nil,1001,"bytea[]"}), internal/testport/pgdump_connsetup_test.go
(arr fixture +blob bytea, blobs bytea[] + asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog pkg PASS; executor TestUserPG* PASS;
TestPort_PgDumpConnectionSetup PASS (exit-0 path confirmed — no downstream logf
under -v, so the arrCols asserts inside `if res.ExitCode==0` actually ran);
pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 68 ===
Known-working array element types now: int2/int4/int8/text/bool/numeric(typmod)/
float8/date/timestamp/float4/time/timestamptz/uuid/bytea.
Remaining array candidates (still scalar-OID-backed, same 3-site pattern):
  - varchar[]  → _varchar 1015 (typmod-bearing: format_type carries n+VARHDRSZ
    onto the array, like _numeric 1231; reuse formatTypeOID(1043,typmod)+"[]").
  - bpchar[]   → _bpchar 1014 (same typmod path, formatTypeOID(1042,typmod)+"[]").
  - oid[]      → _oid 1028 (formatTypeOID already has 30→"oid[]"? NO, 30 is _xid;
    _oid is 1028 — verify before use).
Larger slices (defer): IDENTITY/SEQUENCE (relkind 'S'), ENUM/composite/domain.
ALWAYS: add ONE+ fixture element, run TestPort_PgDumpConnectionSetup -v, confirm
NO downstream logf line prints (proves the ExitCode==0 assert block ran).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
