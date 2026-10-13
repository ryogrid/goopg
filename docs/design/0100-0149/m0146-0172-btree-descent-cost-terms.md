# M0146-0172 — btcostestimate's descent terms (TPC-DS SF1 Q72)

Status: landed 2026-10-13 (`7e6d8b13f`).
Parent: M0146-0009p. Evidence: `analysis/m0146/m0146-0172/`.

## Problem

After M0146-0009p, TPC-DS SF1 Q72 ran 216s, against 3.3s before. goopg
elected an "item-only" route: `inventory_pkey` (`inv_date_sk, inv_item_sk,
inv_warehouse_sk`) probed on its second key alone, as a skip scan over the 261
distinct dates, then the date joins over 13819 rows. PG keeps the
`{catalog_sales, d1}` Parallel Hash Join and probes `inventory_pkey` on both
leading keys. goopg priced the item-only route at 54874, below its own
PG-shaped route (55265).

## Decomposition

Two separate gaps, found by comparing the plans node by node and then
probing a private clone against PG 18.3.

1. **The subtree gap under the `d2` hash** (goopg 47899, PG 44279) is the
   `catalog_sales` Parallel Seq Scan: 44670.87 against 41051.89 for the same
   rows.
   - goopg's SF1 heap is 41067 pages against PG's 37448. Its numeric columns
     hold the decimal TEXT the codec wrote before `02995818d` (2026-08-11).
     The SF1 cluster was loaded 2026-07-25.
   - Tuples average 221 bytes against PG's 200; the 4-byte alignment padding
     of the text form shows in `catalog-sales-heap-pages.txt`.
   - The SF0.25 load (2026-09-16, post-flip) matches PG: 9359 pages against
     9416.
   - This is stale data, the owner-reload class of the `char(n)` item. Both
     of goopg's Q72 routes scan `catalog_sales`, so it does not decide the
     election.
2. **The election** turned on `btreeIndexAMCostPages`, which lacked three
   pieces of PG's `genericcostestimate` / `btcostestimate`
   (`./postgres/src/backend/utils/adt/selfuncs.c`):
   - **The log2 descent.** `ceil(log2(index->tuples)) * cpu_operator_cost`,
     once to startup and `num_sa_scans` times to total. P2-09b (B-15) had
     kept it out.
   - **`qual_op_cost`.** One `cpu_operator_cost` per index qual, on every
     index tuple visited.
   - **`num_outer_scans`.** This is `cost_index`'s `loop_count`, which
     `get_loop_count` makes 1 for an unparameterised path. goopg computed
     `numScans = numSA * loopCount` with `loopCount` unset (0). An
     unparameterised SAOP or skip scan therefore skipped the
     Mackert-Lohman arm and paid one index page for all of its descents. On
     `tpcds025`, a skip probe of `inventory_pkey` cost 968.32 against PG's
     1814.67 (210 descents, `idxRun` 84.47).

   PG also rounds `numIndexTuples / num_sa_scans` on single-descent scans
   and clamps the result to `[1, index tuples]`.

   For the SF1 skip probe (262 descents, about 706 index tuples), the log2
   and `qual_op_cost` terms add about 18 per probe. Over the route's 58
   outer rows that outweighs the 391 by which the item-only route had won.

## Change

- `indexScanInputs.numIndexQuals` is PG's `list_length(indexQuals)`. Every
  producer sets it:
  - `len(clauses)` for the restriction, index-only, parameterised (prefix
    and skip), bitmap (both) and NLI-splice paths;
  - 0 for the clauseless ordered path;
  - 1 for `planner.go`'s single-key probe.
- `btreeIndexAMCostPages`:
  - rounds and clamps `numIndexTuples` as PG does;
  - uses `max(loopCount, 1)` as `num_outer_scans`;
  - charges `numIndexTuples * numSA * (cpu_index_tuple_cost +
    qual_op_cost)`;
  - adds the log2 comparisons to the per-descent charge.
- `qual_arg_cost` (`index_other_operands_eval_cost`) stays 0. goopg's index
  keys are constants, Params and outer Vars, which cost nothing to evaluate.

## Verification

- **Probes on `tpcds025`.**
  - The skip probe costs 1808.91 (PG 1814.67) and an 8-element IN list
    38.44 (PG 38.59).
  - Startups print PG's values: 0.43, 0.29, 0.13.
  - The parameterised probe is 767.45 against PG's 827.70. The remaining gap
    is index geometry: goopg's `inventory_pkey` file is 7697 pages against
    PG's 12606.
- **Unit tests.** Six cost pins re-derived from PG's arithmetic.
  - The R59 orders probe moves to 1.5033, against PG's traced 1.54 (was
    1.4094).
  - `TestSAOPNumSAScansCost` asserted the zero-loop behaviour itself. It now
    expects Mackert-Lohman pages for the descents.
  - One six-row executor fixture requests its index shape with seq scans
    off.
- **SF1 Q72**, on one private clone with both binaries: HEAD 219.9s,
  candidate 2.77–3.40s; 101 rows in both. The plan is PG's route.
- **Fire set.**
  - TPC-DS SF0.25: matches 68 → 70 (Q1, Q80).
  - TPC-DS SF1: 45 → 44 (+Q68, −Q47, −Q96). Filed as M0146-0174 and kept
    per R3; the runtimes of both are unchanged against HEAD.
  - TPC-H: 13/22 both.
  - Values are identical on all 85 SF0.25 and 82 SF1 fired queries, and no
    timeout was introduced.
- **Other gates.**
  - TPC-H: tpch-spotcheck and the acceptance arm (24/24 values).
  - TPC-DS sf025 sweep: PASS=99.
  - ea-ratchet: unchanged at 1 finding.
  - 105 strict isolation tests.
  - Regress A/B over 27 cases: 69614 → 69613 diff lines.

## Residuals

All are in the deferral ledger.

- **Regress `join`'s `j2.id1 = any (array[1])`.** goopg's ordered producer
  builds a clauseless twin of an index that has restriction clauses, and PG
  never does (`build_index_paths`). That twin now ties the SAOP path exactly
  and keeps the `ANY` in the Filter. Filed as M0146-0175.
- **SF1 cost comparisons sit on stale heaps.** `catalog_sales` and
  `store_sales` (numeric as text) are 10–13% above PG. An owner reload is
  needed; the evidence is added to the `char(n)` reload item.
- **Index geometry.** goopg's btree files are smaller than PG's
  (`inventory_pkey` 7697 against 12606 pages), so looped probes price
  slightly below PG's.
