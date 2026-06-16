Task: DU-002 slice 104 — `CHECK (VALUE IN (...))` over name/jsonb domains
survives pg_dump (COMPLETE, committing).

Files:
- internal/executor/operators_ddl.go — domainInValuesCheckExpr: OIDName
  (`'alice'::name`) + OIDJsonb (`'1'::jsonb`) join the bare string-with-cast
  branch (both have native eq operator → no coerceTo envelope). Doc-comment
  table + slice list extended. NO parser change.
- internal/testport/pgdump_connsetup_test.go — fixtures nm_in (name), jb_in
  (jsonb); columns nmi/jbi on public.dom; domainDefs asserts.
- docs/design/0110-0001-pg-dump-tap-port.md — slice 104 section (2-type table).
- .ralph/fix_plan.md — loop #67 progress note.

Key symbols: domainInValuesCheckExpr, domainInValuesCoerced, catalog.TypeNameToOID,
tryParseCheckInValues.

Findings: verified real pg_dump 18.3 (/tmp/pgcheck_du104, cluster removed):
  name  → VALUE = ANY (ARRAY['alice'::name, 'bob'::name])
  jsonb → VALUE = ANY (ARRAY['1'::jsonb, '"hello"'::jsonb])
jsonb byte-identity holds ONLY for canonical scalars (objects re-render w/ key
reorder + whitespace). json EXCLUDED — no eq op, CHECK must be `VALUE::text IN
(...)` (cast-on-VALUE parse shape tryParseCheckInValues can't yet capture).
money/timestamptz/interval excluded (lc_monetary / session-tz / normalization).

Gates run: go build+vet OK; parser/catalog/executor unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.02s); pgbench pre-commit smoke on commit.

Next step: slice 105 candidates: (a) json via `VALUE::text IN (...)` — the
big one, needs tryParseCheckInValues to accept a `::text` cast on the VALUE
left-hand side, then emit the `(VALUE)::text = ANY (ARRAY['1'::text,...])` shape;
(b) move to a new object type — composite CREATE TYPE AS (...) / range / enum
CHECK. ADD fixture, RUN real pg_dump, let it report the real blocker.
