Task: DU-002 slice 100 — `CHECK (VALUE IN (...))` over bigint/boolean/date domains
survives pg_dump (COMPLETE, commit pending).

Files:
- internal/parser/ddl.go — tryParseCheckInValues now also accepts boolean keyword
  literals (KwTrue/KwFalse), stored canonical-lowercase. String/int/numeric already
  accepted. Switch rewritten from `switch p.cur().Kind` to tagless `switch {}`.
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: OIDBool joins the
  verbatim ARRAY branch (with OIDInt4/OIDNumeric); OIDInt8 gets per-element
  `(N)::bigint` wrap; OIDDate joins the string-with-cast branch (castType="date").
  Doc comment table updated.
- internal/testport/pgdump_connsetup_test.go — fixtures b_in (bigint), bo_in (boolean),
  d_in (date); columns bi/boi/di on public.dom; domainDefs asserts exact deparse.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 100 section (3-type table).
- .ralph/fix_plan.md — loop #62 progress note.

Key symbols: domainInValuesCheckExpr (OIDInt8/OIDBool/OIDDate branches),
catalog.TypeNameToOID, tryParseCheckInValues, SetDomainCheck.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du100):
  bigint  → VALUE = ANY (ARRAY[(100)::bigint, (200)::bigint, (300)::bigint])
  boolean → VALUE = ANY (ARRAY[true, false])
  date    → VALUE = ANY (ARRAY['2020-01-01'::date, '2021-06-15'::date])
bigint's IN-list int4 literals are coerced per element; boolean keyword literals and
int/numeric literals render verbatim; date is quoted-literal + bare ::date cast.

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (1.94s); pgbench pre-commit smoke on commit.

Next step: slice 101 candidates: (a) more base types (timestamp/uuid/float — same two
shapes); (b) new object type — composite CREATE TYPE AS (...) or range. ADD fixture,
RUN test, let it report the real blocker.
