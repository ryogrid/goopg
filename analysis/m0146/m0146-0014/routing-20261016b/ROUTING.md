# M0146-0014 — S9 census pass at 7eec2fed8 (2026-10-16)

The `routing-20261016` census is stale: 7eec2fed8 (M0146-0198) touched `internal/optimizer`.

Captures at code equal to HEAD `7eec2fed8` (binary `tmp/m0198-bin`, built from the committed diff):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0198/m0198-tpch*` (reloaded corpus) | 15 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0198/m0198-sf025*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0198/m0198-sf1*` | 69 / 99 |

The first-divergence files in this directory were re-run on those captures.

## Routes

The routes come from `routing-20261013/records-final.tsv` and `routing-20261016/ROUTING.md`, checked against today's
fix_plan. No new non-MATCH record appeared since `routing-20261016`; M0146-0198 removed SF0.25 Q33/Q43/Q56/Q60 and SF1
Q33/Q60.

**Corrected: the scan-type records are an owner corpus residual, not actionable.** These are SF0.25 Q23, TPC-H Q13 and
TPC-H Q16. Each diverges where PG runs a full Index Only Scan as a hash-join input and goopg seq-scans. The route is
M0146-0019's recon, recorded in the ledger on 2026-09-28: the goopg corpora are never vacuumed, so `allvisfrac` = 0.
The evidence is re-measured today:

- PG `:65438` tpcds025 `customer` has relallvisible 2853 of 2872 pages (autovacuum after the load).
- The goopg SF0.25 data dir (`bench/tpcds/runtime_goopg/data-sf025`) holds no `_vm` fork at all.
- goopg `:65433` TPC-H has relallvisible 0 on `customer` and `partsupp`.
- The index-only path exists (`pathindexonly.go`), and VACUUM sets the VM. A scratch probe showed relallvisible =
  relpages and Heap Fetches 0, and autovacuum fired on an insert-only table within one naptime.
- **New finding.** goopg's autovacuum trigger counters (`executor.TriggerSnapshot`) do not survive a restart. A table
  loaded and then restarted never reaches the insert threshold. PG writes its cumulative statistics at a clean
  shutdown and reads them back at startup (`pgstat_write_statsfile` / `pgstat_read_statsfile`, pgstat.c), so its
  post-load autovacuum still runs. This is recorded in the ledger. Moving the plan instrument needs the owner's corpus
  VACUUM or reload either way.

The other exclusions:

- **Waived:** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records, as in `routing-20261016`.
  SF1 Q56 (now sort-strategy only) stays STALE-SF1 / M0141-S2b-4e.
- **Closed:** M0146-0198 (join-method, SF0.25 Q43).

## Actionable records by category

| category | records | count |
|---|---|---|
| parameterisation | TPC-H Q2, SF1 Q32, SF1 Q92 | 3 |
| qual-placement | TPC-H Q20, Q21, SF1 Q54 | 3 |
| join-method | SF1 Q68, TPC-H Q18 | 2 |
| sort-strategy | SF1 Q18, TPC-H Q9 | 2 |
| aggregation-strategy | SF1 Q45 | 1 |
| scan-type | — (owner corpus residual, above) | 0 |

parameterisation and qual-placement tie at 3. The differ's category order puts parameterisation first.

**Filed: M0146-0199** (parameterisation, SF1 Q32 and Q92: one mechanism, two records).

- **SF1 Q32, first divergence at depth 6.** It sits under the Nested Loop of SubPlan 1. PG puts a `Memoize`
  (cache key `catalog_sales_1.cs_sold_date_sk`, 88 outer rows) over the `date_dim_pkey` probe, giving a Nested Loop
  cost of 996.41. goopg probes `date_dim_pkey` directly (88 × 7.54, total 1009.29). SF1 Q92 has the same shape
  (depth 7).
- **SF0.25.** Q32 and Q92 MATCH. PG builds no Memoize there: `date_dim` is the outer side.
- **TPC-H Q2** has a different shape. It diverges at depth 1 under Sort, and is left for the census.
