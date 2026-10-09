# M0146-0014b — parity-closure sweep, routing refresh 2026-10-10

Capture: the M0146-0145 fire-set candidate captures, which equal HEAD
`05897eb09`'s code, at both scales (`goopg-<sf>.plans.txt`,
`pg-<sf>.plans.txt`, `diff-<sf>.txt`). First-divergence records come from
`scripts/pg-plan-first-divergence.py` (`first-divergence-<sf>.txt`).

| scale | match | divergent | change since 2026-10-09 (`routing-20261009`) |
|---|---|---|---|
| SF0.25 | 53 / 99 | 46 | match 49 → 53 (Q4, Q35, Q66, Q67); Q36/Q70/Q86 now plan instead of erroring |
| SF1 | 39 / 99 | 60 | match 37 → 39 (Q66, Q67); Q36/Q70/Q86 now plan |

## Method

- **Carried (84 records).** The first divergence is byte-identical to
  2026-10-09's and the route is still live, so the old route stands
  (`records-provisional.tsv`).
- **Re-routed (22 records).** These are records whose first divergence
  moved, records that are new, and records whose route closed in
  M0146-0130…0144. Each is re-analysed from plan text, with cost numbers:
  - by hand (`manual-routes.tsv`);
  - in two read-only passes, `agent-joinorder.md` (Q64 ×2, Q18 SF1) and
    `agent-sort-ios.md` (Q72 SF1, Q95 ×2).
- The two code claims behind the new tasks were checked against the
  source before filing:
  - `collectStmtColumnNames` returns false for `s.With != nil`
    (pathindexonlyneed.go);
  - the sub-joinlist pathlist half of ledger row M0127-P5.9-a is open and
    owned by no task.

Acceptance bar (M0146-0014): **no unnamed first-divergence record.** All
106 records route to a live task or to a named owner residual.

## Owner-facing residuals (named, measured, not loop work)

- **B8 — 32 records** → M0145-0008ag \[!\], decided by the owner through
  M0146-0068.
  - Newly routed here: Q49 ×2 and Q92 SF0.25. Each is a one-row pkey
    probe where goopg's Bitmap Heap Scan (12.27–12.39) beats its own
    Index Scan, against PG's Index Scan at 7.94–8.32.
  - Also newly routed: Q79 SF0.25 (M0146-0139's finding) and Q18 SF1 (a
    0.03% tie the probe I/O flips, also B-15).
- **COSTTIE — 29 records** (near-ties, waive). New here: Q5 SF0.25. The
  `wsr` `web_site` nested loop costs 19991.00 against goopg's own hash
  alternative at about 19991.2.
- **RELPAGES — 16 records at SF1** → the owner's SF1 reload. New here:
  Q35 SF1 (`customer_address` has about 955 pages, under
  `min_parallel_table_scan_size`).
- **B-15 — 7 records** → ledger `take3-B-15-blocked-2`.
- **GEQO-RNG — 2 records (new class)**, Q64 at both scales: waive.
  - The first divergence is an exact cost tie among 14 one-row probes
    (PG 5.45 = 5.45 at SF0.25), so PG's GEQO tour decides the order.
  - PG's GEQO draws from `pg_prng` (xoroshiro128\*\*); goopg's draws from
    an LCG with a stable pool sort.
  - Even an exact RNG port would not reproduce PG's tours while any tour
    cost differs.
- **RENDERING — 5 records** → M0146-0042 \[!\]:
  - Q6 ×2: a SubPlan argument printed as `$0`;
  - Q58 ×2: InitPlan numbering 1/2 against PG's 2/1;
  - Q5 SF1: carried.
- **Held / carried:** PARAM-APPEND ×2 → M0146-0005dp \[!\];
  HASHSPILL(/WIDTH) ×2 → M0141-S2a-fix2r-a; STATS ×1 → M0146-0009;
  COSTTIE+RELPAGES ×1.

## New tasks filed by this sweep (Parent: M0146-0014b)

| Task | Records | PG mechanism |
|---|---|---|
| M0146-0146 | Q36, Q70, Q86 ×2 | `trivial_subqueryscan` (setrefs.c): a Subquery Scan whose target list carries a computed resjunk ORDER BY key (`CASE WHEN lochierarchy = 0 …`) is not trivial and is kept; goopg strips it |
| M0146-0147 | Q95 ×2 | `check_index_only` needs the columns the query uses; goopg's needed-column walker declines every statement with a WITH clause, so no index-only path is offered (Q94's identical probe is an Index Only Scan) |
| M0146-0148 | Q72 SF1 | `make_rel_from_joinlist` hands the sub-problem's whole RelOptInfo up; goopg publishes only its cheapest-total path (Gather 61108.13), dropping the sorted Gather Merge (about 61108.23) PG builds its nested-loop chain on |

Each first step verifies the mechanism with a trace before changing code.

## Routing table (every divergent record at HEAD)

