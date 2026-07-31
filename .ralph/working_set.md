(idle — nothing in flight)

## Loop summary (loop #19, 2026-08-01)

**M0125-0045 is CLOSED** — aggregate half of the -0044 qualifier-blind
collapse. `count(d1.y)`/`count(d2.y)` now occupy distinct agg slots via the
contested-key treatment: `qualifiedAggregateCallKey` (groupby_alias_key.go,
reuses appendRefQualifiers over the whole FuncCall), collection dedups on the
qualified key, `buildAggregateStage` marks `aggregateAmbiguous` blind keys +
keys contested calls via `aggregateByKeyQual`, both resolution sites
(resolveExprAfterAggregate + resolveExpr havingAgg arm) dispatch through it.

Acceptance was NOT the sweep (no SF0.5 query reaches the defect): 4 planner
unit tests (aggregate_alias_collapse_test.go) + byte-identical PG-oracle diff
on hand-written asymmetric-NULL data (count(d1.y)=3 vs count(d2.y)=1; probe
db on :65438 created and dropped). One ledger row 2026-08-01: PG merges by
resolved-form equality (equal() over Aggref->args) — goopg's parser-form key
can SPLIT count(y)/count(t.y) of one binding (redundant slot, never wrong).

**Next loop: read the `## Current Priority` banner FIRST.** It now names
**`M0125-0046`** (MHJ InExpr qual placement — planner/executor SIBLING PAIR),
then `M0125-0038` last; after M0125 closes, M0126 (cost-driven planning) is
next per the 2026-07-31 USER amendment.

Gates run this loop (all PASS): go build; planner+executor+parser suites;
`RALPH_PRECOMMIT_SCOPE=units`; `scripts/tpch-spotcheck.sh` RESULT=PASS
(Q12=2 Q13=35); full 99-query SF0.5 gate, one binary
`tmp/goopg-sf05-m0125-0045-bin`, 3 chunks
(`analysis/tpcds-sf05-m0125-0045/gate/`) — PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0 SKIP=4, diffed cell-by-cell vs loop #18: ZERO movement;
pre-commit pgbench smoke (hook); `make ralph-state-guard`.

Nightly AI-20260731-001201-001 already filed under M-NIGHTLY — parked per
banner. In-flight: none. Throwaway goopg (5533) stopped and data dir removed;
SF0.5 server lifecycle handled by the sweep script; PG :65438 left as found.
