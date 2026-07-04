(idle — nothing in flight)

---

**Loop #23 (this loop) — COMPLETE, committed + pushed (`2fa1260e`, on
top of the peer's `77a61597`).**

Task: M0122-0004 frame-consuming aggregate window functions —
`sum`/`count`/`avg`/`min`/`max` as window functions (`sum(x) OVER
(...)`, `count(*) OVER (...)`, with FILTER support). This was the
prerequisite loop #22's working_set explicitly named: frame execution
had no consumer since row_number/rank/lag/lead never consult a frame.

Files: `internal/planner/{plan.go,planner.go}`,
`internal/analyzer/{analyzer.go,analyzer_test.go}`,
`internal/executor/{operators_window.go,window_compat_test.go}`,
`docs/design/{0020-0001-window-parser-and-ast.md,README.md}`,
`unimplemented_feat.json`, `.ralph/{fix_plan.md,deferral_ledger.md}`.

Key symbols: `planner.WindowFunc` gained `Star`/`Filter`/`InputType`;
`buildWindowFunc`'s new `"sum","count","avg","min","max"` case
(planner.go) reuses `buildAggregateCall`'s output-type rules and
rejects DISTINCT/aggregate-ORDER-BY with 0A000 (real PG restriction,
see `parse_func.c`'s `transformAggregateCall`). `windowCallKey` gained
a `filter:` key component (fixes a latent collision bug — FILTER
wasn't previously part of the dedup key). Executor
(`operators_window.go`) reuses the EXISTING GROUP BY accumulator
(`aggregateOp.applyAgg`/`finishAgg` in `operators_join_agg.go`) via a
new `windowFuncToAggregateCall` adapter + bare `&aggregateOp{ctx:
o.ctx}` helper — no second aggregation implementation. New
`evalFrameAggFuncs`/`peerGroupBounds` compute peer-group boundaries
per partition (reusing rank()'s existing `samePeer`) and accumulate
cumulatively group-by-group — implements PG's *default* frame (RANGE
UNBOUNDED PRECEDING, peer-inclusive, w/ ORDER BY; whole partition w/o)
with zero ROWS/RANGE/GROUPS frame-clause parsing needed. Values
cross-checked against a scratch upstream PostgreSQL 18.3 instance
(`postgres/local_install`), not assumed.

Gates run: `go build ./...` clean; `go test ./internal/parser/...
./internal/analyzer/... ./internal/planner/... ./internal/executor/...`
PASS (-count=1); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pre-commit pgbench smoke PASS (0 failed, TPC-B ~207TPS/simple-update
~182TPS/select-only ~13.6kTPS); `make ralph-state-guard` auto-repaired
routine running/completed skew, exit 0.

Concurrency note: peer `ralph_loop.sh` was live all loop, writing
`internal/catalog/catalog.go`, `internal/executor/
{operators_ddl.go,pg18_user_catalog_rows*.go,
sys_catalog_postgres_db_mirror.go}`, `internal/initdb/
{open.go,view_ddl_recovery_test.go}`, `.ralph/progress.json`,
`docs/design/0110-0001-pg-dump-tap-port.md` — none touched. Committed
via explicit `git commit -- <11 files>`; `git show --stat HEAD`
confirmed only those 11 changed. Fetched (ahead-1/behind-0) then
pushed clean fast-forward.

Still open (recorded in ledger): ROWS/RANGE/GROUPS frame-clause
parsing/execution itself — now has a real consumer
(`evalFrameAggFuncs` could generalize `peerGroupBounds` into an
arbitrary frame-bounds function once frame clauses parse).
`first_value`/`last_value`/`nth_value`/`ntile`/`cume_dist`/
`percent_rank` as window functions remain unimplemented. Combining
forms (`OVER (win ORDER BY ...)`, named-window-based-on-named-window)
from loop #22 also remain open.

Next step: implement ROWS/RANGE/GROUPS frame-clause parsing (parser
already errors explicitly on them per M0020 step 1 — see
`internal/parser/select.go`'s frame-clause reject) + execution
(generalize `evalFrameAggFuncs`/`peerGroupBounds` in
`operators_window.go`). Or M0122-0003's `pg_stat_io`/
`track_io_timing` (architectural). Or ledger row 480's comma/LATERAL
`ctx.OuterRows` gap. **Re-check `git status` first** — the peer loop
may have new WIP by the time the next loop starts.
