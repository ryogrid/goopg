# M0141-S2a-fix1-sweep — recon: other late/wrong width currencies

Status: recon complete (2026-09-18), no production diff (C1)

## Task

`.ralph/fix_plan.md` banner item 3, `M0141-S2a-fix1-sweep`: "Find every other
cost-function input that is a goopg-native width/byte quantity, or is
narrowed after costing (hash join, sort, material, memoize, agg,
append/gather). For each: the PG expression (`./postgres` file:line), the
goopg site, and the expected movement (named queries/categories) — S5. File
one implementation task per site with `Parent: M0141-S2a-fix1-sweep`. No
production diff (C1)."

Method: for each named category, checked (a) whether a currency-narrowing
mechanism already exists and covers the goopg site, (b) whether it does not
and a cost function genuinely reads the unnarrowed width, (c) whether the PG
source confirms the width PG uses there is already-narrow (so the goopg gap
is real) rather than PG itself using a full-row width (so there is nothing to
fix). No code changed; two `go build`/`go vet` sanity passes on the citations
only (reading, not editing).

## Already covered — not resweept

- **hash join / merge join / nested loop join** — `narrowJoinWidths`
  (`internal/optimizer/narrowcostinputs.go:258-335`), called from both
  `addPath`/`addPartialPath` funnels (`path.go:1054,1106`). R122 Slice B.
- **Gather / GatherMerge / (merge-join) Sort / Memoize** —
  `inheritNarrowedWidths` (`narrowcostinputs.go:337-374`), called from
  `gatherpaths.go:250,282`, `incrementalsortpaths.go:189`,
  `joinpathsmemoize.go:311`, `joinpathsmerge.go:513`. R121 Slice A(ii).
- **Base-rel scans** — `narrowBaseRelCostWidths`
  (`narrowcostinputs.go:204-256`), called from `relfromjoinlist.go:734`. R121
  Slice A.
- **Aggregate input width** — `aggInputWidth`'s `agg.InputTarget` wiring,
  M0141-S2a-fix1 itself (`groupingpaths.go`, `partialaggpaths.go`,
  `partialaggupper.go`) — the task this sweep follows.
- **Memoize's own cache-entry-size currency** — already filed and queued
  separately as **M0139-0007c** (banner item 3, next after this sweep and
  `M0141-S2b-6-resume`): "port `get_expr_width` for Memoize's cache-key
  width." Not re-derived here to avoid filing a duplicate.
- **Append / partial-Append** — no `PathAppend` kind exists in goopg's
  planner at all yet (`path.go`'s `PathKind` enum has no Append entry); the
  only multi-branch upper candidate today is `PathSetOp`
  (UNION/INTERSECT/EXCEPT). Partial-Append is M0140-0006a/b/c (banner item
  5), not yet built — nothing to narrow before that lands.
- **Materialize** — no `PathMaterial` kind exists either; "Materialize" hits
  in the codebase are all about NLI's inner-cache decision (which is
  `PathMemoize`, already covered above), not a standalone path.

## New findings — filed as implementation tasks

### Site A — Sort / ORDERED upper rel (`M0141-S2a-fix1-sweep-a`)