| scale | query | route | target | first divergence / note |
|---|---|---|---|---|
| sf025 | Q1 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=3 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
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
| sf025 | Q52 | B15 | ledger take3-B-15-blocked-2 | depth=6 [join-order] under Nested Loop: PG Parallel Hash Join Inner \| goopg Parallel Hash Join Inner |
| sf025 | Q54 | PARAM-APPEND | M0146-0005dp [!] | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf025 | Q60 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf025 | Q61 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [aggregation-strategy] under Nested Loop: PG Finalize Aggregate \| goopg Aggregate |
| sf025 | Q68 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q69 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [join-method] under Sort: PG Hash Join Left Anti \| goopg Nested Loop Left Anti |
| sf025 | Q72 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left \| goopg Sort |
| sf025 | Q73 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q76 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [join-method] under Append: PG Parallel Hash Join Inner \| goopg Nested Loop Inner |
| sf025 | Q77 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [aggregation-strategy] under Merge Join: PG GroupAggregate \| goopg Finalize GroupAggregate |
| sf025 | Q78 | COSTTIE | M0146-0014 — waived residual (COSTTIE) (0.06 apart, measured 2026-10-09) | depth=2 [qual-placement] under Sort: PG Merge Join Left \| goopg Merge Join Left |
| sf025 | Q81 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer |
| sf025 | Q83 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=7 [join-method] under Nested Loop: PG Hash Join Left Semi \| goopg Hash Join Inner |
| sf025 | Q85 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=7 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner |
| sf025 | Q87 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort |
| sf025 | Q99 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=6 [join-order] under Hash Join: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q3 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=3 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort |
| sf1 | Q4 | HASHSPILL/WIDTH | M0141-S2a-fix2r-a | depth=3 [aggregation-strategy] under Append: PG HashAggregate \| goopg GroupAggregate |
| sf1 | Q5 | RENDERING | M0146-0042 [!] (held) | depth=8 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q7 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=2 [parallelism] under GroupAggregate: PG Nested Loop Inner \| goopg Gather Merge |
| sf1 | Q11 | HASHSPILL | M0141-S2a-fix2r-a | depth=3 [aggregation-strategy] under Append: PG HashAggregate \| goopg GroupAggregate |
| sf1 | Q13 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=0 [aggregation-strategy] under -: PG Finalize Aggregate \| goopg Aggregate |
| sf1 | Q14 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=6 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q16 | B15 | ledger take3-B-15-blocked-2 | depth=4 [parallelism] under Sort: PG Nested Loop Inner \| goopg Gather |
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
| sf1 | Q37 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=2 [sort-strategy] under Group: PG Nested Loop Inner \| goopg Sort |
| sf1 | Q38 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort |
| sf1 | Q39 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=4 [parallelism] under HashAggregate: PG Hash Join Inner \| goopg Gather |
| sf1 | Q40 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=1 [aggregation-strategy] under Limit: PG Finalize GroupAggregate \| goopg GroupAggregate |
| sf1 | Q42 | B15 | ledger take3-B-15-blocked-2 | depth=6 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q48 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=0 [aggregation-strategy] under -: PG Finalize Aggregate \| goopg Aggregate |
| sf1 | Q52 | B15 | ledger take3-B-15-blocked-2 | depth=6 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q53 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=6 [parallelism] under Sort: PG Gather \| goopg Nested Loop Inner |
| sf1 | Q54 | PARAM-APPEND | M0146-0005dp [!] | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf1 | Q55 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=6 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf1 | Q56 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=5 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort |
| sf1 | Q60 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort |
| sf1 | Q61 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=8 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf1 | Q62 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=5 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner |
| sf1 | Q63 | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) | depth=6 [parallelism] under Sort: PG Gather \| goopg Nested Loop Inner |
| sf1 | Q68 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=3 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner |
| sf1 | Q69 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=2 [qual-placement] under GroupAggregate: PG Nested Loop Left Anti \| goopg Nested Loop Left Anti |
| sf1 | Q71 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=1 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate |
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
| sf025 | Q5 | COSTTIE | M0146-0014 — waived residual (COSTTIE) | depth=8 [join-method] under Gather: PG Hash Join Inner \| goopg Nested Loop Inner  \|\| wsr web_site join: goopg NL+web_site_pkey probe 19991.00 vs its hash alternative ~19991.2 (PG hashes 30 rows, +2.73); 0.001% |
| sf025 | Q6 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=7 [parameterisation] under Aggregate: PG Seq Scan on item j \| goopg Seq Scan on item j  \|\| SubPlan correlated argument printed `$0` vs PG `i.i_category` (M0146-0130 not-covered) |
| sf025 | Q36 | SUBQSCAN-RESJUNK | M0146-0146 | depth=2 [scan-type] under Sort: PG Subquery Scan on sub \| goopg WindowAgg  \|\| PG keeps `Subquery Scan on sub`: the outer ORDER BY's CASE resjunk entry makes the scan tlist non-trivial (setrefs.c trivial_subqueryscan); goopg strips it |
| sf025 | Q49 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=15 [scan-type] under Nested Loop: PG Index Scan web_sales_pkey on web_sales ws \| goopg Bitmap Heap Scan on web_sales ws  \|\| web_sales_pkey probe: goopg Bitmap Heap Scan 12.39 vs PG Index Scan 7.94 |
| sf025 | Q58 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=10 [parameterisation] under Seq Scan: PG - \| goopg InitPlan1  -- InitPlan1 present only on goopg side  \|\| identical structure; InitPlan numbering differs (goopg 1/2, PG 2/1 — sublink planning order) |
| sf025 | Q64 | GEQO-RNG | M0146-0014 — waived residual (GEQO-RNG) | depth=5 [qual-placement] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner  \|\| exact tie among 14 one-row probes (PG 5.45 = 5.45); PG's geqo tour (pg_prng xoroshiro128**) decides the order — agent-joinorder.md |
| sf025 | Q70 | SUBQSCAN-RESJUNK | M0146-0146 | depth=2 [scan-type] under Sort: PG Subquery Scan on sub \| goopg WindowAgg  \|\| same |
| sf025 | Q79 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=2 [join-method] under Sort: PG Nested Loop Inner \| goopg Hash Join Inner  \|\| M0146-0139: customer_pkey probe 8.44 vs PG 4.63, else near-tie |
| sf025 | Q86 | SUBQSCAN-RESJUNK | M0146-0146 | depth=2 [scan-type] under Sort: PG Subquery Scan on sub \| goopg WindowAgg  \|\| same |
| sf025 | Q92 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=8 [scan-type] under Nested Loop: PG Index Scan date_dim_pkey on date_dim date_dim_1 \| goopg Bitmap Heap Scan on date_dim date_dim_1  \|\| date_dim_pkey probe: goopg Bitmap Heap Scan 12.27 vs PG Index Scan 8.32 |
| sf025 | Q95 | WITH-NEEDEDCOLS | M0146-0147 | depth=6 [scan-type] under Hash Join: PG Index Only Scan web_returns_pkey on web_returns \| goopg Index Scan web_returns_pkey on web_returns  \|\| collectStmtColumnNames declines statements with WITH (pathindexonlyneed.go), so web_returns_pkey is never index-only; Q94's identical probe is IOS — agent |
| sf1 | Q6 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=7 [parameterisation] under Aggregate: PG Seq Scan on item j \| goopg Seq Scan on item j  \|\| same as SF0.25 |
| sf1 | Q18 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) (+B-15) | depth=8 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner  \|\| 0.03% near-tie flipped by the 2x probe I/O (22 units) or the missing log2(N) descent (17 units) — agent-joinorder.md |
| sf1 | Q35 | RELPAGES | owner SF1 reload | depth=2 [sort-strategy] under GroupAggregate: PG Incremental Sort \| goopg Sort  \|\| M0146-0131: customer_address ~955 pages < min_parallel_table_scan_size at SF1 |
| sf1 | Q36 | SUBQSCAN-RESJUNK | M0146-0146 | depth=2 [scan-type] under Sort: PG Subquery Scan on sub \| goopg WindowAgg  \|\| same |
| sf1 | Q49 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) | depth=16 [scan-type] under Nested Loop: PG Index Scan web_sales_pkey on web_sales ws \| goopg Bitmap Heap Scan on web_sales ws  \|\| same signature at SF1 (12.39 vs 7.9x) |
| sf1 | Q58 | RENDERING | M0146-0042 [!] (EXPLAIN text) | depth=10 [parameterisation] under Seq Scan: PG - \| goopg InitPlan1  -- InitPlan1 present only on goopg side  \|\| same |
| sf1 | Q64 | GEQO-RNG | M0146-0014 — waived residual (GEQO-RNG) | depth=6 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner  \|\| same exact probe tie (PG 5.31); deeper head difference is B8 — agent-joinorder.md |
| sf1 | Q70 | SUBQSCAN-RESJUNK | M0146-0146 | depth=2 [scan-type] under Sort: PG Subquery Scan on sub \| goopg WindowAgg  \|\| same |
| sf1 | Q72 | SUBPROB-PATHLIST | M0146-0148 | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left \| goopg Sort  \|\| sub-joinlist publishes only CheapestTotal (Gather 61108.13); PG's whole RelOptInfo keeps the sorted Gather Merge (~61108.23) it builds the NL chain on — agent-sort-ios.md |
| sf1 | Q86 | SUBQSCAN-RESJUNK | M0146-0146 | depth=2 [scan-type] under Sort: PG Subquery Scan on sub \| goopg WindowAgg  \|\| same |
| sf1 | Q95 | WITH-NEEDEDCOLS | M0146-0147 | depth=7 [scan-type] under Hash Join: PG Index Only Scan web_returns_pkey on web_returns \| goopg Index Scan web_returns_pkey on web_returns  \|\| same — agent-sort-ios.md |
