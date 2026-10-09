# M0146-0014c — parity-closure sweep, routing refresh 2026-10-10 (b)

Capture: the M0146-0148 fire-set candidate captures, which equal HEAD
`1fd148e3a`'s code, at both scales. First-divergence records come from
`scripts/pg-plan-first-divergence.py`.

| scale | match | divergent | change since `routing-20261010` (M0146-0014b) |
|---|---|---|---|
| SF0.25 | 56 / 99 | 43 | Q36, Q86, Q95 → MATCH (M0146-0146, 0147) |
| SF1 | 42 / 99 | 57 | Q36, Q86, Q95 → MATCH |

## Method

- Every record whose first divergence is byte-identical to M0146-0014b's
  keeps its route.
- Three records changed:
  - **Q70 at both scales** has a new first divergence once M0146-0146 kept
    its `Subquery Scan`. It is re-analysed below.
  - **Q72 at SF1** moves from M0146-0148 to COSTTIE, per that task's
    measurement.

All 100 records route to a live task or a named owner residual.

## Q70 → M0146-0149 (new)

The first divergence is `[join-method]` under the MixedAggregate.

- **PG** joins the `s_state IN (…)` subquery `tmp1` with a Hash Semi Join
  (38304.53 at SF0.25).
- **goopg** unique-ifies it and runs it as the 1-row outer of a nested
  loop (38257.76).
- **Root cause: the subquery's row count**, 2 in PG and 1 in goopg.
  - PG's `cost_incremental_sort` (costsize.c:2025) clamps `input_tuples` to
    at least 2 and then sets `path->rows = input_tuples` (:2121).
  - So the window's Incremental Sort over the 1-row Finalize
    GroupAggregate carries 2 rows, as do the WindowAgg and the
    `Subquery Scan on tmp1` above it.
  - With 2 outer rows the nested loop rescans its 19258-cost inner join
    twice, and the hash semi join wins.
  - goopg's incremental sort keeps `sub.Rows` (incrementalsortpaths.go).
  - SF1 has the same signature (goopg 1 row; PG 2 rows, Hash Semi Join).

## Route counts

| route | records |
|---|---|
| B8 | 32 |
| COSTTIE | 30 |
| RELPAGES | 16 |
| B15 | 7 |
| RENDERING | 5 |
| PARAM-APPEND | 2 |
| GEQO-RNG | 2 |
| INCSORT-ROWS | 2 |
| HASHSPILL/WIDTH | 1 |
| HASHSPILL | 1 |
| STATS | 1 |
| COSTTIE+RELPAGES | 1 |

## Routing table (every divergent record at HEAD)

