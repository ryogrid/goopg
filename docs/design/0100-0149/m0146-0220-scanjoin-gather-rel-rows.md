# M0146-0220 — the no-split arms' Gather and Gather Merge carry the scan/join rel's rows

Status: landed 2026-10-17. Evidence: `analysis/m0146/m0146-0220/`.

## 1. Record

TPC-DS SF1 Q48 (S9 pass at 9fcfbc795, aggregation-strategy, SHAPE-DIFF at depth 0):

- PG: `Finalize Aggregate → Gather → Partial Aggregate`.
- goopg: `Aggregate → Gather`.

The record persists with PG's statistics imported.

## 2. Attribution

On an SF1 clone with the reference's statistics imported, goopg's search rel and seed both carry 5 rows, as PG's do.
`GOOPG_PGSHAPED_DP_TRACE=1` shows the grouped rel's arms:

| arm | total |
|---|---|
| plain `Agg` over the search's own Gather (5 rows) | 84021.29 |
| split | 84021.20 |
| gathered no-split `Agg → Gather` | **84021.19** |

The no-split arm prices its Gather at `compute_gather_rows` = 4 (one row per worker times divisor 4), so it is 0.01
cheaper than the split arm, and it wins.

PG's side came from a scratch PG with the SF1 reference restored by `pg_dump --statistics`, plus an add_path elog
(`probe/pg-sf1-q48-addpath.txt`):

- The topmost scan/join rel's Gather carries **rows = 5.0** (2966.26..84021.0089).
- The grouped rel's only Agg-over-Gather is the plain arm over that path, 84021.03.
- The Finalize Aggregate is 84020.94, so it wins.

`generate_gather_paths` and `generate_useful_gather_paths` run on scan/join rels with `override_rows = false`, so
`create_gather_path` and `create_gather_merge_path` stamp `rel->rows`. The call sites are
`apply_scanjoin_target_to_paths`, `standard_join_search` and `set_rel_pathlist`. `compute_gather_rows` is only for
the `override_rows = true` calls on upper rels: `gather_grouping_paths` and the partial-distinct rel. The Q5
reference plan shows the same: its `wsr` Gather crosses 6 rows, the rel's estimate, where per-worker rows × divisor
gives 5.

M0146-0009n had sized both no-split boundaries at `compute_gather_rows`: the `Agg → Gather` Gather and the
worker-sort Gather Merge. Unifying them settled TPC-DS Q56's near-tie at the time, but the count it chose is the
upper-rel one. M0146-0219 recorded the deviation in its ledger row.

## 3. Change

`scanJoinGatherRows(seed, perWorkerRows, d)` in `partialaggupper.go` returns the seed's rows, i.e. the scan/join rel's.
It falls back to `compute_gather_rows` only when there is no seed. `addPartialAggSplitPath` uses it for:

- `gatheredRows`: the Gather of the plain, presorted-plain, grouped sorted and grouped hashed no-split arms;
- `workerSortGatherMergePath`, which now takes the crossing rows from its caller rather than recomputing them.

The two boundaries still agree with each other, which was M0146-0009n's invariant, and now also agree with
upstream. `scanJoinRelKeepsGather` / `scanJoinRelKeepsGatherMerge` already probed at the seed's rows.

## 4. Measured

**Shipped corpora:**

| corpus | before | after | flips |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | none |
| TPC-DS SF0.25 | 88 / 99 | 87 / 99 | Q5 MATCH → SHAPE-DIFF |
| TPC-DS SF1 | 74 / 99 | 74 / 99 | Q71 → MATCH, Q80 MATCH → SHAPE-DIFF |

The SF0.25 sweep reports `PLAN-SHAPE changed = 21`. These are almost all row and cost text on the worker-sort Gather
Merge (Q3: rows 49 → 50); the verdict is unchanged. All executed values are identical:
- sf025 PASS = 99;
- the fire set's 21 SF0.25 and 19 SF1 queries are byte-identical between baseline and candidate;
- the TPC-H acceptance arm is value-identical.

**PG statistics imported (private clones, `census/imp-*`):**

- SF1 goes from 86 to **88 / 99**: Q48 and Q71 match. Q80 matches too, so its shipped-corpus flip is
  estimate-driven.
- SF0.25 plans 91 / 99. It gains Q16, Q61 (M0146-0219), Q38 and Q87 (M0146-0217) against the M0146-0216
  imported-stats census.
- **Q5 is lost even with PG's statistics.**

## 5. The Q5 residual (filed as M0146-0221)

Q5's `wsr` branch groups 6 rows over a 2-row-per-worker parallel Hash Join:

| plan | rows | cost |
|---|---|---|
| goopg no-split `GroupAggregate → Sort → Gather` | 6 | 18000.49..18000.70 |
| goopg split (`sortinput`, Finalize GroupAggregate over Gather Merge) | 5 | 17999.85..18000.65 |
| PG's choice: no-split | — | 17642.09..17642.30 |

goopg's split arm wins the tight-fuzz tie.

A probe on a restored SF0.25 copy rejects PG's split arm by the same tie-break, 0.05 dearer than its no-split arm.
That copy estimates 7 rows for `wsr` rather than the reference's 6, so it is not an exact replica. The input join is
also costed differently: goopg charges its `Subquery Scan` over the UNION ALL leg 8985 against PG's 8447. So the
split arm's price in goopg is about 0.1 below PG's relative to the no-split arm. The cause is not identified yet.
Under R3 this change stays, because it is PG's mechanism: probed directly, and visible in the reference plan.

## 6. Pin

`TestGatheredArmBoundariesCarryScanJoinRelRows`, formerly `…ShareComputeGatherRows`, uses a searched partial path
whose per-worker rows × divisor exceed the seed's rows. Both no-split boundaries must carry the seed's rows.
Mutation-checked: returning `compute_gather_rows` again fails it.
