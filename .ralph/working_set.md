Task: M0142-0008a-3i-plumbing-c2 — LANDED and committed. Grew
`leaves`/`relInfos`/a local `bindings` slice in `tryPGShapedJoinSearch` from
`nprefix` to `nprefix+len(semiAnti)`, giving each synthetic Semi/Anti RHS
leaf a real (if degenerate) `rangeBinding`/`baseRelInfo` (design doc §36
gaps 2-3 / §38 landing note).

Files this loop:
- internal/optimizer/joinsearchseam.go: the leaf-building block right after
  `partitionConjunctsForJoinPlanning` (was line 570-595). Real-leaf loop
  unchanged in effect; added a second loop over `semiAnti` filling index
  `nprefix+k`, sized via `EstimateRows(scans[nprefix+k])` (NOT
  `estimateBaseRelInfo`/`applyRelSizeFallback` — both read `binding.table`
  and silently floor a nil-table binding at 0 rows via
  `estimateTableRowsFallback`'s `tbl == nil` guard, relsize.go:571-572).
  `planJoinlistSearch`'s `joinlistProblem.bindings` now gets the grown
  `bindings` slice instead of `ctx.bindings[:nprefix]`.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §38 —
  the walk-position 1:1 correspondence proof (`extractSearchLeaves` +
  `unnestExistsExpr`'s always-wrap-the-whole-tree shape, unnest.go:491),
  the row-estimate trap finding, and the `validateJoinlistProblem` risk
  this loop weighed BEFORE coding (see Findings below) plus the empirical
  sweep result.
- docs/design/README.md: m0142-0008a-1 row appended with a closer for the
  previously-truncated §37 (`-3i-plumbing-c1`) summary plus a new §38
  (`-3i-plumbing-c2`) summary — the row's stored text literally cut off
  mid-sentence after "**`-3i-plumbing-c1`" before this loop; closed it
  rather than leaving a second truncation next to it.
- .ralph/fix_plan.md: `-3i-plumbing-c2` flipped `[x]`, landing note with
  gate results, "Next" pointer to c3.

Key symbols: `tryPGShapedJoinSearch` (`joinsearchseam.go:216`), the extended
leaf-building block (`joinsearchseam.go:569-~630` post-edit);
`extractSearchLeaves` (`:1122`, unchanged this loop — already appended
`scans`/`semiAnti` 1:1 per walk step); `unnestExistsExpr` (`unnest.go:4461`,
called from `unnest.go:491`); `EstimateRows` (`cardinality.go:43`);
`estimateBaseRelInfo`/`applyRelSizeFallback` (`cardinality.go:719`,
`relsize.go:186`) — NOT used for synthetic leaves, see Findings;
`estimateTableRowsFallback` (`relsize.go:571`); `validateJoinlistProblem`/
`leafRange` (`relfromjoinlist.go:246`, `:287`).

Findings: (1) confirmed by tracing `unnestExistsExpr`'s call site that
`scans[nprefix+k]` and `semiAnti[k]` are always the same leaf — real leaves
walk-contiguous at `[0,nprefix)`, synthetic ones at `[nprefix,nleaves)` in
`semiAnti` order, for single or nested EXISTS unnesting alike. (2) The
task's own flagged row-estimate trap was real: a naive
`rangeBinding{table:nil}` through the existing base-table estimator path
zeros out silently; fixed by using `EstimateRows` on the leaf's own
already-built subtree instead. (3) Before writing code, reasoned that
growing `prob.bindings` to `nleaves` WITHOUT also growing `jl` (that is
`-3i-plumbing-c3`, filed separately, not done this loop) should — by
`validateJoinlistProblem`'s own `jl.leafRange()==(0,len(prob.bindings))`
check — flip any search that currently reaches this point with `semiAnti`
non-empty from "runs to completion" (mishandling the semiAnti predicate but
producing SOME plan, per the b2/c1 notes on TPC-DS Q78) to "declines and
falls back to the syntactic-tree path" — NOT inert the way c1 provably was.
Verified rather than assumed: the TPC-DS SF0.25 sweep came back fully
unchanged (`PLAN-SHAPE: same=99 changed=0`, Q78 byte-identical). Likely
reading, NOT chased further (out of this task's scope): the SF0.25 corpus's
one semiAnti-reaching query doesn't actually survive to
`validateJoinlistProblem` with a nonempty `semiAnti` at all — some earlier
gate in `tryPGShapedJoinSearch` (candidates noted in §38) already declines
it, independent of this loop's change. `-3i-plumbing-c3` should re-run this
same Q78 checksum/shape comparison as its own gate rather than treating this
loop's clean sweep as proof the c2+c3 pairing works end-to-end.

Next step: pick up **M0142-0008a-3i-plumbing-c3** — build the call-site-local
extended joinlist so `validateJoinlistProblem`'s `jl.leafRange() ==
(0,len(prob.bindings))` check covers the grown `nleaves` bindings this loop
introduced (design doc §36 gap 4, §38's own "Next pickup" note;
`relfromjoinlist.go:246-276`). Depends on c2 (this loop) for the bindings
length to size against. After c3, re-run the SF0.25 sweep with a specific
eye on Q78 — c2's clean sweep does NOT yet prove the c2+c3 pairing is
correct, only that c2 alone didn't regress anything.

Gates run this loop: `go build ./...` clean; `go test
./internal/optimizer/...` PASS; `scripts/tpcds-sf025-regression.sh sweep`
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0, PLAN-SHAPE same=99 changed=0 vs
prior commit (Q78 byte-identical, 15 rows, same checksum);
`scripts/tpch-spotcheck.sh` SKIPPED (pre-existing M0142-0003k data-dir
blocker, confirmed unrelated — see CLAUDE.md); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — same pre-existing `internal/parser`
`yacc_locking_test.go` `GroupedJoinUnaliased` AST-drift failure as every
recent loop (internal/optimizer itself green). `make ralph-state-guard`
auto-repaired the same benign prior-loop clean-exit marker seen every recent
loop, then PASS. Commit's own pre-commit hook runs the pgbench smoke.

In-flight: none.
