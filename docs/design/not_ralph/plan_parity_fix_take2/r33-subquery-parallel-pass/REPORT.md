# R33 — Report: post-cache parallel pass recurses into uncorrelated sublinks (K44)

*Slice 2 of the K37 campaign. Design: `DESIGN.md` (REJECT →
revised → REJECT → revised → APPROVE-WITH-NOTES, notes applied;
design commit `b03657c39`).*

## Change

`internal/optimizer/subquery_parallel.go` (new), `parallel.go`
(`MaybeAddGather` → `maybeAddGatherInner` + `graftTop` at the two
non-strip exits), `unnest.go` (Result / IndexScan.Cond /
IndexOnlyScan arms for `walkPlanExprs`), `subquery_pass_test.go`
(8 tests), `exprwalk_inventory_test.go` (3 classifier pins),
`.ralph/deferral_ledger.md` (pin row).

Mechanism: after the top-tree verdict, the pass collects eligible
sublinks (`IsNonCorrelated`, `Plan != nil`, no `Args`, four kinds)
via `WalkPlanExprs` — which never descends into sublink interiors,
so operand-nested sublinks are placement-invisible by construction —
and grafts parallelised Plans back copy-on-write (plan + expr
levels, Plan-pointer seen-set preserving DAG aliasing). Each sublink
gets the full pass (own safety verdict, own partial-agg sizing).
Safety descends into nested sublink Plans via `walkExprRefs`/
`scopeDescend` (`subtreeHasUnsafeNodeDeep`). Nesting rule: at most
one Gather level per ancestor chain (siblings unaffected).

PRICED-VERDICTS-ONLY GATE (found during implementation): the first
build over-admitted plain-scan Gathers over 31-row/1-row subplans
(Q6 InitPlan 1, Q14 InitPlans 2/4 — the EXTRA direction). An outcome
is accepted only with a fresh split (`hasFreshSplit` via
reflection-complete `planChildNodes`): every new Gather rides a
priced `partialAggSplitPays` verdict. Sort-merge, Agg-over-Gather,
and plain-scan sublink shapes stay serial pending the
cost-comparison round.

## What it fixed

- TPC-DS Q9: 15 serial InitPlans → 15× `Finalize (Hash)Aggregate →
  Gather (Workers 3) → Partial → Parallel Seq Scan` — PG's shape.
  9 s → 2 s on the sweep.
- TPC-DS Q44: 2 InitPlans split (PG-conformant placement).
- TPC-H Q22: InitPlan 1 splits (PG parallelises it too).
- Q6/Q14 over-admission caught by the gate pre-commit; both back to
  serial, matching PG.

## Gates (all 2026-09-09, final code)

- Units: `RALPH_PRECOMMIT_SCOPE=units` pass; optimizer + executor
  suites pass (incl. 8 new tests: split, correlated/Args/
  temp-interior/operand-nested/non-aggregate-root/under-Gather
  refusals + walker-coverage pin).
- TPC-H spotcheck: Q12/Q13 PASS.
- SF0.5 sweep: `PASS=95 MISMATCH=0 CKMISMATCH=0`; plan-shape 98
  same / changed Q9; Q9 faster, no verdict changes.
- Parity A/B (base binary vs r33, identical data, pinned env):
  TPC-DS only Q9+Q44 move (both PG-conformant); TPC-H only Q22
  moves (PG-conformant).
- Live probes: EXPLAIN ANALYZE Q9 — 10 executed InitPlans show
  `calls=1 rebuilds=1 misses=1` + `Workers Launched: 3` (5
  CASE-short-circuited, correctly dataless); hand-built correlated
  subquery gains no inner Gather while its top parallelises.

## Evidence

- `/tmp/pp2/k37-ds-r33c.plans.txt`, `/tmp/pp2/tpch-r33.txt` (A/B
  post-shapes); `/tmp/pp2/k37-ds-all-r32.plans.txt` (C-19h clincher)
- Sweep: `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260909-171243.txt`
