# Single Path→Node lowering (M0145-0007)

Status: recon complete; slices 1 (lowering-locality guard), 2 (the NLI route
census) and 3 (the sublink-route census) landed. Both censuses reach the same
conclusion: this task's remaining items are CUTOVER-blocked, not lowering
refactors. Task: `.ralph/fix_plan.md`
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

### 4. The splice / re-resolution family is live — and has ONE caller

All of it is live (non-test reference counts, this recon):
`reresolveJoinByName` 28, `remapByPosMap` 16, `applyJoinTreePosMap` 10,
`remapWithBindings` 9, `layoutPosMap` 7, `remapPosMapAfterRewrite` 5,
`remapSublinkOuterRefs` 4, `spliceSearchedSpine` 3, `remapExprRefsToMHJ` 2.

Slice 3 then measured WHERE from. Tracing the callers: `spliceSearchedSpine`
and `remapSublinkOuterRefs` have zero callers outside `predp.go`, and every
live call of `layoutPosMap`/`remapByPosMap` is inside `predp.go` too — the
post-search re-resolution of the pinned semi/anti spine. The family is not
"the lowering walk's leftovers"; it is one route's machinery.

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

## Slice 3 — the sublink-route census (landed): the family is cutover-blocked

`runJoinSearchBelowPinned` (predp.go) is the LEGACY answer to pre-DP
unnesting: unnest the WHERE sublinks, run the join search on the subtree BELOW
the pinned semi/anti spine, then splice the spine back and re-resolve the
coordinates the splice moved. That splice is the family's only live caller. The
jointree pipeline replaces the route entirely — sublinks are pulled into the IR
before the search (M0145-0003), so nothing is spliced afterwards and nothing
needs re-resolving.

So the question is not how to fold the family into lowering. It is how much of
the corpus still takes the route that needs it. The census (same env gate as
the NLI one, `SUBLINKCENSUS` lines, one per sublink-planning event — subplans
and CTEs plan recursively, so the counts exceed the query count):

| arm | corpus | pinned-spine | jointree-pullup |
|---|---|---|---|
| default | TPC-DS SF0.25 plans | **207** | 0 |
| default | TPC-H SF1 acceptance arm | **16** | 0 |
| `GOOPG_JOINTREE_PIPELINE=1` | TPC-DS SF0.25 plans (EXPLAIN-only, G8) | **294** | **5** |

The knob-arm row is the actionable one: even with the jointree pipeline
selected, the pull-up handles 5 of ~299 sublink-planning events — under 2%.
That is exactly what M0145-0003 landed and ledgered: the flat
`EXISTS`/`NOT EXISTS` arm only, with `IN`/`NOT IN`, non-flat bodies and
outer-local-only correlation deferred. Every other statement falls back to the
pinned spine on BOTH arms.

**Conclusion.** The splice/re-resolution family cannot retire at M0145-0007. It
retires when M0145-0003's pull-up covers the remaining sublink shapes and the
M0145-0008 cutover deletes the legacy route — the same shape of answer slice 2
reached for `rewriteJoinsToNLI`. Attempting the fold now would mean
re-implementing the legacy route's coordinate repair inside the lowering walk,
for a route the milestone is deleting.

### An operational trap this census walked into

Running the plans channel under `GOOPG_JOINTREE_PIPELINE=1` writes a capture
into the same results directory the next DEFAULT sweep diffs against. The
sweep after this census duly reported `same=74 changed=25` — 25 shapes
"changed" because its baseline was the knob-arm capture, not because anything
in the tree moved. Diffing the two default-arm captures directly showed them
byte-identical. Any knob-arm capture must be taken on a private lane, or the
next default capture's plan channel must be read against the last DEFAULT
capture by hand.

## Correction to slice 2's cutover constraint (2026-09-21)

Slice 2 concluded that `rewriteJoinsToNLI` is the only producer of SEMI/ANTI
index-probe joins and that the cutover therefore waits on `addNLIPaths`
learning to file them, "because it files INNER/LEFT today".

