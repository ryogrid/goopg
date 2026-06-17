(idle — nothing in flight)

Last landed: DU-002 slice 159 (loop #126) — a function with a VARIADIC ARRAY parameter
(`public.sum_variadic(VARIADIC arr integer[])`) round-trips through pg_dump. This was a
REAL DIVERGENCE FIX, not a clean positive: `buildFunctionArguments` (expr.go, the body of
pg_get_function_arguments which pg_dump reads) gated ALL mode prefixes behind a `showMode`
flag set only for procedures or functions with an OUT/INOUT arg. A function whose only
non-IN param was VARIADIC had showMode==false → the `VARIADIC ` prefix was silently dropped
(`sum_variadic(arr integer[])`, a non-round-tripping non-variadic function). Fix: make
OUT/INOUT/VARIADIC prefixes unconditional; keep bare `IN ` gated on showMode (preserves
TestPgGetFunctionIdentityArgumentsOutMode's "IN x integer, OUT y integer" convention).
Sibling `buildFunctionDef` (pg_get_functiondef) had the mirror gap (prefixes procedure-only)
and was fixed identically.

Files: internal/executor/expr.go (buildFunctionArguments ~11343, buildFunctionDef ~11394),
internal/testport/pgdump_connsetup_test.go (fixture after add_seven ~1639, assertion after
slice-158 ~2252), docs/design/0110-0001-pg-dump-tap-port.md (slice 159 section),
.ralph/fix_plan.md (loop #126 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPgGetFunctionIdentityArguments* PASS;
./internal/executor + ./internal/parser PASS; TestPort_PgDumpConnectionSetup PASS (2.58s,
not skipped); pgbench pre-commit smoke on commit.

Next direction (slice 160): a `TRANSFORM FOR TYPE` clause (protrftypes, currently NULL —
likely a feature gap), a non-`sql` LANGUAGE (e.g. plpgsql, absent from pg_language → langOID
0, likely a gap) body, or a function returning a composite/RECORD type. NOTE: RETURNS TABLE
maps to argmode 'o' not 't' in goopg (parser/function.go), so it would dump as OUT params +
RETURNS record, NOT RETURNS TABLE(...) — that is a divergence, treat as a feature-gap slice.
DEFAULT-arg functions also a gap: buildFunctionArguments never emits `DEFAULT <expr>`.
