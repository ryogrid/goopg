# M0122-0012 C33 — a read set for index-scan heap fetches

Status: landed 2026-10-28 as C34, loop #218, as an executor-side pass (see "Landed: C34" at the end). Designed in loop #215; the plumbing was revised in loop #216, and the first attempt was dropped in loop #217. Area: `internal/executor` (Build-time deform analysis, `indexScanOp`).
Perf-only: plans, values and EXPLAIN output unchanged. Parent: M0122-0012 (banner item 10). Target, written in the
M0122-0012 entry in loop #214: TPC-DS SF0.25 Q14 instructions:u, bar −3 %, predicted ~4 %.

## Symptom

An index scan's heap fetch decodes every column in `[0, deformBound)`: `decodeRowRangeInfo` with a `colInfo` that
has no `skip` flags. Measured with a debug build on the top TPC-DS queries:

| query | scan | decoded / table width | columns actually read |
|---|---|---|---|
| Q14 | store_sales (NLI inner) | 13 / 23 | ~4 (sold_date_sk, item_sk, quantity, list_price) |
| Q14 | item | 12 or 22 / 22 | ~4 |
| Q72 | date_dim | 28 / 28 | 2–3 |
| Q72 | item, customer_demographics, warehouse | full | 1–2 |

Decode under index scans is 6.8 % of Q14's instructions (2.0 % of Q72's).

A seq scan skips unread columns inside its window: slice 19 (`markProjectScanReads` → `seqScanOp.readCols` →
`colInfo[i].skip`) walks past them with `skipAttrLen` instead of converting them. Index scans have no such read set,
because the deform analysis threads an **integer** bound (`deformFoldRefs` = highest referenced column + 1) and an
index scan under a nested loop, join or projection never sees which columns its consumers read.

## PG

PG's projection fetches attributes up to the highest one referenced (`EEOP_SCAN_FETCHSOME` up to `last_scan`,
`ExecBuildProjectionInfo` / `ExecCreateExprSetupSteps`, execExpr.c). Deforming an attribute it does not use costs
PG almost nothing: by-value datums are copied, and by-reference values (numeric, text) are left as pointers.
goopg converts every decoded value eagerly (numeric to int64 mantissa, text into the arena), so each column inside
the window costs real work. Skipping the unread ones compensates for that eager conversion. It changes no result,
exactly as slice 19 does for seq scans.

## Design: a Build-time read-set pass

The existing integer bound and every `deformBoundBelow` arm stay as they are. One new pass computes read sets for
index scans only:

- `indexScanReadSets(root optimizer.Node) map[*optimizer.IndexScan][]bool` runs once per Build, before
  `buildNode`.
- It walks the plan top-down carrying `need []bool` in the current node's output coordinates. `nil` means every
  column. The root needs every output column.
