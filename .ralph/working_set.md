Task: M0142-0008a-3i-plumbing-c12, completed as a REFUTATION this loop
(design doc §47). c12's own filed hypothesis (missing relids-subset check
in the index-path candidate generator) is disproved by exhaustive live
evidence. Filed follow-on **c13** (fix_plan.md) with a concrete, different
next instrumentation layer. No production code changed this loop.

Files this loop (all documentation/planning, zero production diff):
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §47
  (§47.1 method, §47.2 the refutation evidence, §47.3 EXPLAIN-text-is-
  unreliable-here caveat, §47.4 the `depth=0` reframing, §47.5 c13's
  concrete next step).
- .ralph/fix_plan.md: c12 marked [x] (concluded, refuted — matches the
  established c10-style convention of checking off a recon/diagnostic
  task that reaches a definitive answer without landing a fix); new task
  **M0142-0008a-3i-plumbing-c13** filed.
- .ralph/deferral_ledger.md: new row for this loop.
- internal/optimizer/{joinpathsnli,createplannl,joinsearchseam,nl_index_join}.go:
  temporary instrumentation added AND FULLY REVERTED before commit
  (`git checkout --`); `git diff --stat -- internal/optimizer/` is empty.

Key symbols this loop's instrumentation touched (all reverted, listed for
the next loop's benefit): `addNLIPaths`/`addPartialNestLoopPaths`
(joinpathsnli.go) — exhaustively traced, never build the suspected illegal
pairing. `createNestLoopIndexJoinPlan` (createplannl.go) — the DP search's
only create-plan-phase constructor of `*NestedLoopIndexJoin`; traced
twice per crashing run, both times SAFE (`outer={customer,
customer_address}`). `tryBuildNLI`/`rewriteJoinsToNLI` (nl_index_join.go)
— the OTHER, "legacy" post-search constructor of the same node type;
traced, zero successful conversions for Q69. The panic site itself:
`internal/executor/expr.go:472-478` (`*optimizer.OuterColumnRef`
evaluation, `depth=%d` = `len(ctx.OuterRows)`).

Findings: (1) Both known producers of a `*NestedLoopIndexJoin` node build
it, when they build it at all, in the ONE provably-safe shape for Q69 —
path selection/construction is NOT the defect, contrary to every theory
c9 through c12 pursued. (2) The `EXPLAIN` text's printed indentation/
widths (which the c11-filing loop read as proof of an illegal
`store_sales`-leaf-outer pairing) is NOT reliable evidence here — it may
be a display-only artifact (`nlipricesplice.go` documents an analogous,
though not identical, "stamped after the fact" display seam for
SEMI/ANTI NLI nodes). Do not re-trust it without re-deriving from a live
node-type instrument. (3) The executor's own error text says `depth=0`,
i.e. the lateral outer-row stack (`ctx.OuterRows`) is COMPLETELY EMPTY at
evaluation time — not "wrong relation in scope" but "no lateral push
happened at all". This is the sharpest, most concrete lead: either (a)
the executor never actually opens/rescans the confirmed-correct
`*Join{Lateral:true}` node (meaning the executed tree diverges from what
`createPlan` returned — some post-createPlan pass, `nlipricesplice.go`
being the leading suspect, mutated/misplaced it), or (b) it does open it
but the dispatch that's supposed to push `ctx.OuterRows` before
rescanning the inner `*IndexScan` doesn't trigger for this specific
shape (an M0134-0001-style "wrap type doesn't trigger the lateral push"
gap — see `join_lateral_stream_test.go`'s doc comment for that prior,
analogous incident).

Next step: c13 (fix_plan.md, filed this loop). Instrument the executor's
lateral-outer push/pop (near `operators_nljoin.go`'s
`nestedLoopIndexJoinOp` and the generic `bindOuter`/`lateralBindable`
dispatch) to print every push/pop of `ctx.OuterRows` alongside the Go
type of the node being opened/rescanned, then reproduce Q69's crash live
and read off which of (a)/(b) above is true. Same private-binary SF0.25
method as c9-c12 (design doc §46.1: private data-dir copy, private
binary, direct start bypassing the cgroup wrapper so env vars reach the
process, temporary `joinInfoList: ctx.joinInfoList` one-liner in
joinsearchseam.go re-applied locally to reach the search — revert before
commit either way).

Gates run this loop: `go build ./...` clean (after full instrumentation
revert). `go test ./internal/optimizer/...` PASS. No sweep/spotcheck
needed — zero production diff this loop (pure investigation +
documentation), so the usual planner-change gates are not applicable;
`make ralph-state-guard` run after this write-up, see status block.

In-flight: none. Private diagnostic binary/data/logs
(`tmp/goopg-c12-bin`, `tmp/c12-sf025-data`, `tmp/c12-server.log`,
`tmp/c12-q69-*`) all stopped/removed before this write-up (`tmp/` is
gitignored regardless). All four files' temporary instrumentation
reverted via `git checkout --` before commit; verified empty diff.