| scale | query | route | target | first divergence / note |
|---|---|---|---|---|
| sf025 | Q1 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=3 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q5 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=8 [join-method] under Gather: PG Hash Join Inner \| goopg Nested Loop Inner  \|\| wsr web_site join: goopg NL+web_site_pkey probe 19991.00 vs its hash alternative ~19991.2 (PG hashes 30 rows, +2.73); 0.001% |
| sf025 | Q6 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=7 [parameterisation] under Aggregate: PG Seq Scan on item j \| goopg Seq Scan on item j  \|\| SubPlan correlated argument printed `$0` vs PG `i.i_category` (M0146-0130 not-covered) |
| sf025 | Q14 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=8 [scan-type] under Nested Loop: PG Index Scan item_pkey on item \| goopg Bitmap Heap Scan on item |
| sf025 | Q16 | B15 | ledger take3-B-15-blocked-2 | depth=4 [parallelism] under Sort: PG Nested Loop Inner \| goopg Gather |
| sf025 | Q17 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [parallelism] under Nested Loop: PG Nested Loop Inner \| goopg Gather Merge |
| sf025 | Q19 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate |
| sf025 | Q25 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [parallelism] under Nested Loop: PG Nested Loop Inner \| goopg Gather Merge |
| sf025 | Q26 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge |
| sf025 | Q29 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [parallelism] under Nested Loop: PG Nested Loop Inner \| goopg Gather Merge |
| sf025 | Q30 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=5 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf025 | Q32 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [scan-type] under Nested Loop: PG Index Scan date_dim_pkey on date_dim date_dim_1 \| goopg Bitmap Heap Scan on date_dim date_dim_1 |
| sf025 | Q33 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge |
| sf025 | Q34 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q37 | B15 | ledger take3-B-15-blocked-2 | depth=6 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner |
| sf025 | Q38 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort |
| sf025 | Q39 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=4 [parallelism] under HashAggregate: PG Hash Join Inner \| goopg Gather |
| sf025 | Q40 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=1 [aggregation-strategy] under Limit: PG Finalize GroupAggregate \| goopg GroupAggregate |
| sf025 | Q42 | B15 | ledger take3-B-15-blocked-2 | depth=6 [join-order] under Nested Loop: PG Parallel Hash Join Inner \| goopg Parallel Hash Join Inner |
| sf025 | Q45 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge |
| sf025 | Q46 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=3 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf025 | Q49 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=15 [scan-type] under Nested Loop: PG Index Scan web_sales_pkey on web_sales ws \| goopg Bitmap Heap Scan on web_sales ws  \|\| web_sales_pkey probe: goopg Bitmap Heap Scan 12.39 vs PG Index Scan 7.94 |
| sf025 | Q52 | B15 | ledger take3-B-15-blocked-2 | depth=6 [join-order] under Nested Loop: PG Parallel Hash Join Inner \| goopg Parallel Hash Join Inner |
| sf025 | Q54 | PARAM-APPEND | M0146-0005dp [!] | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf025 | Q58 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=10 [parameterisation] under Seq Scan: PG - \| goopg InitPlan1  -- InitPlan1 present only on goopg side  \|\| identical structure; InitPlan numbering differs (goopg 1/2, PG 2/1 — sublink planning order) |
| sf025 | Q60 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf025 | Q61 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [aggregation-strategy] under Nested Loop: PG Finalize Aggregate \| goopg Aggregate |
| sf025 | Q64 | GEQO-RNG | M0146-0014 — waived residual (GEQO-RNG) | depth=5 [qual-placement] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner  \|\| exact tie among 14 one-row probes (PG 5.45 = 5.45); PG's geqo tour (pg_prng xoroshiro128**) decides the order — agent-joinorder.md |
| sf025 | Q68 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q69 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [join-method] under Sort: PG Hash Join Left Anti \| goopg Nested Loop Left Anti |
| sf025 | Q70 | INCSORT-ROWS | M0146-0149 | depth=6 [join-method] under MixedAggregate: PG Hash Join Left Semi \| goopg Nested Loop Inner  \|\| PG cost_incremental_sort clamps input_tuples to 2 and sets path->rows from it, so the tmp1 subquery is 2 rows and PG hashes it (Hash Semi Join); goopg keeps 1 row and its unique-ified NL outer wins (3 |
| sf025 | Q72 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left \| goopg Sort |
| sf025 | Q73 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q76 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [join-method] under Append: PG Parallel Hash Join Inner \| goopg Nested Loop Inner |
| sf025 | Q77 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [aggregation-strategy] under Merge Join: PG GroupAggregate \| goopg Finalize GroupAggregate |
| sf025 | Q78 | COSTTIE | M0146-0014 — waived residual (COSTTIE) (0.06 apart, measured 2026-10-09) | depth=2 [qual-placement] under Sort: PG Merge Join Left \| goopg Merge Join Left |
| sf025 | Q79 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [join-method] under Sort: PG Nested Loop Inner \| goopg Hash Join Inner  \|\| M0146-0139: customer_pkey probe 8.44 vs PG 4.63, else near-tie |
| sf025 | Q81 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q83 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=7 [join-method] under Nested Loop: PG Hash Join Left Semi \| goopg Hash Join Inner |
| sf025 | Q85 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=7 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner |
| sf025 | Q87 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort |
| sf025 | Q92 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=8 [scan-type] under Nested Loop: PG Index Scan date_dim_pkey on date_dim date_dim_1 \| goopg Bitmap Heap Scan on date_dim date_dim_1  \|\| date_dim_pkey probe: goopg Bitmap Heap Scan 12.27 vs PG Index Scan 8.32 |
| sf025 | Q99 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=6 [join-order] under Hash Join: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q3 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=3 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort |
| sf1 | Q4 | HASHSPILL/WIDTH | M0141-S2a-fix2r-a | depth=3 [aggregation-strategy] under Append: PG HashAggregate \| goopg GroupAggregate |
| sf1 | Q5 | RENDERING | M0146-0042 [!] (held) | depth=8 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q6 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=7 [parameterisation] under Aggregate: PG Seq Scan on item j \| goopg Seq Scan on item j  \|\| same as SF0.25 |
| sf1 | Q7 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=2 [parallelism] under GroupAggregate: PG Nested Loop Inner \| goopg Gather Merge |
| sf1 | Q11 | HASHSPILL | M0141-S2a-fix2r-a | depth=3 [aggregation-strategy] under Append: PG HashAggregate \| goopg GroupAggregate |
| sf1 | Q13 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=0 [aggregation-strategy] under -: PG Finalize Aggregate \| goopg Aggregate |
| sf1 | Q14 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q16 | B15 | ledger take3-B-15-blocked-2 | depth=4 [parallelism] under Sort: PG Nested Loop Inner \| goopg Gather |
| sf1 | Q18 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) (+B-15) | depth=8 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner  \|\| 0.03% near-tie flipped by the 2x probe I/O (22 units) or the missing log2(N) descent (17 units) — agent-joinorder.md |
| sf1 | Q19 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate |
| sf1 | Q22 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=4 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q23 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=4 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q24 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=7 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q26 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge |
| sf1 | Q30 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf1 | Q31 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [join-order] under HashAggregate: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q32 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [parameterisation] under Nested Loop: PG Memoize \| goopg Bitmap Heap Scan on date_dim date_dim_1 |
| sf1 | Q33 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge |
| sf1 | Q34 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf1 | Q35 | RELPAGES | owner SF1 reload | depth=2 [sort-strategy] under GroupAggregate: PG Incremental Sort \| goopg Sort  \|\| M0146-0131: customer_address ~955 pages < min_parallel_table_scan_size at SF1 |
| sf1 | Q37 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=2 [sort-strategy] under Group: PG Nested Loop Inner \| goopg Sort |
| sf1 | Q38 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort |
| sf1 | Q39 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=4 [parallelism] under HashAggregate: PG Hash Join Inner \| goopg Gather |
| sf1 | Q40 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=1 [aggregation-strategy] under Limit: PG Finalize GroupAggregate \| goopg GroupAggregate |
| sf1 | Q42 | B15 | ledger take3-B-15-blocked-2 | depth=6 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q48 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=0 [aggregation-strategy] under -: PG Finalize Aggregate \| goopg Aggregate |
| sf1 | Q49 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=16 [scan-type] under Nested Loop: PG Index Scan web_sales_pkey on web_sales ws \| goopg Bitmap Heap Scan on web_sales ws  \|\| same signature at SF1 (12.39 vs 7.9x) |
| sf1 | Q52 | B15 | ledger take3-B-15-blocked-2 | depth=6 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q53 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=6 [parallelism] under Sort: PG Gather \| goopg Nested Loop Inner |
| sf1 | Q54 | PARAM-APPEND | M0146-0005dp [!] | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf1 | Q55 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=6 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf1 | Q56 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=5 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort |
| sf1 | Q58 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=10 [parameterisation] under Seq Scan: PG - \| goopg InitPlan1  -- InitPlan1 present only on goopg side  \|\| same |
| sf1 | Q60 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf1 | Q61 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=8 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf1 | Q62 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q63 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=6 [parallelism] under Sort: PG Gather \| goopg Nested Loop Inner |
| sf1 | Q64 | GEQO-RNG | M0146-0014 — waived residual (GEQO-RNG) | depth=6 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner  \|\| same exact probe tie (PG 5.31); deeper head difference is B8 — agent-joinorder.md |
| sf1 | Q68 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=3 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf1 | Q69 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [qual-placement] under GroupAggregate: PG Nested Loop Left Anti \| goopg Nested Loop Left Anti |
| sf1 | Q70 | INCSORT-ROWS | M0146-0149 | depth=6 [join-method] under MixedAggregate: PG Hash Join Left Semi \| goopg Nested Loop Inner  \|\| PG cost_incremental_sort clamps input_tuples to 2 and sets path->rows from it, so the tmp1 subquery is 2 rows and PG hashes it (Hash Semi Join); goopg keeps 1 row and its unique-ified NL outer wins (3 |
| sf1 | Q71 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=1 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate |
| sf1 | Q72 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left \| goopg Sort  \|\| M0146-0148: the ordered chain over the sub-problem Gather Merge is built (61228.47) and loses within STD_FUZZ_FACTOR to the unordered chain + 2-row Sort (61228.39) |
| sf1 | Q73 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf1 | Q75 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=8 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q76 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [join-method] under Append: PG Parallel Hash Join Inner \| goopg Nested Loop Inner |
| sf1 | Q77 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [aggregation-strategy] under Merge Join: PG GroupAggregate \| goopg Finalize GroupAggregate |
| sf1 | Q78 | STATS | M0146-0009 (store_returns anti-join estimate 30 vs 281/worker) | depth=2 [join-order] under Incremental Sort: PG Merge Join Left \| goopg Merge Join Left |
| sf1 | Q80 | COSTTIE+RELPAGES | M0146-0014; SF1 reload | depth=12 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q81 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf1 | Q82 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=2 [sort-strategy] under Group: PG Nested Loop Inner \| goopg Sort |
| sf1 | Q84 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=7 [parallelism] under Hash Join: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q85 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=7 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner |
| sf1 | Q89 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=10 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q92 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=7 [parameterisation] under Nested Loop: PG Memoize \| goopg Bitmap Heap Scan on date_dim date_dim_1 |
| sf1 | Q99 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner |
