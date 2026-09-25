# R76 P0 outcome: PASS — a real number emerges (490456.34)

Binary: `/tmp/pp2/r76/goopg-r76probe` (HEAD `0e203b1` + TEMP
env-gated probe, reverted after). Cluster: private clone
`/tmp/pp2/clone-tpch-r65` `:5533`. Query `/tmp/pp2/r71/q4.sql`
EXPLAIN ×2, byte-identical plans (`/tmp/pp2/r76/q4-plan{1,2}.txt` =
R72 shape).

## Probe (SCOPE P0 items 1–2)

At the `tryBuildNLI` SEMI success site, from the FINAL planned
children + catalog only (`defaultCostParams()` noted as the one
approximation — `PlannerSettings` is not threaded to the rewrite):

- outer rel: display cost/rows of the planned outer (15570.66).
- inner probe: `pickIndexCoveringLeadingPrefix`-chosen idx (already
  resolved at the site) + `innerToOuter` bound map →
  `indexPathClauses` / `boundPrefixClauses` →
  `parameterizedIndexSelectivity` / `parameterizedBaserelRows` →
  `costIndexScan` (loopCount = outer rows, relTuples from catalog
  stats) — the production helpers, called directly.
- semi sjinfo + `addNLIPaths` (nil searchCtx, as the spike).

```
R76PROBE semi rows=5 start=0.38 total=490456.34 outer=15570.66 seambase=570.66   (×2 identical)
```

PASS: Total 490456.34 ≥ outer 15570.66; same order as PG's
191195.81 (2.56× — the known upstream rows/width gaps, not this
slice). No BLOCKED piece: every input was assemblable at the site.

## Caveats for P1 (recorded, not solved here)

1. `rows=5` is the PER-PROBE rows (`parameterizedBaserelRows`) copied
   onto the joinrel — wrong for stamping. P1 must set joinrel rows
   to the SEMI output estimate (goopg 57066-shape) before the funnel.
2. `defaultCostParams()` vs statement `PlannerSettings` (GUCs like
   `enable_nestloop`): P1 threads the real cp or measures the delta.
3. `numQualOps` approximated as unbound-movable count (local cond
   `l_commitdate<l_receiptdate` rides the probe `Cond` — count
   consistent with `paramIndexQualOpCount` semantics minus leaf walk).

Temp fully reverted; optimizer builds clean; tree clean.
