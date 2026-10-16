# M0146-0222 — TPC-DS Q72: Memoize on a unique join, and loop counts for probes driven by a prebuilt leaf

Status: slice 1 (get_memoize_path's inner_unique test) landed 2026-10-17. Slice 2 (loop counts under a prebuilt
search leaf) is open. Evidence: `analysis/m0146/m0146-0222/`.

## 1. Record

TPC-DS Q72 (S9 pass at e3a7ae742, sort-strategy, SHAPE-DIFF at depth 3, at both scales; it persists under PG
statistics):

- PG: `GroupAggregate → Nested Loop Left → Nested Loop → Gather Merge → Sort → <joins>`. The group ordering is
  established inside the parallel part and preserved through the upper Nested Loops.
- goopg: `GroupAggregate → Sort → <joins>`.

goopg's whole plan priced 15267.53 against PG's 15490.83.

## 2. Attribution (exact replica)

On the `pg_basebackup` replica under the debug PG build (`probe/pg-exact-replica-q72-grouped-and-input.txt`), PG's
input rel holds two candidates:
- the sorted Nested Loop, 15475.87..15490.79, which PG chooses;
- an unsorted Nested Loop, 1160.03..15491.68, with goopg's shape.

goopg's unsorted join was 15267.48, 224 lower. The plans differ in two places.

1. **Memoize over `date_dim d2`.** PG probes d2 with a plain index scan and leaves
   `d1.d_week_seq = d2.d_week_seq` as a join filter (+305 over 937 rows). goopg wrapped the probe in Memoize (+91).

   PG's `get_memoize_path` (`joinpath.c:752-767`) builds no Memoize when the join is `inner_unique` and some restrict
   clause is not among the parameterization's `ppi_serials`. On a unique join, Nested Loop stops at the first match,
   so the cache entry could only be marked complete for outer rows with no partner. Here `d_date_sk` is unique and
   `d_week_seq` is a join filter. goopg's Memoize gauntlet recorded this gate as unreachable, because "goopg's search
   never marks a join inner-unique".

2. **`date_dim d3` probe cost.** goopg prices `Index Scan d3` at 0.29..8.31; PG prices it at 0.29..0.33. The probe
   is parameterized by `catalog_sales.cs_ship_date_sk`.
   - PG's `get_loop_count` takes catalog_sales's 360397 rows, so `cost_index` prices the repeated scan cheaply.
   - goopg's search sees the gathered parallel subtree as a prebuilt leaf with `Relids == 1` and `rows = 1`.
     `loopCountFor` therefore returns 1 for that probe (`probe/goopg-d3-loopcount.txt`), the single-probe price.
   - The same probe built inside the inner search gets 360397.

   This is slice 2.

## 3. Slice 1

- `searchCtx.extraInnerUnique` is `add_paths_to_joinrel`'s `extra->inner_unique`:
  - false for SEMI and ANTI;
  - for JOIN_UNIQUE_INNER, `bms_is_subset(min_lefthand, outerrel)`;
  - otherwise `innerRelProvenUnique` over the pair's clauses (the same proof the hash and merge cost arms use).
- `addNLIPaths` computes it. The partial nested loop reads it from `mergeUnique.innerUnique`, which is set once per
  pair in `addPathsToJoinrel`.
- `nliMemoizeCandidate` refuses Memoize when `innerUnique` holds and either the inner is unparameterized or a residual
  join clause remains. A residual clause is a restrict clause the parameterization does not enforce, i.e. one outside
  `ppi_serials`.

**Measured:**
- Verdicts are unchanged on every corpus: TPC-H 22/22, SF0.25 88/99, SF1 76/99.
- SF0.25 CATEGORIES-EXCL-MATCH `parameterisation` goes 3 → 2, because Q72's Memoize divergence is gone.
- With PG statistics, goopg's unsorted join is now 15482.78 against PG's 15491.68. Slice 2's `d3` loop count and
  `inventory_pkey`'s stale geometry (100.60 against 101.89; M0146-0216's owner reload) explain the rest.
- Values are unchanged: the fired Q72 / Q64 results are byte-identical and the acceptance arm is value-identical.

**Pin:** `TestNLIMemoizeRefusesUniqueJoinWithFilter`. Mutation-checked: dropping the gate fails it.

## 4. Slice 2 (open)

`loopCountFor` must not take a prebuilt leaf's output rows as the loop count. PG's `get_loop_count` iterates the
base rels in `required_outer`. For a parameter supplied by a column of a base rel that sits inside a prebuilt leaf,
that is the base rel's own rows, as the inner search sized it. The resume point is `pathparamindex.go loopCountFor`:
map the parameter's source column (`SourceTableIdx`) through the prebuilt leaf to the base rel it came from.
