# M0146-0014a — parity-closure sweep, routing refresh 2026-10-09

Capture: the M0146-0129 fire-set captures at HEAD `1e6e497f5`, both scales
(`goopg-<sf>.plans.txt`, `pg-<sf>.plans.txt`, `diff-<sf>.txt`).
First-divergence records come from `scripts/pg-plan-first-divergence.py`
(`first-divergence-<sf>.txt`).

| scale | match | divergent | change since 2026-10-04 (`routing-20261004b`) |
|---|---|---|---|
| SF0.25 | 49 / 99 | 50 | match 42 → 49 |
| SF1 | 37 / 99 | 62 | match 33 → 37 |

## Method

- Records whose earlier route is still live keep it ("carried"):
  B8, COSTTIE, RELPAGES, B-15, and PARAM-APPEND → M0146-0005dp \[!\].
- Records whose target has closed since, or whose first divergence moved,
  were re-analysed from plan text. Three read-only passes did this, one per
  family group: `agent-sort.md`, `agent-partial.md` and `agent-probe.md` in
  this directory, each with a cost-number justification per record.
- `records-final.tsv` holds the resulting route for every record.

Acceptance bar (M0146-0014): **no unnamed first-divergence record.** All
112 records route either to a live task or to a named owner residual (the
table at the end).

## Owner-facing residuals (named, measured, not loop work)

- **COSTTIE — near-ties, 31 records → M0146-0014.** Both shapes exist, and
  they are priced within about 1% (most under 0.1%).
  - Q4 SF0.25: 0.15%.
  - Q19, Q40 and Q71 SF1: 0.13–2.3 cost units above the join.
  - Q38 and Q87: PG's worker Unique adds about 0.03%.
  - Q69 SF1: under 0.01%.
  - Q72 SF0.25: about 0.01.
  - Q77: 0.58%.
  - Q78 SF0.25: PG prices its two left-join orders 0.06 apart.
  - Carried from 2026-10-04: Q5, Q13, Q26, Q31, Q33, Q39, Q45, Q48, Q58,
    Q61, Q62, Q65, Q80 and Q99.

  These flip only with exact cost parity in many small terms.
  Recommendation: waive.
- **B8 — 27 records → M0145-0008ag \[!\], decided by the owner through
  M0146-0068's option choice.** `indexProbeCostMultiplier` = 2 makes a
  one-row index lookup cost 16.27 against PG's 8.30, so a Bitmap Heap Scan
  (12.27) wins.
  - Newly confirmed: Q14, Q32 and Q83 at SF0.25, and Q32 and Q92 at SF1.
- **RELPAGES — 15 records at SF1 → the owner's SF1 reload.** The SF1 bench
  cluster predates the current on-disk format.
  - Page counts against PG's: item 736 vs 1284, customer 2046 vs 2872,
    customer\_address 955 vs 1136.
  - Relations below the 1024-page `min_parallel_table_scan_size` lose PG's
    Parallel Hash shapes.
  - M0146-0009h measured that a fresh load gives PG's sizes.
- **B-15 — 7 records → ledger `take3-B-15-blocked-2`.** The btcostestimate
  log2(N) descent term is missing. Porting it regressed TPC-H timings, so
  it is blocked.
- **Held:** M0146-0042 \[!\] for rendering (Q5 SF1 Hash Cond deparse), and
  M0146-0005dp \[!\] (Q54).

## New tasks filed by this sweep (Parent: M0146-0014a)

