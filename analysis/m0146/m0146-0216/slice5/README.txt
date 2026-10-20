# M0146-0216 slice 5 — COPY heap placement through PG's FSM
#
# Root cause: internal/postmaster/copy.go built server-side COPY's executor context by hand without
# ectx.FSM (and ectx.VM). The bulk insert state's RecordAndGetPageWithFreeSpace step therefore never found
# an earlier page with room (traced: every search returned false, and tryAppendToBlock's FSM records were
# skipped because ctx.FSM == nil), so the heap was filled strictly forward.
# Fix: wire FSM/VM; port PG's FSM tree search (storage/fsm_pgtree.go) and use it on the bulk path
# (GetPageWithFreeSpace, RecordAndGetPageWithFreeSpace, RelationAddBlocks' FreeSpaceMapVacuumRange).
#
# Replay (replay.sh: clone the shipped corpus, CREATE TABLE r_<t> with tpcds.sql's inline key, server-side
# COPY from the corpus TSV; heap = pg_relation_size before VACUUM, pkey pages):
#   replay-full-sf025.txt / replay-full-sf1.txt — every table, both scales.
# Result vs the :65438 census (tmp/m0231/census-pg{025,1}.txt):
#   every pkey equals PG's at both scales (SF0.25 store_sales 2569, SF1 store_sales 11041, store_returns 1082,
#   catalog_sales 5625, customer_demographics 5268).
#   heap pages equal PG's except catalog_page / catalog_returns / household_demographics / web_returns at SF0.25
#   and catalog_page / household_demographics at SF1: COPY's unvacuumed bulk-extension tails. VACUUM of the
#   replayed copies gives 242 / 721 / 53 / 332 = PG.
# Per-block heap row counts vs the PG replica, (ctid::text::point)[0] GROUP BY:
#   SF0.25 store_sales / store_returns / catalog_sales: 0 / 0 / 0 differing blocks (before: 4303 / 387 / 58)
#   SF1    store_sales / store_returns / catalog_sales: 0 / 0 / 0 differing blocks (before: 17539 / 689 / 7361)
#
# Pin probe (internal/postmaster/copy_fsm_placement_test.go): 3000 rows of 800-1200 bytes COPYed on a scratch
# PG 18.3 database: 411 blocks, 8 rows placed back on earlier pages (71:(8,8) ... 2925:(30,8)).
