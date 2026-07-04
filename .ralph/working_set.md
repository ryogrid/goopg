(idle — nothing in flight)

---

**Loop #39 (this session) — COMPLETE, committed + pushed (`f2fb90db`).**

Task: M0122-0004 — implement real `GROUP BY GROUPING SETS / ROLLUP / CUBE`
semantics (previously silently downgraded to a plain GROUP BY via an
`IntegerConst(0)` sentinel, per the fix_plan's "still open" list and a
confirmed-open `unimplemented_feat.json` entry, `task_id: M0097-regress`).

Landed: parser (`internal/parser/select.go`'s rewritten
`parseGroupByElems` + `parseGroupingUnitList`/`parseGroupingSetsList`/
`rollupAlternatives`/`cubeAlternatives`/`cartesianProductGroupingSets`)
expands ROLLUP/CUBE/explicit GROUPING SETS into `SelectStmt.GroupingSets
*GroupingSetsSpec` (`internal/parser/ast.go`), a materialized `[][]Expr`
set list cross-multiplied against plain GROUP BY columns per SQL:1999
§7.9. Planner's `rewriteGroupingSets` (`internal/planner/planner.go`,
hooked into `planSelect` right after the indirection-star rewrite, before
the CTE preplan / `s.SetOp != nil` check) expands this into a synthetic
UNION ALL chain of plain-GROUP-BY branches — falls straight through into
the pre-existing N-ary set-op planning machinery (segment flattening,
`wrapSetOpBranchWithCasts`, `wrapSetOpSortLimit`) completely unmodified.
`substituteGroupingExpr` NULLs out excluded-dimension references per
branch (recursing `BinaryOp`/`UnaryOp`/`IsNullExpr`/`IsBoolExpr`/
`IsDistinctFromExpr`/`CollateExpr`/`CastExpr`/`RowExpr`/`CaseExpr`/
non-aggregate `FuncCall`) and resolves the new `GROUPING(...)`
pseudo-function (dedicated `*parser.GroupingCall` AST node, analyzer-typed
`int4`) to a literal bitmask per branch. No executor change needed.
Removed the now-dead `IntegerConst{Value:0}` sentinel-skip in
`buildAggregateStage` (a literal `GROUP BY 0` now correctly 42P10s).
Tests: `internal/parser/select_test.go` (5 parse-shape tests),
`internal/executor/grouping_sets_compat_test.go` (4 end-to-end
ROLLUP/CUBE/GROUPING-SETS/GROUPING() tests). Design:
`docs/design/0122-0004-grouping-sets-rollup-cube.md`;
`docs/design/README.md` new row; `unimplemented_feat.json` entry updated
in place (`status: resolved`); `.ralph/deferral_ledger.md` row appended
(2026-07-05) for the substitution walker's known-narrow gaps (doesn't
cover every `parser.Expr` variant — `InExpr`/`ExistsExpr`/array exprs —
or window-function `.Over.PartitionBy`/`.OrderBy`). Committed as
`f2fb90db` (12 files, pathspec-scoped to stay disjoint from the peer's
in-flight `internal/executor/pgstat_io.go`/`pgstat_io_test.go`/
`internal/storage/bufpool.go`/`bufpool_counters_test.go`/
`internal/initdb/open.go`/`docs/design/0122-0003-explain-format-xml-yaml.md`),
pushed to `origin/align-data-structure-with-pg`.

Concurrency note: a live peer `ralph_loop.sh` tree was active throughout
(confirmed via `pgrep -af ralph_loop.sh` at loop start — multiple
independent loop processes). The peer landed its own M0122-0003
`write_time` work as commit `13725c89` (pushed) *during* this loop —
confirmed via `git status`/`git diff --stat` polling before staging; this
loop's `git add`/`git commit` used an explicit pathspec covering only the
12 grouping-sets files, so `13725c89` simply became this commit's parent
(shared working tree, no rebase needed) and none of the peer's source
files were ever staged here. `.ralph/fix_plan.md`/`.ralph/deferral_ledger.md`/
`docs/design/README.md` (shared bookkeeping files both loops append to)
picked up peer content in between reads — committed anyway per
established precedent (loop #38's own note): these are
continuously-appended shared logs, not source files; whoever commits next
captures the current merged state, nothing is lost. This file
(`working_set.md`) itself hit a stale-read conflict from the peer's own
loop #33 completion note mid-write — resolved by re-reading before this
overwrite (their detail is preserved in their own commit `13725c89` +
design doc + ledger row, not lost).

Next step: remaining M0122-0004 open items — frame clause parsing/
execution (ROWS/RANGE/GROUPS — `evalFrameAggFuncs`/`frameEnd`/
`evalNtileFuncs` have three real consumers to generalize against),
combining named-window forms, and intervals (timestamp-timestamp
arithmetic, sub-day units — `internal/parser/expr.go`'s `IntervalLit`
only supports day/month/year). M0122-0003's remaining sub-items per the
peer's own note: `extend_time`, `EXPLAIN (BUFFERS)` without ANALYZE,
local/temp-buffer terms, 3 remaining `pg_stat_io` op counters
(reuses/writebacks/fsyncs), EXPLAIN's `I/O Timings` line, a
`CTEDMLPrefix` residual. M0122-0005 has two open sub-items: 1-byte
`char`(OID 18) disambiguation (`internal/catalog/codec.go:1356`
`TypeNameToOID`) and `pg_collation_for()` (large — no collation tracking
in v0 by design). Re-check `git status` + `pgrep -af ralph_loop.sh` fresh
at loop start — multiple independent loops are still running
concurrently on this tree.

Gates run: `go build ./...` clean; `go vet ./internal/parser/...
./internal/analyzer/... ./internal/planner/... ./internal/executor/...`
clean; `go test ./internal/parser/... ./internal/analyzer/...
./internal/planner/... ./internal/executor/...` PASS (no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33, elapsed 26.92s/95.96s);
pre-commit pgbench smoke PASS (machine-enforced hook: 191/172/12450 TPS
across TPC-B/update/select-only); `make ralph-state-guard` run next
(before final status block).
