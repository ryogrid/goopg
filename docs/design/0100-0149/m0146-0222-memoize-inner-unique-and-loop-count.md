# M0146-0222 — TPC-DS Q72: Memoize on a unique join, and loop counts for probes driven by a prebuilt leaf

Status: slice 1 (get_memoize_path's inner_unique test) and slice 2 (loop counts under a sub-problem leaf) landed
2026-10-17. The record stays open (§5). Evidence: `analysis/m0146/m0146-0222/`.

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

## 4. Slice 2 (landed): get_loop_count through a sub-problem leaf

Q72's explicit `JOIN` chain is longer than `join_collapse_limit` (8), so its first eight relations are searched as a
sub-problem.

- PG keeps the result as a joinrel whose base relids stay visible to the enclosing search.
- goopg enters it as one leaf, with the sub-problem's output rows (1 here) and nothing else.

`get_loop_count` takes the smallest row count among the base rels in `required_outer`. goopg's `loopCountFor` took
the leaf's rows instead, which priced the `d3` probe on `catalog_sales.cs_ship_date_sk` as a single execution.

- A sub-problem leaf now records `baseRelInfo.subproblemBaseRows`: the row count its own search gave each base
  relation, keyed by source table, with nested sub-problems merged in.
- `loopCountForKeys` resolves the probe's outer values (`paramIndexClause.outerKey`) through their columns'
  `SourceTableIdx` to those base rows. When none resolves, it falls back to the leaf's own rows.
- The three call sites pass their bound clauses: the parameterized index probe, the skip probe and the
  parameterized bitmap path.

**Measured:**
- `d3` now prices at 0.29..0.33, identical to PG.
- With PG statistics, goopg's unsorted join is 15466.64 against PG's 15491.68. Of the remaining 25, about 24.5 is
  `inventory_pkey`'s stale geometry (100.60 against 101.89 per probe over about 19 rows; M0146-0216's owner reload).
- Census verdicts are unchanged on every corpus. The fired Q72 results are byte-identical at both scales.

**Pin:** `TestLoopCountReadsSubproblemBaseRows`, covering the supplying base relation, the fallbacks, and the
smallest of two suppliers. Mutation-checked.

The slice also updates `joinpathsmemoize.go`'s gate-6 comment, which slice 1 left stale.

## 5. What still separates Q72 from PG

With PG statistics (`probe2/`), goopg's outer top rel holds PG's two arms, but in the opposite order:

| arm | goopg | PG |
|---|---|---|
| sorted (order from the Gather Merge below) | 15459.92..15474.87 | 15475.87..15490.79 |
| unsorted | 1160.17..15474.80 (0.07 cheaper) | 1160.03..15491.68 (0.89 dearer) |

1. **Index geometry** shifts both of goopg's arms by about 16 against PG's; this is owner-gated (M0146-0216).
2. **A 0.96 difference in the arms' relative cost.** Next slice: compare the sub-problem's Gather Merge → Sort
   (goopg 15459.75) and Gather (15459.68) arms term by term with PG's.
3. **The sub-problem boundary passes no partial paths.** With `enable_gathermerge = off`, PG's best plan runs the
   whole join, `d3` and `promotion` included, inside the workers under a Partial GroupAggregate. goopg cannot form
   that, because a sub-problem leaf has no partial pathlist. The pathlist collapse at this boundary is already
   ledgered.

## 6. Slice 3 recon (2026-10-17): the arms' order is decided by startup tie-breaks

Probes on the replica (`probe3/`) and goopg's trace give the 8-rel joinrel (cs, inventory, warehouse, item, cd, hd,
d1, d2):

- **PG** keeps the serial `(7 without item) ⋈ item` Nested Loop at 1159.74..15476.58, generated first. It rejects
  `(7 without warehouse) ⋈ warehouse` at 1159.87..15475.63 and the top Gather at 1159.87..15475.63. All three are
  fuzz-equal on total. On the tight fuzz each newcomer is better on total but worse on startup; the rel has
  `consider_startup` (Q72's `LIMIT`), so the result is COSTS_DIFFERENT and `add_path` keeps the incumbent. PG's
  unsorted arm is therefore 0.88 above its Gather Merge → Sort arm (15475.70).
- **goopg** generates the same pairs in the same order, because its base order matches PG's range-table order. But
  it costs them with the 0.13 startup on the other pair:

  | pair | goopg |
  |---|---|
  | (7 without item) ⋈ item | 1159.87..15459.68, kept |
  | (7 without warehouse) ⋈ warehouse | 1159.74..15460.64, rejected |

  So goopg's unsorted arm sits 0.07 below its Gather Merge → Sort arm, and the top sort wins.

The startups trace down: PG's outer 7-rel paths start at 1159.45 / 1159.74, goopg's at 1159.58 / 1159.61. That
puts PG's base parallel subtree at about 1159.32 and goopg's at 1159.45. The 0.13 originates in the 6-rel partial
paths around the d2 join; PG's startup increment there (+0.16) is not a plain index probe's 0.29.

**Next:** log the 6-rel partial paths (`add_partial_path`) on both sides and find the node whose startup differs.
