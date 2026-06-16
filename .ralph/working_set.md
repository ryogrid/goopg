Task: DU-002 slice 113 — domains over int2vector / oidvector round-trip pg_dump
(COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: new
  `case catalog.OIDInt2vector → "int2vector"` and `case catalog.OIDOidvector →
  "oidvector"` arms.
- internal/testport/pgdump_connsetup_test.go — fixture domains i2v_in/ovec_in;
  columns i2vi/oveci; domainDefs assertions ('1 2'/'3 4'::int2vector|oidvector).
- docs/design/0110-0001-pg-dump-tap-port.md — slice 113 section.
- .ralph/fix_plan.md — loop #76 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDInt2vector (22) /
OIDOidvector (30), TypeNameToOID, userTypeAttrsForOID, format_type.

Findings: both vector types have native equality (int2vectoreq/oidvectoreq) →
simplest render mode (bare string-with-cast). Canonical space-separated form
('1 2') round-trips verbatim. All plumbing already present from vector-column
work (slice 81). Verified byte-identical to real pg_dump 18.3 (temp cluster).

Gates run: go build ./... OK; catalog/parser unit PASS; executor domain unit
PASS; TestPort_PgDumpConnectionSetup PASS (2.13s); pgbench pre-commit smoke on
commit (githook).

Next step: EASY single-arm domain base types are EXHAUSTED. Slice 114+ must
choose between (a) tsvector/tsquery — output functions normalize+requote
lexemes, needs real engine work to match deparse; (b) internal "char" — parser
loses quoted-ident disambiguation from bpchar; (c) STRUCTURAL: range
(int4range) / composite (CREATE TYPE AS) base-type families — need full catalog
support (no OIDInt4Range, no int4range in TypeNameToOID). Recommend pivoting off
the domain-IN-values sub-track to a different DU-002 catalog-surface slice, or
committing to the structural range/composite work as a multi-loop effort.
