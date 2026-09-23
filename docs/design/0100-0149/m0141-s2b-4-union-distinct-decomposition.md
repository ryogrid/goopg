# M0141-S2b-4 — UNION (distinct) planning: decomposition against PG 18.3

Status: S2b-4a landed 2026-09-24 (`ded1b8db3`); S2b-4b (Gather variants),
4c and 4d open. Task: `.ralph/fix_plan.md` M0141-S2b-4.

## Witnesses (TPC-DS SF0.25, current tree)

goopg plans a distinct UNION on exactly two queries, Q49 and Q75, each as
nested **binary** `HashSetOp Union` nodes. PG plans neither that way:

- **Q49** (`… UNION … UNION …`, three branches): `Limit → Incremental Sort →
  Unique → Sort → Append` (three children). One n-ary Append, then Sort →
  Unique, elected over HashAggregate by cost.
- **Q75**: `HashAggregate → Unique → Merge Append` over presorted
  (`Gather Merge`) children. The ordered children let PG skip the Sort.

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
