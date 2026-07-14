(idle — nothing in flight)

Last completed (commit 95d19f61): M-NIGHTLY DateStyle follow-up — `||`
(string concatenation) now honors the session `datestyle` GUC. Added a
`ctx *Context` trailing param to `evalBinary` (internal/executor/expr.go),
threaded through all call sites (evalExprSlot, evalInExpr's ANY/ALL loop,
evalFastExpr pass a live ctx; evalBinaryBatch/windowOp.inRange pass nil —
OpConcat unreachable from those; ~15 test callers pass nil). New reusable
`formatDatumDateStyle(d, ctx)` helper (next to formatTimeDatumDateStyle/
dateStyleFromCtx) dispatches KindTime through the DateStyle-aware path,
falls back to d.Format() otherwise; wired into OpConcat's `ls`/`rs`
computation. New tests internal/executor/concat_datestyle_test.go
(TestConcatHonorsDateStyle, TestConcatNilCtxDefaultsISO); non-vacuousness
confirmed via temporary revert-and-rerun (not git stash, since stashing
just expr.go alone broke the build against the updated test-file call
sites — reverted the two formatDatumDateStyle→Format() lines in place
instead). Live psql verification (port 5541, cleaned up) across
ISO/SQL/Postgres/German x MDY/DMY for both `'prefix' || date_col` and
`timestamp_col || 'suffix'`. Design doc
docs/design/0097-0151-datestyle-partial-set-merge.md "Follow-up
(2026-07-15): || (string concatenation) DateStyle-awareness" + README
index updated. Deferral ledger row appended (open, resume point below).
fix_plan.md M-NIGHTLY task appended. Gates: go build/go vet (repo-wide)
clean; go test -count=1 ./internal/executor/... PASS; tpch-spotcheck.sh
PASS (Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh
— first invocation hit the known transient 1-failed-txn TPC-B flake
(0.010%, unrelated to this change per ledger row 777's precedent), retry
PASS (0 failed, all 3 workloads) confirmed the flake before committing.
make ralph-state-guard: auto-repaired a stale running/completed mismatch
(same pattern as last loop), then OK.

Next DateStyle-adjacent slice (open per the ledger tail, not started):
`operators_join_agg.go`'s `array_to_string`/array-literal-building
FuncCall arms (~lines 1846, 1849, 1867, 1939, 1943, 1949, 3347, 3368)
call `.Format()`/`arg.Format()` per array element — audit `ctx`
reachability the same way this loop did for `evalBinary`, then swap in
`formatDatumDateStyle(elem, ctx)`. After that: `to_char`'s generic
fallback, plpgsql RAISE/string-building (plpgsql_runtime.go), EXPLAIN,
operators_analyze.go bound-rendering — then TIMESTAMPTZ's missing
session-timezone-aware conversion/offset, then pgoutput.go's DateStyle
gap (all still fully open, unchanged from prior rows).

In-flight: none. All work committed (95d19f61); tree clean of my changes.
Stray untracked/modified files present from other processes (weekly_loc.*,
analysis/perf-optimize3/runs/*, ci/logs/*.log, analysis/tpch-explain-
baseline.md, untracked postgres/) were left untouched, same as prior loop.
