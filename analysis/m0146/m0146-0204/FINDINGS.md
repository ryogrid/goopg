# M0146-0204 — TPC-DS SF1 Q68 (join-method): findings (2026-10-16)

Binary built from HEAD `7010eab30`. The plan was traced on a private clone of `bench/tpcds/runtime_goopg/data` on
port 5591, with `GOOPG_PGSHAPED_DP_TRACE=1`. EXPLAIN at HEAD reproduces the census plan byte for byte
(`q68-goopg-head.plan.txt`).

## The divergence is relation size, not costing logic

- **Both candidates are built.** In the parallel subtree `{store_sales, date_dim, household_demographics}`, goopg
  generates the partial hash join with a serial `Hash` inner on `household_demographics`
  (`join.hash.partial`, 63301.26). That path beats the parameterised nested loop for the same relset (63305.80).
  See `dppath-ss-dd-hd-partial.txt`. The hash order then loses at the next level by about 6 units.
- **The cost gap is the seq scan.** goopg prices `household_demographics` at 172 (`dppath-base-rels.txt`). PG prices
  it at 161.00. Both are `relpages + 7200 × (cpu_tuple_cost + 2 × cpu_operator_cost)`: 64 + 108 against 53 + 108.
- **The heap file.** goopg's file has 64 blocks. Decoding the pages gives 52 pages of 136 tuples, one of 128, and 11
  initialised empty pages at the end. The 56-byte tuples are laid out exactly as in PG. The 11 empty pages are the
  tail of the COPY bulk extension (`RelationAddBlocks`, ported in `internal/executor/bulk_insert.go`).
- **Why PG has 53 blocks.** The PG corpus was autovacuumed (`relallvisible` 53 of 53). `lazy_truncate_heap`
  returned the empty tail because 11 ≥ 64 / `REL_TRUNCATE_FRACTION`
  (`postgres/src/backend/access/heap/vacuumlazy.c:3189-3190`). goopg's corpus has never been vacuumed: this is the
  M0146-0019 owner corpus residual.
- **Confirmed.** `VACUUM household_demographics` on the clone truncates the heap to 53 blocks. EXPLAIN then elects
  PG's shape: `Hash Join` on `ss_hdemo_sk` over a `Seq Scan` at 161.00, below the `store` join
  (`q68-goopg-after-vacuum-hd.plan.txt`).
- **Other SF1 heaps.** Comparing goopg's and PG's relpages over every SF1 heap, only one other table differs:
  `catalog_page`, 272 against 242. It has the same cause, 242 data pages plus 30 empty ones (30 ≥ 272/16). The
  remaining differences are btree indexes, which belong to a different class (M0146-0195).

Disposition: re-routed to the M0146-0019 owner residual (corpus VACUUM). There is no planner change to make.

## Defects found on the way (filed)

These were reproduced on a scratch goopg server (port 5533) and a scratch PG 18.3 (port 5545). The script is in
`repro-scratch.sql`.

| repro | PG 18.3 | goopg |
|---|---|---|
| `BEGIN; COPY ... FROM STDIN; ROLLBACK` (`\copy` of 4000 rows, then an inline 1 row) | 0 rows remain | 4000, then 4001, rows remain |
| COPY 7200 rows into a fresh table, then `VACUUM` | relpages 53, relallvisible 53 | relpages 53, relallvisible 64 |
| VM fork after that truncation | bits for blocks ≥ 53 cleared (`visibilitymap_prepare_truncate`) | blocks 53-63 still all-visible and all-frozen |
| insert 20000 rows, delete 18000, `VACUUM` twice | heap truncated to 59 pages | 589 pages kept |

- **M0146-0205:** COPY ignores the open transaction block, so its rows survive ROLLBACK (wrong results, S2). This
  was ledgered on 2026-08-13 under M0132-S7, but no task was ever filed.
- **M0146-0206:** VACUUM's tail truncation does not truncate the VM and does not clamp relallvisible
  (`vacuumlazy.c:904`). A tail emptied by DELETE is not truncated at all.

Also noticed: `pg_relation_size('t')` returns NULL and `pg_relation_filepath` does not exist on goopg.
