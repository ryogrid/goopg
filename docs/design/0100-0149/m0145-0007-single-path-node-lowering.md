# Single Path→Node lowering (M0145-0007)

Status: recon complete; slices 1 (lowering-locality guard) and 2 (the NLI
route census) landed. Task: `.ralph/fix_plan.md`
M0145-0007. Parent: M0145-0005 (the DP that produces the path tree),
M0145-0006 (the upper rels that extend it). Kind: impl.

## What the task is

PG keeps `Path`s alive through base rels, join search and every upper rel, then
converts ONCE in `create_plan` (`postgres/src/backend/optimizer/plan/createplan.c`).
goopg's `createPlanNode` (`createplan.go:44`) is already that funnel, so the
task is not to build a lowering pass — it is to make the lowering the ONLY
place output-layout-dependent state is established, and retire the post-passes
that re-establish it afterwards. M0145-0001 §5.2 assigns every state item an
owner; the four items it marks for 0007 are the subject here.

## Recon — the four items, measured

### 1. `translateToLayout` is already confined to lowering

0001's retirement list names "`translateToLayout` as separate calls". Measured:
every call site is already inside a `createplan*.go` file — 4 in
`createplanjoin.go`, 3 in `createplannl.go`, 2 in `createplansimple.go`, 1 in
`createplangather.go`, 0 anywhere else. The mechanism is therefore NOT what
needs retiring; what needs retiring is the OTHER coordinate machinery that runs
outside the walk. This item is retired by absence, like M0145-0005's
`outer-on-qual` family.

### 2. `fillJoinHashKeys` must retire LAST, not first

It looks like the easiest item — one post-pass, folded into the join arm — and
it is the most dangerous one to move. Its own header states why it is late:
`Predicate` is mutated after join construction by `reresolveJoinByName`'s
predRebind, the bushy sub-remap, constant folding, `lowerSubPlanParams` and the
qual-placement passes, and **a field filled early is a field every one of those
passes must be taught to maintain**. The one time that was not done, the shared
`ColumnRef` pointer was mutated under the keys and chained NATURAL JOINs probed
the wrong column (M0097-0060).

So the dependency is the reverse of the intuition: folding the keys into
lowering is only safe once the mutating passes are gone, i.e. AFTER the
qual-placement family (M0145-0005's clause distribution) and the re-resolution
family below. Sequencing it first would re-open a known wrong-answer class.

### 3. `rewriteJoinsToNLI` + `stampSemiProbePrices` are legacy-arm-only already

Both are node passes at `planner.go:1781`/`:1789` (with a second, idempotent
stamp at `:2609`). Measured: `walkRewriteNLI` returns at `isSearchedTree(n)`
(P5.9-b) — the PG-shaped search already chose the join method, NLI included
(`joinpathsnli.go`), and re-deciding it here would both override a costed
choice and rebuild keys in coordinates a searched join does not use.
`stampSemiProbePrices` "mirrors `walkRewriteNLI`'s descent exactly, so it
stamps precisely the rewrite-built population".

Consequence for the plan: these two do not need to be FOLDED into lowering at
all. They serve the population the search never built, so they retire when that
population does. Whether that retirement is SAFE was the open question, and
slice 2 measured it — see below. The answer is no, not yet.

### 4. The splice / re-resolution family is the real work

All of it is live (non-test reference counts, this recon):
`reresolveJoinByName` 28, `remapByPosMap` 16, `applyJoinTreePosMap` 10,
`remapWithBindings` 9, `layoutPosMap` 7, `remapPosMapAfterRewrite` 5,
`remapSublinkOuterRefs` 4, `spliceSearchedSpine` 3, `remapExprRefsToMHJ` 2.
Nothing here is dead; each is a second establishment of coordinates the
lowering walk could have established once.

## Slice 2 — the NLI route census (landed): the rewrite is NOT dead

`nlicensus.go` counts, per construction site, which route built each
`*NestedLoopIndexJoin`: the elected-path arms in `createplannl.go`
(`route=search`) or `tryBuildNLI` inside `rewriteJoinsToNLI`
(`route=rewrite`). EXPLAIN prints the same node for both, so the counter is the
only way to attribute one. It is off unless `GOOPG_NLI_CENSUS=1` (registered in
`flagProvenanceExempt`: diagnostic only, it cannot change which node is built)
and emits one stderr line per node, carrying the route, the join type and the
probed index — the index name is what makes a single fire attributable.

### TPC-DS SF0.25, 99 queries

| route | nodes | join types |
|---|---|---|
| search | 143 | all INNER (100 probe `date_dim_pkey`, then `store_pkey` 8, `warehouse_pkey` 6, `customer_pkey` 6, …) |
| rewrite | 1 | INNER, probing `date_dim_pkey` |

### TPC-H SF1, the 22-query acceptance arm

| route | nodes | join types |
|---|---|---|
| search | 11 | all INNER |
| rewrite | 4 | **2 SEMI + 2 ANTI** (`idx_lineitem_orderkey_fkidx` ×3, `order_customer_fkidx`) |

### What this settles

The rewrite is not vestigial, and the shape of its population is the point:
on TPC-H every node it builds is a SEMI or ANTI join, and the search builds
none of those. That is the same fact `stampSemiProbePrices`' header already
states from the other side — "the search never sees the unnested relset, so the
node the rewrite builds carries no price" — now measured as a count rather than
inferred.

**Hard constraint for M0145-0008**: deleting `rewriteJoinsToNLI` with the legacy
pipeline would delete the ONLY route that builds a SEMI/ANTI index-probe join,
and those statements would fall back to a hash or plain nested loop. The
cutover therefore depends on the join search electing SEMI/ANTI NLI paths
first — `addNLIPaths` files INNER/LEFT today — which is a pathgen task, not a
lowering one. Ledgered with that as the resume point.

The INNER fire on TPC-DS is a single node and its attribution is still open:
`cmd_plans` captures all 99 queries regardless of `QUERIES`, so the obvious
bisect measures the same full run every time. Attribution needs either the
`sweep` channel (which does honour `QUERIES`) or an outer-relation field added
to the census line.

## Slice plan

| slice | scope | why this order |
|---|---|---|
| 1 | Retire item 1 by absence: pin that `translateToLayout` is lowering-local (a guard test that fails if a call site appears outside `createplan*.go`), and record the measurement. | Cheap, and it converts a retirement-list line into an enforced invariant instead of a claim. |
| 2 | **Landed.** The NLI route census (above). Result: the search has a coverage hole — SEMI/ANTI — that must be closed before 0008 may delete the rewrite. |
| 3 | `OuterColumnRef` → outer-layout mapping (`remapOuterRefsInSubplan`) into lowering: each lowered node publishes its binding-space→`outputLayout` map, subplan OCRs translate once. | The narrowest member of the re-resolution family with a self-contained contract. |
| 4 | The rest of the splice/re-resolution family, once the DP's clause distribution (0005) has removed the passes that mutate `Predicate`. | Everything here depends on nothing else re-writing the tree afterwards. |
| 5 | `fillJoinHashKeys` folded into the join arm. | LAST, per item 2 — its lateness is a defence, and the defence is only unnecessary once slices 3-4 have removed the mutators. |

## Gates

Same as every M0145 slice: optimizer suite + units + `tpch-spotcheck` +
TPC-DS SF0.25 sweep + TPC-H acceptance arm, with the sweep's plan channel as
the movement evidence. A lowering change is exactly the class the row-count
anchors exist for, so a slice that moves a shape must name the statement and
check it against the PG oracle, as M0145-0006's Q24 and Q51 moves were.
