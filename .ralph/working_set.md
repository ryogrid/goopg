(idle — nothing in flight)

Last landed: DU-002 slice 247 (loop #13) — composite field whose type carries a
TYPMOD (`numeric(10,2)`, `varchar(8)`) now PARSES and round-trips through pg_dump.
Closed the parser gap flagged at the end of slices 245/246.

Mechanism (parser-only):
- parser.parseCreateType (internal/parser/ddl.go ~L5443): composite-field type-token
  collection now tracks parenDepth; breaks only on a TOP-LEVEL ','/')' . Inner '('++ /
  ')'-- so `numeric ( 10 , 2 )` (and `… [ ]` array suffix, slice 246) is captured intact.
- NO executor change: executor.parseCompositeFieldType already decoded the space-joined
  `"numeric ( 10 , 2 )"` form into base+atttypmod (slice 243); it was just unreachable
  via SQL.

Files: internal/parser/ddl.go, internal/parser/m0097_0017_test.go
(+TestCompositeFieldTypmodParsing), internal/testport/pgdump_connsetup_test.go
(money_amt fixture + compositeDefs assertions), docs/design/0110-0001-pg-dump-tap-port.md
(Slice 247).

Gates: gofmt clean; go build ./... clean; parser pkg tests PASS;
TestPort_PgDumpConnectionSetup PASS (3.4s, real pg_dump round-trips
`amount numeric(10,2)` / `code character varying(8)`); pgbench pre-commit smoke on commit.

Next (slice 248+): composite field whose type is a DOMAIN (slice 90 analog:
DeclaredTypeName→cat.LookupDomain), then nested-composite field, then
ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
