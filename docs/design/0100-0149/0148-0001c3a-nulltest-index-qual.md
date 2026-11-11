# M0148\-0001c3a — `col IS [NOT] NULL` as a btree index qual

**Parent:** M0148\-0001c3 · **Status:** slices 1 to 3 landed 2026\-11\-11
(plain and index\-only scans, leading column, behind an equality prefix);
closed — residue in M0148\-0001c3a1

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

## Slice 3: a NullTest behind an equality prefix

- **Shape.** `a = 3 AND b IS NULL` on `(a, b)`. PG's Index Cond is
  `((a = 3) AND (b IS NULL))`.
- **Producer.** `restrictionNullTestOnColumn` generalises the leading
  NullTest to column n, the one after the equality prefix.
  `addOneRestrictionIndexPath` files it beside that column's range bounds
  (the `prefixRange` arm).
- **Exact, unlike bounds.** Range bounds behind a prefix keep a Filter
  recheck: an open bound runs past the NULL\-keyed entries. A NullTest bound
  is exact, because IS NULL probes `[prefix, NULL]` and IS NOT NULL stops at
  it in `indexRangeBounds`. So its conjunct leaves the Filter.
- **Lowering and rendering.** `createRangeIndexScanPlan` puts it on
  `IndexScan.LeadingNullTest`, which now means the range column
  `Columns[len(RangePrefix)]`. EXPLAIN prints it in the RangePrefix form.
- **Index\-only.** `plainRestrictionBindsOnlyPrefix` treats it as a
  non\-prefix clause, so the plain path keeps the shape; IndexOnlyScan has
  no RangePrefix.
- **Order.** `withNullTestInConjunctOrder` finds a prefixed bound's source
  conjunct by key identity, since such bounds carry no `local`.
- **Probe vs PG.** Row counts identical. PG chooses a Bitmap Heap Scan for
  these shapes, whose bitmap Index Cond carries the NullTest; goopg's bitmap
  producer does not yet (c3a1).
- **Tests.** `TestNullTestIndexQual` plans with bitmap and seq scans off and
  checks the probe and Index Cond text, plus row counts against twins for
  IS NULL, IS NOT NULL, IS NOT NULL with a bound, and IS NULL beside a bound
  (empty).
- **Gates.** units, `TestPort_RegressSuite`, tpch\-spotcheck, sf025 99/99,
  acceptance 24 MATCH, fire\-set `fires=none` on all three corpora.
  - The first RegressSuite run failed `hash_index` and `index_including`.
  - The same diff appeared on a HEAD worktree (VACUUM\-timing\-dependent
    index\-only choice), and the re\-run passed.

## M0148\-0001c3a1 item (1): the bitmap producer

- **Producer** (`buildOneBitmapPath`, pathbitmap.go).
  - After the equality prefix, a NullTest on the next column becomes an
    index clause (`restrictionNullTestOnColumn`).
  - With no prefix, the leading column's NullTest joins the leading
    range (`withNullTestInConjunctOrder`).
  - nulltestsel joins the selectivity.
  - The clause's conjunct is exact, so `createBitmapHeapScanPlan` moves it
    into the Recheck Cond (`bitmapqualorig`) rather than a Filter.
- **Lowering** (`createBitmapIndexScanPlan`). The clause goes onto the new
  `BitmapIndexScan.LeadingNullTest` / `NullTestAfterBounds`. It applies to
  the column after the equality Keys, or to the leading column beside
  LowKey/HighKey.
- **Executor** (`bitmapIndexScanOp.lookupBounds`). NullTest probes go
  through `indexRangeBounds`, with the Keys as the prefix.
- **EXPLAIN.** `formatBitmapNullTestIndexCond` prints PG's text, e.g.
  `Index Cond: ((a = 3) AND (b IS NULL))`.
- **Probe vs PG.**
  - The Bitmap Index Scan lines are identical, and so are the full plans
    for the leading\-column shapes (`b IS NULL`, `b IS NOT NULL AND b <
    300`).
  - The heap node still differs for a literal equality key: goopg prints
    `Recheck Cond: (b IS NULL)` plus `Filter: (a = 3)`, where PG puts both
    in the Recheck Cond. That is M0146\-0012's narrowing: the bitmap arm
    keeps a literal key as a Filter because it skips
    `restrictionKeyUsable`'s cast checks.
- **Tests.** `TestNullTestIndexQual` plans with plain index and seq scans
  off, checks that the Bitmap Index Scan carries the NullTest behind its
  prefix, and compares row counts with twins.

## Not covered (M0148\-0001c3a1)

- **A literal equality key under a bitmap** stays a Filter instead of
  joining the Recheck Cond (M0146\-0012).
- **A range behind an equality prefix under a bitmap**, and **BitmapOr
  over OR clauses** (`thousand = 42 AND (tenthous = 1 OR … OR tenthous IS
  NULL)`): goopg's bitmap producer generates neither (PG's
  `generate_bitmap_or_paths`).

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
- **A NullTest beside an equality on the same column.**
- **Index Cond order.** goopg's leaf Filter is already cost\-sorted
  (order\_qual\_clauses puts the NullTest first), so `x < 5 AND x IS NOT
  NULL` prints the NullTest first, where PG keeps WHERE order.
