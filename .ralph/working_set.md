Task: DU-002 slice 97 — `CHECK (VALUE IN (...))` over a text DOMAIN survives pg_dump (COMPLETE, commit pending)

Files:
- internal/parser/ddl.go — IN-values CONSTRAINT branch now threads the explicit
  constraint name into stmt.CheckName (was discarded).
- internal/executor/operators_ddl.go — execCreateDomain synthesizes the deparse via
  new domainInValuesCheckExpr(baseType, vals): for text base type returns
  `VALUE = ANY (ARRAY['v'::text, ...])` (quote-doubled literals), then SetDomainCheck
  routes it through the slice-96 pg_constraint plumbing. Non-text → "" (runtime-only).
- internal/testport/pgdump_connsetup_test.go — fixtures colr (auto colr_check) +
  named_in (CONSTRAINT must_be_color); dom cols co/ni; domainDefs asserts
  `CONSTRAINT colr_check CHECK ((VALUE = ANY (ARRAY['red'::text, 'green'::text])))`.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 97 narrative.
- .ralph/fix_plan.md — consolidated slices 91–97 progress note.

Key symbols: domainInValuesCheckExpr, CreateDomainStmt.CheckInValues/CheckName,
SetDomainCheck, pg_constraint VirtualRows domain loop, pg_get_constraintdef domain branch.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du97). text domain IN deparses to
`CHECK ((VALUE = ANY (ARRAY['red'::text, 'green'::text])))`. varchar form is
`CHECK (((VALUE)::text = ANY ((ARRAY['a'::character varying, ...])::text[])))` — needs
a base-type coercion envelope, DEFERRED to slice 98.

Gates run: go build+vet OK; parser+executor+catalog unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.35s); pre-commit pgbench smoke on commit.

Next step: slice 98 candidates: (a) varchar/char IN-values coercion-envelope deparse;
(b) composite type CREATE TYPE AS (...) — third object type, dumpCompositeType;
(c) range type. ADD fixture, RUN test, let it report the real blocker.
