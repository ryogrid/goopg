Task: DU-002 slice 102 — `CHECK (VALUE IN (...))` over smallint/bytea/inet
domains survives pg_dump (COMPLETE, committed).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: OIDInt2 joins the
  verbatim integer branch (small int consts const-fold to int2, no cast);
  OIDBytea → `'\x…'::bytea`, OIDInet → `'…'::inet` join the string-with-cast
  branch. Doc-comment table extended. NO parser change (literals already accepted).
- internal/testport/pgdump_connsetup_test.go — fixtures si_in (smallint), by_in
  (bytea), inet_in (inet); columns sii/byi/ineti on public.dom; domainDefs asserts.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 102 section (3-type table).
- .ralph/fix_plan.md — loop #64 progress note.

Key symbols: domainInValuesCheckExpr, domainInValuesCoerced, catalog.TypeNameToOID,
tryParseCheckInValues.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du102):
  smallint → VALUE = ANY (ARRAY[10, 20, 30])      (verbatim, no cast)
  bytea    → VALUE = ANY (ARRAY['\xdeadbeef'::bytea, '\xcafe'::bytea])
  inet     → VALUE = ANY (ARRAY['192.168.0.1'::inet, '10.0.0.0/8'::inet])
EXCLUDED: interval (PG normalizes '2 hours'→'02:00:00', not byte-identical from raw
token). DEFERRED: json/jsonb (no eq op → CHECK is `VALUE::text IN (...)`, a different
parse shape than `VALUE IN (...)`; tryParseCheckInValues only handles bare VALUE).

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.05s); pgbench pre-commit smoke on commit.

Next step: slice 103 candidates: (a) json/jsonb via `VALUE::text IN (...)` —
needs tryParseCheckInValues to accept a cast on VALUE; (b) move to a new object
type — composite CREATE TYPE AS (...) / range / enum CHECK. ADD fixture, RUN test,
let it report the real blocker.
