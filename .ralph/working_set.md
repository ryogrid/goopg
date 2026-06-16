Task: DU-002 slice 103 — `CHECK (VALUE IN (...))` over macaddr/macaddr8/cidr
domains survives pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: OIDMacaddr/
  OIDMacaddr8 join the bare string-with-cast branch; OIDCidr is special (no
  cidr-eq op → coerces both sides to inet). Generalized `coerceToText bool` →
  `coerceTo string` (varchar→text, cidr→inet share one path). Doc-comment table
  extended. NO parser change (string literals already accepted).
- internal/testport/pgdump_connsetup_test.go — fixtures mac_in (macaddr),
  mac8_in (macaddr8), cidr_in (cidr); columns maci/mac8i/cidri on public.dom;
  domainDefs asserts.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 103 section (3-type table).
- .ralph/fix_plan.md — loop #66 progress note.

Key symbols: domainInValuesCheckExpr, domainInValuesCoerced, catalog.TypeNameToOID,
tryParseCheckInValues.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du103):
  macaddr  → VALUE = ANY (ARRAY['08:00:2b:01:02:03'::macaddr, ...])
  macaddr8 → VALUE = ANY (ARRAY['08:00:2b:01:02:03:04:05'::macaddr8, ...])
  cidr     → (VALUE)::inet = ANY ((ARRAY['192.168.0.0/24'::cidr, ...])::inet[])
cidr reuses the varchar→text coercion-envelope mechanism, target type = inet.

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.06s); pgbench pre-commit smoke on commit.

Next step: slice 104 candidates: (a) json/jsonb via `VALUE::text IN (...)` —
needs tryParseCheckInValues to accept a cast on VALUE; (b) other string types
(name, money, bit/varbit, tsvector — verify each against real pg_dump); (c) move
to a new object type — composite CREATE TYPE AS (...) / range / enum CHECK.
ADD fixture, RUN test, let it report the real blocker.
