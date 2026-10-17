# M0146-0216 slice 1 — physical-parity census, both TPC-DS corpora vs the PG reference

Inputs are SELECT-only on the reference (`:65438` dbs `tpcds025`, `tpcds`) and private clones of goopg's
`data-sf025` (port 5587) and SF1 `data` (port 5588). The census query is `pg_class` relpages / relallvisible /
reltuples plus `pg_relation_size`, for every public table and index. The four outputs and
`census-diff.txt` sit alongside this file.

## Heap

- **relpages and physical size: identical on every table, at both scales.** Heap packing is at parity.
- **reltuples:** identical at SF0.25. At SF1 the three large fact tables differ by under 0.02%: sample
  extrapolation, M0146-0215's class.
- **relallvisible differs on 20 (SF0.25) / 23 (SF1) tables.** goopg is fully all-visible after the owner's VACUUM.
  PG keeps non-visible tails on the large tables (store_sales 12933/12936, catalog_sales 9359/9416) and has 0 on
  every small table (call_center, promotion, store, web_site, …), which autovacuum's insert threshold never
  reached. The PG side is load history, not an algorithm. And goopg cannot carry PG's value even by import:
  goopg's planner reads allvisfrac live from the visibility map (`relAllVisibleFraction`), where PG reads the
  stored `pg_class.relallvisible` that VACUUM/ANALYZE set (`plancat.c:1112`, `vac_update_relstats`). That is the
  one physical input no current mechanism can align.

## Indexes

| index (SF0.25) | goopg corpus | goopg replay (current engine) | PG |
|---|---|---|---|
| inventory_pkey | 14502 | 12774 | 12606 |
| store_sales_pkey | 2481 | 2496 | 2569 |
| catalog_sales_pkey | 1212 | 1226 | 1250 |
| store_returns_pkey | 277 | 277 | 280 |
| web_sales_pkey | 594 | 598 | 598 |
| catalog_returns_pkey | 145 | 138 | 137 |
| customer_demographics_pkey | 5269 | — | 5268 |

Both clusters were loaded the same way (`tpcds-load.sh` / `tpcds-sf025-regression.sh load-pg`): schema with
primary keys, then COPY. So every index is insert-built. A bulk-built `store_sales_pkey` is about 1976 pages.

- **Replay** (`goopg-sf025-replay.txt`): `CREATE TABLE t_rep (LIKE t INCLUDING ALL)` followed by the same
  server-side COPY of the SF0.25 TSV, on the current engine. It moves the worst case, inventory_pkey, from 14502 to
  12774, within 1.3% of PG. The corpus predates the M0146-0213 split-after-new-item port.
- **Remaining insert-split gap:** under 3%, in both directions (store_sales_pkey −2.8%, catalog_sales_pkey −1.9%,
  inventory_pkey +1.3%; web_sales_pkey exact).
- The replay heaps carry a COPY bulk-extension tail (catalog_returns_rep 784 physical pages, 721 used). Both PG
  and the corpus are post-truncation (memory pg_autovacuum_truncates_bulk_extension_tail), so this is not a
  packing difference.

## Routing

1. **Stored relallvisible (engine, PG semantics).** Set it at VACUUM/ANALYZE (`vac_update_relstats`), read it in
   the planner's allvisfrac, and let `pg_restore_relation_stats` set it. Then the M0146-0215 import carries PG's
   VM statistic exactly. This is the next slice.
2. **Index corpus refresh (owner).** Reloading the corpora on the current engine brings index pages within about
   3% of PG.
3. **Residual split-location gap (engine).** Compare goopg's leaf split choice with `_bt_findsplitloc`
   (`nbtsplitloc.c`) on these insert orders.
