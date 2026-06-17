(idle — nothing in flight)

Last landed: DU-002 slice 162 (loop #129) — a function with an ARRAY RETURN type
(`public.make_arr() RETURNS integer[] LANGUAGE sql AS $$ SELECT ARRAY[1, 2, 3] $$`)
now round-trips through pg_dump. REAL DIVERGENCE FIXED (sibling-path bug).

Root cause: the parser stores an array type as the base name ("integer") + IsArray=true,
NOT "integer[]". The CREATE FUNCTION executor re-appends "[]" for ARGUMENT types
(operators_ddl.go:5510) but did NOT for the RETURN type. So catalog.Routine.ReturnType.Name
held bare "integer"; pg_proc view's typeNameToOIDStr → scalar OID 23 (not array OID 1007);
pg_dump format_type(23)="integer" → dropped the array. Fix: added retTypeName computation
(lower + "[]" when s.ReturnType.IsArray) before the catalog.Routine literal, mirroring the
argTypes path. format_type(1007) already renders "integer[]" (slice 159 proved it for args).

Files: internal/executor/operators_ddl.go (~5557 retTypeName + ReturnType.Name),
internal/testport/pgdump_connsetup_test.go (fixture ~1704, assertions ~2367),
docs/design/0110-0001-pg-dump-tap-port.md (slice 162 section),
.ralph/fix_plan.md (loop #129 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.32s, not skipped); go test ./internal/executor/ PASS; pgbench pre-commit smoke on commit.

Next direction (slice 163): the remaining function-attribute cells are GENUINE feature gaps,
not clean positives. In likely order of value:
- composite/RECORD return type (RETURNS record / a named composite type) — prorettype handling
- plpgsql LANGUAGE body round-trip (check pg_language OID join + prosrc rendering)
- TRANSFORM FOR TYPE (protrftypes always NULL — feature gap)
- RETURNS TABLE (goopg parser maps to OUT params, argmode 'o' not 't' — known divergence;
  pg_dump would render OUT params instead of RETURNS TABLE).
Note: STRICT / SECURITY DEFINER / LEAKPROOF / COST / IMMUTABLE / STABLE / VOLATILE / PARALLEL
SAFE/RESTRICTED / VARIADIC / DEFAULT / multi-statement / SETOF / ROWS / procedures + OUT/INOUT
are ALL already covered (slices 149-162). The easy positives are exhausted.
