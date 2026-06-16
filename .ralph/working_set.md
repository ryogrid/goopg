Task: DU-002 slice 108 — interval/money domain `CHECK (VALUE IN ...)` survive
pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: 2 new bare
  string-with-cast cases OIDInterval/OIDMoney (castType = "interval"/"money",
  no coerce, no quoted cast). Doc table + canonical-only note + slice list.
- internal/testport/pgdump_connsetup_test.go — fixtures iv_in/mny_in; columns
  ivi/mnyi on public.dom; domainDefs assertions.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 108 section.
- .ralph/fix_plan.md — loop #71 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDInterval(1186)/OIDMoney(790).

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du108, cluster removed):
  interval → VALUE = ANY (ARRAY['1 day'::interval, '02:00:00'::interval,
             '1 year 2 mons'::interval])
  money    → VALUE = ANY (ARRAY['$1.00'::money, '$2.50'::money])
Both have native eq operators → bare string-with-cast. Byte identity holds ONLY
for canonical inputs (interval normalizes '2 hours'→'02:00:00'; money depends on
lc_monetary, C→'$1.00') — same canonical-only contract as jsonb scalars.
Re-probed + EXCLUDED: tsvector/tsquery (lexemes single-quoted, 'cat'→'''cat''');
timestamptz (session-tz); "char" (OID 18, TypeNameToOID→bpchar, quote-state).

Gates run: go build+vet OK; catalog/parser unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.19s); pgbench pre-commit smoke on commit.

Next step: slice 109 candidates — remaining excluded base types each need
render-normalization (timestamptz session-tz, tsvector/tsquery lexeme requote)
or parser quote-state ("char"); OR move to a NEW object type (composite
CREATE TYPE AS (...) / range type / enum CHECK). ADD fixture, RUN real pg_dump,
let it report the real blocker.
