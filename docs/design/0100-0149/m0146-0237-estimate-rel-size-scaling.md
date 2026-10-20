# M0146-0237 — estimate\_rel\_size scales a stale reltuples to the relation's current size

Status: closed 2026-10-20. Parent: M0146-0233. Evidence: `analysis/m0146/m0146-0237/`.

## 1. Record

M0146-0233's end-to-end pin found this. After ANALYZE of 10000 rows and 10000 more inserted (autovacuum off), PG
estimates a plain scan at 19778 rows and costs it over 89 pages. goopg used the stored reltuples (10000) and relpages
(45), so every estimate on the table was halved.

## 2. What PG does

`estimate_rel_size` (plancat.c) → `table_block_relation_estimate_size` (tableam.c), on every plan:

- curpages = `RelationGetNumberOfBlocks(rel)`, the live size;
- `rel->pages` = curpages, not pg\_class.relpages;
- with `reltuples >= 0 && relpages > 0`: density = reltuples / relpages, and `rel->tuples` = rint(density ×
  curpages);
- otherwise the density comes from the declared widths and the fillfactor, plus the 10-page floor for a
  never-vacuumed relation.

`get_variable_numdistinct` resolves a negative stadistinct against that `rel->tuples`, so a key column's distinct
count grows with the table too.

## 3. What goopg did

- The density arm was already ported (`estimateRelSize`, M0125-0003). It was applied only to never-analysed
  relations, as a deliberate stage-1 divergence: "adopting upstream's scaling is a separate, later decision".
- `tableRows` returned the stored RowCount, and the direct readers of `Stats.RowCount` did the same.
- `baseRelPages` returned `Stats.Pages`.
- ANALYZE stores a distinct count in both forms. `StaDistinct()` reports the relative form once the fraction passes
  0.1, as compute\_scalar\_stats would have stored it. `ResolvedNDistinct` preferred the absolute count, so it
  never scaled. Those two siblings disagreed.

## 4. Change

- **storage** `CachedNBlocks`: an open fork's block counter, with no file-system call.
- **catalog** `RelationBlocksFunc`, wired by initdb with the runtime catalog. It answers from the open fork, or
  after the stat-only `Exists` guard otherwise.
- **optimizer** `liveRelSize` (relsize.go) is `estimate_rel_size` for an analysed table whose statistics carry a
  page count:
  - `tableRows` returns its scaled tuples;
  - `baseRelPages` returns curpages;
  - the direct `Stats.RowCount` readers (raw-row divisors and relTuples in joinkeyproof, nlipricesplice,
    pathbitmap, pathindexrestrict, planner's seqWinsEqualityProbe and subplan\_cost) now read `tableRows`.
- **catalog** `ResolvedNDistinct` resolves the form `StaDistinct` reports: the relative form against the tuple
  count, and the absolute count when no tuple count is given.

## 5. Result

- **Stale-statistics workload** (`stale-stats-probe.sql`): every statement's top EXPLAIN line, costs included,
  equals PG 18.3's:
  - `SELECT * FROM ep`: cost 0.00..286.78, 19778 rows;
  - `id < 9500`: 18788 rows. The relative ndistinct now resolves against 19778; it was 18787 against 10000;
  - the range pair, equality, GROUP BY and count(distinct) also match.
- **Shipped corpora:** relpages equals the actual pages on both corpora, so nothing moves. The sf025 sweep shows
  plans same=99, the SF1 fire set fired nothing, and TPC-H parity stays 22 / 22 with acceptance values identical.
- **Fire-set capture note:** the baseline capture failed Q64's EXPLAIN at SF0.25 (`timeout 120`). Q64 takes 40-235 s
  to plan on every binary, pre-0233 included (`q64-explain-timing.txt`). The candidate captured it as MATCH (98 /
  99).

## 6. Pins

`TestInequalityEstimateReadsActualIndexEndpoints` (postmaster) now asserts PG's exact lines:

- the plain scan's 19778 rows and its cost;
- `id < 9500` at 18788;
- `v > 3` at 8475;
- the endpoint cases at 98 and 1978.

Disabling `liveRelSize` fails it (50 rows), and so does restoring the old `ResolvedNDistinct` order (18787).

## 7. Residuals (ledger)

- A relation whose current size is 0 blocks keeps the stored count. PG would estimate 0 tuples, but goopg's callers
  read 0 as "unknown".
- The 10-page floor and the width-based density for a never-analysed relation still go through the separate
  fallback (`applyRelSizeFallback`). They are not unified with `tableRows`.
- Planning SF0.25 Q64 takes minutes, which brushes the fire-set capture's 120 s timeout under load.
