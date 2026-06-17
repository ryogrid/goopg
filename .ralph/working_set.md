(idle — nothing in flight)

Last landed: DU-002 slice 150 (loop #115) — `PARALLEL SAFE` now round-trips
through pg_dump. This was a REAL divergence, not a fidelity-only test: the
pg_proc virtual view hardcoded proparallel='u' AND the CREATE FUNCTION parser
parsed `PARALLEL safe|restricted|unsafe` then discarded it, so a
`CREATE FUNCTION … PARALLEL SAFE` was silently downgraded to unsafe on dump.
Threaded the marker end-to-end:
  - parser: CreateFunctionStmt.Parallel (new, default 'u'; captures 's'/'r'/'u')
  - catalog: Routine.Parallel (new field)
  - executor: execCreateFunction stores s.Parallel (operators_ddl.go ~5577)
  - view: pg_proc_view.go user-routine builder emits r.Parallel (''→'u')
  - sibling deparse: pg_get_functiondef (expr.go) emits PARALLEL SAFE/RESTRICTED
dumpFunc (pg_dump.c:13581) appends ` PARALLEL SAFE` inline after the LANGUAGE
line when proparallel[0] != 'u' (it's the LAST of the volatility/strict/secdef/
leakproof/cost/rows/support/parallel clause chain). Procedures keep 'u' (PG
rejects PARALLEL on CREATE PROCEDURE), so the procedure executor path
(operators_ddl.go ~6240) is intentionally unchanged.

Key symbols: registerPgProcView (pg_proc_view.go), dumpFunc (pg_dump.c:13581),
catalog.Routine.Parallel, CreateFunctionStmt.Parallel, getFunctionDef deparse
in expr.go.
Files: internal/parser/{ast,function,function_test}.go,
internal/catalog/routines.go, internal/executor/{operators_ddl,expr}.go,
internal/initdb/pg_proc_view.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: gofmt OK; go build ./internal/... OK; go vet ./internal/executor/
clean; parser+catalog+initdb tests PASS; TestPort_PgDumpConnectionSetup PASS
(2.87s, not skipped). ralph-state-guard + pgbench smoke (on commit) pending.

Next direction (slice 151): a fresh pg_dump catalog-surface gap. Candidates:
a SECURITY DEFINER / LEAKPROOF function (exercise those dumpFunc clauses —
goopg already stores r.SecurityDefiner/r.Leakproof and the view emits them, so
likely a clean positive test); a set-returning function's ROWS clause (prorows
non-default); or a CREATE PROCEDURE (prokind='p') round-trip.
