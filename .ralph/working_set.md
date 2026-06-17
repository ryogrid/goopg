(idle — nothing in flight)

Last landed: DU-002 slice 151 (loop #116) — explicit `COST`/`ROWS` now
round-trip through pg_dump. Another REAL divergence (like Parallel in slice
150), not a fidelity gap: the pg_proc view derived procost from language and
prorows from ReturnsSet, and the CREATE FUNCTION parser parsed `COST n`/`ROWS n`
then DISCARDED the numeric in consumeFunctionAttribute — so `COST 50` was
silently reset to the language default (100) on dump. Threaded both end-to-end:
  - parser: CreateFunctionStmt.Cost/.Rows (new, raw literal text, ""=no clause);
    captured in the attribute switch (function.go ~213), out of consume-discard
  - catalog: Routine.Cost/.Rows (new fields)
  - executor: execCreateFunction stores s.Cost/s.Rows (operators_ddl.go ~5576)
  - view: pg_proc_view.go emits override when non-empty else language/SRF default
  - sibling deparse: buildFunctionDef (expr.go ~11447) emits COST/ROWS non-default
dumpFunc (pg_dump.c:13556 COST, 13571 ROWS): COST when procost != language
default; ROWS when proretset='t' and prorows ∉ {0,1000}. Test asserts
`public.add_four(integer) … COST 50` → `LANGUAGE sql COST 50` /
`AS $_$ SELECT $1 + 4 $_$;`. Parser unit test TestParseCreateFunctionCostRows
pins COST/ROWS capture (incl. fractional COST 0.5, combined COST 0.5 ROWS 200).

Key symbols: registerPgProcView (pg_proc_view.go procost/prorows), buildFunctionDef
(expr.go), catalog.Routine.Cost/.Rows, CreateFunctionStmt.Cost/.Rows,
consumeFunctionAttribute (function.go — cost/rows still consumed there for the
non-captured fallback paths).
Files: internal/parser/{ast,function,function_test}.go,
internal/catalog/routines.go, internal/executor/{operators_ddl,expr}.go,
internal/initdb/pg_proc_view.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: gofmt OK; go build ./internal/... OK; go vet ./internal/executor/
clean; parser+catalog+initdb tests PASS; TestPort_PgDumpConnectionSetup PASS
(2.62s, not skipped). ralph-state-guard consistent. pgbench smoke runs on commit.

Next direction (slice 152): a fresh pg_dump catalog-surface gap. Candidates:
an SRF `ROWS` *round-trip* (RETURNS SETOF int ... ROWS 5 — exercises the SETOF
return-type deparse + prorows non-default together); a SECURITY DEFINER /
LEAKPROOF function (stored end-to-end already, likely clean positive); or a
CREATE PROCEDURE (prokind='p') round-trip.
