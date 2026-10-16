# M0146-0221 — the partial-aggregate split arm's cost terms against PG's

Status: slice 1 (clamped crossing rows) and slice 2 (the get_agg_clause_costs port) landed 2026-10-17. Evidence:
`analysis/m0146/m0146-0221/` (`census/` for slice 1, `census2/` and `fireset2/` for slice 2).

## 1. Record

TPC-DS SF0.25 Q5 went from MATCH to SHAPE-DIFF with M0146-0220. Its `wsr` branch groups 6 rows over a parallel Hash
Join with 2 rows per worker.

- PG keeps the no-split `GroupAggregate → Sort → Gather`.
- goopg keeps the split `Finalize GroupAggregate → Gather Merge → Partial GroupAggregate → Sort`.

The record persists with PG's statistics imported.

## 2. An exact PG oracle

A `pg_dump` restore of `tpcds025` into the scratch PG has the same statistics but not the same plan: `wsr` comes out
at rows 7 / 17825 instead of 6 / 17642. PG scales `reltuples` by the relation's current block count
(`estimate_rel_size`), and a fresh COPY lays the heap out differently. `pg_basebackup` of the reference cluster is a
read-only operation, and the `tmp/pgdbg` debug build runs on it unchanged (8 KB blocks). That gives the reference's
plan exactly, and with an add_path elog on upper rels it gives every arm
(`probe/pg-exact-replica-q5-wsr-arms.txt`):

| PG arm | cost | add_path verdict |
|---|---|---|
| partial GroupAggregate over Sort | 16641.4252..16641.4952, 2 rows | — |
| Gather Merge over it | 17641.4489..17642.0961, 5 rows | — |
| no-split GroupAggregate over Sort over Gather | 17642.0927..17642.3027 | accepted |
| Finalize GroupAggregate over that Gather Merge | 17641.4489..17642.3286 | rejected (tight fuzz, dearer) |

goopg's input join costs 358.40 more (its Subquery Scan over the UNION ALL leg is 8985 against PG's 8447). Shifted
by that amount, goopg's arms compare with PG's as follows:

| arm | goopg (shifted) | PG |
|---|---|---|
| no-split | 17642.0927..17642.3027 | 17642.0927..17642.3027 (identical) |
| split | ..17642.2530 | ..17642.3286 (goopg 0.0756 lower) |

## 3. The 0.0756, term by term

PG's split arm, from `cost_gather_merge` and `cost_agg` with `get_agg_clause_costs`. There are 4 `sum(numeric)`
aggregates, with combinefn, serialfn, deserialfn and finalfn all at procost 1.

**Gather Merge** (N = 3, logN = 1.585):
- startup: 1000 + 0.005 · 3 · 1.585 = 1000.0238 above the partial path's startup;
- run on 5 rows: 5 · 0.005 · 1.585 + 0.0025 · 5 + 0.1 · 5 · 1.05 = 0.577;
- total 17642.0961.

**Finalize** (`AGGSPLIT_FINAL_DESERIAL`):

| term | cost |
|---|---|
| (combinefn + deserialfn) × 4 aggregates × 5 input rows | 0.1 |
| finalfn × 4 × 6 groups | 0.06 |
| grouping comparison × 5 rows | 0.0125 |
| cpu_tuple_cost × 6 groups | 0.06 |
| total | 0.2325 |

goopg differed in two places:

1. **Crossing rows were not clamped.** goopg used `partialGroups * d` = 2 × 2.4 = 4.8, where PG uses
   `compute_gather_rows` = `clamp_row_est` = 5. That is 0.023 in the Gather Merge and 0.0005 in grouping.
2. **No deserialize cost.** goopg's `costAgg` charges one `cpu_operator_cost` per aggregate per input row whatever the
   split mode. PG's FINAL_DESERIAL arm adds the deserialfn: 4 × 0.0025 × 5 = 0.05.

0.023 + 0.0005 + 0.05 + rounding = 0.0756.

## 4. Slice 1 (landed): crossing rows are compute_gather_rows

Every Gather and Gather Merge over a partial grouping path now carries `clampRowEst(partialGroups * d)`, as
`gather_grouping_paths` passes `compute_gather_rows`. This covers:

- the tournament's `crossedRows` (`partialaggpaths.go`);
- the hashed split arm (`addPartialAggSplitArm`);
- the presorted, sorted-input and partial-Group arms (`partialaggupper.go`).

The partial-DISTINCT arm already clamped (M0146-0210).

Measured:

| corpus | before | after | flips |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | none |
| TPC-DS SF0.25 | 87 / 99 | 87 / 99 | none |
| TPC-DS SF1 | 74 / 99 | **75 / 99** | Q80 → MATCH |

Q5 still diverges: after slice 1 the split arm is 17642.2765 against the no-split arm's 17642.3027.

Pin: `TestSplitArmBoundariesCarryComputeGatherRows` uses 37 groups × 2.4 = 88.8, which must cross as 89.
Mutation-checked: unclamping every site fails it.

