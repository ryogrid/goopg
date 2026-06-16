Task: DU-002 slice 93 — text DOMAIN with string DEFAULT survives pg_dump (COMPLETE, commit pending)

Files:
- internal/catalog/catalog.go — Domain.DefaultBin() now appends `::<base>` for a
  *parser.StringConst default (PG get_const_expr cast decoration); scoped to domain
  path, formatExprForAttrdef (column attrdef) untouched.
- internal/testport/pgdump_connsetup_test.go — fixture CREATE DOMAIN public.label AS
  text DEFAULT 'n/a' + dom.lbl column; domainDefs asserts
  `CREATE DOMAIN public.label AS text DEFAULT 'n/a'::text;`; retargeted slice-90
  empty-DEFAULT negative guard from `AS text DEFAULT` to `DEFAULT;`/`DEFAULT \n`.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 93 narrative.

Key symbols: Domain.DefaultBin (StringConst cast branch), get_const_expr (PG oracle:
casts every non-self-evident Const; int4/numeric/bool bare, text/varchar cast).

Findings: pg_dump dumpDomain uses pg_get_expr(typdefaultbin) verbatim (non-literal
branch); goopg pg_get_expr is pass-through so DefaultBin string IS the emitted clause.
Verified real pg_dump 18.3 emits `'n/a'::text`. Integer defaults stay bare.
TestPort_PgDumpConnectionSetup PASS (1.6s).

Gates run: go vet catalog OK; catalog unit PASS; executor Domain|Type PASS;
TestPort_PgDumpConnectionSetup PASS; pre-commit pgbench smoke runs on commit.

Next step: slice 94 — next domain/object increment. Candidates:
  (a) DOMAIN CHECK (VALUE IN (...)) — needs pg_constraint contype='c' row with
      contypid=domain OID (currently 0) + pg_get_constraintdef deparse; verify real
      pg_dump first.
  (b) composite type (CREATE TYPE AS (...)) — third object type; dumpCompositeType.
  (c) varchar/char domain DEFAULT (cast renders `::character varying` — exercises the
      DefaultBin base-name path for a multi-word type name).
ADD fixture object, RUN TestPort_PgDumpConnectionSetup, let it report the real blocker.
