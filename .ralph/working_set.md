Task: M0142-0006 — apply the SEMI/ANTI match fraction in
`estimateNLIndexJoin`. **DONE and COMMITTED** this loop (banner item 4,
M0142 sub-group), commit `bacafa91f`.

Files: `internal/optimizer/cardinality.go` (rewrote `estimateNLIndexJoin`,
added `nliSemiMatchFraction`), `internal/optimizer/cardinality_propagation_test.go`
(new `TestEstimateRowsNLIndexJoinSemiScalesByMatchFraction`),
`docs/design/0100-0149/m0142-0006-nli-semi-anti-match-fraction.md` (new,
full writeup), `docs/design/README.md` (indexed), `.ralph/fix_plan.md`
(0006 `[x]`).

Key symbols: `estimateNLIndexJoin` (cardinality.go:238), `nliSemiMatchFraction`
(new, same file), `nliInnerProbe` (plan.go:925), `resolveBaseColumn`
(joinkeyproof.go:141 — the shared resolver both sides route through),
`eqjoinselSemiCore` (cardinality.go:863, the core formula reused unchanged).

Findings: the ledger's own framing ("mirror lines 621-628 at :240") does
NOT literally work — `NestedLoopIndexJoin.Predicate` is residual-only
(`createplannl.go:418-422` strips the index-clause columns into
`is.Key`/`is.Keys` before building `Predicate` from `p.Residual`), so a
synthetic-`*Join`-wrapper fix that reads `Predicate` via
`joinEquiPairs`/`semiJoinMatchFraction` finds zero equi-pairs on the common
fully-bound probe and silently reproduces the pre-fix `EstimateRows(Outer)`
behavior — caught by hand-building a SEMI-NLI test fixture BEFORE
committing to that approach, since no pre-existing test exercised NLI
SEMI/ANTI narrowing at all (only pass-through, `TestEstimateRowsNLIndexJoinCarriesOuter`).
Correct fix sources the equi-key from `Inner.Key`/`Inner.Keys` (bound to
`Index.Columns`) instead, resolving both the outer key expression (against
`j.Outer`, any wrapper chain) and the bound index column (against `j.Inner`
directly — an `*IndexScan`/`*IndexOnlyScan` is itself a `resolveBaseColumn`
leaf) via the SAME generic resolver the `*Join` arm's stats helpers use,
avoiding their merged-left‖right coordinate assumption which does not hold
for NLI's asymmetric Outer/Inner shape.
TPC-DS SF0.25 corpus shows PASS=96/MISMATCH=0 with plan-shape
`same=99 changed=0` and `make ea-ratchet` unchanged 112->112 — no query in
the *current* corpus has its plan CHOICE gated by this estimate at SF0.25,
so this landed as a pure correctness fix with no corpus-visible shape
effect yet (not a sign the fix is inert — the new unit test proves the
estimate itself moved 1000->100/900 on a direct fixture).

In-flight: none. No server/gate process left running (verified via `ps aux`
after each gate).

Next step: per the banner, item 4 (M0141/M0142 group) is still open.
M0142-0006 is now closed. Remaining open M0142 items at the same priority:
**M0142-0005** (Memoize/probe-multiplier interlock — two independent lines
of corpus evidence now, Q72 timeout + M0142-0010's Q34/Q73 findings; sized
like an executor slice, `nl_index_join.go:127`/`joinpathsnli.go:313,498`/
`cost_funcs.go:1053` — scope it into sub-tasks before implementing, same
treatment M0140-0006 got, rather than attempting it whole in one loop),
0003c (level-6 enumeration-order parity), 0004b (still-open C2
CTE-UNION-ALL recon), 0007 (corr=0 index fallback, re-measure first per
M0138-0004 side effect), 0008 (forced-rewrite-vs-search census — note its
own scope-correction: `parser.JoinSemi` IS handled in the DP search,
M0139-0005's narrower finding is that no SEMI *path* is ever filed through
`addPath`). Also open: M0141 S2b/S3-S7 (S7 Incremental Sort blocks 14
TPC-DS queries).

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...` PASS
(full package, incl. new test); `scripts/tpch-spotcheck.sh` RESULT=PASS
(Q12=2, Q13=34); `scripts/tpcds-sf025-regression.sh sweep` PASS=96
MISMATCH=0 CKMISMATCH=0 ERROR=0 SKIP=3, plan-shape same=99 changed=0;
`make ea-ratchet` 112->112 PASS (unchanged); pre-commit hook's mandatory
pgbench smoke PASS (all 3 transaction types, 0 failed); `make
ralph-state-guard` — one self-repair (same recurring benign
stale-clean-exit-marker pattern several prior loops have noted), clean
after repair.
