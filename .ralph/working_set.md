# Working set — loop 19 (2026-09-27), CLOSED

Task: M0146-0005x — `subquery_push_qual` aggregate arm +
tlist-regime-aware `trivial_subqueryscan` (TPC-DS Q34/Q73 witnesses,
filed by the slice-24 closeout).

LANDED + PUSHED: `96ad5efd3` on plan-parity-with-pg-take2-ralph2; all gates
PASS-stamped against the staged index (units 44 pkgs, tpch-spotcheck
Q12=2/Q13=33, sf025 sweep 96 PASS/0 err, acceptance arm 24/24 MATCH,
fireset `introduced=none` both scales, fires={Q2 Q21 Q34 Q38 Q39 Q44
Q46 Q59 Q65 Q68 Q73 Q79 Q87} — the label-strip + pushdown set; every
fire executes PASS on both arms).

Final state:
- `pushQualsIntoSubqueryLeaf` (internal/optimizer/subquerypushdown.go)
  sinks leaf-local-safe conjuncts into a `Filter` directly above the
  leaf subquery's `*Aggregate` — PG's `subquery_push_qual` havingQual
  arm. Rebases through `*Project`/`*Sort`/`*IncrementalSort`/`*Filter`
  passthroughs (bare-ColumnRef project targets only); adopts the
  aggregate's own output name so `expandAggOutputRef` renders
  `count(*)`. Fail-closed on volatile/correlated/sublink/nullable-side/
  non-passthrough; shallow-copies wrappers, shares the agg subtree
  (PartialSource linkage preserved), never mutates on decline.
- `subqueryQualPushdownSafe` uses `walkExprRefs`+`scopeVeto` (the
  exprwalk inventory gate rejects new hand Expr switches).
- `stripTrivialSubqueryScans` (subqueryscan_strip.go) gained the
  createplan-flags regime walk: breaker (Join, NestedLoopIndexJoin,
  Aggregate, ProjectSet → bare 0/LABEL/IGNORE → physical tlist → strip
  qual-free regardless of consumption), reset (Sort, IncrementalSort,
  Memoize, WindowAgg, Gather, GatherMerge, RecursiveUnion →
  EXACT/SMALL → pathtarget → consumption-identity decides), passthrough
  (Limit, LockRows, Unique/Distinct, Project, Filter → propagate),
  region boundary restarts EXACT. `NestedLoopIndexJoin` was the missed
  breaker — it renders "Nested Loop" and hid under the label.
- Live: Q34/Q73 = PG's bare `GroupAggregate` + `count(*)` filter, no
  label; counts 87/0 = oracle. Q8 `a1` label retained. All regime cells
  verified against PG 18.3 :65438 (incl. `u.c+1` keep — the scan is
  projection-capable so a computed select list is the scan's own
  tlist, hence `*Project` is NOT a breaker).
- Residue ledgered: non-aggregate `subquery_push_qual` arm (jointree
  re-entry, a different mechanism); volatile-qual pushdown (PG admits,
  goopg declines); merge-join-with-sortkeys-over-leaf cell
  (unmodelable at node-kind granularity, no corpus consumer).
- Files: internal/optimizer/subquerypushdown.go(+test),
  joinsearchseam.go (seam hook), subqueryscan_strip.go,
  subqueryscan_leaf_test.go (regime matrix), executor
  subqueryscan_explain_test.go + explain_alias_source_keys_test.go
  (re-pinned keep cases to pathtarget-regime shapes); design
  docs/design/0100-0149/m0146-0005x-subquery-push-qual-agg-arm.md;
  analysis/m0146/m0146-0005/slice25/.
- Nightly triage: AI-20260927-002707 — IsolationEvalPlanQual PASSes at
  HEAD (stale); IsolationReadWriteUnique4 + IsolationTemporalRangeIntegrity
  FAIL at HEAD — filed (MVCC/SSI, outside commit gate, pre-existing).
- Scratch: tmp/fireset-m0146-0005x (copied to analysis/),
  tmp/arm-on-m0146-0005x.txt, tmp/m0146-0005v-data-tpcds-sf025 clone
  server STOPPED.

Next loop: fix_plan banner order — M0146-0005 umbrella remains `[ ]`;
child family now a–x. Residual candidates surfaced: the non-aggregate
`subquery_push_qual` arm (ledgered under 0005x), M0146-0010 Materialize,
the sorted-input `Partial GroupAggregate` arm (0016 residue), and the
filed testport SSI divergence (outside the optimizer stream).
