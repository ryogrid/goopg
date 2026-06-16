Task: DU-002 slice 99 — `CHECK (VALUE IN (...))` over integer/numeric domains survives
pg_dump (COMPLETE, commit pending).

Files:
- internal/parser/ddl.go — tryParseCheckInValues now accepts TokenIntLit/TokenNumericLit
  (not just TokenStringLit); stores raw token text. Before this, numeric IN-lists fell
  through to skipParenExpr → NO constraint captured at all.
- internal/executor/operators_ddl.go — domainInValuesCheckExpr gains OIDInt4/OIDNumeric
  branch: emits literals verbatim (no quotes, no per-element cast) →
  `VALUE = ANY (ARRAY[1, 2, 3])`. bigint still returns "" (needs `(N)::bigint` wrap).
- internal/testport/pgdump_connsetup_test.go — fixtures i_in (integer, auto i_in_check),
  i_in_n (integer, CONSTRAINT must_set), n_in (numeric(10,2), auto n_in_check); dom cols
  ii/iin/ni2; domainDefs asserts each exact deparse.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 99 section (integer/numeric table).
- .ralph/fix_plan.md — loop #61 progress note.

Key symbols: domainInValuesCheckExpr, catalog.TypeNameToOID (OIDInt4/OIDNumeric),
tryParseCheckInValues, SetDomainCheck, expr.go domain CHECK runtime validation
(string-compare, needed no change).

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du99). integer→bare `1,2,3`;
numeric→bare `1.5,2.5`; bigint→`(100)::bigint, (200)::bigint` (int4 literals coerced).
Runtime membership uses result.StringValue() EqualFold so numeric values compare fine.

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (1.91s); pgbench pre-commit smoke on commit.

Next step: slice 100 candidates: (a) bigint IN-values (`(N)::bigint` per-element wrap);
(b) boolean/date IN-values; (c) new object type — composite CREATE TYPE AS (...) or range.
ADD fixture, RUN test, let it report the real blocker.
