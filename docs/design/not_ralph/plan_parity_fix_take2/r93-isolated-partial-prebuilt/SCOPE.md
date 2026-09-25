# R93 SCOPE — isolated materialization of a partial NLI source

R93 follows R92 report commit `5a41b0b24`.

## Evidence and precise problem

PG18.3 Q96 reaches `Partial Aggregate -> Gather -> Finalize Aggregate` over a
partial `store_sales -> household_demographics -> store` prefix. Goopg R91
selects the same prefix relation order but stays serial. R92 established that
the searched final-relset partial `PathNestLoop` is a candidate with workers,
zero `RequiredOuter`, and a worker-capable outer scan, but its path contains a
depth-three `PathPrebuilt` holding a shared `*Filter` node. Calling ordinary
`createPlanNode` on that path is unsafe: its common funnel stamps `PlanCost`
onto every returned prebuilt node, mutating the already-published serial tree.

The displayed serial child cannot be substituted: it has no driving scan. The
existing `clonePlanReplacingOuter` / `clonePlanVerbatimOrShare` helpers are
not an admissible solution. They are unnest rewrites, intentionally reject
some plan kinds and may return the original pointer on failure. An empty
replacement map does not turn that fallback into an isolated copy.

The execution side has a second, separate fact. A partial
`NestedLoopIndexJoin` partitions its `Outer` only; its parameterized `Inner`
must remain a private per-outer probe. `partialPathDrivingKind`,
`drivingScan`, `stampParallelScan`, and all three executor claim walkers
(`attachParallelScan`, `attachParallelBitmapScan`, and
`attachParallelIndexScan`) do not currently model that exact outer-only
descent. Enabling only one of them is a wrong-results risk: a worker either
rereads every outer row or partitions the probe that must be complete for each
outer row.

There is also a coordinate boundary, not a costing license. The searched
`PartialPathlist` candidate already reports per-worker `Rows` and a per-worker
`Cost`; the serial aggregate seed reports total input rows and its serial
comparator cost. The isolated partial seed must take the candidate's rows and
cost verbatim, with no `parallelSeedCost` call and no second divisor. The
total-row seed remains the source for total input cardinality, final-group
estimation, and the serial versus gathered-no-split comparator. A missing,
non-positive, non-finite, or coordinate-inconsistent candidate declines. The
split's worker count must be exactly `candidate.ParallelWorkers`, and its
divisor must be calculated from that exact worker count; it may not be
re-derived from the serial child. A mismatch or non-positive candidate worker
count declines. R93 does not invent a conversion or make the partial candidate
cheaper.

## Authorized production change

Add an optimizer-private, fail-closed isolated-materialization route used only
when `addPartialAggSplitPath` evaluates a searched partial source for the
upper partial-aggregate candidate. It must:

1. Copy the candidate `Path` recursively before construction. Each copied
   `PathPrebuilt` must hold a structurally independent node, never the source
   pointer. Do not change normal `PathPrebuilt` construction or the serial
   seed path.
2. Provide a dedicated exact plan-and-expression copier for the node kinds
   reachable from the accepted source. It must copy mutable structs, slices,
   expression trees, and nested plan-bearing expressions; it must preserve
   semantics and alias contracts such as `NestedLoopIndexJoin.InnerMemo.Child
   == Inner`. It must reject an unknown/unsupported node or expression rather
   than share it. No unnest substitution, node simplification, scan demotion,
   cost adjustment, or best-effort fallback is allowed.
3. Build the copied path through the existing `createPlanNode` funnel only
   after isolation succeeds. A construction panic or a failed copy declines
   the candidate, leaves the original serial child untouched, and must not
   turn an ordinary non-trace query into an error.
4. Use the partial candidate's per-worker `Rows` and `Cost` exactly once for
   the isolated partial seed. Keep the original total-coordinate serial seed
   for all total-row and serial-comparator terms. Do not divide either
   candidate coordinate again, back-compute a serial candidate cost, or use
   an isolated source to price the gathered no-split arm. Use exactly the
   candidate's positive `ParallelWorkers` and the divisor derived from it;
   decline rather than mix it with `upperSplitWorkers` from the serial child.
5. Use the isolated built node as the partial seed only if all existing upper
   safety checks pass: statement capability, no unsafe node, no Gather,
   positive workers/divisor, no unresolved binding, and one driving scan.
   The serial seed and its costing remain the comparator; do not add, remove,
   or reprice unrelated paths.
6. Add a narrowly approved `NestedLoopIndexJoin` arm to the optimizer's three
   worker-spine decisions (path shape classifier, `drivingScan`, and
   `stampParallelScan`) and to every executor claim walker
   (`attachParallelScan`, `attachParallelBitmapScan`, and
   `attachParallelIndexScan`). Every arm descends the NLI `Outer` only and
   declines unless the node/path is an INNER or LEFT NLI with a legal
   parameterized inner probe. It must never descend into, stamp, or attach a
   parallel allocator to the inner probe. Update the corresponding
   sort-crossing walk if and only if it follows the same spine.

Keep the route behind the existing opt-in upper partial-aggregate / gather
path controls. The default plan remains unchanged.

## Required focused proof

Tests must establish all of the following.

- Isolated construction does not alter the source node, source `Path`, or any
  nested prebuilt node's cost, and it does not alias mutable node/slice/expression
  state back to the source.
- Unsupported node or expression, clone failure, and recovered constructor
  panic decline only the partial source and retain serial planning/execution.
- A representative Filter -> NLI -> partial outer scan source builds an
  upper `Partial Aggregate -> Gather -> Finalize Aggregate` shape only under
  the opt-in controls. Its values equal a serial execution exactly, including
  duplicate-sensitive aggregate inputs and a parameterized inner index probe.
- The executor attaches worker allocation to the NLI outer scan and never to
  its inner probe for sequential, bitmap, and index/index-only outer scans.
  INNER and LEFT behavior, NLI residual handling, Memoize aliasing, and all
  existing non-NLI partial paths remain covered.
- The partial candidate's per-worker rows/cost are propagated once, while the
  serial seed remains the total-coordinate comparator. Pin a tournament in
  which a partial candidate competes honestly against the unchanged serial
  and gathered-no-split candidates; a second division or a fabricated serial
  conversion must fail the test. Pin that a different serial-derived worker
  count/divisor is rejected and that the accepted split uses the candidate's
  workers and divisor.
- Every optimizer/executor worker-spine decision agrees for accepted and
  rejected NLI shapes; non-legal join type, missing worker-capable outer scan,
  parameterized root, unsafe subtree, inner-only scan, and each mismatched
  seq/bitmap/index claim-walker combination all fail closed.
- Q96 with opt-in controls is captured alongside Q9/Q41/Q91 controls. Its
  values must match PG; plan movement is reported, not forced into a parity
  claim.

## Non-goals and gates

R93 does not change PostgreSQL sources, defaults, worker-count arithmetic,
cost formulas, hash geometry, join order, join method selection, aggregate
split eligibility, Gather cost, partial-path dominance, or EXPLAIN rendering.
It does not make a source executable by fabricating a scan, unwrap a Gather
outside existing rules, or reuse an unisolated prebuilt node.

Before implementation: agent review, revise as needed, then commit with
`git commit -n` and push. After implementation: focused optimizer/executor
tests, `go test ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit`,
matching `go vet`, and `git diff --check`; build a committed binary and run
foreground TPC-H 24-query values, SF0.25 96-query values, Q96 natural and
opt-in plan/value capture, Q9/Q41/Q91 trace controls, and a fresh live-PG
shape census. Commit and push an English report with exact verdicts.
