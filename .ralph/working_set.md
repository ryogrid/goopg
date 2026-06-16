Task: DU-002 slice 111 — domain over `time with time zone` (timetz) round-trips
through pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: new
  `case catalog.OIDTimeTZ: castType = "time with time zone"` arm.
- internal/testport/pgdump_connsetup_test.go — fixture domain ttz_in; column
  ttzi; domainDefs assertions (canonical '12:30:00+09'/'23:59:59-05' literals).
- docs/design/0110-0001-pg-dump-tap-port.md — slice 111 section.
- .ralph/fix_plan.md — loop #74 progress note.

Key symbols: domainInValuesCheckExpr, catalog.OIDTimeTZ (1266),
userTypeAttrsForOID, format_type.

Findings: timetz is lower-risk than timestamptz (slice 110) because timetz_out
preserves the stored zone offset verbatim — it does NOT rotate into the session
TimeZone GUC (unlike timestamptztypoutput). So byte-identity is unconditional
for already-canonical literals; no UTC-session requirement. Verified against
real pg_dump 18.3 (pg_get_constraintdef + '…'::timetz round-trip). All plumbing
besides the one switch arm was already present from timetz-column slice 83.

Gates run: go build ./... OK; catalog/parser unit PASS; executor domain unit
PASS; TestPort_PgDumpConnectionSetup PASS (2.24s); pgbench pre-commit smoke on
commit (githook).

Next step: slice 112 candidates — the two remaining slice-108 excluded base
types: `tsvector`/`tsquery` (need output-fn lexeme normalize+quote so stored
literals match dumped form) and internal `"char"` (parser quote-state
preservation to disambiguate from bpchar in base-OID resolution). OR move to a
composite (`CREATE TYPE AS (...)`) or range domain base type. ADD fixture, RUN
real pg_dump under a fixed TZ, let it report the real blocker.
