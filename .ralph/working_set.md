(idle — nothing in flight)

Last landed: DU-002 slice 152 (loop #118) — set-returning functions now
round-trip their `SETOF` result type through pg_dump. Another REAL divergence
(like slices 150/151), not a fidelity gap: pg_dump builds the RETURNS clause from
pg_get_function_result(oid), which in PG (ruleutils.c) prefixes the result type
with `SETOF ` for SRFs. goopg's pg_get_function_result (expr.go ~7288) returned
the bare type name regardless of proretset, so `RETURNS SETOF integer` was
silently downgraded to scalar `RETURNS integer` on dump. The prorows/ROWS
plumbing from slice 151 already worked — only the SETOF marker on the result type
itself was dropped.

Fix (both sibling deparse paths, per sibling-paths rule):
  - pg_get_function_result (expr.go ~7288): prepend "SETOF " when r.ReturnsSet —
    this is what the external pg_dump binary consumes for the RETURNS clause.
  - buildFunctionDef / pg_get_functiondef (expr.go ~11406): emit "RETURNS SETOF …"
    when r.ReturnsSet — goopg's own deparser kept in sync.
dumpFunc (pg_dump.c:13571) then appends ` ROWS 5` since proretset='t' and
prorows ∉ {0,1000}. Test adds public.gen_series_lite(integer) RETURNS SETOF
integer … ROWS 5 and asserts `RETURNS SETOF integer` + one-line
`LANGUAGE sql ROWS 5` / `AS $_$ SELECT $1 $_$;`.

Key symbols: pg_get_function_result (expr.go case), buildFunctionDef (expr.go),
catalog.Routine.ReturnsSet, CreateFunctionStmt.ReturnsSet (parser already
captures SETOF + Rows since slice 151).
Files: internal/executor/expr.go, internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: gofmt OK; go build ./internal/... OK; go vet ./internal/executor/
clean; parser tests PASS; executor function tests PASS;
TestPort_PgDumpConnectionSetup PASS (2.44s, not skipped). ralph-state-guard
consistent. pgbench smoke runs on commit.

Next direction (slice 153): a fresh pg_dump catalog-surface gap. Candidates:
SECURITY DEFINER / LEAKPROOF function (stored end-to-end already, likely clean
positive but verifies the secdef/leakproof view columns through dumpFunc); or a
CREATE PROCEDURE (prokind='p') round-trip — exercises the no-RETURNS branch +
`CREATE PROCEDURE` keyword in dumpFunc.
