Task: DU-002 slice 106 — xml/oid/bit/varbit domain `CHECK (VALUE IN ...)` survive
pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: new cases
  `OIDXML` (lhsCast="text", reuses json mode), `OIDOID`
  (domainInValuesCoerced(vals,"oid")), `OIDBit` (castType=`"bit"` QUOTED),
  `OIDVarbit` (castType="bit varying"). Doc comment table + slice list extended.
  NO parser change (slice 105's VALUE::text form covers xml).
- internal/testport/pgdump_connsetup_test.go — fixtures xml_in/oid_in/bit_in/
  vbit_in; columns xmli/oidi/biti/vbiti on public.dom; domainDefs assertions.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 106 section.
- .ralph/fix_plan.md — loop #69 progress note.

Key symbols: domainInValuesCheckExpr (lhsCast + coerced + bare-cast modes),
domainInValuesCoerced, catalog.OIDXML/OIDOID/OIDBit/OIDVarbit.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du106, cluster removed):
  xml  → ((VALUE)::text = ANY (ARRAY['<a/>'::text, '<b>1</b>'::text]))
  oid  → (VALUE = ANY (ARRAY[(1)::oid, (2)::oid, (3)::oid]))
  bit  → (VALUE = ANY (ARRAY['1010'::"bit", '0101'::"bit"]))  ← cast QUOTED
  varbit → (VALUE = ANY (ARRAY['101'::bit varying, '110'::bit varying]))
xml round-trips byte-identically (verbatim text, no eq operator → lhsCast mode).
timestamptz/interval/money still excluded (session-tz / normalization / lc_monetary).

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.12s); pgbench pre-commit smoke on commit.

Next step: slice 107 candidates: (a) NEW object type — composite CREATE TYPE
AS (...) / range type / enum CHECK; or (b) the excluded base types need
render-normalization (timestamptz session-tz, interval canonical form).
ADD fixture, RUN real pg_dump, let it report the real blocker.
