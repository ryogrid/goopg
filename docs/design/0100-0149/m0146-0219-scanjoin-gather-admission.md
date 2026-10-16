# M0146-0219 — the scan/join rel's simple Gather must survive add_path before the grouped rel aggregates it

Status: landed 2026-10-16. Evidence: `analysis/m0146/m0146-0219/`.

## 1. Record

TPC-DS SF0.25 Q61 (S9 pass at c2a4421fc, category aggregation-strategy, SHAPE-DIFF at depth 3) plans each of its
two scalar `sum()` FROM subqueries as follows:

- PG: `Finalize Aggregate → Gather → Partial Aggregate`.
- goopg: `Aggregate → Gather`.

The divergence does not depend on the FROM nesting: the first subquery on its own plans the same way
(`probe/q61-sub.sql`).

## 2. Attribution

The trace (`GOOPG_PGSHAPED_DP_TRACE=1`, `probe/goopg-head-upper-arms.txt`) shows the four arms goopg filed on the
grouped rel:

| arm | goopg total |
|---|---|
| serial plain `Aggregate` | 18131.24 |
| split (`Finalize → Gather → Partial`) | 17966.27 |
| gathered no-split (`Aggregate → Gather`) | **17966.24** |

The no-split arm won the fuzzy tie on add_path's final strict comparison.

PG, probed with `elog`s in add_path and add_paths_to_grouping_rel of the `tmp/pgdbg` build
(`probe/pg-addpath-q61-sub.txt`), files no aggregate over a Gather at all. Its grouped rel receives:

- the plain Agg over the scan/join rel's only remaining serial path, a Nested Loop over a lower Gather
  (1002.48..18131.32);
- the Finalize Aggregate.

The input rel's pathlist holds that Nested Loop and nothing else.

The reason is in `generate_gather_paths` (allpaths.c), which `apply_scanjoin_target_to_paths` calls on the topmost
scan/join rel:

- It runs with `override_rows = false`, so the Gather carries the rel's rows (1) and costs 1017.56..17964.74.
- add_path then compares it with the Nested Loop. The totals are within 1%, and the Nested Loop starts more than 1%
  earlier, so `compare_path_costs_fuzzily` returns `COSTS_BETTER2` and the Gather is rejected.
- `add_paths_to_grouping_rel` aggregates only the paths that survived.

goopg builds its no-split arms straight onto the grouped rel, so a Gather that PG dropped still competed. M0146-0190
ported the same filter for the worker-sort Gather Merge (`scanJoinRelKeepsGatherMerge`, TPC-DS Q26), but not for
the simple Gather.

## 3. Change

`scanJoinRelKeepsGather` (`internal/optimizer/partialaggupper.go`) is the Gather Merge check's twin:

- It re-prices the arm's Gather at the seed's (the rel's) rows, with no pathkeys.
- It compares that against every candidate the search left on the scan/join rel, through `comparePaths`. A Gather
  has no pathkeys, so unordered candidates count too; the Gather Merge twin skips them. Gathered paths the search
  stamped on the rel are skipped, as in the twin.

The function decides `keepGather` once. Every arm built over the no-split Gather is filed only when it is true:

- the plain `Agg → Gather`;
- the presorted plain `Agg → Sort → Gather`;
- the grouped `GroupAgg → Sort → Gather`;
- the grouped hashed `HashAgg → Gather`.

## 4. Measured

| corpus | before | after | flips |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | none |
| TPC-DS SF0.25 | 86 / 99 | **88 / 99** | Q61, Q16 → MATCH |
| TPC-DS SF1 | 73 / 99 | **74 / 99** | Q16 → MATCH |

No query regressed. SF1 Q13's aggregation now matches PG; its first divergence moved deeper, to qual-placement.

CATEGORIES-EXCL-MATCH:
- SF0.25: aggregation-strategy 0, join-order 12→10, join-method 7→6, parameterisation 5→3, parallelism 5→4.
- SF1: join-order 21→20, join-method 9→7, parameterisation 6→4, aggregation-strategy 5→4, parallelism 11→10;
  scan-type 12→13 and qual-placement 3→4, both from records whose first divergence moved deeper (Q13).

Values are unchanged:
- the TPC-H acceptance arm is value-identical to `bench/tpch/baseline-digests.txt`;
- the fire set's Q16/Q61 (SF0.25) and Q13/Q16 (SF1) results are byte-identical between baseline and candidate.

Remaining aggregation-strategy records at SF1:
- Q48 (`Finalize Aggregate` against goopg's `Aggregate` at depth 0, with join estimates 434 against PG's 531);
- Q23, Q26 and Q71.

## 5. Not ported (ledger)

When PG's scan/join-rel Gather does survive, the aggregate above it reads the rel's rows. goopg's no-split arms keep
pricing their own Gather at `compute_gather_rows` (M0146-0009n). Only the admission probe uses the rel's rows.

## 6. Pin

`TestScanJoinRelDropsATiedGather` uses Q61's PG-logged numbers:

- the tied, earlier-starting serial path rejects the Gather, with and without startup interest;
- the Gather Merge twin cannot reject it, because it skips unordered paths;
- a Gather more than 1% cheaper is kept;
- the search's own Gather and a non-searched input are no incumbents.

Mutation-checked: skipping unordered candidates fails the test.
