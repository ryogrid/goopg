# M0142-0005e — stamp the real path cost on NLI probe nodes

Kind: impl. Parent: M0142-0005c.

Status: implemented + verified on a TPC-H clone (DONE 2026-09-19).

## The bug

`EXPLAIN` printed `DeriveLegacyDisplayCost` on nested-loop index-probe
nodes — `Index Scan … (cost=0.00..0.18 rows=18)` on TPC-H Q10 — while the
path that won the search was costed at `4.59`. Plan captures therefore
attributed wrong costs to every probe, which is what made the M0142-0005c
recon look like a costing anomaly in the first place.

Mechanism: `createPlanNode(innerPath)` does call `stampPlanCost`, but the
stamp lands on the **outermost emitted node**. When the parameterised leaf
carries quals, that node is a leaf-local `*Filter{*IndexScan}`; the NLI
arms then run `absorbableLeafCond`, move the predicate into
`IndexScan.Cond`, and keep the **bare `*IndexScan`** — the carrier of the
stamp was discarded with the wrapper. Three arms share the defect:

- `createNestLoopIndexJoinPlan` — decomposed lateral `Join` (the R25
  shape), probe under `Join.Right`;
- `createNestLoopIndexJoinPlanFused` — `NestedLoopIndexJoin` with
  `InnerMemo`; the `*Memoize` node was also never stamped (`PathMemoize`
  is represented as a field, not a funnel-emitted node);
- `createNestLoopBitmapJoinPlan` — `*BitmapHeapScan` unstamped after the
  unwrap, and its `*BitmapIndexScan` child built inside the arm never
  priced at all.

A fourth producer had the same seam by construction: `priceSemiProbe`
(`internal/optimizer/nlipricesplice.go`) replays the NLI arm for
rewrite-built semi/anti joins and already fabricates the arm-priced probe
`Path`, but stamped only the join node — `x.Inner` kept the legacy
display.

## The fix

Stamp each emitted node with **its own path's** cost, the numbers PG
prints on the same nodes:

- decomposed arm: `stampPlanCost(is, innerPath)` on the unwrapped probe
  (a no-op re-stamp when no `*Filter` wrapped the leaf);
- fused arm: `stampPlanCost(is, innerPath)` plus
  `stampPlanCost(nli.InnerMemo, memoPath)`;
- bitmap arm: `stampPlanCost(bhs, innerPath)` +
  `stampPlanCost(bis, idxPath)`;
- semi/anti splice: `stampPlanCost(x.Inner, probePath)` beside the
  existing `stampPlanCost(x, p)`.

No path-selection input changes — `PlanCost` is display/provenance only,
written after the search picked winners. Expected plan movement: none.

## Verification

- Unit: `TestNLIInnerScanCarriesProbePathCost` and
  `TestNLIFusedInnerAndMemoizeCarryPathCosts`
  (`internal/optimizer/createplannl_test.go`) pin that the unwrapped probe
  carries the `PathIndexScan`'s own `{startup, total, rows}` and
  `InnerMemo` carries the `PathMemoize`'s cost.
- Live: TPC-H Q10 `EXPLAIN` on a private `:5533` clone went from
  `cost=0.00..0.18 rows=18` (legacy derive) to `cost=0.38..8.49 rows=5`
  (the winning path's startup-includes-descent cost and per-probe rows,
  the quantities PG prints).
- TPC-DS SF0.25 plan captures: every `Memoize … (cost=0.00..0.02)` /
  `Index Scan … (cost=0.00..0.01)` pair now prints real costs
  (`0.25..0.44` over `0.25..0.44`, …); the sweep's shape channel correctly
  reports 81 text-changed plans with zero verdict changes.

## Gates

`go test ./internal/optimizer/` + `./internal/executor/`, units suite,
tpch-spotcheck (Q12=2, Q13=34), tpcds-sf025 sweep (PASS=96 MISMATCH=0),
tpch-acceptance-arm 24/24 value-MATCH vs `tmp/acceptance-base.txt` — all
stamped on the staged index.
