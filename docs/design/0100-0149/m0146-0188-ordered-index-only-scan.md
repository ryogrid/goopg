# M0146-0188 — an ordered full index scan is index-only when it can be

Status: closed 2026-10-14.

## Problem

TPC-DS Q77's `ws` and `wr` subqueries drive their nested loops with an
ordered scan of `web_page_pkey`:

- PG 18.3 uses an Index Only Scan;
- goopg used a plain Index Scan (0.14..10.04).

A minimal case shows the same gap. For `SELECT k FROM io_t ORDER BY k LIMIT
10` on a vacuumed table, PG plans `Limit -> Index Only Scan using io_t_pkey`
and goopg planned `Limit -> Index Scan using io_t_pkey`.

## Diagnosis

PG's `build_index_paths` (indxpath.c) builds ONE path per index. It computes
the index's useful pathkeys, and `create_index_path` makes the path
index-only when `check_index_only` allows it. So when the index covers what
the query reads, the ordered full scan *is* the Index Only Scan, and no
plain twin exists.

goopg splits that into two producers:

- `addOrderedIndexPaths` files the ordered full scan as a plain Index Scan;
- `addIndexOnlyPaths` files the full-index index-only path *without*
  pathkeys. Its `colExprs` were computed only for its probe arm.

The index-only path then lost every ordering contest. Where its cost tied
the plain twin's, as it does with no all-visible pages, the twin also won
as incumbent.

## Change

- `addOneIndexOnlyPath` (pathindexonly.go) gives the full-index shape the
  index's useful ordering: `indexPathOrdering`, under the ordered producer's
  own guards (orderable AM, no predicate). The useful-column map is now
  computed for the bare shape too.
- `orderedFullIndexOnlyCovers` names exactly when the index-only producer
  will file that path: `enable_indexonlyscan` on, needed columns known and
  covered, a bare leaf, and no nullable unbound key column.
  `addOrderedIndexPaths` skips such an index. The pair now reproduces PG's
  single path per index.

## Evidence

Files are in `analysis/m0146/m0146-0188/`.

- **Fixture:** PG 18.3 and the new build both plan `Limit -> Index Only
  Scan using io_t_pkey`; HEAD planned an Index Scan. Results are correct.
- **Q77 at SF0.25:** both `web_page_pkey` scans are now Index Only Scans,
  PG's scan type. Q77's categories are unchanged because its `ws` side still
  differs (M0146-0189).
- **Fire set:** match 76 / 64 and the categories are unchanged; only Q77
  fires.
- **SF0.25 sweep:** 99 of 99 pass, Q77 the only change; Q11 ran 8 s → 3 s.
- **TPC-H:** the acceptance arm is value-identical, and the spotcheck timing
  is unchanged (8.9 s).
- **Test:** `TestExplainOrderedIndexOnlyScan` checks PG's plan and the row
  order, and fails with both producers at HEAD.

## Not ported (ledgered)

- The index-only cost differs from PG's on the TPC-DS clusters: goopg's
  `web_page_pkey` Index Only Scan costs 5.04 against PG's 14.04, because the
  heap-fetch term's all-visible fraction differs. That is M0146-0178's
  territory (index-only heap-fetch term).
- The backward-scan arm of `build_index_paths` (descending pathkeys) remains
  unported, as for the plain ordered producer.
