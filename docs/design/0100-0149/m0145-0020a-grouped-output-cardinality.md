# M0145-0020a — Q39 grouped-output cardinality

Status: **IN PROGRESS 2026-09-22.**
Kind: impl
Parent: M0145-0020

## Problem

TPC-DS Q39 at SF0.25 feeds 11,703 estimated rows into its `inv` aggregate,
but goopg publishes 20 rows after the HAVING filter where PostgreSQL publishes
about 3,900. The query’s later `d_moy` restriction has the same default
selectivity in both engines; only the aggregate output is collapsing the CTE
consumer to one row.

## Initial arithmetic

The observed 20 is exactly one third of 60. One third is PostgreSQL's
`DEFAULT_INEQ_SEL` for the unresolvable aggregate-output HAVING comparison.
The first measurement rules out a stale input-row override: the pre-existing
R61 CTE-body census shows that Q39's CTE aggregate has no searched relation
tag, so `groupCountInputRows` falls back to the built child’s displayed 11,703
rows. The 60-row source is therefore inside `estimateNumGroups`, before the
HAVING selectivity is applied. The next probe must identify which of the four
written grouping keys fails to contribute its base-column distinct estimate.

`estimateAggregate` and `sizeGroupingRelFromAgg` share
`groupCountInputRows`; any eventual correction must preserve that agreement
between search-path sizing and display recomputation.

## Resolver crossing

The Q39 plan's item probe is rendered as `Memoize -> Index Scan`. `Memoize`
is row- and value-preserving, but `resolveBaseColumn` and its Yao/Dell'Era
filtered-row sibling stopped at that wrapper. The implementation now crosses a
non-nil Memoize child in both walkers. `TestMemoizeResolverFamilyCrossesIndexProbe`
pins the shared contract, including catalog identity and filtered-row result;
the full optimizer package passes.

The private capture refutes this as Q39's direct cause. The staged tree still
prints the same 60-before-HAVING / 20-after-HAVING estimate and 20-row CTE
scans, so the Q39 grouping expression does not reach this direct wrapper path
in the estimator. Keep the correct resolver crossing, but do not claim it
fixes Q39 or start value gates from it.

## PostgreSQL reference

PostgreSQL applies `estimate_num_groups` before `cost_agg` applies HAVING
qualifications; its Q39 plan estimates the aggregate at 3,912 rows from 11,735
input rows, then CTE scans at 20 after `d_moy` filtering. The relevant upstream
paths are `postgres/src/backend/utils/adt/selfuncs.c` (`estimate_num_groups`)
and `postgres/src/backend/optimizer/path/costsize.c` (`cost_agg`).

## Root cause and repair

The group-variable trace showed that all four keys resolve correctly. The
per-relation trace then isolated the loss: the `item` `IndexScan` reported
`tuples=18000`, `filtered=1`, and one surviving distinct value. That row count
is the estimate for one parameterized NLI probe, not `item`'s base-relation
restriction estimate. Its probe expression is a plain `ColumnRef` in the
outer-row frame, so the previous `OuterColumnRef` detector did not recognize
it.

`relFilteredRowsWalk` now declines a relation when it is the legal
`NestedLoopIndexJoin.Inner` probe. This leaves the Yao/Dell'Era adjustment for
real local restrictions intact while preventing a per-probe row count from
being treated as a filter. `TestNumGroupsIgnoresParameterizedNLIProbeRows`
constructs the exact shape and proves that the group count reaches the input
clamp rather than collapsing to the outer relation's five values.

This follows PostgreSQL's `estimate_num_groups` treatment of each source
relation's base statistics and restriction estimate at
`postgres/src/backend/utils/adt/selfuncs.c:3449`; a parameterized path's
per-execution cardinality is not a base restriction.

## Measurements and gates

The private SF0.25 capture at `tmp/m0145-0020a-nliprobe/` records Q39's
pre-HAVING aggregate at 11,703 and its post-HAVING estimate at 3,901, with both
CTE consumers at 19. PostgreSQL's same capture reads 3,901 and 19. The
default/off CTE-fallback A/B at `tmp/m0145-0020a-cte-ab/` changes only Q74;
Q31 and Q39 no longer need the fallback, while Q74 still moves into a nested
loop collapse shape. Its category lines are ON
`join-method=65` and OFF `join-method=64`; the other category counts are
unchanged.

`go test ./internal/optimizer/...` passes. The gates were then re-run with a
working tree carrying ONLY this change (unrelated WIP parked in a stash), so
every stamp is keyed on exactly the committed code:

| gate | result |
|---|---|
| `scripts/tpch-spotcheck.sh` | PASS, Q12=2 Q13=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0; PLAN-SHAPE same=99 changed=0 |
| `PGSHAPED=1 scripts/tpch-acceptance-arm.sh` | PASS, 24 MATCH |
| floor capture, TPC-DS SF0.25 | `match=2` — held |
| floor capture, TPC-H parallel | `match=1` — held |
| `make ea-ratchet` | FAIL, 2 NEW / 3 FIXED (owned by M0145-0020c) |

`CATEGORIES-EXCL-MATCH: join-order=91 join-method=66 scan-type=61
parameterisation=54 aggregation-strategy=44 sort-strategy=61 parallelism=85
qual-placement=27 rendering=26` (TPC-DS SF0.25, `tmp/m0145-0020a-final/`).

### The Q9 acceptance failure was an arm-configuration artifact

Two loops recorded this change — and M0145-0004 before it — as unlandable
because the acceptance arm reported `1 BOTH-ERROR, 23 MATCH` with Q9 cancelled
at 600 s, and read that as a pre-existing red gate that merely needed a
clean-HEAD proof (M0145-0020b supplied one). The real cause is the arm's own
default: `scripts/tpch-acceptance-arm.sh` defaults `PGSHAPED=0`, i.e.
`GOOPG_PGSHAPED_DP=0`, which is NOT the production default (the planner ships
with the PG-shaped DP search on). Q9 does not complete inside 600 s on the
legacy search; on the shipped configuration it finishes in 2.8 s. The same arm
run with `PGSHAPED=1` against the loop-77 baseline returns `SUMMARY: 24 MATCH /
VERDICT: PASS`. An arm that measures a non-default planner configuration cannot
discharge — or block — a gate about the shipped one.

`make ea-ratchet` is red, and with a clean tree the number is much smaller than
the 61 identifiers the contaminated run reported: `baseline findings: 54
current: 53`, with 3 FIXED (Q62, Q80, Q99) and **2 NEW** — `Q44:item+ss1`
(est 1831 vs actual 10) and `Q83:cte:sr_items+cte:wr_items` (est 4 vs actual
171). Both are grouped/CTE-derived relsets, so they are attributed to this
change rather than claimed pre-existing, and are owned by **M0145-0020c** with
a deferral-ledger row.
