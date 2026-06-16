Task: DU-002 slice 101 — `CHECK (VALUE IN (...))` over real/double precision/
timestamp/time/uuid domains survives pg_dump (COMPLETE, commit pending).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: OIDFloat4 →
  `(N)::real`, OIDFloat8 → `(N)::double precision` (via new shared helper
  domainInValuesCoerced, which OIDInt8/bigint now also uses); OIDTimestamp/OIDTime/
  OIDUUID join the string-with-cast branch (castType = canonical multi-word name).
  Doc comment table extended. NO parser change needed (literals already accepted).
- internal/testport/pgdump_connsetup_test.go — fixtures r_in (real), f8_in (float8),
  ts_in (timestamp), tm_in (time), u_in (uuid); columns ri/f8i/tsi/tmi/ui on
  public.dom; domainDefs asserts exact deparse.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 101 section (5-type table).
- .ralph/fix_plan.md — loop #63 progress note.

Key symbols: domainInValuesCheckExpr, domainInValuesCoerced (new helper),
catalog.TypeNameToOID, tryParseCheckInValues.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du101):
  real   → VALUE = ANY (ARRAY[(1.5)::real, (2.5)::real])
  float8 → VALUE = ANY (ARRAY[(1.5)::double precision, (3.0)::double precision])
  timestamp/time/uuid → quoted-literal + bare ::canonical-name cast.
timestamptz EXCLUDED: PG re-renders the constant in session tz (+00→+09), not
byte-identical from the raw token. Single-word base aliases (real/float8/timestamp/
time/uuid) used so object-name parser accepts them; pg_dump renders canonical name.

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.12s); pgbench pre-commit smoke on commit.

Next step: slice 102 candidates: (a) move to a new object type — composite
CREATE TYPE AS (...) or range / enum CHECK; (b) numeric-typmod / interval base.
ADD fixture, RUN test, let it report the real blocker.