## 5. Slice 2 (landed): get_agg_clause_costs

PG prices each aggregate from `pg_aggregate` and `pg_proc.procost` (`get_agg_clause_costs`, `prepagg.c`):

- per transition state (AggTransInfo): the transfn per input row, or the combinefn when combining; plus the
  deserialfn per row when deserializing; plus the serialfn per group when serializing;
- per distinct aggregate (AggInfo): the finalfn per group, unless the final step is skipped;
- plus the arguments' and FILTER's evaluation cost when not combining.

goopg's `costAgg` charged `nAggs × cpu_operator_cost` for both the per-row and the per-group term, in every split mode.

**The table.** `cmd/gen-pg-proc-data -aggcost` reads `pg_aggregate.dat` and `pg_proc.dat` and emits
`internal/catalog/builtin_agg_costs_gen.go`. For each of the 161 built-in aggregates (keyed `name(argtypes)`) it
records each support function's procost, 0 where there is none, and a transition identity
(transfn | serialfn | deserialfn | combinefn | transtype | initval) for `find_compatible_trans` sharing.

- A bare `aggfnoid` (non-overloaded aggregates such as `regr_count`) resolves through `pg_proc.dat`'s `prokind 'a'`
  rows.
- A support function whose name carries two procost values stops the generator; none does in PG 18.3.
- Every support function there is procost 1.

`catalog.LookupBuiltinAggCost(name, argTypes)` takes an exact signature first, then polymorphic `any*` matches.
For an unknown type it accepts any overload set whose costs all agree; otherwise it reports false.

`TestBuiltinAggCostTableAgreesWithPgAggregate` (initdb) checks every bootstrapped `pg_aggregate` row against the
table: same count, and the same support functions present. The two are separate copies of one catalog, because the
optimizer cannot import initdb.

**The costing.** `aggClauseCosts(cp, agg, split)` (`optimizer/aggclausecosts.go`) computes `transCost.per_tuple` and
`finalCost.per_tuple` for `aggSplitSimple`, `aggSplitInitialSerial` (the Partial Aggregate) and
`aggSplitFinalDeserial` (the Finalize).

- Identical calls share one AggInfo.
- Calls over the same arguments with an equal transition identity share one AggTransInfo (`sum` and `avg` of a
  numeric column).
- A call it cannot resolve (user-defined aggregates, unnamed argument types) keeps the legacy one-unit pricing.

`costAggClause` is `cost_agg` over those costs; `costAgg` is now the legacy-priced wrapper. Every grouping arm
switched together, because they compete with one another:

| arms | split mode |
|---|---|
| serial arms in `addGroupingPaths` | SIMPLE |
| gathered no-split arms | SIMPLE |
| partial arms | INITIAL_SERIAL |
| finalize arms | FINAL_DESERIAL |

Grouping sets keep the legacy pricing (ledger).

**Measured:**

| corpus | before | after | flips |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | none |
| TPC-DS SF0.25 | 87 / 99 | **88 / 99** | Q5 → MATCH |
| TPC-DS SF1 | 75 / 99 | **76 / 99** | Q52 → MATCH |
| SF0.25, PG statistics imported | 91 / 99 | **92 / 99** | |
| SF1, PG statistics imported | 88 / 99 | **90 / 99** | Q26, Q52 |

No query regressed. In the PG-statistics census the only aggregation-strategy record left is SF1 Q23
(`Finalize HashAggregate` inside a CTE). Executed values are unchanged: the 96 fired TPC-DS queries are byte-identical
and the acceptance arm is value-identical. The SF0.25 sweep reports 46 plan texts changed, which are aggregate cost
numbers.

**Pins:**

- `TestAggClauseCostsPortsGetAggClauseCosts`: Q5's four `sum(numeric)` under each split mode, `sum`/`avg` state
  sharing, identical-call dedup, `count(*)` with no finalfn, and the user-aggregate fallback.
- `TestLookupBuiltinAggCostResolvesOverloads`.

`TestExplainLimitClampsToInputRows` pins a Limit relative to its input. Its input aggregate moved from 46.62 to 46.12,
because `count(*)` has no finalfn. PG's 46.90 differs by the HAVING quals' evaluation cost, which goopg undercharges
(ledger).

## 6. Not ported (ledger)

- The arguments' and FILTER's evaluation cost (`cost_qual_eval` of `transinfo->args` / `aggfilter`, and of
  `aggdirectargs`). goopg has no per-operator expression costing, and the same gap affects HAVING quals.
- Grouping sets (`groupingSetsHashedCost`, `costMixedRollups`, `costAggSortedRollup`) still price `nAggs` units.
- User-defined aggregates use the legacy pricing. PG would read their support functions' procost: 100 for non-C
  languages, by default.
- Ordered-set and `ORDER BY` aggregates are never deduplicated; PG shares identical ones.