**That cause was wrong.** `addNLIPaths` (joinpathsnli.go:280) declines only
`parser.JoinRight`, and its own comment states the admitted set: "mirrors
partialHashJoinTypeOK (Inner/Left/Semi/Anti)". The join type was never the
gate.

The real mechanism is what `stampSemiProbePrices`' header says from the other
side: on the DEFAULT arm the semijoin is created by the legacy unnest AFTER the
search, so there is no joinrel to file a path against, whatever `addNLIPaths`
would admit. That makes the pull-up (M0145-0003), not `addNLIPaths`, the thing
that changes it. Measured, same corpus, both arms:

| arm | search-built | rewrite-built |
|---|---|---|
| default, TPC-H SF1 | 11 INNER | 4 (2 SEMI + 2 ANTI) |
| **knob, TPC-H SF1** | 10 INNER | **1 SEMI** |

The pull-up removed three of the four — those semijoins now reach the search as
real leaf items. But the search did not replace them with SEMI/ANTI NLI paths;
it elected some other method, so search-built semi/anti NLIs remain zero on
both arms.

**The open question was narrower and different**: for a pulled-up semi joinrel,
is an NLI path generated and out-costed, or never generated? `addNLIPaths` has
gates beyond the join type — `uniq == uniqueSideOuter`, `o.RequiredOuter != 0`,
and `try_nestloop_path`'s `param_source_rels` test (joinpath.c:882-889) — any
of which could decline before a path is filed.

### Answered (2026-09-21): the paths ARE filed

`noteNLIPathGate` (nlicensus.go, same `GOOPG_NLI_CENSUS=1` gate) reports, per
semi/anti joinrel, which of `addNLIPaths`' outcomes occurred: `jointype-right`,
`outer-unusable`, `no-parameterised-inner`, `inner-rejected`, or `filed`. It is
restricted to semi/anti because the inner/left population is large, understood,
and would drown the signal.

Knob arm, TPC-H SF1:

```
1 NLIGATE jointype=semi gate=filed
1 NLIGATE jointype=anti gate=filed
```

No gate declined. The search generates an NLI path for the pulled-up semi and
anti joinrels and then **out-costs** it — `add_path` prefers a hash or merge
semijoin. So the capability is not missing; the cost comparison simply goes the
other way.

**This removes the claimed cutover blocker.** Deleting `rewriteJoinsToNLI` at
M0145-0008 does not delete the ability to build a SEMI/ANTI index-probe join;
it deletes the legacy route's OVERRIDE, which forced an NLI where the cost
model prefers another method. Whether that preference is right is a
cost-accuracy question, and a live one — `stampSemiProbePrices` exists
precisely because a rewrite-built NLI carries no path price while a search-built
one does — but it is not a capability gap, and it does not gate the cutover.

## Slice plan

| slice | scope | why this order |
|---|---|---|
| 1 | Retire item 1 by absence: pin that `translateToLayout` is lowering-local (a guard test that fails if a call site appears outside `createplan*.go`), and record the measurement. | Cheap, and it converts a retirement-list line into an enforced invariant instead of a claim. |
| 2 | **Landed.** The NLI route census (above). Result: the search has a coverage hole — SEMI/ANTI — that must be closed before 0008 may delete the rewrite. |
| 3 | **Landed as a census, not a fold.** The family's only live caller is the legacy pinned-spine route, which still plans 207 TPC-DS and 16 TPC-H sublink events on the default arm (294 vs 5 on the knob arm). Folding it into lowering would re-implement a route the milestone deletes. |
| 4 | The rest of the splice/re-resolution family — now understood to be **blocked on M0145-0003**, not on 0005: it retires with the pinned-spine route once the jointree pull-up covers `IN`/`NOT IN`, non-flat bodies and outer-local-only correlation. | The census puts the pull-up's current coverage under 2% of sublink-planning events. |
| 5 | `fillJoinHashKeys` folded into the join arm. | LAST, per item 2 — its lateness is a defence, and the defence is only unnecessary once slices 3-4 have removed the mutators. |

