# M0146-0190 — PG oracle notes (tmp/pgdbg, OPTIMIZER_DEBUG, db tpcds025)

The scratch PG build was temporarily instrumented with LOG lines in
`generate_useful_gather_paths` (allpaths.c), `add_path` for upper rels and
`create_agg_path` (pathnode.c). The sources were restored afterwards.

## TPC-DS Q26, SF0.25

Top scan/join rel (b 1 2 3 4 5), rows 26, pathlist before
apply_scanjoin_target_to_paths:

    NESTPATH 2964.66..14291.96   (Gather below, unsorted)
    NESTPATH 14285.24..14292.59  (Gather Merge over rel (1 2 3 4), then an
                                  inner index probe; sorted on i_item_id)
    NESTPATH 14059.21..15621.97  (sorted)
    ...

generate_useful_gather_paths(top rel, override_rows = false) then builds:

    GUGP-GM relids=(b 1 2 3 4 5) startup=14286.46 total=14289.53 rows=26.0

- Its rows are the rel's (26), not compute_gather_rows (8 × 3.1 = 25):
  rowsp is NULL when override_rows is false.
- Its cost is fuzz-equal to the NL over Gather Merge, and so is its startup.
  Pathkeys, rows and parallel safety are equal. The tight-fuzz
  (1.0000000001) comparison finds a better total and a worse startup, which
  is COSTS_DIFFERENT because the rel considers startup (LIMIT 100), so
  add_path rejects the new path.
- No create_agg_path call takes a GatherMerge of 26 rows / 14289.53 as its
  input. The grouped rel sees:

      AGGPATH strat=1 input=Sort(over Gather) 14289.51..14289.57 -> 14289.51..14290.42
      AGGPATH strat=1 input=Result(NL over GM) 14285.24..14292.59 -> 14285.24..14293.44
      AGGPATH strat=1 input=GatherMerge rows=25 (partially grouped) -> 14286.46..14290.67

  The Sort-over-Gather aggregate arrives first and keeps the fuzz-equal tie.

## TPC-DS Q45, SF0.25

The top rel holds a NESTPATH of 8878.40..8983.29 over Gather Merge, sorted on
(ca_zip, ca_city), the group keys. The top-level Gather Merge,
8909.33..8922.84, is rejected the same way. PG plans Sort over Gather.

## goopg at SF1, Q45 (private clone, trace build)

The search's NL over Gather Merge, 25028.54..25423.04 with 657 rows, rejects
the worker-sort Gather Merge, 25118.98..25196.71: the totals are within
1.009, under the 1.01 fuzz. PG at SF1 keeps its Gather Merge (25174.01..25229.97,
473 rows). goopg's group-input estimate (657 vs PG's 473) moves the NL's
inner-probe cost, and with it the fuzz verdict.
