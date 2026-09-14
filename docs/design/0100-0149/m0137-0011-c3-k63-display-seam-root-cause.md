# M0137-0011 — root-causing C3/K63, the second display/estimator seam

Status: accepted (recon task — diagnosis only, no production change)

## Task

`.ralph/fix_plan.md` M0137-0011: root-cause C3 (`02-open-problems.md`), the
general form of which is K63 — *"goopg's EXPLAIN reports a scan cost the
planner did not use"* — traced by R37/R73/R76/R77 to two live symptoms (Q22
display rows and Q12's 4.5x scan-cost understatement) but never diagnosed to a
specific code defect. Per the plan-parity harness's "Way of working", this is
a recon task: measurement plus a design note plus a ledger row, no production
diff.

## Method

R37's own closing lesson governs this task twice over: *"Instrument the term,
never infer it from the sum."* Static reading of `createplan.go` /
`plancost.go` narrowed the search to two candidate mechanisms (a `Project`
column-narrowing wrapper losing the stamp, or a `PathPrebuilt` subtree whose
internal nodes are never individually stamped); neither survived contact with
a live capture. The defect was found only by adding a temporary,
env-gated trace (`GOOPG_STAMP_TRACE`, mirroring R37's own
`GOOPG_SEQCOST_TRACE`/`GOOPG_HJ_TRACE` — added, used, reverted, zero diff
remains — `git diff --stat` on both touched files is empty) to
`stampPlanCost` (`internal/optimizer/plancost.go`) and `explainCostFields`
(`internal/executor/operators_explain.go`), then reproducing R37's exact
capture:

```
go build -o bin/goopg ./cmd/goopg
bench/tpch/setup_goopg.sh   # confirms K63 still reproduces at HEAD, byte-identical to R37:
  Seq Scan on orders    (cost=0.00..43435.00  rows=1500000 width=448)
  Seq Scan on lineitem  (cost=0.00..60299.79  rows=28724   width=550)   <- legacy number, not the 271421.24 the search used
```

Traced with `GOOPG_STAMP_TRACE=1` (private binary, `bench/tpch` 65433 lane
restarted with it and then restarted back to the ordinary binary afterward —
the lane was down at loop start and is left down again):

```
STAMPTRACE stamp  node=*optimizer.SeqScan ptr=0x22c89a1a00e0 pathKind=0 total=43435.00     <- orders leaf
STAMPTRACE stamp  node=*optimizer.Filter  ptr=0x22c7773390a0 pathKind=0 total=271421.24    <- lineitem leaf (Filter{Child: SeqScan})
...
STAMPTRACE render node=*optimizer.SeqScan ptr=0x22c89a1a00e0 CostSet=true  total=43435.00  <- orders: correct
STAMPTRACE render node=*optimizer.Filter  ptr=0x22c7773390a0 CostSet=false rows=28724       <- lineitem: SAME pointer, stamp lost
```

## The defect

`buildInitialRels` (`internal/optimizer/joinsearch.go:433`) wraps every base
relation's leaf node in a `PathPrebuilt` and prices it with `costSeqscan`
(PG-faithful, page cost + qual-eval cost included):

```go
p := newPrebuiltPath(rel, leaf)
scanPages, scanTuples, scanQualOps := baseSeqScanCostInputs(ri, leaf, rows, width)
p.Cost = costSeqscan(cp, scanPages, scanTuples, scanQualOps)
```

`leaf` is whatever node type the FROM-item rewrite produced. A relation with
**no** base-local qualifier (`orders` in Q12 — its only restriction is the
join key) is a bare `*SeqScan`. A relation **with** a base-local qualifier
(`lineitem` in Q12 — `l_shipmode`, the two date comparisons) is wrapped one
level up as `*Filter{Child: *SeqScan}` by the relation-local-filter
attachment pass (M0077-0001, `attachRelationLocalFilters`) — this is
`Filter.LeafLocal`'s documented purpose (`plan.go:1531`).

`stampPlanCost` (`plancost.go:82`) is the single funnel every winning `Path`
passes through on its way to `createPlan`/`createPlanNode`:

```go
func stampPlanCost(n Node, p *Path) {
    ...
    setter, ok := n.(planCostSetter)
    if !ok {
        return          // <-- silently drops p.Cost
    }
    setter.setPlanCost(...)
}
```

`planCostSetter`/`PlanCostCarrier` are implemented by embedding the `PlanCost`
struct. **`SeqScan` embeds it (`plan.go:641`); `Filter` does not
(`plan.go:1531-1569` — it embeds only `searchedTree`).** So:

- `orders`'s bare `*SeqScan` satisfies `planCostSetter` → stamped correctly →
  EXPLAIN reads a real `PlanCostCarrier` → **43,435.00, matches the search
  exactly.**
