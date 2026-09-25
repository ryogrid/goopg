# M0141-S2b-4 — UNION (distinct) planning: decomposition against PG 18.3

Status: S2b-4a (`ded1b8db3`), S2b-4b (`f311b4b1a`) and S2b-4c
(`5af350059`) and S2b-4d (`fcae392ae`) landed 2026-09-24; 4e blocked (see
its section). Task: `.ralph/fix_plan.md` M0141-S2b-4.

## Witnesses (TPC-DS SF0.25, current tree)

goopg plans a distinct UNION on exactly two queries, Q49 and Q75, each as
nested **binary** `HashSetOp Union` nodes. PG plans neither that way:

- **Q49** (`… UNION … UNION …`, three branches): `Limit → Incremental Sort →
  Unique → Sort → Append` (three children). One n-ary Append, then Sort →
  Unique, elected over HashAggregate by cost.
- **Q75**: goopg plans the distinct UNION as nested binary HashSetOps.
  (Corrected 2026-09-24 by S2b-4e: this line first claimed PG plans
  `Unique → Merge Append` over Gather Merge children. The fire-set PG
  captures at both scales show `HashAggregate → Gather → Parallel Append`,
  the hashed candidate over the pure parallel Append. After S2b-4a/4b goopg
  plans the same structure; the remaining difference is the `Unique` label
  on goopg's hashed step, which is S2b-4d.)

The knob (jointree) pipeline plans both exactly like the default one; M0145's
appendrel work flattened only `UNION ALL`.

## What PG does (`prepunion.c`)

1. **Flatten.** `plan_union_children` (prepunion.c:1269) folds a child
   `SetOperationStmt` into its parent when `op` is the same, `all` matches
   the parent's or the child is `ALL` (a UNION ALL can be pulled up into a
   UNION: the distinct step removes duplicates anyway), and column types
   and collations agree. The result is one list of leaf children.
2. **Append.** `generate_union_paths` (prepunion.c:676) builds one Append
   over the cheapest child paths (`apath`), plus a Gather over a partial
   Append when every child has a partial path (`gpath`).
3. **Distinct candidates** (`!op->all`), with `dNumGroups = apath->rows`,
   the worst case, by PG's own comment:
   - hashed: `create_agg_path(AGG_HASHED)` over `apath`, and over `gpath`;
   - sorted: `create_sort_path` + `create_upper_unique_path` over `apath`,
     and over `gpath`;
   - merge: when every child offers a path already sorted on
     `union_pathkeys` (`try_sorted`, from `build_setop_child_paths`),
     `create_merge_append_path` + `create_upper_unique_path`.
   `add_path` elects among them.

## What goopg does

`createSetOpPaths` / `addSetOpPaths` (`windowsetoppaths.go`) price one
binary `SetOp` per pair of branches: a streaming arm for UNION ALL, and a
hashed arm (`HashSetOp`) for UNION. There is no n-ary flattening, no sorted
(Sort → Unique) candidate, and no Merge Append candidate, so the election PG
makes never happens.

## Slices

- **S2b-4a — flatten same-kind UNION chains.** Port `plan_union_children`'s
  fold rule (same op; `all` equal or child `ALL`; column types and
  collations equal). Represent the flattened distinct UNION as one distinct
  step over a left-deep UNION ALL chain. goopg's binary streaming SetOp
  already executes an n-ary append that way. EXPLAIN renders the chain as
  one `Append` with N children, as PG does. Witness: Q49's node count and
  shape. Values gate: the full set; the de-duplication semantics must not
  change.
- **S2b-4b — the distinct election.** Over the flattened Append, offer the
  hashed candidate (`cost_agg` AGG_HASHED) and the sorted candidate
  (`cost_sort` + `cost_unique`-equivalent Unique), with `dNumGroups` = input
  rows, and let `addPath` elect. Add the Gather variants where the Append
  has a partial path. Witness: Q49 elects Sort → Unique.
