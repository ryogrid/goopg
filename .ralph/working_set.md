Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 68 COMPLETE
(committing this loop). NEXT loop starts on slice 69.
NOTHING in flight after commit.

=== DONE (this loop) — DU-002 slice 68 (varchar[]/bpchar[]/oid[] arrays) ===
Gap: scalar varchar(1043)/bpchar(1042)/oid(26) already round-tripped, but their
array forms _varchar(1015)/_bpchar(1014)/_oid(1028) were missing → fell back to
scalar. This COMPLETES every simple scalar-OID-backed array type.
FIX (proven 3-site additive pattern):
  1. catalog/codec.go: OIDArrayVarChar=1015, OIDArrayBpChar=1014, OIDArrayOID=1028
     consts; cases in ArrayOIDForBase & BaseOIDForArray.
  2. executor/pg18_user_catalog_rows.go userTypeAttrsForOID: 3 array rows (typlen
     -1, byval f, align 'i', storage 'x'; _varchar/_bpchar carry default collation
     like _text; _oid no collation).
  3. executor/expr.go canonical formatTypeOID (~10413): _varchar/_bpchar are
     TYPMOD-BEARING like _numeric → formatTypeOID(1043|1042,typmod)+"[]"; _oid bare
     "oid[]". Name-based twin (~133) already had 1015/1028; added nothing there.
Files: internal/catalog/codec.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/expr.go, internal/executor/pg18_user_catalog_rows_test.go
(+3 rows), internal/testport/pgdump_connsetup_test.go (arr fixture +label/labels/
code/codes/oids + asserts), docs/design/0110-0001-pg-dump-tap-port.md.
Gates: gofmt clean; build ./... ok; catalog PASS; TestUserPGAttributeArrayColumn
PASS; TestPort_PgDumpConnectionSetup PASS (1.8s, no downstream logf under -v →
ExitCode==0 assert block ran); pgbench CI-parity via pre-commit hook.

=== NEXT STEP — DU-002 slice 69 ===
All SIMPLE scalar-OID-backed array types are now done (int2/int4/int8/text/bool/
numeric/float8/date/timestamp/float4/time/timestamptz/uuid/bytea/varchar/bpchar/
oid). Remaining pg_dump column-type gaps are LARGER slices (defer to dedicated):
  - IDENTITY columns (GENERATED ... AS IDENTITY) + SEQUENCE objects (relkind 'S').
  - ENUM types (CREATE TYPE ... AS ENUM), composite types, DOMAIN types.
  - json/jsonb/inet/cidr/macaddr scalar columns if any still fall back to text.
Pick the smallest next gap; verify scalar round-trips before the array form.
ALWAYS: add fixture col(s), run TestPort_PgDumpConnectionSetup -v, confirm NO
downstream logf prints (proves the ExitCode==0 assert block ran).
NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop).
NOTE: WSL cwd hazard — a `cd postgres` in a Bash compound persists; use abs paths.
