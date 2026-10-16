# M0146-0221 — the partial-aggregate split arm's cost terms against PG's

Status: slice 1 (clamped crossing rows) landed 2026-10-17; slice 2 (aggregate serialize/deserialize/final function
costs) is open. Evidence: `analysis/m0146/m0146-0221/`.

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

## 5. Slice 2 (open): get_agg_clause_costs

PG prices each aggregate from `pg_aggregate` and `pg_proc.procost`:

- transfn, or combinefn when combining, per input row;
- plus deserialfn per input row when deserializing;
- serialfn per group when serializing;
- finalfn per group unless the final step is skipped;
- plus the arguments' evaluation cost.

goopg's `costAgg` uses `nAggs × cpu_operator_cost` for both the per-row and the per-group term. It is exact for
procost-1 aggregates in the simple case, overcharges aggregates without a finalfn (e.g. `sum(int4)`), and misses the
deserialize term.

The metadata exists only in `internal/initdb/pg_aggregate_bootstrap.go`, which the optimizer cannot import. The port
needs:
- a leaf-package table keyed by aggregate;
- resolution of each aggregate call to its `pg_aggregate` row;
- `costAgg` taking per-aggregate cost terms and a split mode.

With the deserialize term alone, Q5's split arm would cost 17642.3265 against 17642.3027, and PG's choice follows.
