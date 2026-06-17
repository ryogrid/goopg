(idle — nothing in flight)

Last landed: DU-002 slice 164 (loop #131) — a function returning the pseudo-type
`record` (`public.ret_rec() RETURNS record LANGUAGE sql AS $$ SELECT (1, 2) $$`)
now round-trips through pg_dump. REAL DIVERGENCE FIXED (sibling-path bug).

Root cause: typeNameToOIDStr (pg_proc_view.go) had no `record` case, so the
runtime pg_proc view resolved prorettype to "0" (InvalidOid). pg_dump's dumpFunc
builds the RETURNS clause from format_type(prorettype, NULL); format_type(0)
yields the placeholder "-", so the dump rendered `RETURNS -` (broken SQL).
Fix (one sibling path): typeNameToOIDStr adds record→2249 and record[]→2287.
The OTHER sibling — goopg's format_type (executor/expr.go) — already maps
2249→"record", so the two now agree. No executor change: body `SELECT (1, 2)`
parses as a single row-constructor column, so validateSQLFunctionBody's
one-column check accepts it. Oracle (PG 18.3): record=2249, _record=2287.

Files: internal/initdb/pg_proc_view.go (2 typeNameToOIDStr cases),
internal/initdb/pg_proc_view_test.go (TestPgProcViewRecordReturnType → 2249/2287),
internal/testport/pgdump_connsetup_test.go (fixture ~1760, assertions ~2442),
docs/design/0110-0001-pg-dump-tap-port.md (slice 164), .ralph/fix_plan.md (loop #131).
Verified: gofmt OK; go build ./internal/... OK; go vet clean;
TestPort_PgDumpConnectionSetup PASS (2.19s, not skipped); internal/initdb suite PASS;
pgbench pre-commit smoke on commit.

Next direction (slice 165): remaining function-attribute cells are GENUINE feature gaps:
- TRANSFORM FOR TYPE (protrftypes always NULL — feature gap)
- RETURNS TABLE (goopg parser maps to OUT params, argmode 'o' not 't'; known divergence —
  pg_dump would render OUT params + RETURNS record instead of RETURNS TABLE).
Covered (slices 149-164): STRICT / SECURITY DEFINER / LEAKPROOF / COST / IMMUTABLE / STABLE /
VOLATILE / PARALLEL SAFE|RESTRICTED / VARIADIC / DEFAULT / multi-statement / SETOF / ROWS /
array return / plpgsql language / record return / procedures + OUT/INOUT.
