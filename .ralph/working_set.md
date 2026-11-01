# Working Set — loop end 2026-11-01 (loop #17: M0148-0001c slice (b) LANDED; census consumed)

- Task: M0148-0001c slice (b) (item 10 suite). Multi-target scalar group agg —
  `select max(unique2), generate_series(1,3) as g from tenk1 order by g desc` — now plans
  `Sort → (InitPlan 1: Limit → IOS Backward) → ProjectSet → childless Result`.
  29 → 28 hunks (hunk 1016 closed byte-exact, no new hunks). Code `e1f19ff73`,
  records `d36d05876`, census `be2248839`; HEAD `be2248839` pushed, tree clean.
- Files: `internal/optimizer/planner.go` (gate `aggIdx` scan + `strictMinMaxTarget` +
  `minmaxTListSurvivor`; multi-target assembly = childless Result over the surviving tlist +
  `ProjectSet{SrfCols, OtherExprs}` above; `wrapMinMaxOrderByDistinct` re-scan/label/raw-name
  fallback/ordinal decline), `internal/executor/operators_explain.go` (new `*optimizer.ProjectSet`
  arm in `resolveKeySourceDepth`), tests `minmax_distinct_orderby_test.go` (the S19 decline pin
  rewritten into landing + 5 scope bounds) and `explain_correlated_minmax_test.go` (new pin),
  design doc `0148-0001c-…` (slice (b) section) + README row, fix_plan 0001c, 3 ledger rows,
  `analysis/m0146/m0146-0014/routing-20261101f/`.
- Key symbols: `rewriteMinMaxAggregates` (gate ~12618, assembly ~13030); `strictMinMaxTarget`
  (can_minmax_aggs shape); `minmaxTListSurvivor` — dispatches on `*FuncCall` FIRST, because
  `isConstantExpr` reads a 0-arg call (`count(*)`) as constant; `SrfCol` + `OtherExprs` nil-slot
  marker; `openSelectSrfMode` zip executor (unchanged); the ProjectSet chase arm (synth
  `optimizer.FuncCall{generate_series}` → S18 parens); `LevelInitPlansOf` descends the ProjectSet,
  so `InitPlan 1` prints on the Sort.
- Hypothesis/Findings: planagg gates per-AGG, not on tlist length; `create_minmaxagg_plan` =
  `make_result(tlist, quals, NULL)` keeps every non-agg entry; the jointree is EMPTIED by
  `build_minmax_path` (the RTE moves into the InitPlan subquery) — that is why extras must be
  Var/sublink/aggregate-free. TPC-DS/TPC-H carry no multi-target min/max tlist ⇒ fires=none.
  Remaining 0001c: (c) inheritance-tree Merge Append under the Limit, hunk 1066 (72→38 lines);
  (d) correlated `distinct min` sharing one InitPlan 1 + SubPlan 2, hunk 1145 (goopg declines
  the correlated rewrite at M0146-0114's gate).
- Next step: **M0148-0001c slice (c)** — inheritance/appendrel min/max: planagg accepts a single
  RangeTblRef that is an appendrel (planagg.c:121-137) and `build_minmax_path` builds a path per
  child, so `create_minmaxagg_plan` emits Merge Append of per-child index paths under the Limit.
  goopg's gate currently declines partitioned parents (planner.go ~12649 `len(tbl.PartitionKey) > 0`)
  and inheritance trees (~12652 `AccessibleInheritanceChildren`). Baseline `aggregates` = 28 hunks.
- Top-residual: aggregates 28 hunks; largest single block = parallel-agg 7 hunks 1667-1756
  (=0001i). Plan-parity census: EXHAUSTED at routing-20261101f (fires=none,
  99/99 + 99/99 + 22/22, all categories 0).
- Skip-rationale (bypassed items 0-9; item 10 selected): 0 channel (P0-E8 `[x]`), 1/2/2a
  discharged, 3 channel (the `M0146-0005dp` banner mention has no filed entry; 0229/0229a `[x]`),
  4-8 channel, 9 M0147 channel + 2 owner-gated `[!]`, 10: M0122-0015h self-records
  `Rank: below item 10` (S7 carve-out) → live M0148 queue head 0001c. Five open M-NIGHTLY
  P0-E9/E10/E11/E13/E15 = ESCALATION, OWNER PLACEMENT NEEDED. Slice (c) chosen over the
  1667-1756 block: M0148 discipline advances the suite queue in expected-line order and 1066 is
  the next queued hunk.
- Gates run: optimizer + executor units PASS (only the DST `timetz` flake — a pure COPY-decode
  test whose EDT assertion is past the 2026-11-01 transition); runner 29→28; tpch-spotcheck
  Q12=2/Q13=35 stamped; tpcds-sf025 sweep stamped (plan-shape 99/99); acceptance arm off
  24 MATCH stamped; fireset gate SF0.25+SF1 PASS fires=none (G9 IN-SCOPE: optimizer staged);
  pgbench smoke via the hook (both commits); census DUE → consumed (routing-20261101f,
  fireset stamp PASS on code_tree 1f1ba3af18a3).
- In-flight: none.
