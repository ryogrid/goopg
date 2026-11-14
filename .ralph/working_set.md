# Working Set — loop end 2026-11-14 (census routing-20261114o IDENTICAL; c3a5 (a) slice 5 LANDED 3c0e01d75)

- Anomaly: none (prev-loop check OK).
- Nightly triage: items 0. Nothing filed.
- S9: DUE → routing-20261114o (s9ct, BASELINE_REV=a349f61e3) IDENTICAL ×3, committed ebb9df844.
  - 3c0e01d75 touches the planner, so the next census is likely DUE.
- Task: M0148-0001c3a5 item (a) slice 5, landed as 3c0e01d75.
  - Files:
    - planner.go: planMinMaxSubqueryViaSearch, minmaxPlanPresorted, the call in buildMinMaxInitPlan before the Sort fallback;
    - operators_explain.go: indexOnlyCondDropRelabel, formatIndexOnlyCondRelabeled;
    - minmax_rewrite_test.go: 3 tests → assertMinMaxNoSortFallback;
    - saop_range_bounds_test.go: TestMinMaxSubqueryPlannedBySearch.
  - Findings:
    - The min/max body planned by the search matches PG (costs too).
    - A body needing a Sort declines to the Aggregate (PG-faithful).
    - The search plan fixed a subselect nested-sublink error.
    - An IOS prints varchar keys bare; Index Scan / Bitmap keep the relabel (verified on PG).
  - Movement: subselect 2462→2443, partition_prune 6778→6772; aggregates still PASS.
- parted_minmax remaining:
  1. The one-member appendrel collapses to the bare child scan (planScanRangeVar single-leaf path).
     - The parent alias is lost (`parted_minmax1 parted_minmax`).
     - It is not a search leaf: `where a > 0 order by a limit 1` plans Sort + `Index Cond: (a >= 0) Filter: (a > 0)` where PG has an IOS. Investigate where that rule-based scan comes from.
  2. Members with quals: addOrderedPartitionAppendPaths refuses Filter members, so per-member index clauses are needed; min/max with members>0 should also go through the search.
- Next step: (1) — find the single-partition collapse in planScanRangeVar (planner.go ~5262-5336) and probe on scratch PG 5545 vs goopg 5533 with tmp/pmm8.sql.
- Top-residual: M0148 residues. Open children: c3a5, c3a6, c3a7, c3a8, c3b, g1, f3, e2, h1, i2, k1; then the M0148-0002 copy suite.
- Skip-rationale (bypassed items 0-9; item 10 selected):
  - Items 0-9: M0146-0014(channel; census routing-20261114o done this loop); M0146-0042(channel); M0146-0009(channel); M0146-0007(below-item-10); M0146-0010a(owner-held [!]); M0146-0035(owner-held [!]); M0140-0007(owner-held [!]); M0142-0005(owner-held [!]); M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003).
  - Item 10: M-NIGHTLY open items are below-item-10 or S2 awaiting owner (P0-E9..E16, E24, E25, E26). Then M0148 in file order → c3a5.
- Gates run (all PASS):
  - units (after fixing 3 Sort-fallback-pinned tests), tpch-spotcheck, sf025 same=99;
  - fire-set c3a5a5 fires=none ×3; acceptance 24 MATCH; TestPort_Isolation ok (-timeout 55m, 526s);
  - regress A/B tmp/regress-c3a5a5 vs tmp/regress-c3a5a4.
- In-flight: none. Scratch PG (5545) RUNNING. goopg probe server (5533) stopped. Restarting it hits P0-E24: drop leftover partitions.