**PG expression:** `create_sort_path` (`postgres/src/backend/optimizer/util/pathnode.c:3221-3250`)
sets `pathnode->path.pathtarget = subpath->pathtarget` ("Sort doesn't
project, so use source path's pathtarget") and calls `cost_sort(...,
subpath->pathtarget->width, ...)` (`pathnode.c:3245`, `cost_sort` itself at
`costsize.c:2144`). By the time `create_ordered_paths`
(`planner.c:5308-5352`) runs, `subpath->pathtarget` has already been narrowed
by PG's query-wide target-projection machinery (`grouping_planner`'s upper
pipeline applies `apply_projection_to_path` at each stage) — PG's width input
here is currency-correct by construction, the same "already-narrow
PathTarget" fact M0141-S2a-fix1's design doc cited for `cost_agg`.

**goopg site:** `sizeUpperRelFromNode` (`internal/optimizer/upperrel.go:177-187`)
sets `rel.NCols = len(cols)` / `rel.AvgVarBytes = nodeAvgVarBytes(cols)` from
`child.Output()` — the FINISHED input Node's full row, not narrowed to
anything. `createOrderedPaths` (`internal/optimizer/upperordered.go:64-133`)
calls it, then `addOrderedPaths` → `sortPathForBounded`
(`internal/optimizer/joinpathsmerge.go:480-514`) prices the Sort via
`costSortRunWithWidth(cp, sub.Rows, pathNCols(sub), pathAvgVarBytes(sub),
limitTuples, pathWidth(sub), "sortpath")` — `pathNCols`/`pathAvgVarBytes` on
the `newPrebuiltPath` seed fall back to the unnarrowed rel `sizeUpperRelFromNode`
just stamped.

**The gap is not "no mechanism exists" — it exists but fires too late.**
`sort.InputTarget`/`InputTargetKnown` (`internal/optimizer/sort_input_target.go`,
`deriveSortInputKeep`) already computes the EXACT keep-set PG's narrow
`pathtarget` represents (sort-key columns ∪ whatever the plan chain above the
Sort reads), but it is a "B-01c Slice 1 COMPUTE-ONLY" stamp applied to the
`*Sort` NODE **after** `createPlanNode(best)` has already built it from the
already-costed winning Path (`stampSortInputTarget` calls sit at
`planner.go:1972`, `:2027`, `:2441` — all downstream of `createOrderedPaths`).
There is no `*Sort` node yet at `sortPathForBounded`'s cost-time call site, so
the stamp cannot be read there as wired today.

**Implementation shape (not done here — recon only):** derive the SAME
keep-set (`sortKeyColumnNames` ∪ "what the statement's own final SELECT list
needs", i.e. the top-level query's output target — knowable before costing,
since ORDER BY sorts have no further Node above them in `createOrderedPaths`'s
caller) at `createOrderedPaths`'s entry, and feed it into
`sizeUpperRelFromNode`'s NCols/AvgVarBytes in place of the full
`child.Output()`, mirroring `agg.InputTarget`'s "compute once ahead of
costing, consume at the existing read site" shape from fix1. A generalized
version might make `deriveSortInputKeep` itself computable before node
construction (it only needs `keys` + an "above" Node, and the SELECT list is
already known before `createOrderedPaths` runs) rather than only after.

**Expected movement:** this file's own header names TPC-H Q18's `Sort
(rows=1565307)` as "the largest sort in the suite," previously uncosted at
all pre-`cost_sort`-wiring and still carrying fix1's documented residual
`aggregation-strategy`/`sort-strategy` SHAPE-DIFF after fix1 landed
(fix1's design doc, "Resume points": "Q18's residual `aggregation-strategy`
mismatch... are candidates to re-check"). Re-measure Q18 plus any TPC-DS
query in M0141-S1's 32-query serial-shaped set with a top-level ORDER BY over
a wide join (S5 gate).

### Site B — WINDOW upper rel's internal sort costing (`M0141-S2a-fix1-sweep-b`)

**PG expression:** PG's window path is built in two steps —
`create_one_window_path` (`postgres/src/backend/optimizer/plan/planner.c:4620-4760`)
stacks a `create_sort_path` over the input (when not already sorted) BEFORE
`create_windowagg_path`, and that `create_sort_path` call prices from
`subpath->pathtarget->width` exactly as Site A (already-narrow, per
`grouping_planner`'s upper-pipeline projection). `cost_windowagg` itself
(`costsize.c:3098-3098+`) takes no width parameter at all — it only reads
`input_tuples`/`input_startup_cost`/`input_total_cost`, because PG's window
node assumes pre-sorted input and never sorts internally.

**goopg site:** goopg's `windowOp` sorts internally (`operators_window.go`
`Open`, per this file's own header comment at
`internal/optimizer/windowsetoppaths.go:170-176`: "goopg's `windowOp` drains
its child and sorts internally... so the sort is part of THIS node's price or
it is charged nowhere at all"), so `costWindow`
(`internal/optimizer/windowsetoppaths.go:206-238`) folds PG's separate
narrow-width Sort into itself and prices it with
`costSortRunWithWidth(cp, tuples, inNcols, inAvgVarBytes, -1, inWidth,
"window")` / `costIncrementalSort(...)`. The caller, `addWindowPaths`
(`windowsetoppaths.go:260-284`), supplies those three from
`cols := belowNode.Output()` — the FULL row of the node one level below this
window spec group, never narrowed — at line 274:
`len(cols), nodeAvgVarBytes(cols), nodeTupleWidth(belowNode)`.
`sizeWindowRelFromNode` (`windowsetoppaths.go:132-141`) makes the same
full-`Output()` choice for the rel's own published width.

Because goopg folds PG's two separate steps (narrow-width Sort, then
width-blind WindowAgg) into one node, this is a genuine divergence rather
than "PG is also full-width here": PG's real window-input Sort is priced
narrow and goopg's equivalent internal sort is priced wide.

**Implementation shape (not done here — recon only):** no `WindowAgg`-side
`InputTarget` stamp exists yet (unlike Sort's). Would need a fresh derivation
mirroring `sort_input_target.go`'s pattern: keep-set = PARTITION BY ∪ ORDER
BY ∪ window-function argument columns ∪ whatever the plan chain above this
window level reads (the next window spec group in the stack, or the
statement's final output target for the topmost one) — `addWindowPaths`
already loops per spec group with `below`/`belowNode` threaded, so the
"above" context is locally available at each level without a new tree walk
that Sort's `enclosingNodeScopeOf` needed.

**Expected movement:** any TPC-H/TPC-DS query with a `WindowAgg` over a wide
join whose window frame reads few columns. TPC-DS is the more likely witness
corpus (window functions are far more common there than in TPC-H); no
specific query pre-identified — S5's gate is to re-run the full TPC-DS SF0.25
serial capture and diff `shape-delta.sh` for any query whose tag set includes
`WindowAgg`/ranking-function shapes before/after.

## Declined — considered, not filed

- **DISTINCT upper rel** (`internal/optimizer/distinctpaths.go:98-112`,
  `sizeDistinctRelFromNode`) uses the same full-`Output()` pattern as Sites A
  and B, but `distinctCost`
  (`distinctpaths.go:180-194`) — the only cost function DISTINCT's own path
  feeds — charges `cpu_operator_cost`/`cpu_tuple_cost` **per row**, with no
  width or byte-size term at all (the file's own comment: "no spill arm
  exists for distinct"). Narrowing DISTINCT's rel would not change any cost
  DISTINCT itself charges; it would only matter for a wrapper ABOVE it (e.g.
  a further ORDER BY) inheriting the rel's width — which is Site A's defect
  surfacing through a second input, not an independent site. Not filed
  separately to avoid double-counting Site A's fix.
- **SETOP upper rel** (`windowsetoppaths.go:345-359`, `sizeSetOpRelFromNode`
  feeding `costSetOp`'s `numCols` at `:411-419`) looks like the same shape at
  first read, but `costSetOp`'s own comment
  (`windowsetoppaths.go:376`: "Every output column is a comparison column
  here: goopg's `computeBuffered` keys on the whole row") states the PG-real
  reason it must NOT narrow: UNION/INTERSECT/EXCEPT deduplication compares
  every output column by definition, so `numCols` here is not a currency
  defect — it is the correct quantity, full width, by the operator's own
  semantics. `AvgVarBytes` is stamped on the rel but never read by
  `costSetOp` at all. Not filed.

## What every M0137-M0143 task report must contain (per AGENT.md)

This is a recon task (C1, no production diff) — the category-movement /
shape-delta / stats-epoch reporting duties apply to the two implementation
tasks this recon files, not to this document itself; each site's report
above states its own expected-movement witness for its own future task to
measure.
