# Working Set — loop end 2026-11-01 (loop #16: M0148-0001c slice (a) LANDED, census exhausted)
- Task: M0148-0001c slice (a) (item 10 suite). `(InitPlan N).col1` rendering at EXPLAIN
  Sort/Group Key sites above a min/max-rewritten Result. 33 → 29 hunks (950/967/984/1001
  closed byte-exact). Code `31b75104b`, records `9c7b3bd61`, census `047b86d8d` pushed.
- Files: `internal/executor/operators_explain.go` (Result arm in `resolveKeySourceDepth`
  lands on the initplan SubqueryExpr target; `chaseKeyExprToInitPlanRef` for computed keys;
  new composite arm in `sortKeyParts`), test pin `TestExplainKeyOverMinMaxResultInitPlanRef`
  in `explain_correlated_minmax_test.go`, design doc `0100-0149/0148-0001c-initplan-ref-key-rendering.md`
  + README row, fix_plan 0001c slice progress + DST-flake note, `analysis/.../routing-20261101e/`.
- Key symbols: `resolveKeySourceDepth` Result arm (M0146-0129, extended);
  `optimizer.SublinkIsInitPlan` (IsNonCorrelated||ParamInitPlan); `chaseKeyExprToInitPlanRef`
  (fail-closed: only columns landing on an InitPlan sublink); formatExprQual SubqueryExpr arm
  (R65 B, prints `(InitPlan N).col1`); S18 `forceParen` at the sites; `claimLevelInitPlans`
  numbering dedupe (assign hits r.num, InitPlan prints once under level top before `-> Result`).
- Hypothesis/Findings: goopg's `rewriteMinMaxAggregates` (planner.go:12608) ALREADY produces
  the InitPlan+Limit+IOS-Backward shape; 0001c was label-rendering, not planning. Remaining
  0001c slices: (b) SRF multi-target hunk 1016 (`max(unique2), generate_series(1,3)` —
  multi-target rewrite + ProjectSet + `(generate_series(1, 3))` label entangled w/ 0001f),
  (c) inheritance Merge Append 1066, (d) correlated `distinct min` InitPlan/SubPlan sharing
  1108 (goopg rejects correlated rewrite at M0146-0114 gate; PG shares one InitPlan 1+SubPlan 2).
  New flake documented: TestCopyFromZonelessTimetzTakesSessionZone (US DST ended 2026-11-01,
  date-sensitive assertion, fails on clean HEAD, self-heals inside DST period).
- Next step: **M0148-0001c slice (b)** — multi-target scalar group agg: extend the rewrite
  gate (planner.go:12608 rejects multi-target) toward planagg's multi-agg tlist +
  `Result{Targets:[init, init, SRF]}` + ProjectSet shape; PG oracle planagg.c
  preprocess_minmax_aggregates (targetlist scan loop). Runner baseline `aggregates` = 29 hunks.
- Top-residual: aggregates 29 hunks; largest single block = parallel-agg 7 hunks 1667-1756
  (=0001i). Plan-parity census: EXHAUSTED at routing-20261101e (fires=none,
  22/22+99/99+99/99, all categories 0).
- Skip-rationale (bypassed items 0-9; item 10 selected): 0 channel (P0-E8 `[x]`), 1/2/2a
  discharged, 3 channel (M0146-0229/0229a `[x]`), 4-8 channel, 9 M0147 channel+2 owner-gated
  `[!]`, 10: M0122-0015h self-records `Rank: below item 10` (S7 carve-out) → live M0148 queue
  head 0001c. Five open M-NIGHTLY P0-E9/E10/E11/E13/E15 = ESCALATION, OWNER PLACEMENT NEEDED.
  Slice (b) chosen over the 1667-1756 block: M0148 discipline advances the suite queue in
  expected-line order and 1016 is the next queued hunk.
- Gates run: executor units PASS (only DST flake, clean-HEAD reproduced); runner 33→29;
  tpch-spotcheck Q12=2/Q13=35 stamped; tpcds-sf025 sweep stamped (plan-shape 99/99);
  acceptance arm off 24 MATCH stamped; fireset-scope OUT (internal/executor, G9); pgbench
  smoke via hook; census DUE → consumed (routing-20261101e, tpcds-fireset stamp PASS on
  code_tree 561211ba48a5).
- In-flight: none.
