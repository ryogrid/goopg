# R38 — narrow the width the COST MODEL sees (K65)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Cost-computation axis. Implements K65, the largest single divergence
found in this session.*

## 1. The measurement

TPC-H Q12 reads one column from `orders` and four from `lineitem`.

| | goopg | PG |
|---|---|---|
| `orders` width | **448** | 22 |
| `lineitem` width | **550** | 17 |

At the capture's `work_mem=64MB`, hashing 1.5M `orders` rows costs

- goopg: 1,500,000 x 448 B = **641 MB** -> multi-batch -> ~200,000 of
  spill I/O
- PG: 1,500,000 x 22 B = **31 MB** -> single batch -> no spill

That ~200,000 is the entire gap between the two hash-join orientations
goopg costed (328,627 vs 534,007, both instrumented), and it is why
goopg refuses PG's build side. `hashJoinCost` itself is sound; it is
handed a build side 20-32x too wide.

## 2. Root cause: narrowing happens AFTER costing

goopg is not missing the machinery. `narrowoutput.go` has
`narrowBuildInput` / `narrowMergeInput`, on by default
(`GOOPG_NARROW_BUILD`), driven by a real keep-set derivation
(`joinKeepSet`, Take2 P4-01 Slice 3), and `searchCtx` already carries
`neededCols` / `outputCols` — "PG's `reltarget` + `attr_needed` for this
statement".

What is wrong is WHEN. `narrowBuildInput`'s call site is `joinInputsFor`,
"immediately after `createPlanNode(innerPath)`" — plan CONSTRUCTION,
which runs after the path search has already costed every candidate and
chosen. So the executed plan may be narrow while every cost that
selected it was computed on full-width rows.

PG does the opposite. `set_rel_width` (costsize.c:6300-6351) sums the
widths of `reltarget`'s expressions — the needed columns only — and
stores the result in `rel->reltarget->width` **before** any path is
costed, so hash geometry, spill batching, Gather transfer and sort
footprints all see the narrow number.

This is the same shape as three earlier findings this session
(`EstimateRows` vs `calcJoinrelSize`; EXPLAIN's rendered scan cost vs
the search's; the reliability gate): **goopg computes the right answer
in a place the decision cannot see.**

## 3. Change

Set `RelOptInfo.Width` from the needed-column set at rel construction,
so every cost function reads a PG-faithful width:

- **base rels** — width = sum of the needed columns' widths, from the
  same `neededCols` the index-only path already consumes, instead of
  the full tuple width.
- **join rels** — PG unions its children's reltargets; goopg's
  `joinPublishesInner` already states which side's columns a join
  emits, so the union runs over the already-narrowed child widths.

Fall back to the full width wherever the keep-set is unknown
(`NeededColsKnown == false`), which is the existing decline and
preserves today's numbers exactly. Fail-closed: an unknown set must
never be read as "keep nothing".

`narrowBuildInput` stays as-is for now. It narrows the executed node;
this round narrows the COSTED width. They should eventually share one
derivation, and §6 files that.

## 4. Prediction (stated before measuring)

- Q12: `orders` build footprint falls from 641 MB to roughly PG's
  31 MB, the spill term disappears, and the two orientations' costs
  reorder. Whether goopg then picks PG's build side is the round's
  question — the spill term is 200,000 of a 205,380 gap, so it should,
  but "should" is not a measurement.
- **join-method** (TPC-DS 80, TPC-H 13) is the primary target.
- **parallelism** (88) and **sort-strategy** (84) may move too: width
  feeds Gather transfer cost and sort footprints.
- Widths are NOT normalised away by the parity metric the way estimates
  are — N1 strips `rows=`/`cost=`, and `width=` rides in the same
  parenthesis, so this must be checked rather than assumed. If width is
  also stripped, the round is judged by shape changes alone.
- `match` is NOT predicted to move.

## 5. Gates

Suites; TPC-H values digest byte-identical; SF0.5 sweep all-zero;
parity BOTH corpora on the fresh clone with `shape-delta.sh` counts
reported alongside the categories (K50).

Runtime may move in either direction. Per the goal's standing
instruction that is not a regression if the plan moves toward PG's.

## 6. Risks, and what must be verified rather than assumed

1. **Row-shape panics.** goopg has layout/schema panic guards between
   node schemas and layouts. Changing a COSTED width must not change
   any node's actual schema — this round touches `RelOptInfo.Width`
   only. If any consumer derives a schema from `Width`, that is a real
   coupling and must be found before landing, not after.
2. **Executor geometry.** `hashsize.Choose` is shared by planner and
   executor. If the executor sizes its table from the planner's width,
   a narrower planner width could under-size a table that still carries
   full-width rows at runtime — the exact hazard `narrowBuildInput`'s
   ordering currently avoids. This must be checked first; it is the one
   finding that could invalidate the whole design.
3. **Double-narrowing.** With both this and `narrowBuildInput` active,
   confirm the width is not narrowed twice.
4. The `pathNCols` / `pathAvgVarBytes` accessors feed `hashJoinCost`;
   whether they read the rel's width or the node's schema decides where
   the fix must land. Establish this by instrumentation, per this
   session's twice-learned lesson: **instrument the term, never infer
   it from the sum.**
