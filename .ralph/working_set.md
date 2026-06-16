Task: DU-002 slice 92 — DOMAIN with DEFAULT survives pg_dump (COMPLETE, commit pending)

Files:
- internal/parser/ast.go — CreateDomainStmt.Default Expr field
- internal/parser/ddl.go — parseCreateDomain now parseExpr's the DEFAULT (was skipped)
- internal/catalog/catalog.go — Domain.Default Expr + DefaultBin() (formatExprForAttrdef)
- internal/executor/operators_ddl.go — execCreateDomain sets d.Default = s.Default
- internal/executor/pg18_user_catalog_rows.go — buildUserPGTypeRowForDomain emits typdefaultbin
- internal/testport/pgdump_connsetup_test.go — fixture CREATE DOMAIN public.qty AS integer
  DEFAULT 0 + dom.q column; domainDefs asserts `CREATE DOMAIN public.qty AS integer DEFAULT 0;`
- docs/design/0110-0001-pg-dump-tap-port.md — slice 92 narrative

Key symbols: parseCreateDomain (KwDefault case), Domain.DefaultBin, formatExprForAttrdef,
buildUserPGTypeRowForDomain (typdefaultbin slot), dumpDomain (pg_dump.c: typdefaultbin
branch emits ` DEFAULT <expr>` verbatim, NOT literal-quoted).

Findings: integer const deparses identically goopg/PG (`DEFAULT 0`); TEXT default gains a
`::text` cast in real PG (`'foo'::text`) that formatExprForAttrdef does NOT synthesize —
so this slice uses an integer base. Verified real pg_dump 18.3 on throwaway cluster.
parseExpr correctly stops at NOT/CHECK boundaries (tested DEFAULT 42 NOT NULL, NOT NULL
DEFAULT 7, DEFAULT 'x' CHECK(...)). TestPort_PgDumpConnectionSetup PASS (1.88s).

Gates run: build ./internal/... OK; go vet executor/catalog/parser OK;
parser+catalog unit PASS; executor Domain|Type PASS; TestPort_PgDumpConnectionSetup PASS;
pre-commit pgbench smoke runs on commit.

Next step: slice 93 — next domain/object increment. Candidates:
  (a) DOMAIN text DEFAULT with `::text` cast rendering (formatExprForAttrdef must emit cast
      to match real PG `'foo'::text`) — extends slice 92.
  (b) DOMAIN CHECK (VALUE IN (...)) — needs pg_constraint contype='c' row with contypid=
      domain OID (currently 0) + pg_get_constraintdef deparse; verify real pg_dump first.
  (c) composite type (CREATE TYPE AS (...)) — third object type.
ADD fixture object, RUN TestPort_PgDumpConnectionSetup, let it report the real blocker.
