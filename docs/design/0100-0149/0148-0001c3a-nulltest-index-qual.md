# M0148\-0001c3a — `col IS [NOT] NULL` as a btree index qual

**Parent:** M0148\-0001c3 · **Status:** slices 1 and 2 landed 2026\-11\-11
(plain and index\-only scans, leading column); open for the bitmap and
behind\-equality producers

## Divergence

Table `nt (x int, y int)` with every tenth `x` NULL and an index on `x`:

| query | PG 18.3 | goopg before |
|---|---|---|
| `select * from nt where x is null` | `Index Scan … Index Cond: (x IS NULL)` | `Seq Scan … Filter: (x IS NULL)` |
| `select * from nt where x is not null and x < 5` | `Index Cond: ((x IS NOT NULL) AND (x < 5))` | `Index Cond: (x < 5)`, `Filter: (x IS NOT NULL)` |
| `select x from nt where x is not null order by x limit 1` | `Index Only Scan … Index Cond: (x IS NOT NULL)` | `Index Scan … Filter: (x IS NOT NULL)` |

## PG mechanism

- **Index clause.** `match_clause_to_indexcol` (indxpath.c) accepts a
  NullTest on an index column as an index clause when the access method has
  `amsearchnulls`, which btree does.
- **Scan keys.** `_bt_preprocess_keys` turns `IS NULL` into an
  `SK_SEARCHNULL` scan key and `IS NOT NULL` into `SK_SEARCHNOTNULL`.
  - `IS NULL` positions the descent on the column's NULL group.
  - `IS NOT NULL` stops the scan at that group.
  - The group sits at the end the column's NULLS FIRST / LAST option names.
- **Unsatisfiable combination.** `IS NULL` beside a comparison on the same
  column can never be true; `_bt_preprocess_keys` reports it so and the scan
  returns nothing.
- **Selectivity.** `nulltestsel`.
- **Order.** `Index Cond` lists one column's clauses in baserestrictinfo
  order.

## Change (slice 1)

- **Planner** (internal/optimizer).
  - `restrictionLeadingNullTest` (pathindexrestrict.go) finds a NullTest on
    the leading key column. It applies when the restriction producer would
    build a leading\-range path (no equality, SAOP or skip binding the
    column). It runs on its own or beside the range bounds, which
    `withNullTestInConjunctOrder` keeps in restriction\-list order.
  - **`IS NULL`** is taken only for an index that stores NULL\-keyed entries
    (`catalog.IndexHasNullKeyedEntries`). An older cluster's index does not
    contain those rows, so there the qual stays a Filter.
  - **`IS NOT NULL`** is always answerable, because an index without
    NULL\-keyed entries holds only non\-NULL keys.
  - `rangeIndexSelectivity` includes the NullTest conjunct (nulltestsel).
- **Lowering** (createplanindex.go). The clause (`indexPathClause.nullTest`)
  goes through the range arm onto `IndexScan.LeadingNullTest` and
  `NullTestAfterBounds`, and its conjunct leaves the Filter.
  `tryPromoteIndexOnlyScan` refuses such a scan: IndexOnlyScan has no
  NullTest field, and the qual's Filter is already gone.
- **Executor** (`indexRangeBounds`, operators\_index.go).
  - `IS NULL` probes `[k, compositeUpperBound(k)]`, where `k` is the prefix
    plus a NULL key part (`nullStopRangeBound`). Beside a value bound it
    returns no rows.
  - `IS NOT NULL` with no value bound applies the same NULL stop the
    one\-sided range scan already used. The stop is exclusive, and sits at
    the high end for NULLS LAST or the low end for NULLS FIRST in index
    order.
- **EXPLAIN** (`formatIndexCondPrefixed`) prints `col IS [NOT] NULL` before
  or after the bounds.

## Tests and measurement

- **`TestNullTestIndexQual`** (executor, NULL\-keyed capability on).
  - Plan text: `Index Cond: (a IS NULL)` and
    `Index Cond: ((a IS NOT NULL) AND (a < 5))`, with no Filter left.
  - Results match a non\-indexable twin (`(a + 0) IS NULL`) on ASC, NULLS
    FIRST, DESC and composite `(a, b)` indexes.
  - The case list includes `IS NULL AND a < 5`, which must return nothing.
  - With the capability off, `IS NULL` never becomes an index qual.
- **Probe against a scratch PG 18.3.** Plan text and counts are identical
  for the first two rows of the table above and for a DESC index.
- **Regress, 17 suites:** line\-count and hunk identical. The NullTest
  hunks left in create\_index belong to later slices or to GiST.
- **Gates:** units, `TestPort_RegressSuite`, tpch\-spotcheck, sf025 99/99,
  acceptance 24 MATCH, fire\-set `fires=none` on all three corpora.

## Slice 2: index\-only scans

- **Planner.** `indexOnlyLeafClauses` (pathindexonly.go) composes the
  leading range with `restrictionLeadingNullTest`, exactly as the plain
  producer does. `plainRestrictionBindsOnlyPrefix` counts a leading NullTest
  as a non\-prefix clause, so the index\-only path stands in for the plain
  one. Its selectivity and pathkeys follow `hasRangeClause`.
  `createplanindex.go` lowers the clause onto the new
  `IndexOnlyScan.LeadingNullTest` / `NullTestAfterBounds`.
- **Promotion.** `tryPromoteIndexOnlyScan` now copies the fields instead of
  refusing the scan.
- **Executor.** The index\-only scan has its own copy of the range code,
  so `indexOnlyScanOp.lookupRangeBounds` gets the same NullTest arm as
  `indexRangeBounds`.
- **EXPLAIN.** `formatIndexOnlyCondPrefixed` renders the qual through the
  helper both scan nodes share, `formatNullTestIndexCond`.
- **Probe vs PG.** These are now identical to PG:
  - `select x from nt where x is not null order by x limit 1`: Index Only
    Scan, `Index Cond: (x IS NOT NULL)`;
  - `count(*) … where x is null` and `x is not null and x < 5`.
- **Tests.** `TestNullTestIndexQual` adds the plan text and twin results for
  those shapes.
- **Regress.** Neutral. create\_index's duplicated NullTest now prints one
  `Index Cond` plus one Filter (two more diff lines); join shows its known
  row\-order variance.
- **Gates.** units, `TestPort_RegressSuite`, tpch\-spotcheck, sf025 99/99,
  acceptance 24 MATCH, fire\-set `fires=none` on all three corpora.

## Not covered (still under M0148\-0001c3a)

- **Duplicated or contradictory NullTests on one column**
  (`unique1 IS NULL AND unique1 IS NULL`, regress create\_index). Only the
  first becomes an index qual, and the rest stay a Filter; PG puts all of
  them in Index Cond.
- **Backward scans.** `order by x desc limit 1` with `IS NOT NULL` is an
  Index Only Scan Backward in PG. goopg's path\-based index scans carry no
  scan direction at all (createplanindex.go refuses a non\-forward
  `IndexScanDir`), so goopg sorts.
- **min/max rewrite.** Its `IndexOnlyScan.Cond` prints an `Index Cond` but
  is a residual per index row, not a scan key.
- **Bitmap producer.** `Index Cond: ((thousand = 42) AND (tenthous IS NULL))`
  under a BitmapOr.
- **A NullTest behind an equality prefix** (`a = 1 AND b IS NULL`), or beside
  an equality on the same column.
- **Index Cond order.** goopg's leaf Filter is already cost\-sorted
  (order\_qual\_clauses puts the NullTest first), so `x < 5 AND x IS NOT
  NULL` prints the NullTest first, where PG keeps WHERE order.
