# M0141-S2b-4 — UNION (distinct) planning: decomposition against PG 18.3

Status: design slice 2026-09-24 (no code). Task: `.ralph/fix_plan.md`
M0141-S2b-4. The implementation slices below are filed as M0141-S2b-4a..4c.

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
