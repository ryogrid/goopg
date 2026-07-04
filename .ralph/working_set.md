(idle — nothing in flight)

---

**Loop #22 (this loop) — COMPLETE, committed + pushed (`5ed98264`, on
top of the peer's `3e0429e9`).**

Task: M0122-0004 named windows — `WINDOW name AS (...)` clause + bare
`OVER name` reference (previously only anonymous `OVER (...)` parsed).

Files: `internal/parser/{ast.go,expr.go,select.go,window_test.go}`,
`internal/analyzer/{analyzer.go,analyzer_test.go}`,
`internal/executor/window_compat_test.go`, `docs/design/0020-0001-*.md`,
`docs/design/README.md`, `unimplemented_feat.json`,
`.ralph/{fix_plan.md,deferral_ledger.md}`.

Key symbols: `parser.SelectStmt.WindowClause`/`WindowDef.RefName`;
`parseWindowDef`/`parseWindowSpecBody`/`parseWindowClauseList`
(select.go); `analyzer.resolveNamedWindowRefs`/`resolveWindowRefsInExpr`
(mirrors `exprHasWindowFunc`'s traversal). Resolution happens entirely
in the analyzer (mutates the shared AST before planner/executor read
it) — zero planner/executor changes needed.

Gates run: `go build ./...` clean; `go test ./internal/parser/...
./internal/analyzer/... ./internal/planner/... ./internal/executor/...`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pre-commit
pgbench smoke PASS (0 failed, TPC-B ~178TPS/simple-update
~238TPS/select-only ~14.3kTPS); `make ralph-state-guard` auto-repaired
routine running/completed skew, exit 0.

Concurrency note: peer `ralph_loop.sh` (screen chain) was live all
loop, writing `internal/catalog/catalog.go`, `internal/executor/
operators_ddl.go`/`pg18_user_catalog_rows*.go`, `internal/initdb/
open.go`/`view_ddl_recovery_test.go` — none touched. Committed via
explicit `git commit -m ... -- <12 files>`; `git show --stat HEAD`
confirmed only those 12 changed. Fetched (ahead-1/behind-0) then
pushed clean fast-forward.

Still open (recorded in ledger, not next-loop-urgent): combining form
`OVER (win_name ORDER BY ...)`, a named window based on another named
window — both real upstream syntax, no current caller. Frame clauses
(ROWS/RANGE/GROUPS) remain M0020's only other gap — blocked on having
any frame-consuming window function first (only row_number/rank/lag/lead
have executor support today; sum/count/avg/min/max OVER don't exist).

Next step: M0122-0004's frame-consuming aggregate window functions
(SUM/COUNT/AVG/MIN/MAX OVER (...)) would be the natural prerequisite
before frame clauses are worth implementing — check
`internal/executor/operators_window.go`'s `evalWindowFuncs` default
case. Or M0122-0003's `pg_stat_io`/`track_io_timing` (architectural).
Or ledger row 480's comma/LATERAL `ctx.OuterRows` gap. **Re-check
`git status` first** — the peer loop may have new WIP by the time the
next loop starts.