- **S2b-4c — the Merge Append arm.** When every child offers a path sorted
  on the union pathkeys, offer Merge Append → Unique. Needs a Merge Append
  path and executor operator if goopg lacks one; check before scoping.
  Witness: Q75.

Order: 4a before 4b (the election needs the n-ary input), 4c last. Each is
an impl slice with the full default-arm gate set plus the fire-set gate.
EXPLAIN-shape movement lands in `CATEGORIES-EXCL-MATCH` on Q49/Q75.

## S2b-4a landed (2026-09-24, `ded1b8db3`)

- `applySetOp` (planner.go) records each UNION result's leaves and folds
  children by `plan_union_children`'s rule. A branch rewrapped by type
  unification is a new node, so it stays one leaf (upstream's colTypes
  condition).
- A distinct UNION is a `Distinct` over a left-deep UNION ALL chain, which
  EXPLAIN already renders as one n-ary Append.
- `createUnionDistinctPaths` (windowsetoppaths.go) elects on a fresh SETOP
  rel with `dNumGroups` = input rows, hashed candidate first as in
  `generate_union_paths`. So the hashed vs Sort → Unique election planned
  for 4b landed with 4a; 4b keeps only the Gather variants.
- Zero-column UNION: with no columns the Unique takes its input without a
  Sort (PG's `groupList != NIL` guard). The first cut built a key-less Sort
  path and crashed the server in upstream `union.sql`; pinned by
  `TestZeroColumnUnionBothStrategies`.

Result on TPC-DS SF0.25: only Q49 and Q75 change. Q49 is now `Unique →
Sort → Gather → Parallel Append` over all three branches; PG uses a serial
Append, a parallel-costing difference left for 4b. The Sort key is PG's
exact column order. Q75's nested HashSetOps are gone. The default-arm
floor holds (match 2), and qual-placement moved 27 → 28, inside the ±3
band.

## What was tried and held back (filed as S2b-4d)

- **Relabelling the hashed `Distinct` as `HashAggregate` + `Group Key`**
  (PG's spelling for SELECT DISTINCT and hashed UNION alike). Correct on its
  own, but it turned TPC-DS Q41's floor MATCH into a shape-diff. The match
  had rested on the old `Unique` label, which equals PG's actual `Unique`
  over a Sort. goopg's DISTINCT really does elect hashed where PG sorts,
  and AGENT.md treats losing a floor match as a regression.
- **PG's candidate order for DISTINCT** (sorted first, as in
  `create_final_distinct_paths`). In PG both candidates survive `add_path`
  (the Unique has pathkeys) and the choice happens after the ORDER BY stage
  costs its Sort. goopg elects one winner at the DISTINCT rel, before ORDER
  BY, so PG's order alone elected Unique on `SELECT DISTINCT a, b … ORDER BY
  a, b LIMIT 100`, where PG elects HashAggregate + Sort.
- **A presorted-input arm** (Unique straight over an already-sorted input).
  goopg's pipeline puts the ORDER BY Sort below the DISTINCT, a non-PG stage
  order, so the arm fired where PG hashes.

All three need the same prerequisite: carrying the DISTINCT rel's
candidates to the ordered rel, as PG's upper-rel pathlists do. With that in
place, Q41 can match honestly with the correct label.

## S2b-4b landed (2026-09-24, `f311b4b1a`)

Q49's Append ran under a Gather because S2b-4a's folded UNION ALL chain went
through `createSetOpPaths`, and Q49's union sits in a FROM subquery, where
`ParallelStatementOK` is false. There `addPartialSetOpPath` files the MIXED
parallel-Append arm, which it reserves for appendrels, and the Sort-topped,
non-partial branches were claimed whole. PG plans a distinct UNION's input
in `generate_union_paths` at any depth, and that files only the PURE arm
(every child partial).

`SetOp.UnionDistinctInput` marks the chain links, and `addPartialSetOpPath`
applies the top-level (pure-only) rule to them. Q49 now plans `Unique →
Sort → Append(3)`, PG's shape node for node. Q75's children are partial,
so its pure-arm Gather stays; its remaining gap is S2b-4c.

Not ported (ledgered): PG offers each distinct candidate over both the
serial Append (`apath`) and the Gather (`gpath`) and elects among all four.
goopg's chain elects serial vs Gather first and builds the distinct
candidates over that one input.

## S2b-4c landed (2026-09-24, `5af350059`)

goopg had no Merge Append path, node or executor. The slice adds all three,
narrowly, for the distinct UNION:

- **Plan node.** `SetOp.MergeKeys` makes a UNION ALL link an ordered-merge
  link. A left-deep chain of them is the Merge Append; it is only built
  serially.
- **Path.** `addUnionMergeAppendPath` (windowsetoppaths.go) sorts every
  leaf on the union pathkeys, chains the Sorts, prices the chain as one
  node by `cost_merge_append` and offers `Unique` over it. It is filed
  after the hashed and Sort → Unique candidates, as in `generate_union_paths`.
- **Executor.** The streaming `setOp` does a two-way merge on `MergeKeys`,
  sharing Gather Merge's key comparison (`mergeKeysLess`).
- **EXPLAIN.** `Merge Append` with a `Sort Key:` line deparsed through the
  first branch (PG deparses through the merge's targetlist, which is the
  first child's). Merge links and plain Append links never absorb each
  other.

The serial chain's Append seed is now priced by `cost_append` over the same
leaves (`unionAppendCost`). The chain's SetOp links carry no PlanCost, and
the legacy display derivation charged a full `cpu_tuple_cost` per row. With
that tilt, upstream `union.sql`'s two-row VALUES unions elected the merge
where PG keeps Sort → Unique. `add_path`'s 1% fuzz makes these ties
decisive: a Merge Append within 1% of the cheapest total, with cheaper
startup, dominates it.

Result: upstream `union.sql`'s `enable_hashagg = off` cases gain PG's
`Unique → Merge Append → Sort, Sort`; `select_distinct` is unchanged. On
TPC-DS SF0.25 only Q49's cost moves (shape the same); the floor holds.

### Not ported (filed as S2b-4e)

PG's `build_setop_child_paths` offers each child's cheapest path sorted on
the union pathkeys, and a path that is already sorted needs no Sort: an
index scan, a Gather Merge over a partial path (TPC-DS Q75), or an
Incremental Sort over a partially sorted path (the `item_pkey` probe
below). goopg sorts every branch's finished plan explicitly, so its merge
candidate costs more than PG's, and Q75 still elects the hashed candidate.

PG 18.3, `tpcds025`, `enable_hashagg = off`, a two-branch UNION over `item`:
`Unique → Merge Append → Incremental Sort (Presorted Key: i_item_sk) → Index
Scan using item_pkey`, for each branch.

## S2b-4e: presorted branch paths (2026-09-24, blocked)

Tried and not landed. The mechanism is sound, but it has nothing to reuse
on the default arm yet.

- **Q75 is not a witness.** PG elects the hashed candidate for Q75's UNION
  at SF0.25 and SF1 (see the corrected Witnesses entry). No TPC-DS query
  plans a Merge Append in PG.
- **What was built.** Each simple UNION branch was planned a second time with
  `ORDER BY 1, …, n`, drawing RTIDs from a scratch scope that started at the
  branch's original first RTID (so a substituted plan keeps its alias
  numbering). The Merge Append candidate took that plan when its total cost
  was below the explicit Sort's. That is the same choice as PG's
  `get_cheapest_path_for_pathkeys(TOTAL_COST)` over the child pathlist that
  `build_setop_child_paths` fills.
- **Why it was inert.** On the default arm a single-table `ORDER BY` never
  gets an ordered index path. goopg planned `SELECT i_item_sk FROM item
  WHERE i_item_sk < 100 ORDER BY 1` as `Sort → Index Scan using item_pkey`,
  where PG plans a bare `Index Only Scan`. This is the ledgered
  `c07-single-rel-never-reaches-ordered-index-producer`: `addOrderedIndexPaths`
  runs only inside the join search, which declines one-relation scopes on
  the legacy arm. The knob arm searches single-table statements since
  M0145 slice 4, so the default arm gets them at the M0145-0008 cutover.
  With no cheaper ordered plan available, the second planning pass only
  cost planning time. The plans were unchanged: upstream `union.sql`,
  TPC-DS Q49/Q75, and a large two-table UNION with `enable_hashagg = off`
  (where PG also sorts both branches explicitly).
- **Resume.** Re-apply the above after M0145-0008, then find a witness:
  upstream `union.sql`'s `enable_hashagg = off` tenk1 cases plan Merge
  Append over `Index Only Scan` children in PG.

## S2b-4d landed (2026-09-24, `fcae392ae`)

The prerequisite named in "What was tried and held back" turned out to be
mostly in place: `electOrderedDistinct` (upperordereddistinct.go) already
offers every DISTINCT candidate on the ordered rel. What made Q41's match
accidental was the input and the order credit:

- goopg's ORDER BY stage stacks its Sort below the DISTINCT, so both
  DISTINCT candidates were built over an ORDER BY-sorted input.
- The hashed candidate was credited with the executor's incidental
  ascending re-sort (`distinctOp`), so over that Sort it needed no Sort of
  its own and won Q41, where its `Unique` label matched PG's real Unique.

Now, as in PG:

- **Distinct-clause order** (`transformDistinctClause`, parse\_clause.c):
  `Distinct.SortKeys` lists the ORDER BY items first, with their direction
  and NULLS placement, then the remaining output columns ascending
  (`distinctClauseKeys`). The unique candidate sorts on it, so its output
  delivers the ORDER BY: `DISTINCT a, b … ORDER BY b DESC` is Unique over
  one Sort `(b DESC, a)`.
- **DISTINCT over the unsorted input** (`create_distinct_paths` runs
  before `create_ordered_paths`): when every ORDER BY key resolves against
  the DISTINCT output, `spliceOrderSortBelowDistinct` takes the ORDER BY
  stage's Sort out from under the DISTINCT. It walks through Project/Filter
  parents only; a non-deferrable Limit, LockRows or ProjectSet in between
  keeps the Sort.
- **Hashed carries no order** (AGG\_HASHED has no pathkeys):
  `distinctEmissionPathkeys` returns an empty order for it, so the ordered
  rel stacks a Sort above it.
- **EXPLAIN**: the hashed `*Distinct` prints `HashAggregate` with a `Group
  Key:` in distinct-clause order. Keys are chased to their source as the
  Aggregate arm does, and a target listed twice prints once. This applies
  to SELECT DISTINCT and to a hashed UNION.

Result on TPC-DS SF0.25:

- Q41 is `Limit → Unique → Sort → Seq Scan`, still a MATCH, now on the
  right mechanism.
- Q75's UNION is `HashAggregate → Gather → Parallel Append`, PG's
  structure (Group Key text differs, see below).
- Default arm: match 2 → 2; aggregation-strategy 44 → 43; D4-upperrel
  25 → 24. SF1 unchanged.

Upstream regress (`select_distinct`, `union`): several plans where goopg
hashes but PG sorts over a presorted input (Gather Merge, Incremental Sort,
index order) used to match by accident under the `Unique` label. They now
show as HashAggregate against PG's Unique. Those election gaps pre-date
this slice; the presorted inputs share S2b-4e's blocker.

Not ported (ledgered):

- PG rejects `SELECT DISTINCT … ORDER BY <expression not in the select
  list>` (42P10); goopg accepts it and keeps an outer Sort.
- A hashed UNION's Group Key deparses through the first branch in PG
  (`date_dim.d_year`, `((ss_quantity - COALESCE(...)))`); goopg prints the
  union output names.
- goopg's `distinctOp` still re-sorts its output, work PG's HashAggregate
  does not do (no plan effect now that no order is credited).
