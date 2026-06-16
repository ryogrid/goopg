Task: DU-002 slice 96 — DOMAIN generic CHECK (VALUE <cmp>) survives pg_dump (COMPLETE, commit pending)

Files:
- internal/parser/ast.go — CreateDomainStmt gains CheckExpr + CheckName.
- internal/parser/ddl.go — parseCreateDomain now CAPTURES a generic (non-IN) CHECK
  via new parseDomainCheckExpr (uppercases the VALUE placeholder; table twin
  parseCheckExpr must NOT). Dead skipParenExpr removed.
- internal/catalog/catalog.go — Domain gains CheckExpr/CheckName/CheckOID;
  SetDomainCheck allocates the constraint OID + auto-name <domain>_check;
  pg_constraint VirtualRows emits the contype='c' row keyed on contypid=domain OID;
  AllDomains() snapshot accessor added.
- internal/executor/operators_ddl.go — execCreateDomain calls cat.SetDomainCheck.
- internal/executor/expr.go — pg_get_constraintdef renders `CHECK ((expr))` for a
  domain check (iterates AllDomains by CheckOID).
- internal/testport/pgdump_connsetup_test.go — fixtures posqty (auto-named) +
  named_chk (explicit CONSTRAINT must_be_pos); dom cols pq/nc; domainDefs asserts
  `CONSTRAINT posqty_check CHECK ((VALUE > 0))` etc.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 96 narrative.

Key symbols: parseDomainCheckExpr, CreateDomainStmt.CheckExpr/CheckName,
SetDomainCheck, Domain.CheckOID, pg_constraint VirtualRows domain loop,
pg_get_constraintdef domain branch.

Findings: verified real pg_dump 18.3 — domain check dumps inline as
`\n\tCONSTRAINT <name> CHECK ((<expr>))`. getDomainConstraints reads
`WHERE contypid=$1 AND contype IN ('c','n')`. goopg lexer lowercases the VALUE
keyword token → needed uppercase fix to match PG's deparse. The `CHECK (VALUE IN
(...))` form deparses to `VALUE = ANY (ARRAY['a'::text,...])` (ScalarArrayOpExpr) —
DEFERRED (CheckInValues still parsed, not emitted).

Gates run: go build parser/catalog/executor OK; go vet OK; parser+catalog+executor
unit PASS; TestPort_PgDumpConnectionSetup PASS (2.2s); pre-commit pgbench smoke on commit.

Next step: slice 97 candidates: (a) `CHECK (VALUE IN (...))` ANY/ARRAY deparse;
(b) composite type CREATE TYPE AS (...) — third object type, dumpCompositeType;
(c) domain COLLATE clause. ADD fixture, RUN test, let it report the real blocker.