| Task | Records | PG mechanism |
|---|---|---|
| M0146-0130 | Q6 ×2, Q92 SF0.25 | a correlated SubPlan (PARAM\_EXEC) is parallel-restricted (`max_parallel_hazard_walker`, `set_rel_consider_parallel`) |
| M0146-0131 | Q35 ×2 | the LIMIT fraction picks an ordered (Incremental Sort) grouping input (`get_cheapest_fractional_path`) |
| M0146-0132 | Q5 SF0.25 | `min_parallel_table_scan_size` applies to base rels only, not to appendrel members (`compute_parallel_worker`) |
| M0146-0133 | Q49 ×2 | a Unique keeps its input's pathkeys, so the ORDER BY above gets an Incremental Sort |
| M0146-0134 | Q64 ×2 | pathkeys match through equivalence classes (`cs_item_sk` satisfies `i_item_sk`) |
| M0146-0135 | Q95 SF1 | a parameterised semijoin inner keeps the class's redundant join filter (`get_joinrel_parampathinfo`) |
| M0146-0136 | Q72 SF1, Q95 SF0.25 | recon: a candidate looks missing (the parameterised `d3` probe; the parallel leaf); trace `addPath` before naming a fix |
| M0146-0137 | Q66 ×2 | a Parallel Append prices a non-partial member at per-worker cost (`add_paths_to_append_rel`) |
| M0146-0138 | Q67 ×2, Q18 SF1 | grouping sets: Gather Merge sort order; hashed per-set cost and `hash_mem` limit; the sorted rollup is dropped |
| M0146-0139 | Q79 SF0.25 | hash join build batching cost (PG batches a 9.8 MB build at 8 MB `hash_mem`); re-decide M0139-0007a's held-off arm |
| M0146-0140 | Q36, Q70, Q86 ×2 | harness: the capture wrapper's SQL is a syntax error at `;` on both engines, so these are not plan records |

These are plan-text findings from the analysis passes. Each task's first
step verifies the mechanism with a trace before changing code.

## Routing table (every divergent record at HEAD)

