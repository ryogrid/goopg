Task: DU-002 slice 105 — `json` domain `CHECK (VALUE::text IN (...))` survives
pg_dump (COMPLETE, committing).

Files:
- internal/parser/ddl.go — tryParseCheckInValues now accepts an optional
  `::<typename>` cast after VALUE (via parseTypeNameAfterCast; cast type
  discarded, deparse shape keyed on base type). First parser change since slice 99.
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: `case
  catalog.OIDJSON` + new `lhsCast` render mode → `(VALUE)::text = ANY
  (ARRAY['1'::text, '{"a": 1}'::text])` (LHS cast, array NOT re-cast). Doc table
  + slice list extended.
- internal/testport/pgdump_connsetup_test.go — fixture js_in (json), column jsi
  on public.dom; domainDefs assert (uses object value '{"a": 1}').
- docs/design/0110-0001-pg-dump-tap-port.md — slice 105 section.
- .ralph/fix_plan.md — loop #68 progress note.

Key symbols: domainInValuesCheckExpr (lhsCast mode), tryParseCheckInValues,
parseTypeNameAfterCast, catalog.OIDJSON.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du105, cluster removed):
  json → ((VALUE)::text = ANY (ARRAY['1'::text, '"hello"'::text, '{"a": 1}'::text]))
json round-trips byte-identical EVEN for objects (verbatim text, no key reorder /
whitespace norm) — strictly better than jsonb (slice 104, scalars only).
timestamptz/interval/money still excluded (session-tz / normalization / lc_monetary).

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.07s); pgbench pre-commit smoke on commit.

Next step: slice 106 candidates: (a) move to a NEW object type — composite
CREATE TYPE AS (...) / range type / enum CHECK; or (b) the excluded base types
need render-normalization (timestamptz session-tz, interval canonical form).
ADD fixture, RUN real pg_dump, let it report the real blocker.
