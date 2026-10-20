# M0146-0233 — ineq_histogram_selectivity's endpoint refresh and cutoff clamp
# endpoint-probe.sql: ANALYZE at ids 1..10000, insert 10001..20000, autovacuum off. Run on the private PG replica
#   (tmp/m21-refclone, scratch db) and a fresh goopg cluster; endpoint-probe-pg-vs-goopg.txt has both EXPLAINs.
#   PG scales reltuples by current pages (19778 rows); goopg keeps 10000 (M0146-0237), so compare selectivities:
#   id > 15000: PG 98/19778 = 0.00495, goopg 50/10000 = 0.005 (without the probe goopg clamped to 1 row).
# fireset/: SF0.25 98/99, SF1 96/99 held; fired Q13 Q48 (results PASS).
# Row-estimate census over the fire-set captures (rows= sequences vs PG, per query):
#   SF0.25 nodes equal to PG 2536 -> 2539, queries with every estimate equal 86 -> 87 (Q48 11/13 -> 13/13, Q13 12 -> 13 of 14)
#   SF1    nodes equal to PG 2603 -> 2606, queries with every estimate equal 85 -> 86 (Q48 13/14 -> 14/14, Q13 10 -> 12 of 16)
