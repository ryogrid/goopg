Task: DU-002 slice 98 — `CHECK (VALUE IN (...))` over char/varchar domains survives
pg_dump (COMPLETE, commit pending).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr is now OID-driven
  (catalog.TypeNameToOID(baseType)) instead of string-matching "text". text→`::text`,
  bpchar→`::bpchar` (bare shape); varchar→`(VALUE)::text = ANY ((ARRAY[…])::text[])`
  coercion envelope. Non-string base types still return "".
- internal/testport/pgdump_connsetup_test.go — fixtures vc_in (varchar, auto vc_in_check),
  vc20_in (varchar(20), CONSTRAINT must_ab), ch_in (char(4), auto ch_in_check); dom cols
  vci/vc20i/chi; domainDefs asserts each exact deparse.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 98 table (text/bpchar/varchar).
- .ralph/fix_plan.md — loop #60 progress note.

Key symbols: domainInValuesCheckExpr, catalog.TypeNameToOID (OIDText/OIDBpChar/OIDVarChar),
SetDomainCheck, pg_get_constraintdef domain branch.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du98). bpchar has native eq → bare
shape like text; varchar borrows text's eq → coercion envelope. typmod never in element
cast. NOTE: CREATE DOMAIN parser only accepts single-token base names — fixtures must use
`varchar`/`char` aliases, NOT multi-word `character varying`/`character` (parseObjectName
doesn't merge them; parseColumnType does, but CREATE DOMAIN uses parseObjectName).

Gates run: go build+vet OK; executor/parser/catalog unit PASS;
TestPort_PgDumpConnectionSetup PASS (1.64s); pgbench pre-commit smoke on commit.

Next step: slice 99 candidates: (a) non-string IN-values (integer VALUE = ANY (ARRAY[1,2]));
(b) new object type — composite CREATE TYPE AS (...) (dumpCompositeType) or range type.
ADD fixture, RUN test, let it report the real blocker.
