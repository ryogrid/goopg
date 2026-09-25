# R72 Step-0: election census — Q4 (P0–P2) + controls (P3) + R71 audit (P4)

Binary: `/tmp/pp2/r72/goopg-r72attr` built from HEAD `35d047b` + TEMP
env-gated stderr census (`GOOPG_R72DBG`, two sites only —
`createGroupingPaths` post-`setCheapest`, `electOrderedGrouping`
entry + elect; reverted before the slice). Instrumentation adds no
planning branches: with the env unset the code path is identical.
Deviation from SCOPE §1 disclosed: plans + census come from the
instrumented binary, not a pristine clean-HEAD binary; plan bytes are
unaffected (identical across both runs, and equal to the r69b-era
goopg Q4 shape).

Cluster: private TPC-H SF=1 clone `/tmp/pp2/clone-tpch-r65`, port
`:5533`, `GOOPG_R72DBG=1`. PG reference: live `:65432` (TPC-H SF=1)
superuser `postgres`; serial setting
`SET max_parallel_workers_per_gather=0` reproduces the canonical
fixture Q4 byte-for-byte (`r69bpg.pg.plans.txt` §Q4: GroupAggregate
192121.20..192222.42).

## P0 — goopg Q4 offers (×2 runs, byte-identical plans)

```
R72GRP strategy=0 rows=5 start=1426.65 total=1426.71 WINNER   (hashed)
R72GRP strategy=1 rows=5 start=5649.63 total=6077.69          (sorted)
R72ORD ncands=2 anyTranslated=true nkeys=1 nodeIsAggNode=true
R72ORD ELECTED built=*optimizer.Sort bestTotal=1426.78
```

- Grouping elects HASHED outright (4.26× gap — no tie, pathkeys
  never consulted).
- Ordered loop runs (2 cands, translated, gates pass) and elects
  Sort-over-hashed (1426.71 + Sort ≈ 1426.78).
- Final shape: `Sort → HashAggregate → NL Semi Join (57066) → …`
  (`/tmp/pp2/r72/q4-plan1.txt`, `q4-plan2.txt` identical).

## P1 — PG Q4 winner + both-side prices

| setting | winner | total | source |
|---|---|---|---|
| serial | GroupAggregate, NO Sort above (Sort UNDER agg) | 192222.42 | live `:65432` = fixture §Q4 |
| serial + `enable_sort=off` | Sort(disabled) → HashAggregate (counterfactual) | 191263.38 | `/tmp/pp2/r72/pg-q4-counterfactuals.txt` |
| serial + `enable_hashagg=off` | GroupAggregate (unchanged) | 192222.42 | same file |
| default (parallel) | Finalize GroupAggregate → Gather Merge → Partial GroupAggregate → Sort | 69885.37 | live `:65432` |

PG-side election fact: hashed+Sort (191263.38) is 0.5% CHEAPER than
sorted-no-sort (192222.42) yet PG picks the sorted shape. PG cites:
`postgres/src/backend/optimizer/util/pathnode.c:50`
(`STD_FUZZ_FACTOR 1.01`), `:156`/`compare_path_costs_fuzzily :185`
(costs within 1% tie, pathkeys/startup break ties), cheapest
adjudication `set_cheapest :272`; grouping producer
`postgres/src/backend/optimizer/plan/planner.c:3763` doc /
`create_grouping_paths :3780`, ordered loop `create_ordered_paths
:5291` / call `:1852`. Within the fuzz the two Q4 candidates tie and
the no-top-Sort pathkeys win. goopg has no such tie to break (4.26×).

## P2 — node-by-node rows/width/price (serial PG fixture vs goopg)

| node | PG rows / width / total | goopg rows / width / total |
|---|---|---|
| orders scan (date filter) | 58222 / 22 / 50314.00 | 57066 / 448 / 15570.66 |
| NL Semi Join (EXISTS) | 13490 / 16 / 191195.81 | 57066 / 448 / 570.66 |
| Sort (under agg) | 13490 / 16 / 192154.92 | — (sort is ABOVE agg) |
| Agg (sorted / hashed) | GroupAgg 192222.42 | HashAgg 1284.03 |
| Top Sort | — (no-sort win) | 1426.78 |

