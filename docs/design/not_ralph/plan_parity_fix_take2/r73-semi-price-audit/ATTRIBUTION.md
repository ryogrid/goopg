# R73 P0 verdict: display seam (no planning site produces 570.66)

Binary: `/tmp/pp2/r73/goopg-r73attr` (HEAD `e010cea` + TEMP env-gated
stderr, reverted after). Cluster: private clone
`/tmp/pp2/clone-tpch-r65` `:5533`. Query `/tmp/pp2/r71/q4.sql`
EXPLAIN ×4 total, all byte-identical to `/tmp/pp2/r72/q4-plan1.txt`.

## Verdict table (SCOPE P0 items 1–3)

| question | answer | evidence |
|---|---|---|
| PATH-level SEMI cost? | NO SEMI path is ever filed — zero `addPath` calls with `Jointype==JoinSemi` (any Kind) | `R73PATH` silence over 4 EXPLAINs (stderr capture proven live by `R73EXPL`) |
| DISPLAY-level SEMI cost? | carrier-UNSET `*NestedLoopIndexJoin`, fallback prices it childless: 0 + 0.01×57066 = 570.66 | `R73EXPL semi-NLI carrier-UNSET`; `legacyDisplayChildren` (`plancost.go`) has no NLI arm → nil children → default arm Total = childTotal(0) + perRow |
| Producing site? | neither `addNLIPaths` (`joinpathsnli.go:341`) nor `addNestLoopPath` (`pathgen.go:157`) runs for Q4's EXISTS-SEMI; the node is legacy-built, unpriced | path silence + carrier-UNSET jointly |

## Mechanism

1. Q4's EXISTS→SEMI never enters the search (no SEMI path filed —
   the parameterised-inner NLI arm at `joinpathsnli.go:341` does not
   fire for it; the plain arm at `pathgen.go:157` is correctly
   skipped for a parameterised inner).
2. The emitted `*NestedLoopIndexJoin` bypasses `createPlanNode`'s
   `stampPlanCost` funnel (`createplan.go:50`) — carrier unset
   (`CostSet=false`).
3. `explainCostFields` (`operators_explain.go:2757`) falls to
   `DeriveLegacyDisplayCost`, whose child walker lacks the NLI arm —
   the semi prices as a childless node (570.66 = pure per-row charge
   on 57066 rows).
4. Upstream elections price on the seam number: the grouping seed
   reads `legacyDisplayCostOf(child)` (`groupingpaths.go:77`), so
   R72's hashed-1426.71/sorted-6077.69 contest sits on a 570.66 base
   that no planner computed.

## SCOPE P1 branch taken: display seam

NO cost-model fix in this slice (nothing mis-transcribed at a
planning site — there is no planning site). Per SCOPE:

- R72 P2 correction: goopg's semi "price" 570.66 is not a planner
  price; the election priced the seam. The 4.26× grouping gap itself
  (agg/sort terms on identical inputs) stands as measured.
- Re-scope pointer: Q4's SEMI needs search admission (a real filed
  NLI path the elections can price) or NLI-node pricing. Either is a
  producer-scope change, not a term fix — file as the next program
  item, do not bundle here.
- PG inequality holds vacuously for goopg today (PG SEMI ≥ outer
  always, `costsize.c:3267/:3307/:3349/:3412`; goopg has no SEMI
  price at all).

Temp instrumentation fully reverted; `internal/optimizer` +
`internal/executor` build clean; tree clean.
