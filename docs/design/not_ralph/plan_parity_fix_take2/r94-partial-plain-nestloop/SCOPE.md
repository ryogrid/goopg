# R94 SCOPE — ordinary nested-loop outer partition

R94 follows R93 report commit `a28300912` and R60's already-landed partial
nested-loop producer.

## Evidence

R93 copy-isolated the Q96 final partial path and established that its
`PathNestLoop` builds an ordinary `*optimizer.Join`, not an
`*optimizer.NestedLoopIndexJoin`. The node has no worker driving scan today,
so the upper partial-aggregate producer correctly refuses it. R60 independently
recorded the same generic Gather completion barrier: the partial-NL producer
offers paths, but the path classifier and executor do not model their worker
shape.

For the specific ordinary INNER nested loop, the execution contract is
testable. `joinOp` streams its left/outer input and materializes/replays its
unparameterized right/inner input. Giving each worker a disjoint outer scan
and its own complete inner materialization partitions the emitted joined pairs
exactly once. The inner must not receive any shared scan claim.

This reasoning does not carry to RIGHT or FULL joins: their unmatched-inner
sweep would occur in every worker and duplicate right-only rows. It also does
not establish parameterized NLI/Memoize semantics; those are separate node
shapes and remain refused.

## Authorized change

Under the existing opt-in gather/upper partial-aggregate controls only:

1. Recognize a partial `PathNestLoop` only when it is an unparameterized
   ordinary INNER join: root `RequiredOuter == 0`, exactly two children,
   outer child is a recognized partial worker shape, and inner child has
   `RequiredOuter == 0` and is not `PathMemoize`. The path walk descends the
   outer child only. All other join types and parameterized/memoized inners
   return the existing refusal marker.
2. Add a node-side approval predicate for `*Join` with
   `Algo == JoinAlgoNestedLoop` and `Type == JoinTypeInner`. It must reject
   nil children, a parameterized/NLI node, and every non-INNER join. Extend
   `drivingScan`, `stampParallelScan`, and the sort-crossing sibling to descend
   the left/outer child only under that predicate, copy-on-write as today.
3. Extend all three executor claim walkers — sequential, bitmap, and index —
   to descend `joinOp.left` only when its plan is the approved ordinary INNER
   nested loop. The right/inner operator receives no claim state and is built
   independently per worker. Keep the current hash and merge behavior intact.
4. Allow `addPartialAggSplitPath` to select an isolated constructed partial
   source only when the source path itself meets the same ordinary-NL predicate,
   has positive finite per-worker rows/cost and workers, and the constructed
   node meets every existing worker safety/no-Gather/driving-scan guard. Copy
   every prebuilt leaf structurally before normal construction; unknown node or
   expression, panic, coordinate mismatch, or failed guard declines to the
   unchanged serial path. Candidate per-worker rows/cost and workers/divisor
   must be used exactly once; the serial seed remains the total-coordinate
   comparator and prices the gathered no-split arm.

No generic path is admitted solely by `PathKind`; path and node predicates
must agree and be unit-pinned together.

## Required proof

- A duplicate-sensitive ordinary INNER nested loop with a sequential outer,
  filtered complete inner, and multiple workers returns the exact serial
  multiset. The workers claim the outer scan only; assert that the inner has
  no parallel allocator.
- Repeat the identity proof for bitmap and index/index-only outer scans.
  Reject an inner-only driving scan, RIGHT/FULL/LEFT/SEMI/ANTI, a parameterized
  inner, a Memoize inner, malformed children, nil claims, and unsafe/Gather
  sources.
- Pin path-classifier, node `drivingScan`, stamp, sort-crossing, and all three
  executor claim walkers to the same approval/refusal matrix.
- Pin isolated prebuilt copying: a source and every nested prebuilt node retain
  their pointer identity/cost/expression state after candidate construction;
  an unsupported leaf or recovered constructor panic falls back to serial.
- Pin partial rows/cost/workers/divisor propagation and the serial versus
  split/gathered-no-split tournament, including rejection of a serial-derived
  worker count differing from the candidate's workers.
- Capture Q96 natural and opt-in plan/value results plus Q9/Q41/Q91 controls.
  A plan change is reported, not forced; values must match PG.

## Non-goals and gates

R94 does not alter defaults, PostgreSQL sources, worker sizing, join order,
join/aggregate cost formulas, join-method choice, partial-path dominance,
EXPLAIN text, NLI/Memoize behavior, or RIGHT/FULL/LEFT/SEMI/ANTI nested-loop
parallelism. It does not attach a claim to an inner or force a Gather.

Before code: agent review, then `git commit -n` and push. After code: focused
tests; `go test ./internal/optimizer ./internal/executor ./internal/testutil/estimateaudit`;
matching `go vet`; whitespace; then committed-binary foreground TPC-H values,
SF0.25 values, Q96/Q9/Q41/Q91 captures, and a fresh live-PG shape census.
Commit and push an English report with exact results.
