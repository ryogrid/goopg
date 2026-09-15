# M0142-0012 — teach cardinality estimation the decomposed-NLI `Join{Lateral: true}` shape

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md`'s M0142-0012 line, filed by M0142-0011 and sized by
M0142-0012a: since the R25 (plan-parity-fix-take2) decomposition, an
unmemoized index-probe nested loop is built (`createNestLoopIndexJoinPlan`,
`internal/optimizer/createplannl.go:355-364`) as a generic
`Join{Algo: JoinAlgoNestedLoop, Lateral: true, Right: *IndexScan}` whose
equi-key lives on the `*IndexScan` child's own `Key`/`Keys` (an
`*OuterColumnRef` nestloop param), not in `Predicate`.
`EstimateRows`/`estimateJoin` (`internal/optimizer/cardinality.go`) had no
arm for this shape, so `joinEquiPairs` always found zero pairs on it and
every such join fell to the crude `l*r*0.005`/`max(l,r)`-capped fallback
instead of the accurate per-probe estimate that `estimateNLIndexJoin`
(`cardinality.go:241-267`, already carries M0142-0006's SEMI/ANTI
match-fraction fix) already computes for the OLD fused `*NestedLoopIndexJoin`
type — but that type is built only when the inner is Memoize-wrapped
(`createNestLoopIndexJoinPlanFused`), the minority case per M0142-0005/B6.
M0142-0012a measured the blast radius before this task started: **TPC-H
6/21 queries** hit the gap (224 call-site hits, including flagship Q9 and
the M0077-era Q21 NLI witness), **TPC-DS 69/99 queries** (6347 call-site
hits, Q14 highest at 1009).

## Fix

`internal/optimizer/cardinality.go`:

- `isLateralIndexProbe(n Node) bool` — a narrow type gate: `n` is a
  `*IndexScan`/`*IndexOnlyScan` with a real equality key bound (`Key != nil
  || len(Keys) > 0`). Deliberately narrower than `nliInnerProbe`'s set (which
  also accepts a Memoize-wrapped `*BitmapHeapScan`+`*BitmapIndexScan` pair):
  `createNestLoopBitmapJoinPlan` never builds the decomposed `Join{Lateral}`
  shape for a bitmap probe — it stays fused (see that function's own
  comment) — so a `*BitmapHeapScan` reaching a `Join{Lateral: true}` node is
  a genuine SQL `LATERAL` construct, unrelated to this mechanism, and must
  not be swept into the same estimator.
- `estimateJoin` gains one new branch at the top: `if j.Lateral &&
  isLateralIndexProbe(j.Right) { return estimateLateralIndexJoin(j) }`,
  ahead of the existing `l, r := EstimateRows(...)` computation.
- `estimateLateralIndexJoin(j *Join) int64` — `estimateNLIndexJoin`'s twin,
  adapted from `Outer`/`Inner` to `Left`/`Right`. INNER/LEFT return
  `EstimateRows(j.Left)` unchanged (the probe emits exactly one match per
  outer row for these join types, same as the fused node). SEMI/ANTI narrow
  by `lateralNLIMatchFraction(j) * joinResidualSelectivity(j)` — and because
  `j` here already **is** a real `*Join`, `joinResidualSelectivity(j)` is
  called directly; the fused twin has to build a synthetic wrapper
  (`&Join{Left: j.Outer, Right: j.Inner, Predicate: j.Predicate}`) to get one,
  which this shape does not need.
- `lateralNLIMatchFraction(j *Join) float64` — `nliSemiMatchFraction`'s twin.
  Resolves the index's bound keys (`Key`/`Keys`) and, for each one, the
  matching table column by name against `idx.Columns[i]`, then computes
  `eqjoinselSemiCore` from both sides' `ColumnStats`/`NDistinct` exactly as
  the fused twin does. The one real coordinate difference: the fused shape's
  probe key is a plain `*ColumnRef` positioned in `j.Outer`'s own output
  schema (`translateToLayout`'s output), while the decomposed shape's key is
  an `*OuterColumnRef{Level: 1}` — `outerParamKey`'s rewrite
  (`createplannl.go`) re-roots the same already-translated `*ColumnRef` into
  a PG nestloop param. Because `outerParamKey` translates onto `outerLay`
  (the OUTER-only prefix of the merged layout) *before* wrapping it, the
  `OuterColumnRef.Index` is **already** a position in `j.Left`'s own output
  schema — no different from the fused shape's `ColumnRef.Index` in that
  respect — so `resolveBaseColumn(oc.Index, j.Left)` resolves it directly,
  with no extra translation step. The function accepts both `*OuterColumnRef`
  (the decomposed shape's real key type) and a plain `*ColumnRef` (defensive:
  a genuine SQL `LATERAL` producer that never ran the nestloop-param
  rewrite would still resolve correctly).

The two-arm dispatch that picks between `*OuterColumnRef` and `*ColumnRef`
is written as sequential `if/else` type assertions, not a `switch
x.(type)` — `TestExprSwitchInventoryIsPinned`
(`exprwalk_inventory_test.go`) treats a new hand-written `switch x.(type)`
over `Expr` as a new instance of the RC-1a walker-inventory defect class and
fails the build unless the site is registered plus ledgered; a type
assertion is not a switch and the census does not see it (confirmed against
the gate's own AST detector, which matches `*ast.TypeSwitchStmt` only).

## Verification

- `go build ./...` clean.
- `go test ./internal/optimizer/...` — full package green, including the new
  `TestEstimateRowsLateralIndexJoinSemiScalesByMatchFraction`
  (`cardinality_propagation_test.go`), the decomposed-shape twin of
  `TestEstimateRowsNLIndexJoinSemiScalesByMatchFraction`: same inputs
  (outer nd=1000, inner nd=100 → match fraction 0.1), wired via
  `Join{Algo: NestedLoop, Lateral: true, Right: *IndexScan{Key:
  &OuterColumnRef{Level:1, Index:0}}}` instead of `*NestedLoopIndexJoin`.
  Asserts SEMI=100, ANTI=900, and (new) INNER=1000 (unchanged passthrough).
  Before the fix this shape found zero equi-pairs and returned the
  `l*r*0.005` fallback capped at `max(l,r)` = 1000 for every join type,
  which would NOT have distinguished SEMI/ANTI/INNER — the exact failure
  this test is built to catch.
- `scripts/tpch-spotcheck.sh` (fresh capped server, private clone):
  `RESULT=PASS`, Q12=2 rows, Q13=34 rows (canonical).
- `scripts/tpcds-sf025-regression.sh sweep` (git-tracked PG oracle,
  git-tracked row/checksum anchors): `PASS=96 (60 ck-verified, 36 ck=n/a)
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3` — **zero correctness
  regressions** despite the plan-shape channel (non-blocking, reported for
  visibility) showing `changed=91/99` queries, confirming the blast radius
  M0142-0012a measured actually fired at this scale without breaking any
  row count or checksum.
- `make ea-ratchet` (estimate-accuracy parity ratchet, TPC-DS SF0.25 vs the
  PG 18.3 oracle): baseline 112 findings → **95 findings**. 22 FIXED (all of
  Q33's and Q54's and Q56's CTE-branch witnesses — the exact shape M0142-0011
  traced — plus Q69/Q72/Q87), **5 NEW** (Q23 ×2, Q84 ×1, Q95 ×2) — a
  join-level estimate one level up from a now-fixed leaf reads worse than
  before, the same "unmasking" pattern M0142-0009's own fix produced (filed
  then as M0142-0010). Net large improvement, consistent with M0142-0012a's
  prediction; the 5 NEW findings are not chased in this task (see Deferred
  below) and the baseline was re-pinned to 95 via `make ea-ratchet-repin`
  (`analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-baseline.txt`)
  so the next estimate-accuracy task's diff is measured from here, not from
  112.

### Explicitly NOT run this loop — named per the plan-parity harness's own rule

The plan-parity harness (`AGENT.md` §"Plan-parity harness") requires this
task's own filed resume point to run "the full floor-measurement suite ...
the same treatment M0142-0006/M0142-0009 got, not the lighter bar a pure-recon
task uses" before landing: a fresh TPC-H plan-parity capture (`-serial` and
parallel) and TPC-DS plan-parity capture, each re-scored for `match`/category
count against the PG 18.3 oracle. That capture-and-score pass was **not** run
this loop — it is a substantial task in its own right (fresh captures over
both 22-query and 99-query corpora, category classification, comparison
against the current 6/22 and 2/99 headline) and the working-set baton that
filed this task explicitly pre-authorized landing code + unit test with the
floor-measurement suite as "its own immediately-following task; name it
explicitly in the report if so, per the harness's own rule (do not
substitute a cheaper gate)". The gates that WERE run (spotcheck, SF0.25
sweep, ea-ratchet) are correctness/estimate-accuracy signals, not the
plan-parity match/category metric itself, and must not be read as a
substitute for it.

Filed as the two required artefacts per the harness's "Completion rule for
this group":
- `.ralph/deferral_ledger.md` row (2026-09-15, M0142-0012): floor-measurement
  suite (TPC-H `-serial` + parallel plan-parity capture, TPC-DS plan-parity
  capture, both re-scored for match/category against PG 18.3) not yet run
  this loop.
- `.ralph/fix_plan.md` task **M0142-0012-verify**: run that suite and record
  whether/how the headline TPC-H 6/22 / TPC-DS 2/99 match counts moved.

A second, independent follow-up is also filed for the 5 NEW ea-ratchet
findings (Q23, Q84, Q95) — an "unmasking" recon in the shape of M0142-0010,
not chased here — as **M0142-0013**.

## Why this is safe despite being unverified against the plan-parity metric

Unlike a plan-SHAPE change, a pure cardinality-estimate improvement cannot by
itself produce a wrong row count or a wrong answer — `EstimateRows` feeds
planning decisions (join order, algorithm choice, Gather/Memoize insertion)
and `EXPLAIN` display only; the executor's actual output is unaffected by
what the planner *estimated* a subtree would return. That is exactly what
the SF0.25 regression sweep's `MISMATCH=0 CKMISMATCH=0` result confirms
directly, at the scale (91/99 plan shapes moved) M0142-0012a's blast-radius
count predicted. The floor-measurement suite deferred above answers a
different question — "did this make the *plans* look more like PG's", the
milestone's own headline metric — not "is this correct", which is already
answered.