## Gates

Same as every M0145 slice: optimizer suite + units + `tpch-spotcheck` +
TPC-DS SF0.25 sweep + TPC-H acceptance arm, with the sweep's plan channel as
the movement evidence. A lowering change is exactly the class the row-count
anchors exist for, so a slice that moves a shape must name the statement and
check it against the PG oracle, as M0145-0006's Q24 and Q51 moves were.


## Slice-4 blocker RE-TESTED (2026-09-21, loop \#25)

The conclusion above rests on a measured ratio, so it is re-testable — and two
M0145-0003 changes landed after it was taken (the ANY arm, and the body-local-
qual fix `fef25625d` that stopped `classifyPulledQuals` refusing any body whose
WHERE carried a single-rel qual). Both increase pull-up coverage, so the
blocker was re-measured rather than carried forward on trust.

Same method, same corpus, both arms, private lanes (the trap below):

| arm | corpus | pinned-spine | jointree-pullup | previously |
|---|---|---|---|---|
| default | TPC-DS SF0.25 plans | 207 | 0 | 207 / 0 — **unchanged** |
| `GOOPG_JOINTREE_PIPELINE=1` | TPC-DS SF0.25 plans | 291 | **17** | 294 / **5** |

The default-arm row is byte-identical to the earlier one, which is the control:
every movement is knob-arm only, as the arm flag requires.

The knob-arm row moved: pull-up coverage went from 5 of 299 events (1.7%) to
17 of 308 (5.5%) — **3.4x more pulled conjuncts**. M0145-0003's landed work is
real and measurable here.

**The verdict is nevertheless unchanged.** 291 of 308 sublink-planning events
(94.5%) still take the pinned spine, so `runJoinSearchBelowPinned` and its
splice/re-resolution family remain live for almost the whole corpus and cannot
retire at M0145-0007. What changes is the number the blocker cites: it is no
longer "under 2%", it is 5.5%.

### What the remaining fallbacks actually are

The refined census separates the population the pull-up *examines* from the one
it never sees. `PULLUPCENSUS` fires 60 times on the knob arm against 308
`SUBLINKCENSUS` events, so 248 events never present a conjunct to the pull-up at
all — those are the pinned spine's own domain (subplans and CTEs plan
recursively) and no pull-up work can reach them.

Of the 60 it does examine:

| outcome | count | reading |
|---|---|---|
| `(pulled)` | **21** | succeeded |
| `any-body-leaf-(*optimizer.CTEScan)` | 15 | the CTE-scan body blocker — B-06, filed as **M0145-0009** |
| `SubqueryExpr` | 15 | scalar sublink — **correctly** declined; PG does not convert `EXPR_SUBLINK` either |
| `any-nested-sublink` | 6 | needs subplan-internal rebasing (ledgered) |
| `ExistsExpr` / `InExpr` | 2 / 1 | residual shapes |

Two things follow. First, `PULLUPCLASSIFY` now fires **zero** refusals — the
`body-qual-not-consumable` class that dominated before `fef25625d` is gone
entirely, which is independent confirmation that the fix works. Second, the
largest genuinely-convertible remainder is the CTEScan-body class (15), whose
blocker is B-06 CTE-output statistics = **M0145-0009**. That is the lever on
this ratio, and it is already filed and ordered.

### Consequence for the M0145-0008 cutover

M0145-0008's own text requires M0145-0007 ("the cutover must not retire the
stage builders while upper-rel elections still live in them"). M0145-0007 stays
blocked by this measurement, so the cutover **as written** is not selectable —
even though both of its named TIMING blockers (the semijoin regression and Q17)
are now discharged and the arm-vs-arm gap is 1.06x.

Whether the cutover's two halves could be split — flip the default now, defer
deleting the legacy pipeline until the pull-up covers more shapes — is a
scoping decision for the banner's owner, not one this loop takes.