- `lineitem`'s `*Filter` wrapper does **not** satisfy `planCostSetter` → the
  type assertion fails, `stampPlanCost` returns having done nothing, and the
  correct cost (271,421.24, page cost + all five qual-eval ops) is silently
  discarded. Nobody ever stamps the `*SeqScan` **underneath** the Filter
  either — `stampPlanCost` is called once, on `p.node` (the Filter), never on
  its `.Child`.
- At EXPLAIN time, `explainCostFields` fails the same `PlanCostCarrier`
  assertion on the very same `*Filter` object (confirmed by pointer identity
  in the trace) and falls to `optimizer.DeriveLegacyDisplayCost`.
  `legacyDisplayChildren` (`plancost.go:210`) does have a `case *Filter:
  return []Node{p.Child}` arm, so it recurses into the child `*SeqScan` — but
  that child was *also* never stamped (see above), so the recursion bottoms
  out in `DeriveLegacyDisplayCost` on a **leaf** node with `EstimateRows`
  reading the *unfiltered* table row count. The default branch's
  `perRow := cpuTupleCost * rows` then gives `0.01 * 6,001,255 = 60,012.55`
  (the ~287 residual to the observed 60,299.79 is `TupleWidth`/rounding
  noise in the surrounding sum, not a second defect — not pursued further,
  as this recon's job is the mechanism, not the last two significant
  figures).

This is a **structural gap in one type's method set**, not a control-flow bug
in `createPlan`, `DeriveLegacyDisplayCost`, or the join-costing search
itself — all three of those already handle the case as well as they can
*given* that `*Filter` cannot carry a cost. It is the general form K63 named:
**any base relation that reaches `buildInitialRels` with a base-local filter
wrapper loses its priced cost at the display seam**, which is a wider blast
radius than R37 established (R37 pinned one query's one symptom; this
extends it to "every base-local-filtered scan in the corpus", since
`attachRelationLocalFilters` is unconditional whenever a relation carries a
restriction that cannot be pushed into an index/bitmap probe).

## Why this is display-only (confirms R37's K62 correction, does not reopen it)

The join search itself never reads through `stampPlanCost`'s output — it
costs candidates directly off `Path.Cost` (`p.Cost = costSeqscan(...)` above,
consumed by `addHashJoinPath`/`hashJoinCost` on the `Path`, not on the
`Node`). So the defect cannot move a plan-choice category (`join-method`,
`join-order`, etc.) — R37's K62 correction ("the search saw 271,421.24 for
lineitem... the mixing happens only in the RENDERING") stands and this task
sharpens *why* rendering mixes them rather than contradicting it. What it
**does** corrupt, per K63's original filing, is every downstream artefact
that reads `EXPLAIN`'s printed cost as ground truth: `make plan-gate
MODE=semantic-cost`, the estimate-audit tables, and any human or automated
reader comparing a rendered scan cost against PG's.

## Fix shape (not implemented here — recon task)

The minimal, structurally honest fix is to give `Filter` its own `PlanCost`
embed (mirroring `SeqScan`) and have `stampPlanCost` reach it exactly the way
it already reaches every other carrier — no special-casing, no recursion
change needed, since `legacyDisplayChildren`'s `*Filter` arm already exists
for whatever fraction of Filters remain unstamped (a `Filter` produced by a
later rewrite pass rather than `buildInitialRels`, for instance). This is
listed as the resume point in the ledger row below rather than attempted
here, per the recon-task boundary ("filing only, no code change" — the same
discipline M0137-0012 already applied to B6/B8/B10/O15).

## What is NOT yet established

- Whether any OTHER `Node` type reachable from `buildInitialRels`'s `leaf`
  (besides bare `*SeqScan` and `*Filter`) also lacks the embed — e.g. an
  `*IndexScan` base-rel leaf that additionally carries a residual filter.
  Not audited in this task; the resume point below should grep every
  `createPlanNodeUnpriced` `PathPrebuilt`-eligible leaf shape before fixing.
- The exact ~287-unit residual between the derived legacy formula (60,012.55)
  and the observed render (60,299.79) — cosmetic to this diagnosis, not
  chased.
- Corpus-wide blast radius (how many of the 22 TPC-H / 99 TPC-DS queries
  carry a base-local-filtered scan whose display is therefore wrong). Not
  measured; the ledger row's resume point is the natural place to add this
  count if the fix is ever scheduled.

## Filed as

`.ralph/deferral_ledger.md` row `m0137-0011-filter-node-missing-plancost-embed`.

## Gates

Recon-only task — no production diff. `git diff --stat` on both temporarily
edited files (`internal/optimizer/plancost.go`,
`internal/executor/operators_explain.go`) is empty (instrumentation added and
fully reverted in the same loop). `go build ./...` clean at HEAD after the
revert. No `go test` gate is implicated — no test-visible code changed.
