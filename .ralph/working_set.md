(idle — nothing in flight)

Last landed: DU-002 slice 160 (loop #127) — a function with a DEFAULT parameter
(`public.add_default(a integer, b integer DEFAULT 10)`) round-trips through pg_dump.
REAL DIVERGENCE FIX: `buildFunctionArguments` (expr.go, body of pg_get_function_arguments
which pg_dump reads) NEVER emitted the ` DEFAULT <expr>` clause, even though the parser
captured `a.Default` and CREATE FUNCTION stored it positionally in
`catalog.Routine.ArgDefaults`. So `add_default(a integer, b integer DEFAULT 10)` dumped as
`add_default(a integer, b integer)` — a function that rejects the one-arg call form.
Fix: append ` DEFAULT <expr>` for INPUT args only (new `argIsInput` helper: IN/INOUT/
VARIADIC yes, OUT no), gated on a new `printDefaults bool` param mirroring PG's
print_defaults flag (ruleutils.c:3420). pg_get_function_arguments passes true;
pg_get_function_identity_arguments passes false (identity drops defaults). Sibling
`buildFunctionDef` (pg_get_functiondef, also print_defaults=true upstream) got the
identical fix (sibling-paths rule).

Files: internal/executor/expr.go (buildFunctionArguments ~11325 now takes printDefaults
bool + argIsInput helper; buildFunctionDef arg loop; 2 call sites at ~7248/7266),
internal/executor/pg_get_function_identity_arguments_test.go (TestPgGetFunctionArgumentsDefault),
internal/testport/pgdump_connsetup_test.go (fixture ~1660, assertion ~2285),
docs/design/0110-0001-pg-dump-tap-port.md (slice 160 section),
.ralph/fix_plan.md (loop #127 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPgGetFunctionArgumentsDefault +
TestPgGetFunctionIdentityArguments* PASS; ./internal/executor + ./internal/parser PASS;
TestPort_PgDumpConnectionSetup PASS (2.70s, not skipped); pgbench pre-commit smoke on commit.

Next direction (slice 161): a `TRANSFORM FOR TYPE` clause (protrftypes, currently NULL —
likely a feature gap), a non-`sql` LANGUAGE (e.g. plpgsql, absent from pg_language →
langOID 0, likely a gap) body, or a function returning a composite/RECORD type. NOTE:
RETURNS TABLE maps to argmode 'o' not 't' in goopg (parser/function.go), so it dumps as OUT
params + RETURNS record, NOT RETURNS TABLE(...) — a divergence, treat as a feature-gap slice.
