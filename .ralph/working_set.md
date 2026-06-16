Task: DU-002 slice 110 — domain over `timestamp with time zone` round-trips
through pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: new
  `case catalog.OIDTimestampTZ: castType = "timestamp with time zone"`.
- internal/testport/pgdump_connsetup_test.go — fixture domain tstz_in; column
  tstzi; domainDefs assertions (UTC `+00` canonical literals).
- docs/design/0110-0001-pg-dump-tap-port.md — slice 110 section.
- .ralph/fix_plan.md — loop #73 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDTimestampTZ (1184),
buildUserPGTypeRowForDomain, format_type.

Findings: timestamptz was a slice-108 "excluded" base type only because PG's
output function re-renders the stored instant in the session TimeZone GUC (same
domain under Asia/Tokyo dumps `+09` literals). goopg's deparse is TZ-independent
(emits verbatim stored CheckInValues literals — no output fn / conversion), so
byte-identity holds once the fixture pins the UTC `+00` canonical form and the
oracle is run under a UTC session. One-arm engine change; all other timestamptz
plumbing (TypeNameToOID/userTypeAttrsForOID/pgTypeCategoryForOID/format_type for
OID 1184) was already present from timestamptz-column work.

Gates run: go build ./... OK; catalog/parser unit PASS; executor domain/enum
unit PASS; TestPort_PgDumpConnectionSetup PASS (2.23s); pgbench pre-commit smoke
on commit.

Next step: slice 111 candidates — the two remaining excluded base types:
`tsvector`/`tsquery` (need the output-function lexeme normalize+quote so stored
literals match dumped form), or internal `"char"` (needs parser quote-state
preservation to disambiguate from bpchar in the base-OID resolution). OR move to
composite (`CREATE TYPE AS (...)`) / range domain base types. ADD fixture, RUN
real pg_dump under a fixed TZ, let it report the real blocker.