| Scale | Query | First divergence | Family | Route |
|---|---|---|---|---|
| SF0.25 | Q1 | depth=3 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q4 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q5 | depth=9 [join-order] under Nested Loop: PG Parallel Hash Join Inner \| goopg Parallel Hash Join Inner | MECHANISM | M0146-0132 |
| SF0.25 | Q6 | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Inner \| goopg Sort | MECHANISM | M0146-0130 |
| SF0.25 | Q14 | depth=8 [scan-type] under Nested Loop: PG Index Scan item_pkey on item \| goopg Bitmap Heap Scan on item | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q16 | depth=4 [parallelism] under Sort: PG Nested Loop Inner \| goopg Gather | B15 | ledger take3-B-15-blocked-2 |
| SF0.25 | Q17 | depth=4 [parallelism] under Nested Loop: PG Nested Loop Inner \| goopg Gather Merge | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q19 | depth=2 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q25 | depth=4 [parallelism] under Nested Loop: PG Nested Loop Inner \| goopg Gather Merge | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q26 | depth=2 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q29 | depth=4 [parallelism] under Nested Loop: PG Nested Loop Inner \| goopg Gather Merge | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q30 | depth=5 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q32 | depth=6 [scan-type] under Nested Loop: PG Index Scan date_dim_pkey on date_dim date_dim_1 \| goopg Bitmap Heap Scan on date_dim date_dim_1 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q33 | depth=5 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q34 | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q35 | depth=2 [sort-strategy] under GroupAggregate: PG Incremental Sort \| goopg Sort | MECHANISM | M0146-0131 |
| SF0.25 | Q36 | depth=0 [error] under -: PG - \| goopg -  -- error marker in both block | CAPTURE | M0146-0140 |
| SF0.25 | Q37 | depth=6 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner | B15 | ledger take3-B-15-blocked-2 |
| SF0.25 | Q38 | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q39 | depth=4 [parallelism] under HashAggregate: PG Hash Join Inner \| goopg Gather | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q40 | depth=1 [aggregation-strategy] under Limit: PG Finalize GroupAggregate \| goopg GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q42 | depth=6 [join-order] under Nested Loop: PG Parallel Hash Join Inner \| goopg Parallel Hash Join Inner | B15 | ledger take3-B-15-blocked-2 |
| SF0.25 | Q45 | depth=2 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q46 | depth=3 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q49 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | MECHANISM | M0146-0133 |
| SF0.25 | Q52 | depth=6 [join-order] under Nested Loop: PG Parallel Hash Join Inner \| goopg Parallel Hash Join Inner | B15 | ledger take3-B-15-blocked-2 |
| SF0.25 | Q54 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | PARAM-APPEND | M0146-0005dp [!] |
| SF0.25 | Q58 | depth=2 [qual-placement] under Incremental Sort: PG Merge Join Inner \| goopg Merge Join Inner | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q60 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q61 | depth=3 [aggregation-strategy] under Nested Loop: PG Finalize Aggregate \| goopg Aggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q64 | depth=3 [sort-strategy] under GroupAggregate: PG Incremental Sort \| goopg Sort | MECHANISM | M0146-0134 |
| SF0.25 | Q66 | depth=3 [parallelism] under Sort: PG Append \| goopg Gather | MECHANISM | M0146-0137 |
| SF0.25 | Q67 | depth=6 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort | GSETS | M0146-0138 |
| SF0.25 | Q68 | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q69 | depth=3 [join-method] under Sort: PG Hash Join Left Anti \| goopg Nested Loop Left Anti | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q70 | depth=0 [error] under -: PG - \| goopg -  -- error marker in both block | CAPTURE | M0146-0140 |
| SF0.25 | Q72 | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left \| goopg Sort | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q73 | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q76 | depth=6 [join-method] under Append: PG Parallel Hash Join Inner \| goopg Nested Loop Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q77 | depth=5 [aggregation-strategy] under Merge Join: PG GroupAggregate \| goopg Finalize GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q78 | depth=2 [qual-placement] under Sort: PG Merge Join Left \| goopg Merge Join Left | COSTTIE | M0146-0014 — waived residual (COSTTIE) (0.06 apart, measured 2026-10-09) |
| SF0.25 | Q79 | depth=2 [join-method] under Sort: PG Nested Loop Inner \| goopg Hash Join Inner | HASHSPILL | M0146-0139 |
| SF0.25 | Q81 | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q83 | depth=7 [join-method] under Nested Loop: PG Hash Join Left Semi \| goopg Hash Join Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q85 | depth=7 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF0.25 | Q86 | depth=0 [error] under -: PG - \| goopg -  -- error marker in both block | CAPTURE | M0146-0140 |
| SF0.25 | Q87 | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF0.25 | Q92 | depth=3 [parallelism] under Aggregate: PG Nested Loop Inner \| goopg Gather | MECHANISM | M0146-0130 |
| SF0.25 | Q95 | depth=3 [sort-strategy] under Aggregate: PG Nested Loop Left Semi \| goopg Sort | TRACE | M0146-0136 |
| SF0.25 | Q99 | depth=6 [join-order] under Hash Join: PG Hash Join Inner \| goopg Hash Join Inner | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q3 | depth=3 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q4 | depth=3 [aggregation-strategy] under Append: PG HashAggregate \| goopg GroupAggregate | HASHSPILL/WIDTH | M0141-S2a-fix2r-a |
| SF1 | Q5 | depth=8 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner | RENDERING | M0146-0042 [!] (held) |
| SF1 | Q6 | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Inner \| goopg Sort | MECHANISM | M0146-0130 |
| SF1 | Q7 | depth=2 [parallelism] under GroupAggregate: PG Nested Loop Inner \| goopg Gather Merge | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q11 | depth=3 [aggregation-strategy] under Append: PG HashAggregate \| goopg GroupAggregate | HASHSPILL | M0141-S2a-fix2r-a |
| SF1 | Q13 | depth=0 [aggregation-strategy] under -: PG Finalize Aggregate \| goopg Aggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q14 | depth=6 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q16 | depth=4 [parallelism] under Sort: PG Nested Loop Inner \| goopg Gather | B15 | ledger take3-B-15-blocked-2 |
| SF1 | Q18 | depth=2 [aggregation-strategy] under Sort: PG GroupAggregate \| goopg MixedAggregate | GSETS | M0146-0138 |
| SF1 | Q19 | depth=2 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q22 | depth=4 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q23 | depth=4 [parallelism] under Gather: PG Parallel Hash Join Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q24 | depth=7 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q26 | depth=2 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q30 | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q31 | depth=3 [join-order] under HashAggregate: PG Hash Join Inner \| goopg Hash Join Inner | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q32 | depth=6 [parameterisation] under Nested Loop: PG Memoize \| goopg Bitmap Heap Scan on date_dim date_dim_1 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q33 | depth=5 [sort-strategy] under GroupAggregate: PG Sort \| goopg Gather Merge | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q34 | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q35 | depth=2 [sort-strategy] under GroupAggregate: PG Incremental Sort \| goopg Sort | MECHANISM | M0146-0131 |
| SF1 | Q36 | depth=0 [error] under -: PG - \| goopg -  -- error marker in both block | CAPTURE | M0146-0140 |
| SF1 | Q37 | depth=2 [sort-strategy] under Group: PG Nested Loop Inner \| goopg Sort | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q38 | depth=5 [sort-strategy] under Gather Merge: PG Unique \| goopg Sort | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q39 | depth=4 [parallelism] under HashAggregate: PG Hash Join Inner \| goopg Gather | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q40 | depth=1 [aggregation-strategy] under Limit: PG Finalize GroupAggregate \| goopg GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q42 | depth=6 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner | B15 | ledger take3-B-15-blocked-2 |
| SF1 | Q48 | depth=0 [aggregation-strategy] under -: PG Finalize Aggregate \| goopg Aggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q49 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | MECHANISM | M0146-0133 |
| SF1 | Q52 | depth=6 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner | B15 | ledger take3-B-15-blocked-2 |
| SF1 | Q53 | depth=6 [parallelism] under Sort: PG Gather \| goopg Nested Loop Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q54 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | PARAM-APPEND | M0146-0005dp [!] |
| SF1 | Q55 | depth=6 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q56 | depth=5 [sort-strategy] under GroupAggregate: PG Gather Merge \| goopg Sort | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q58 | depth=2 [qual-placement] under Incremental Sort: PG Merge Join Inner \| goopg Merge Join Inner | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q60 | depth=1 [sort-strategy] under Limit: PG Incremental Sort \| goopg Sort | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q61 | depth=8 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q62 | depth=5 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q63 | depth=6 [parallelism] under Sort: PG Gather \| goopg Nested Loop Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q64 | depth=3 [sort-strategy] under GroupAggregate: PG Incremental Sort \| goopg Sort | MECHANISM | M0146-0134 |
| SF1 | Q66 | depth=3 [parallelism] under Sort: PG Append \| goopg Gather | MECHANISM | M0146-0137 |
| SF1 | Q67 | depth=5 [aggregation-strategy] under Subquery Scan: PG GroupAggregate \| goopg MixedAggregate | GSETS | M0146-0138 |
| SF1 | Q68 | depth=3 [join-method] under Nested Loop: PG Nested Loop Inner \| goopg Hash Join Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q69 | depth=2 [qual-placement] under GroupAggregate: PG Nested Loop Left Anti \| goopg Nested Loop Left Anti | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q70 | depth=0 [error] under -: PG - \| goopg -  -- error marker in both block | CAPTURE | M0146-0140 |
| SF1 | Q71 | depth=1 [aggregation-strategy] under Sort: PG Finalize GroupAggregate \| goopg GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q72 | depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left \| goopg Sort | TRACE | M0146-0136 |
| SF1 | Q73 | depth=2 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q75 | depth=8 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q76 | depth=6 [join-method] under Append: PG Parallel Hash Join Inner \| goopg Nested Loop Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q77 | depth=5 [aggregation-strategy] under Merge Join: PG GroupAggregate \| goopg Finalize GroupAggregate | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
| SF1 | Q78 | depth=2 [join-order] under Incremental Sort: PG Merge Join Left \| goopg Merge Join Left | STATS | M0146-0009 (store_returns anti-join estimate 30 vs 281/worker) |
| SF1 | Q80 | depth=12 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner | COSTTIE+RELPAGES | M0146-0014; SF1 reload |
| SF1 | Q81 | depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer \| goopg Bitmap Heap Scan on customer | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q82 | depth=2 [sort-strategy] under Group: PG Nested Loop Inner \| goopg Sort | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q84 | depth=7 [parallelism] under Hash Join: PG Parallel Hash Join Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q85 | depth=7 [join-order] under Nested Loop: PG Nested Loop Inner \| goopg Nested Loop Inner | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q86 | depth=0 [error] under -: PG - \| goopg -  -- error marker in both block | CAPTURE | M0146-0140 |
| SF1 | Q89 | depth=10 [parallelism] under Nested Loop: PG Parallel Hash Join Inner \| goopg Hash Join Inner | RELPAGES | owner SF1 reload (relpages; M0146-0009h closed, reload pending) |
| SF1 | Q92 | depth=7 [parameterisation] under Nested Loop: PG Memoize \| goopg Bitmap Heap Scan on date_dim date_dim_1 | B8 | M0145-0008ag [!] (owner B8 decision, M0146-0068) |
| SF1 | Q95 | depth=6 [qual-placement] under Nested Loop: PG Hash Join Inner \| goopg Hash Join Inner | MECHANISM | M0146-0135 |
| SF1 | Q99 | depth=5 [join-order] under Sort: PG Hash Join Inner \| goopg Hash Join Inner | COSTTIE | M0146-0014 — waived residual (COSTTIE) |