Disjoint inputs (the election sees these, it does not create them):

1. Semi-join selectivity: PG 13490/58222 = 0.2317; goopg
   57066/57066 = 1.0 — goopg applies NO EXISTS reduction.
2. Widths: scan 448 vs 22 (20×), join 448 vs 16 (28×) —
   same family as the R70 width gap (R70 BLOCKED on `minimize_datum`).
3. Semi-join unit price: PG 191195.81 for 13490 rows vs goopg
   570.66 for 57066 rows (335× on 4.2× more rows — the NL-semi
   rescan/startup model, cf. R69 which fixed only the inner-join arm).

Both engines' totals are dominated by the semi-join input price
(PG 191195.81 of 192222.42; goopg's sorted arm carries sort on
57066 wide rows → 6077.69). The 4.26× grouping gap is a downstream
effect of (1)–(3), not an election-rule defect at the site logged.

## P3 — controls

- Q6: loop quiet (no R72ORD lines — no ORDER BY, gate decline),
  plan MATCH (`Finalize → Gather → Partial`, both engines). ✓
- Q13: ordered loop elects Sort-over-hashed outer (agrees with PG
  outer shape) BUT the INNER agg diverges: goopg `GroupAggregate +
  Sort` (R72GRP single candidate rows=150000 total=350574.23 — hashed
  arm absent, elected by default) vs PG `HashAggregate` (65167.54).
  Grouping-level admission gap, not an ordered-loop flip. (pp69bpost
  §Q13 SHAPE-DIFF [join-order,aggregation-strategy,sort-strategy]
  reproduces at HEAD — Slice-2's "Q13 MATCHED" no longer holds;
  R69's `nestloopCost` change is the prime suspect, unproven.)
- Q22: outer diverges the SAME direction as Q4 — goopg
  `Sort → HashAggregate` (ELECTED 969.46) vs PG `GroupAggregate`
  no-sort win (16637.45). Second member of the Q4 family. ✓ family

## P4 — R71 audit (numbers do not reproduce)

- R71 "PG 0.23": located — it is the semi-join SELECTIVITY
  13490/58222 = 0.2317, misreported as a cost. No PG node costs 0.23
  (serial winner total is 192222.42; parallel 69885.37).
- R71 "PG join rows=3 vs goopg 1500": UNLOCATED in every canonical
  source — serial fixture join rows=13490, live parallel=3372,
  goopg=57066. The variable-side non-MCV mechanism (DIAGNOSIS §3)
  may still explain the rows gap (PG 13490 vs goopg 57066), but the
  cited 3/1500 pair is discarded; the rows gap is re-derived above.
- Consequence: R71's "rows are election-inert" (36-site census) is
  UNSOUND — it varied rows around unreproducible anchors. The P2
  table re-anchors the rows question at 13490 vs 57066 + widths.

## Ruling: (c)

The election priced what it saw — the defect is the price, owned
upstream. The winning site's price (hashed+Sort 1426.78) is wrong by
two orders of magnitude against PG's same shape (191263.38), and the
wrongness flows from the P2 inputs (semi selectivity 1.0, widths
20–28×, semi rescan model), not from any rule at the grouping or
ordered site. No election-rule slice: neither `setCheapest` semantics
nor the ordered loop needs a change to explain Q4. Follow-up program
(not this slice): (1) EXISTS semi selectivity, R71 mechanism
re-anchored at 13490 vs 57066; (2) scan/join widths (R70 unblocks or
scoped equivalent); (3) NL-semi rescan/startup (R69 sister arm).
Q22-outer rides that program as verification; Q13-inner hashed-arm
admission is a separate slice — file under the program, do not
bundle. PG fuzz-tiebreak parity is conditional on a future tie (no
tie exists at 4.26×) — note only, not a slice.
