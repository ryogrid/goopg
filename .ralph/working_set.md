Task: M0142-0008a-3i-plumbing-c6 — LANDED and committed.
Real production population of `ctx.joinInfoList` for semiAnti links (not the
rejected §40.3 "vacuous" pattern). Corpus sweep proved it's real (a NEW
decline reason appeared), then root-caused the NEXT blocker to a precise
line via reverted throwaway instrumentation, and filed it as c7.

Files this loop:
- internal/optimizer/joinsearchseam.go: ~line 569 (right before the c5 gate),
  new loop appending each semiAnti link's non-nil `.sjinfo` into
  `ctx.joinInfoList` via `joinInfoListHas` for idempotency. This IS the c6
  deliverable — 28 lines, all comment + the 4-line append loop.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §41 —
  the c6 fix + why it differs from §40.3's rejected alternative + PG-oracle
  cross-check (`pull_up_sublinks` before `deconstruct_jointree`), the
  before/after decline-reason table, per-query isolation pinning the new
  decline to Q69, and §41.3's full root-cause of Q69's NEW blocker.
- docs/design/README.md: m0142-0008a-1 row tail extended (Python exact-
  string-replace on the c5-row anchor — same technique as last loop; do NOT
  full-file-rewrite this row, it has embedded raw newlines from older loops).
- .ralph/fix_plan.md: `-3i-plumbing-c6` flipped `[x]`; new
  `-3i-plumbing-c7` filed (split `rebaseChainQual`'s single `base` shift
  into per-operand shifts for chained semiAnti links).
- .ralph/deferral_ledger.md: new row, task-id `m0142-0008a-3i-plumbing-c6`.

Key symbols: the new population loop (`joinsearchseam.go` ~line 569, right
before `if !semiAntiLinksHaveSJInfos(...)`); `joinInfoListHas`
(`relfromjoinlist.go:408`, pointer-identity idempotency guard);
`unnest.go:4694`'s `innerKey.Index = outerWidth + params[0].SubCol.Index`
(correct for execution, root cause of c7's blocker when rebased into flat
leaf-space); `rebaseChainQual` call in `extractSearchLeaves`'s walk
(joinsearchseam.go ~line 1298, uses ONE `base` for both operands — wrong
for chained links); `semiAntiOnQualsOK` (:1737, now the live decline point
for Q69).

Findings: (1) c6's fix IS reachable and genuinely changes behavior — full-
corpus sweep: `semianti-link-no-sjinfo` declines 5→3, NEW
`semianti-on-qual` decline 0→1. Per-query isolation (EXPLAIN each of the 6
EXISTS/NOT-EXISTS corpus files individually) pinned the new decline to
**Q69** specifically (TPC-DS's only chained-multi-EXISTS query — 3 EXISTS/
NOT-EXISTS conjuncts over the same 3-relation outer); Q10/Q16/Q35/Q94 all
decline earlier (`leaf-count`/`outer-over-derived`), unrelated to this
change. (2) Root-caused Q69's new blocker via THROWAWAY debug instrumentation
(added, run, then fully `git checkout`-reverted before commit — no debug
code landed): Q69's SECOND chained link's inner-key equality resolves to
relset bits {0,3} instead of the correct {0,4} — the outer operand
(`c.c_customer_sk`) correctly lands in leaf 0, but the inner operand lands
in leaf 3 (the FIRST link's own opaque leaf) instead of leaf 4 (this
link's own opaque leaf). Cause: `rebaseChainQual(pred, base)` applies ONE
additive `base` (captured before recursing into `j.Left`) to the WHOLE
folded eq expression; `base` is correct for the outer operand (0-based from
the true original outer schema) but wrong for the inner operand
(`outerWidth + innerColIndex`-encoded, needs `base + width-contributed-by-
j.Left's-subtree` instead) whenever an earlier sibling semiAnti leaf is
already spliced into `j.Left`. (3) Full corpus-wide `jointype=semi`/`anti`
DPPATH reachability is STILL zero — c6 alone doesn't achieve it, but it
does prove the population mechanism works and hands off a precisely
diagnosed next blocker rather than a vague one.

Next step: pick up **M0142-0008a-3i-plumbing-c7** — split the
`rebaseChainQual` call (or the eq/residual construction feeding it, around
joinsearchseam.go ~line 1291-1304) so the outer-side operand(s) shift by
`base` and the inner-side operand(s) shift by the post-left-recursion
`width` value (available right after `walk(j.Left, preserved)` returns,
currently uncaptured under its own name). Likely cleanest: rebase
`j.LeftKey`/`j.RightKey` SEPARATELY before folding them into the `eq`
BinaryOp, rather than rebasing the folded `eq` as one expression. MUST NOT
regress Q78 (single-EXISTS, byte-identical since c2) — re-run BOTH the
TPC-DS SF0.25 sweep AND the full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep
after landing; the real test is a `jointype=semi`/`anti` DPPATH line
finally appearing, not just a decline-reason shift like this loop's.

Gates run this loop: `go build ./...` clean; `go test
./internal/optimizer/...` green; full-corpus `GOOPG_PGSHAPED_DP_TRACE=1`
sweep (private binary `tmp/goopg-m0142-c6-bin`, built+removed this loop) —
96/100 EXPLAINs succeeded (4 pre-existing unrelated parse gaps), 184704
DPPATH lines, 0 `jointype=semi`/`anti`, decline reasons: 3
`semianti-link-no-sjinfo` + 1 `semianti-on-qual` (was 5+0 before this
loop); per-query isolation pass on the 6 EXISTS-family files; instrumented
Q69-only run (temporary debug prints, reverted before commit — `git diff`
confirms only the real 28-line c6 fix remains in the tracked file);
`scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0, PLAN-SHAPE same=99 changed=0 vs the c5 commit (Q78 byte-identical);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — same
pre-existing `internal/parser` `GroupedJoinUnaliased` AST-drift failure as
every recent loop (`internal/optimizer` itself green). `make
ralph-state-guard` auto-repaired the same benign prior-loop clean-exit
marker seen every recent loop, then PASS. Commit's own pre-commit hook runs
the pgbench smoke.

In-flight: none. Private trace binary and sf025 server both stopped/removed
(`GOOPG_BIN=tmp/goopg-m0142-c6-bin bench/tpcds/server.sh stop sf025`, then
`rm tmp/goopg-m0142-c6-bin`).
