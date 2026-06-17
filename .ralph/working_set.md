(idle — nothing in flight)

Last landed: DU-002 slice 161 (loop #128) — a SET-RETURNING function
(`public.gen_one() RETURNS SETOF integer LANGUAGE sql AS $$ SELECT 1 $$`) round-trips
through pg_dump. CLEAN POSITIVE (no production change). pg_dump's dumpFunc reads
proretset/prorettype directly from pg_proc and renders `RETURNS SETOF <rettype>` when
proretset='t'. The plumbing was already complete: parser strips SETOF + sets
ReturnsSet=true (function.go:97); CREATE FUNCTION stores it on catalog.Routine.ReturnsSet
(operators_ddl.go:5568); validateSQLFunctionBody skips the scalar single-column check when
ReturnsSet (operators_ddl.go:5728); the runtime pg_proc view emits proretset='t' + the
SRF-default prorows='1000' (pg_proc_view.go:330/351) with prorettype=element type
(integer, OID 23). pg_dump suppresses the ROWS clause at the 1000 default, so no explicit
ROWS in the dump; the `$`-free body keeps the plain `$$` delimiter.

Files: internal/testport/pgdump_connsetup_test.go (fixture ~1681, assertion ~2308),
docs/design/0110-0001-pg-dump-tap-port.md (slice 161 section),
.ralph/fix_plan.md (loop #128 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.17s, not skipped); pgbench pre-commit smoke on commit.

Next direction (slice 162): the remaining function-attribute cells are all already wired
through the runtime pg_proc view (pg_proc_view.go), so each is likely a clean positive
like slice 161 — STRICT (proisstrict), SECURITY DEFINER (prosecdef), LEAKPROOF
(proleakproof), or COST n (procost override; pg_dump emits ` COST n` only when procost !=
language default). Verify each by adding a fixture + assertion and running
TestPort_PgDumpConnectionSetup. The likely REAL feature-gaps (treat as separate slices):
`TRANSFORM FOR TYPE` (protrftypes always NULL), a non-`sql` LANGUAGE such as `plpgsql`
(check pg_language → langNameToOIDStr), a composite/RECORD return type, and RETURNS TABLE
(maps to argmode 'o' not 't' in goopg parser/function.go — dumps as OUT params, a known
divergence).
