(idle — nothing in flight)

---

**Loop #31 (this session) — COMPLETE, committed + pushed.**

Task: M0122-0004 — implement `dense_rank()` as a window function, the
last remaining gap named by the prior loop's ledger row (all other
standard PostgreSQL window functions were already implemented).

Landed: `dense_rank` joins the `row_number`/`rank` case in
`buildWindowFunc` (`internal/planner/planner.go`) and
`analyzeWindowFuncCall` (`internal/analyzer/analyzer.go`) — same
zero-arg/no-DISTINCT/no-star shape check, `int8` return type. No
catalog change needed (`pg_proc` OID 3102 `window_dense_rank` was
already seeded, just never dispatched). `internal/executor/
operators_window.go`'s `evalWindowFuncs` gains a `denseRank` counter
alongside the existing `rank`/`rowNum` locals: `rank` jumps to the
current row's 1-based position on a peer-group change; `denseRank`
just increments by 1 at the same point, so it never skips a value
after a tie (matches `window_dense_rank` in
`postgres/src/backend/utils/adt/windowfuncs.c`). Tests:
`internal/analyzer/analyzer_test.go` (`TestAnalyzeWindowRankingFunctionsAccepted`
gains a `dense_rank()` case, `TestAnalyzeWindowRankingFunctionsRejected`
gains a `dense_rank(1)` case; `TestAnalyzeWindowFunctionUnsupportedRejected`
repointed at `array_agg() OVER ()`), `internal/executor/
window_compat_test.go`'s `TestCompatWindowDenseRankPeerGroups`
(cross-checked against upstream PostgreSQL 18.3). Design:
`docs/design/0020-0001-window-parser-and-ast.md` new Follow-up
section; `docs/design/README.md` row extended; `unimplemented_feat.json`
entry annotated in place. Ledger row appended
(`.ralph/deferral_ledger.md`, 2026-07-05, M0122-0004). Committed as
`4f0b6c78` (8 files, pathspec-scoped commit).

Concurrency note: a live peer `ralph_loop.sh` tree (screen `ralph`,
loop #36→#37) was active throughout this loop and landed two of its
own commits on fully disjoint source files — `340787cb` (domain-CHECK-
renderer stale-entry closure, M0122-0005) and `5687a425`
(`pg_ts_config`/`pg_ts_config_map` OID stale-entry closure, M0122-0005)
— confirmed via `git status`/`git diff --cached` before every commit
here, no functional conflicts. The peer's `.ralph/fix_plan.md` /
`unimplemented_feat.json` staging swept up this loop's in-progress
edits to those same two shared bookkeeping files (both loops' content
verified present and correct in the resulting commit before this loop
proceeded), so this loop's own commit used an explicit pathspec
(`git commit ... -- <exact files>`) covering only the files it
exclusively owned (ledger, design docs, analyzer/planner/executor
source + tests) — never touching the peer's in-flight staged files.
One `git commit` attempt hit `index.lock` (peer's concurrent git
operation); resolved by polling until the lock cleared (~10s), then
re-verifying `git log`/`git status` fresh before retrying — did not
remove the lock file. The peer's own working_set.md carry (their loop
#37, M0122-0005 bookkeeping) is superseded by this entry; see git log
(`340787cb`, `5687a425`) for that loop's detail if needed.

Next step: M0122-0004's window-function series is now fully closed —
all 11 standard PostgreSQL window functions implemented under the
default frame. Remaining items in that bucket: ROWS/RANGE/GROUPS
frame-clause parsing/execution (largest, now has 3 real consumers to
generalize against — `evalFrameAggFuncs`/`frameEnd`/`evalNtileFuncs`),
combining named-window forms, GROUPING SETS/ROLLUP/CUBE, DEFAULT-clause
parsing, intervals. Per the peer's own carry note, M0122-0005 now has
only two open sub-items left: 1-byte `char`(OID 18) disambiguation
(parser-level: `internal/catalog/codec.go:1356` `TypeNameToOID` folds
quoted `"char"` and bare `char` together) and `pg_collation_for()`
(large — no collation tracking in v0 by design). Re-check `git status`
+ `pgrep -af ralph_loop.sh` fresh at loop start before picking either
up — multiple independent loops may still be running concurrently on
this tree.

Gates run: `go build ./...` clean; `go vet` clean on
`internal/analyzer/... internal/planner/... internal/executor/...`;
`go test ./internal/analyzer/... ./internal/planner/...
./internal/executor/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pre-commit pgbench smoke PASS (machine-enforced
hook, ~233-14400 TPS across TPC-B/update/select-only); `make
ralph-state-guard` — already consistent, no repair needed.