- Each node maps `need` to its children's coordinates and adds the columns its own expressions read
  (`consumerLiveColumns` / `deformScanReadSet`, whose unknown shapes return nil = all):
  - **Project:** the union of the live targets' references; non-identity targets map by their own refs.
  - **Filter, Sort, IncrementalSort, Limit, LockRows, Result:** `need` ∪ own expressions.
  - **Aggregate:** a fresh set from the group expressions, arms and passthrough, because the reshape boundary
    drops the incoming set.
  - **Join and NestedLoopIndexJoin:** split `need` by the left/outer width. The predicate, residual and keys
    split the same way (`deformSplitPredicate`'s index-space rule). The NLI inner also adds its index condition's
    and `Cond`'s columns.
  - **Memoize, Materialize, SubqueryScan, Gather, GatherMerge:** pass through, plus GatherMerge's keys.
  - **Every other node** (WindowAgg, SetOp, CTE, Distinct, DML, Append, …) needs every column below it.
- At an `IndexScan` leaf, the recorded set is `need` ∪ the leaf's own `Cond` ∪ its index key columns (recheck).
  It is in table-column coordinates, because an IndexScan's output is the table row.

**Consumption.** The two IndexScan build arms (`buildNode`'s IndexScan case and the NestedLoopIndexJoin inner)
look up the set. They mark `colInfo[i].skip = true` for each column `i < deformBound` outside it, after
`resolveColTypeInfo` in `openPrep`, mirroring the seq scan. Skipped cells decode to `NullDatum`, so a consumer that
was wrongly omitted reads NULL. Under the debug poison flag that read panics, the existing EX1-01 tripwire.

**Declines, so the set falls back to every column:**

- a `ctid` or whole-row reference anywhere above (`exprTreeUsesCTID`, row expressions);
- FOR UPDATE/SHARE (LockRows re-fetches by ctid);
- an index scan whose rows feed a node the pass does not model.

## Slices

1. **The Target slice.** The pass for the node kinds on Q14's and Q72's paths (Project, Filter, Sort, Limit,
   Aggregate, Join, NLI, Memoize, Gather, GatherMerge, SubqueryScan), consumed by `indexScanOp`. Target: Q14
   instructions:u, predicted ~4 %. Also measure Q72 (~1–2 % predicted).
2. Bitmap-heap-scan and index-only-scan heap fetches through the same sets.
3. Replace `markProjectScanReads` (slice 19) with the same pass, so seq scans under joins get read sets too.

## Tests

- `TestIndexScanReadSetMatchesFullDecode` runs queries with the pass on and off (a knob) and requires identical
  rows. Cases:
  - NLI inner index probes reading a few of many columns;
  - a residual predicate on an inner column;
  - a projection above a join;
  - aggregates over the inner;
  - ORDER BY on an inner column that is otherwise unread;
  - `ctid` and whole-row references (must decline);
  - LIMIT;
  - a numeric column between read columns.
- A debug-poison run of the same queries proves no omitted column is read.
- Gates: units, spotcheck, acceptance, sf025 sweep with plans, fire-set, RegressSuite, all as for any executor
  decode change.

## Risks

- **A missed consumer would make a column read NULL silently.** The poison tripwire, the on/off equivalence test
  and the TPC-H/TPC-DS value gates cover it. The pass must fall back to "all columns" on anything it does not
  understand.
- **Build cost.** One extra walk per statement, small next to planning.

## Plumbing (decided in loop #216)

The read set cannot ride the existing Build recursion without changing its signature. `buildNode(plan, bound,
scope)` has about 39 call sites, and the sibling slab builder (`BuildFast` / `buildRec`) would need the same change
(hard-won rule 2). Worker trees are built later, in their own goroutines, through `BuildWorker` and the Gather
`buildChild` closures. Package-level state is not an option, because concurrent sessions build concurrently.
Writing the set onto a cached plan node from the executor at Build would race between sessions sharing the plan.

**Decision.** Stamp the read set at the tail of `Plan()`, the way `fillJoinHashKeys` publishes `Join.HashKeys`:

- `fillIndexScanReadCols(node)` runs after `fillJoinHashKeys`, once every rewriting pass (`lowerSubPlanParams`,
  `applyUpperNarrowing`, the SubqueryScan clean-up) has finished.
- It writes `IndexScan.ReadCols []bool` in table-column coordinates, or nil meaning every column.
- The plan is read-only from then on, so cached plans share it without races.
- Every executor build path reads the field: `buildNode`'s IndexScan arm and NLI inner arm, `buildRec`, and the
  worker builds. Each sets `colInfo[i].skip` for columns `i < deformBound` outside it, in `indexScanOp.openPrep`.

**The pass's expression walker** must be conservative: a `ColumnRef` contributes its index, and any expression kind
it does not recognise returns "every column" for that node's coordinates. That includes `OuterColumnRef`, sublinks,
`CTIDExpr`, row and whole-row references, and function calls whose arguments it cannot see. The executor's
`deformScanReadSet` already behaves this way, so the optimizer walker should be checked against it in a unit test
over the same expression corpus.

Because `internal/optimizer` changes, the S9 census re-runs after the commit. The pass is plan-neutral: it only adds
a field that EXPLAIN never prints.

## Measured and dropped: the NestedLoopIndexJoin slice (loop #217)

The executor's existing consumer threading (`buildCompleteChild`) carries consumer expressions down from
consumer-complete roots, so the first slice was built on it rather than on the Plan()-tail pass:

- `buildCompleteChild` got a `NestedLoopIndexJoin` case.
- The consumers and the join predicate are split at the outer width. The inner read set is their inner columns
  plus the probe's `Cond`.
- The outer is built consumer-complete. Its consumers are the outer columns referenced by the consumers, the
  predicate, the probe's key expressions and the Memoize keys.
- Any expression the deform visitor cannot enumerate declines the case.
- The read set became `indexScanOp.readCols`, applied as `colInfo.skip`.

An on/off/poison equivalence test passed; 2 of its 7 queries armed.

**Measured:** Q14 128.744 → 128.701 G (−0.03 %). Q72, Q17, Q25 and Q29 did not move.

**Why it missed.** Q14's index probes are the inner IndexScans of lateral or parameterised `Join`s
(`lateralJoinStream`), not `NestedLoopIndexJoin`s, and two-table index nested loops plan as `Join` too.
`joinLeftConsumers` declines lateral joins, so no consumer chain reaches those probes. The code was correct but
inert, and it was reverted.

**What is left.** Thread consumers through lateral joins:

- The right read set is the consumers' right-side columns, plus the predicate's, plus the inner `Cond`'s own
  columns.
- The left consumers additionally include every outer column the inner reads through `OuterColumnRef{Level: 1}`
  (its keys and `Cond`).

This needs a visitor that understands `OuterColumnRef`; `deformLateralOuterRefs` is today's integer-bound twin.
That path, or the Plan()-tail pass above, is the next attempt. Both are design-sized.

## Landed: C34, an executor-side pass (loop #218)

**Upper bound first.** Before building, a throwaway binary skipped columns by table name (same binary on and off;
Q14 output identical). Skipping every unread index-scan column took Q14 from 129.0 to 123.95 Ginstr/q (−3.9 %).

| skipped | change |
|---|---|
| item alone | −2.6 % |
| date\_dim alone | −0.9 % |
| the fact tables | ±0 |
| item with an integer bound of 12 | ±0 |

The item volume is the CTE `cross_items`: about 750 k `item_pkey` probes under HashSetOp Intersect → Gather →
lateral Nested Loop. Three things kept a read set from reaching those probes:

- `buildCompleteChild` stops at SetOp;
- the Gather worker build does not receive the consumer set;
- the lateral join declines.

The integer bound already stops at column 12, so the gain lies in a set.

**Plumbing, revised.** Neither the Plan()-tail stamp nor a new build parameter was needed. The pass runs in the
executor and is published per execution on `Context`:

- `computeIndexReadSets(plan)` (`index_read_sets.go`) runs once per `OpIterator` in `OpIterator.Open`.
- It sets `ctx.indexReadSets`, a `map[*optimizer.IndexScan][]bool`.
- `OpIterator.Close` restores the caller's value, so a nested statement leaves the outer statement's sets in place.
- `NewWorkerContext` copies the map into worker contexts. It is read-only once published.
- `indexScanOp.openPrep` applies the set as `colInfo.skip` (`applyIndexReadSet`) after `resolveColTypeInfo`, which
  returns a fresh slice per Open.
- Every build path (`buildNode`, `BuildFast`, the worker builds) reaches the same `openPrep`, so none of them changed.
- A missing entry, including the extended-protocol path that builds through `Build`, means full decode. A lookup
  can therefore only ever narrow a scan whose plan root computed the set.

**The walk** carries the set of output columns a parent may read (nil means every column) and mirrors the integer
pass's arms (`deformBoundBelow`, `deformJoinBounds`):

| node | set passed to the child |
|---|---|
| Project | the columns read by the targets the parent reads, plus every target `projectTargetSkippable` refuses (those are evaluated anyway); IsolatedScope means every column |
| Filter, Sort, IncrementalSort, Limit, GatherMerge | need ∪ own expressions |
| Gather, SubqueryScan, Materialize, Memoize | need, passed through |
| Aggregate | a fresh set from `aggConsumerExprs`; a Finalize aggregate passes every column |
| SetOp, DistinctOn, Distinct, CTEScan body | every column (whole rows are compared, emitted or materialised) |
| Join (inner, left, right, full, cross) | need and the merged-space predicate split at the left width; positional keys at face value; a lateral join's left side also gets `lateralOuterReadSet` (the inner probe's and Memoize's OuterColumnRef\{Level 1\} and key ColumnRef reads, else every column) |
| NestedLoopIndexJoin | outer: every column; the inner probe is not modelled |
| any other node | the walk stops; no entry, so full decode |

At an IndexScan the recorded set is need ∪ the Cond's columns ∪ the index key columns. A scan reached twice keeps
the union.

The expression reader, `exprReadSet`, is `consumerLiveColumns` plus one change: an uncorrelated sublink with no
ParParam reads nothing of the row (`deformSublinkArgs`, slice 53). Q14's statement-2 date\_dim probe filters on
`d_week_seq = (SELECT …)`, and without this the lateral join above it read its whole left side.

**Measured** (instructions:u, 3 runs × 2, fresh A/B against a HEAD build):

| query | before | after | change |
|---|---|---|---|
| TPC-DS Q14 (Target) | 129.443 / 129.067 | 123.901 / 123.909 | **−4.1 %** |
| TPC-DS Q23 | 62.606 | 62.102 | −0.8 % |
| TPC-DS Q72 | 24.116 | 24.054 | −0.3 % |

Plans are unchanged and Q14's output is byte-identical.

**Gates:**

- units;
- spotcheck;
- acceptance: 24 MATCH;
- sf025 PASS=99, with plans 99/99 same;
- fire-set: PASS, no fires;
- RegressSuite: counts equal to the C32 run except the `plpgsql` flapper;
- isolation suite: PASS.

**Test.** `TestIndexReadSetsMatchFullDecode` runs 13 queries three ways and requires identical rows, in order:
read sets on, read sets off, and on with skipped cells poisoned. The queries cover:

- lateral index nested loops;
- a projection and a sort key over an otherwise unread inner column;
- a residual on the inner;
- aggregates;
- INTERSECT;
- a materialised CTE read through IN;
- an uncorrelated sublink in the probe filter;
- LIMIT;
- a left join;
- a Gather.

`ctid` and whole-row references must decline. A hook counts the probes that skipped columns: 11 of 13 queries
arm.

**Left (ledgered):**

- the NestedLoopIndexJoin inner probe;
- bitmap-heap and index-only-scan heap fetches;
- semi and anti joins, WindowAgg, Result and LockRows, which stop the walk;
- seq scans under the same walk (slice 3 above);
- the extended-protocol `Build` path.
