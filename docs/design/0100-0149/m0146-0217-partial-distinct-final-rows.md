# M0146-0217 — the partial-DISTINCT split arm's final row estimate

Status: landed 2026-10-16. Evidence: `analysis/m0146/m0146-0217/`.

## 1. Record

TPC-DS Q38 and Q87 (S9 pass at 4334a3714, category sort-strategy) plan each DISTINCT set-operation leg in two
ways:

- PG: `Unique → Gather Merge → Unique → Sort`, a per-worker Unique (`create_partial_distinct_paths`, planner.c).
- goopg: `Unique → Gather Merge → Sort`, the unsplit arm.

## 2. Attribution

- **Both arms are generated.** goopg already ports `create_partial_distinct_paths` (`addPartialDistinctPaths`).
  `GOOPG_PGSHAPED_DP_TRACE=1` shows both arms filed on the DISTINCT rel.
- **Costs are PG's.** Q38's `web_sales` leg was measured on a private SF0.25 clone with the reference statistics
  imported (M0146-0215):
  - split arm: 8918.69..9031.81, which is PG's to the cent;
  - unsplit arm: 8918.69..9029.03.

  The two are fuzzily equal under add_path's 1.01 factor, and so is the pathkey comparison.
- **The tie-break differs.** A probe in the `tmp/pgdbg` scratch PG logged each arm's rows after
  `create_final_distinct_paths` and `create_partial_distinct_paths`. PG's split arm reports **890** rows, its
  unsplit arm 891. `add_path` breaks a fuzzy cost tie on rows, so PG keeps the split arm. goopg gave both arms the
  DISTINCT rel's 891 and kept the older, unsplit path.

The 890 comes from PG's structure. `create_partial_distinct_paths` builds the split arm's leader Unique by calling
`create_final_distinct_paths` on the **partial** distinct rel, whose cheapest path is the Gather Merge. That call's
`numDistinctRows` is therefore `estimate_num_groups` over the Gather Merge's rows (`compute_gather_rows`, 890 here).
`estimate_num_groups` never returns more groups than input rows.

## 3. Change

`addPartialDistinctPaths` computes `splitFinalRows`: `estimateNumGroups` over the crossing rows, clamped to them
and to at least one. Both split arms use it for their final Unique: the sorted arm (`Unique → Gather Merge →
Unique → Sort`) and the hashed arm (`Unique → Gather Merge → Sort → HashAggregate`). The unsplit arm keeps the
DISTINCT rel's rows, as PG's `create_final_distinct_paths` on the input rel does. Costs are unchanged.

## 4. Measured

| corpus | match before | match after | movement |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | none |
| TPC-DS SF0.25 (shipped) | 86 / 99 | 86 / 99 | none; categories identical |
| TPC-DS SF0.25, PG statistics imported | Q38 SHAPE-DIFF | **Q38 MATCH** | the record this task fixes |
| TPC-DS SF1 (shipped) | 74 / 99 | 73 / 99 | Q87 MATCH → SHAPE-DIFF |
| TPC-DS SF1, PG statistics imported | — | **Q87 MATCH** | PG-identical under PG's inputs |

Why the shipped corpora do not move toward PG:

- **SF0.25 Q38.** goopg's own ANALYZE gives a per-worker group estimate of 387 where PG's gives 371. The crossing
  rows then exceed the leg's estimate, the clamp leaves the two arms tied on rows, and the older unsplit arm stays.
  This is sample variance, the M0146-0215 class.
- **SF1 Q87.** It flips the other way. On goopg's own statistics, a leg's crossing rows fall below its DISTINCT
  estimate where PG's do not, so goopg now keeps the split arm and PG the unsplit one. A private clone of the SF1
  corpus with `pg_dump --statistics` from `:65438 tpcds` imported plans Q87 exactly as PG does
  (`probe/g18-q87-imp.txt`).

So the change is the same logic as PG's, and both residuals are estimate-driven. Per R3 the change stays, and the
SF1 Q87 movement is filed as M0146-0218. It is gated on the owner's statistics-import decision (M0146-0215).

Executed values are unchanged: the TPC-H acceptance arm has 24 / 24 value MATCH against
`bench/tpch/baseline-digests.txt`, and the fire set gives byte-identical Q38/Q87 results at SF1 for baseline and
candidate.

## 5. Pin

`TestPartialDistinctSplitArmReestimatesOverCrossingRows` uses 400001 unique-column rows, which plan one worker.
There `clamp_row_est(400001 / 1.7) × 1.7` rounds to 400000 crossing rows, one fewer than the DISTINCT rel. The test
requires that a split arm is kept and that its final Unique carries the crossing-row estimate. Restoring the DISTINCT
rel's rows on both split arms fails it (no split arm kept).
