# M0137-0015 — embed `PlanCost` in `optimizer.Filter`

Status: accepted

## Task

`.ralph/fix_plan.md` M0137-0015: land the one-line fix M0137-0011 root-caused
and named but did not implement (recon-task boundary) — `optimizer.Filter`
does not embed `PlanCost`, so `stampPlanCost`'s `n.(planCostSetter)` assertion
silently fails on every base-local-filtered scan, and EXPLAIN falls back to
`DeriveLegacyDisplayCost`'s cruder formula instead of the real cost the search
computed. Closes C3/K63, the display seam that "corrupts `plan-gate
MODE=semantic-cost` and every estimate audit" (fix_plan's own wording).

## The fix

```go
type Filter struct {
	// PlanCost carries the search's cost for this node (plancost.go). ...
	PlanCost
	// searchedTree: ...
	searchedTree
	...
}
```

Mirrors `SeqScan`'s embed (`plan.go:641`) exactly, per M0137-0011's "Fix
shape" section. `PlanCostCarrier`/`planCostSetter` are satisfied by embedding
alone — no other code changes: `stampPlanCost` (`plancost.go:82`) already
funnels every `PathPrebuilt`/`PathIndexScan` node through the same
`n.(planCostSetter)` assertion, and `legacyDisplayChildren`'s `*Filter` arm
(`plancost.go:210`) already exists for whatever fraction of `Filter`s a later
rewrite pass still leaves unstamped (M0137-0011 built that arm's correctness
into its diagnosis, it was just unreachable because no `*Filter` ever
satisfied `PlanCostCarrier` in the first place).

## Verification

**Unit test** (`internal/optimizer/createplan_test.go`,
`TestCreatePlanNode_StampsCostOnFilterWrappedPrebuiltLeaf`): builds a
`Filter{Child: SeqScan}` as a `PathPrebuilt`'s wrapped node with a distinct
`Cost{Startup, Total}`, calls `createPlanNode`, and asserts the returned
`*Filter` (not the child `*SeqScan`, which PG would price on the rel rather
than a wrapper) now carries that exact cost via `PlanCostInfo()`. This pins
the mechanism `stampPlanCost` relies on rather than any one query's numbers.

**Live reproduction of M0137-0011's exact symptom.** Built a private binary,
ran it against the `bench/tpch` SF=1 clone (port 5581, `tmp/goopg-spotcheck-tpch-data`
— the shared `:65433` lane was never touched), and re-ran M0137-0011's own Q12
EXPLAIN (`max_parallel_workers_per_gather=0` to force the serial path, per
`goopg_tpch_bench_plans_are_all_parallel` — the parallel path renders the qual
as an annotation on the driving scan, not through this seam):

```
before (M0137-0011's capture, analysis/m0137-0011/m0137-0011-q12.plans.txt):
  Seq Scan on lineitem  (cost=0.00..60299.79 rows=28724 width=550)   <- legacy DeriveLegacyDisplayCost fallback

after (this fix, live):
  Seq Scan on lineitem  (cost=0.00..271421.46 rows=46885 width=550)  <- the search's own costSeqscan number, now rendered
```

(The row count/date-literal differ slightly from the 2026-09-06 capture — a
year's worth of unrelated planner changes sit between the two captures — but
the mechanism is the one under test: the rendered cost is no longer the
`cpuTupleCost × unfiltered rows` legacy fallback, it is the Filter's own
stamped `PlanCost`.)

## Corpus-wide blast radius (M0137-0011 left this unmeasured)

M0137-0011's "What is NOT yet established" section named this as the natural
follow-up. Counted directly from the committed PG-oracle plan captures
(`bench/tpch/plans-pg/*.txt`, `bench/tpcds/plans-pg/*.txt` — no server
needed, this counts `Filter:` lines attached to a base scan in PG's own
reference plans, which is exactly the shape `attachRelationLocalFilters`
reproduces on the goopg side for the same query):

- **TPC-H: 20 of 22** queries carry at least one base-local-filtered scan.
- **TPC-DS: 85 of 99** queries carry at least one base-local-filtered scan.

Before this fix, EXPLAIN's cost column on the majority of both corpora's
queries was `DeriveLegacyDisplayCost`'s crude formula wherever a base
relation had a local qualifier — corrupting `make plan-gate MODE=semantic-cost`
and any estimate-audit table entry that reads a rendered scan cost as
ground truth, exactly as the fix_plan task text states.

## Also fixes: the `*IndexScan`-with-residual-filter case M0137-0011 flagged as unaudited

M0137-0011's "What is NOT yet established" also asked whether an `*IndexScan`
leaf carrying a residual filter (`Filter{Child: *IndexScan}`, rebuilt fresh by
`scanLeafFor`'s `rewrap` closure in `createplanindex.go:158-168`, not the
`p.node` from `buildInitialRels`) has the same gap. It does not need a
separate fix: `*IndexScan` already embeds `PlanCost` (`plan.go:773`), and the
wrapper produced by `rewrap` is the same `Filter` type this task fixes — so
both the `PathPrebuilt` (seq-scan leaf) and `PathIndexScan` (rebuilt leaf)
routes to `createPlanNode` are closed by the one embed.

An `awk` sweep of every `optimizer` struct that embeds `searchedTree` (the
scan-arm-reachable marker) but not `PlanCost` found exactly one other hit,
`Project`. It is not a `buildInitialRels`/`scanLeafFor` leaf shape — nothing
in `internal/optimizer` constructs a `*Project` as a FROM-item's `scans[i]`
entry or as an index-scan-leaf rewrap target — so it is out of this task's
scope; noted here rather than silently ignored in case a later slice adds a
Project-shaped base leaf.

## Gates

`go build ./...` clean. `go test ./internal/optimizer/...` full package `ok`
(includes the new pinning test). `go test ./internal/executor/... -run
Explain` `ok` (the EXPLAIN consumer side, `explainCostFields`, needed no
change — confirming M0137-0011's diagnosis that only the producer side,
`Filter`'s method set, was the gap). `scripts/tpch-spotcheck.sh` RESULT=PASS
(Q12=2, Q13=34, canonical; private port 5580, clone never touched `:65433`).
Live Q12 reproduction above used a second private clone (port 5581, same
private data dir `tmp/goopg-spotcheck-tpch-data`), stopped and its transient
scope torn down after use; shared clusters (`:65432`/`:65433`/`:65437`/
`:65438`) confirmed still listening and untouched throughout.

## Filed as

Closes `.ralph/deferral_ledger.md` row
`m0137-0011-filter-node-missing-plancost-embed` (marked `resolved` in this
loop) and `.ralph/fix_plan.md` M0137-0015 (checked `[x]`).
