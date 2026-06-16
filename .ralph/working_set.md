Task: DU-002 slice 114 — domains over tsvector / tsquery round-trip pg_dump
(COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: new
  `case catalog.OIDTsvector → "tsvector"` and `case catalog.OIDTsquery →
  "tsquery"` arms (bare string-with-cast).
- internal/testport/pgdump_connsetup_test.go — fixture domains tsv_in/tsq_in;
  columns tsvi/tsqi; domainDefs assertions.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 114 section.
- .ralph/fix_plan.md — loop #78 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDTsvector (3614) /
OIDTsquery (3615), TypeNameToOID, userTypeAttrsForOID, format_type,
tryParseCheckInValues.

Findings: both FTS types have native equality (tsvector_eq/tsquery_eq) →
simplest render mode. Excluded only for output normalization (lexeme requote /
sort / dedup), NOT deparse shape — so canonical-only-fixture pattern (jsonb
scalar / interval) applies. Fixtures pin canonical lexeme forms. goopg emits
verbatim stored literals, re-escaped for SQL → byte-identical to oracle.
Doubled single quotes are SQL escaping of lexeme quotes; parser unescapes them
(confirmed by passing E2E). Verified vs real pg_dump 18.3 pg_get_constraintdef.

Gates run: go build ./... OK; catalog/parser unit PASS; executor domain unit
PASS; TestPort_PgDumpConnectionSetup PASS (2.23s); pgbench pre-commit smoke on
commit (githook).

Next step: EASY base-type track is EXHAUSTED. Only internal "char" remains
(needs parser quote-state). Next meaningful DU-002 work is STRUCTURAL — range
(int4range) / composite (CREATE TYPE AS) domain base-type families need full
catalog support (no OIDInt4Range, no int4range/CREATE TYPE AS in TypeNameToOID),
a multi-loop effort. RECOMMEND pivoting off domain-IN-values to a different
DU-002 catalog-surface slice, or committing to the structural range/composite
work as a dedicated multi-loop track.
