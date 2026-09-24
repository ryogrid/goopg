# M0146-0001: post-cutover parity re-baseline

Status: **RECON COMPLETE 2026-09-25**. No production code changed. Task:
`.ralph/fix_plan.md` M0146-0001 (Kind: recon, Parent: M0145-0008). Evidence:
`analysis/m0146/m0146-0001/`.

## What was measured

The census ran on the post-flip default arm at HEAD `8b249d462`, with the
same instruments as the M0144 census (`m0144-0002-first-divergence-census.md`):
- plans captured per corpus with `scripts/jointree-parity-capture.sh`;
  TPC-H uses its pinned-seed estimate-audit lane, TPC-DS SF0.25 and SF1
  use private clones;
- category counts from `pg-plan-parity-diff.py`;
- the per-stage class report from `pg-plan-divergence-class.py`;
- the first-divergence census from `pg-plan-first-divergence.py`.

## Headline

| corpus | match (M0144, 2026-09-20) | match now | CATEGORIES-EXCL-MATCH leaders |
|---|---|---|---|
| TPC-H (parallel) | 1/22 | **3/22** | join-order 17, scan-type 12, join-method 11, parallelism 11 |
| TPC-DS SF0.25 | 2/99 | **4/99** | join-order 91, parallelism 74, join-method 66, sort-strategy 62 |
| TPC-DS SF1 | 1/99 | **6/99** | join-order 85, parallelism 76, sort-strategy 68, join-method 65 |

First-divergence rollups:
- **TPC-H:** join-method 5, sort-strategy 4, aggregation-strategy 4,
  qual-placement 3. It was led by aggregation-strategy 7.
- **TPC-DS SF0.25:** sort-strategy 32 (was 43), aggregation-strategy 13,
  qual-placement 11, parallelism 10, join-method 10.
- **TPC-DS SF1:** sort-strategy 36 (was 39), aggregation-strategy 17 (was
  26).

## Ranked residuals → owners

`rank.py` maps each first-divergence record to the M0146 task that owns its
decision point:
- PG Incremental Sort → 0006;
- goopg CTE scan where PG inlines → 0007;
- PG Finalize or Partial aggregation, or Gather Merge, → 0003;
- goopg Gather where PG stays serial → 0002a;
- join method or order, or PG getting presorted input from a Nested Loop →
  0005;
- qual placement → 0012;
- parameterisation → 0011;
- a hash vs sort grouping election → 0009.

| owner | TPC-H | SF0.25 | SF1 | total |
|---|---|---|---|---|
| M0146-0005 join order / method (+ presorted input) | 5 | 24 | 21 | **50** |
| M0146-0003 row-emitting PartialAgg / parallel aggregation | 7 | 11 | 14 | **32** |
| M0146-0002a parallel over-election | 1 | 13 | 12 | **26** |
| M0146-0012 restriction placement | 3 | 11 | 8 | **22** |
| M0146-0006 Incremental Sort | 0 | 10 | 11 | **21** |
| M0146-0007 inline_cte | 0 | 8 | 7 | **15** |
| M0146-0009 statistics (grouping election) | 1 | 4 | 4 | 9 |
| M0146-0011 lateral / parameterised | 1 | 2 | 2 | 5 |
| **new** M0146-0016 Subquery Scan retention | 0 | 3 | 3 | 6 |
| **new** M0146-0017 WindowAgg sort sharing | 0 | 2 | 2 | 4 |
| **new** M0146-0018 Group node | 0 | 2 | 2 | 4 |
| **new** M0146-0019 index-only scan where goopg seq-scans | 1 | 1 | 1 | 3 |
| **new** M0146-0020 MixedAggregate (grouping sets) | 0 | 1 | 2 | 3 |
| not a divergence (Q36/Q70/Q86 error on both arms) | 0 | 3 | 3 | 6 |

The first eight rows set the M0146 sequence, which is recorded on the
M0146-0001 entry. The five "new" rows had no owner and were filed as recon
tasks, so every record except the error rows now has one.

## Caveats

- A first-divergence record names the first wrong node. Fixing it can
  expose a deeper divergence in the same query, so the counts rank
  decision points; they do not predict how much the match count will move.
- The owner mapping is heuristic, keyed on node kinds. `rank.py` prints the
  per-record assignment so a mis-bucketed record can be re-assigned by
  hand.

Movement: none (recon).
