# M0148\-0001c3a4 — ScalarArrayOp and OR index\-path residue, part 2

**Parent:** M0148\-0001c3a2 · **Status:** item \(i\) slices 1–5, \(d\), \(e\), \(g\) and \(j\) landed 2026\-11\-14; item \(f\) and the rest of \(i\) open

Part 1, with the item list and items \(a\), \(b\), \(c\) and \(h\), is
[0148\-0001c3a4\-scalararrayop\-index\-residue.md](0148-0001c3a4-scalararrayop-index-residue.md).
This part holds item \(i\) slices 1–5 and items \(d\), \(g\), \(j\) and
\(e\), in landing order.

## Item \(i\) slice 1 — array literals and quals merged into the array

PG prints every btree qual on the array's own column in the same Index Cond
\(`((unique1 = ANY ('{1,42,7}'::integer[])) AND (unique1 >= 42))`\).
\_bt\_preprocess\_keys merges them into the array before the scan:
\_bt\_merge\_arrays keeps the intersection of two arrays, and
\_bt\_compare\_array\_scankey\_args keeps the elements a bound admits. Before
this slice goopg left those quals in the Filter.

It also had a separate failure. A lone array literal \(`= ANY ('{7, 8, 9}')`\)
became one probe key holding the whole `{7, 8, 9}` string, and the scan
failed with 22P02.

### Change

- **Array literals:** `saopElementsFor` \(pathbitmapor.go\) splits a lone
  `'{…}'` literal into its elements, and so does a cast to the column's own
  array type. This matches collectInValues' single\-element expansion. NULL
  elements become NullConst. A literal it cannot split exactly \(quotes,
  nesting, empty, a cast to another element type\) is not an index qual and
  stays a Filter. The restriction producer and the bitmap producer both use
  it.
- **Column quals:** `saopColumnQuals` \(pathindexrestrict.go\) collects the
  other constant `= ANY` lists and `<` `<=` `>` `>=` bounds on the array's
  column. Each is an `indexPathClause.saopColQuals` entry carrying its
  conjunct, and the entries are sorted by source position, because Index
  Cond keeps restriction order. Only equality arrays take column quals.
  Paths multiply in each qual's selectivity \(restriction and index\-only
  producers\).
- **Plan:** `IndexScan.SAOPColQuals` / `IndexOnlyScan.SAOPColQuals`
  \(`SAOPColQual`: Op/Key or Array, BeforeArray\). `lowerSAOPColQuals`
  drops their conjuncts from the Filter in both createPlan sites.
- **Executor:** `saopWindows` filters the sorted elements before it builds
  any windows. It compares each element's encoded key against the qual's key
  with the tree comparator, and flips the result on a DESC column. An array
  qual keeps the intersection. A `NULL` bound admits nothing. Whatever
  remains is probed as before.
- **Sibling paths:**
  - `indexScanPredicate` rebuilds the quals for the recheck/predicate
    consumers.
  - EXPLAIN prints them before or after the array \(BeforeArray\).
  - `inListArrayConst` prints NULL elements inside the literal, and prints
    a lone NULL as `NULL::type[]`.

### Verification

- **Probe against PG 18.3 \(scratch instance\):** 5 of 7 edge statements
  give the same plan and rows:
  - a cross\-type `> 42.5` stays a Filter, as in PG;
  - `<=` and a commuted `42 < unique1`, on a DESC index;
  - a text column;
  - two arrays plus a bound, in source order.
  The other two differ in ways that predate this slice \(see Not covered\).
- **`TestSAOPArrayLiteralAndColumnQuals`:** 8 PG plans plus their rows.
- **Regress:**
  - create\_index 3015→2933 lines; its only new mismatch line is the
    cross\-type array below.
  - inherit −1: `IN (null, 'cd')` now prints `'{NULL,cd}'::text[]`.
  - The other 20 of the 22 suites are unchanged.
- **Gates:** units, tpch\-spotcheck, sf025.

### Not covered

- **Cross\-type arrays:** `unique1 = ANY ('{33, 44}'::bigint[])` on an int4
  column is not split \(castIsColumnArrayType\). PG merges it through the
  cross\-type opfamily operator.
- **Empty array literal:** slice 5 makes it an index qual when it merges
  into another array. A lone one is still a Filter.
- **Array behind a prefix plus a bound on the array column:** closed by
  slice 2.
- **Array behind a leading range** \(`thousand < 2 AND tenthous IN (…)`\):
  still open.

## Item \(i\) slice 2 — index\-only ScalarArrayOp behind an equality prefix

PG builds one path per index and makes it index\-only when the index covers
the query \(build\_index\_paths\), so `hundred = 42 AND unique1 IN (…)` on
`(hundred, unique1)` prints `Index Only Scan` with
`((hundred = 42) AND (unique1 = ANY (…)))`. goopg's index\-only producer
handled only a leading array, so the plain Index Scan kept these clauses.

### Change

- **Producer:** `indexOnlyLeafClauses` files the equality prefix, the array
  on the next column and the bounds on the column after it, exactly as
  `addOneRestrictionIndexPath` does. Since the plain producer asks
  `restrictionPathIsIndexOnly`, only one of the two builds the path.
- **An equality array outranks a bound on its column:**
  `prefixEqualitySAOP`. With `hundred = 42 AND unique1 IN (…) AND
  unique1 < 100`, both producers used to bind the bound \(prefix range\) and
  leave the array in the Filter. PG makes every btree qual on the column an
  index qual and merges the bound into the array, so the array now becomes
  the column's probe and the bound becomes one of its `saopColQuals`
  \(slice 1\).
- **Costing:** `addOneIndexOnlyPath` finds the array clause by position
  \(`indexClausesSAOPAt`\). It multiplies in the prefix's equality
  selectivity and prices the bounds after the array, matching the plain
  producer.
- **Plan and executor:** the prefix lowers onto `IndexOnlyScan.Keys` ahead
  of `SAOPKeys`, the same layout as BitmapIndexScan. `indexOnlyScanOp` hands
  Keys to `saopWindows` as the prefix, and EXPLAIN renders the prefix as the
  index scan's RangePrefix. `lateralProbeIsPartialProbe` and
  `isLateralIndexProbe` no longer read those Keys as an equality probe.
- **Selectivity of a lone array literal:** the InExpr arm of
  `clauseSelectivity` now splits `'{100,300}'` into its elements
  \(`arrayLiteralElements`, shared with `saopElementsFor`\), as
  scalararraysel deconstructs the constant array. `unique1 < ANY
  ('{100,300}')` used to get the default 1/3, which made the row estimate
  33 where PG has 4.

### Verification

- **Live server against a scratch PG 18.3:**
  - Plans: on 7 EXPLAIN variants, costs match PG to the cent \(12.89,
    8.65, 12.90, 8.61, …\).
  - Rows: every statement in the probe script returns the same rows.
  - The two remaining plan differences are the constant\-prefix Sort
    \(`ORDER BY unique1` behind `hundred = 42`, M0148\-0001c3a5 \(d\)\), which
    also hides PG's `Index Only Scan Backward`.
- **`TestIndexOnlySAOPBehindPrefix`:** 4 PG plans with their rows.
  - The unit VM fixture keeps relallvisible at 0, which overprices
    index\-only heap fetches by random\_page\_cost per row. Its inequality
    case therefore runs with bitmap scans off.
  - A live server does not have this problem \(relallvisible = relpages
    after VACUUM\).
- **Regress:** the 22 suites are byte\-identical to slice 1. The
  create\_index prefix\-array cases select columns outside the index, so
  they stay plain Index Scans.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Index\-only width:** the scan's width counts columns used only by
  index quals \(`width=8` where PG prints 4\). PG prices the reltarget.
- **Prefix plus a range, index\-only:** `hundred = 42 AND unique1 < 100`
  covered by the index is still a plain Index Scan
  \(plainRestrictionBindsOnlyPrefix declines\).
- **An array behind a leading range:** closed by slice 3 for index\-only
  scans.

## Item \(i\) slice 3 — an array behind a leading range; descents per array

PG's `thousand < 2 AND tenthous IN (1001,3000)` on `(thousand, tenthous)` is
an Index Only Scan with `((thousand < 2) AND (tenthous = ANY (…)))`. nbtree
turns the range column into a skip array and applies the second column's
array to every entry it reads. goopg left the array in the Filter.

### Change

- **Producer:** `indexOnlyLeafClauses`' leading\-range branch also files an
  `= ANY` array on the second column, when that column is covered and the
  range column has no NullTest.
- **Plan:** `IndexOnlyScan.TrailingSAOP` \(elements plus column quals,
  constants only\). `createIndexScanPlan` lowers the range as before and
  drops the array's conjuncts from the Filter.
- **Executor:** `indexOnlyScanOp` checks each index entry against the array
  \(`trailingSAOPQual`, evaluated over the row decoded from the key\)
  before any heap fetch. Heap Fetches therefore counts only entries that
  pass, as PG's index quals do. An undecodable key is checked on the heap
  row instead. The output stays in index order, forward and backward.
- **EXPLAIN:** the range text and the array text \(rendered by the index
  scan's renderer over the index shifted to column 1\) are spliced into one
  AND \(`indexCondInner`\).
- **Costing — btcostestimate's skip\-array branch** \(selfuncs.c:7399\-7590\):
  - The range column's ndistinct, scaled by the range's own selectivity,
    becomes num\_sa\_scans.
  - If that fraction is below DEFAULT\_RANGE\_INEQ\_SEL, the ndistinct is a
    default, or the descents would exceed the index's pages, PG stops
    building indexBoundQuals before the array. The array then adds no
    descents and does not narrow numIndexTuples \(`boundSelectivity` = the
    range alone\).
  - Either way it narrows indexSelectivity on the heap side
    \(genericcostestimate\), and it costs a qual op per index tuple.
- **Three costing fixes to slices 1–2, found by the same probe:**
  - Column quals count as index quals: `indexQualCount` for qual\_op\_cost,
    and `indexClausesEvalOps` removes them from the qpquals.
  - Every array multiplies num\_sa\_scans \(`saopDescents`\): two arrays
    of 3 are 9 descents.
  - The bitmap ScalarArrayOp carrier merges the column quals too
    \(`BitmapIndexScan.SAOPColQuals`, applied by saopWindows, kept in the
    Recheck Cond and printed in Index Cond\). Without this, a two\-array
    query fell to a bitmap path that was cheaper only because it ignored the
    second array.

### Verification

- **Live server against a scratch PG 18.3:**
  - Plans: the 8 leading\-range statements \(forward, backward, column
    quals, the bound\-counted `thousand < 500` arm\) have identical plans and
    costs.
  - Rows: every statement returns the same rows.
  - The slice 1–2 probe sets now match PG on costs too, including the
    two\-array case \(34.69\) and its forced bitmap plan.
- **`TestIndexOnlySAOPBehindLeadingRange`:** 4 PG plans with rows, on the
  heap\-fetch path.
- **Regress:**
  - create\_index 2933→2856, with no new mismatch line.
  - join \+5 is the known row\-order flap \(a rerun gives 17626\).
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Plain Index Scan behind a leading range:** closed by slice 4.
- **Real skip descents:** the scan reads the whole leading range and checks
  each entry. Results and order are PG's, but it does not reposition per
  element the way nbtree does.
- **Arrays behind a range further right:** an array on a column after the
  second, or behind a range with a NullTest, stays in the Filter.

## Item \(i\) slice 4 — the plain Index Scan's trailing array

When the index does not cover the query, PG uses an `Index Scan` \(or
`Index Scan Backward`\) with the same
`((thousand < 2) AND (tenthous = ANY (…)))`. goopg kept a bitmap or range
scan with the array in the Filter.

### Change

- **Planner:**
  - The plain producer's leading\-range branch files the trailing array
    \(`trailingSAOPOnRange`, shared with the index\-only producer\).
  - It is priced by the same skip\-array arm, now the shared
    `trailingSAOPSelectivity`.
  - `createRangeIndexScanPlan` lowers it onto `IndexScan.TrailingSAOP` and
    drops its conjuncts from the Filter.
  - `trailingSAOPOnRange` refuses interval key columns, whose keys cannot be
    decoded.
- **Executor:** `trailingKeyCheck` \(index\_trailing\_saop.go\) decodes the
  second key column from each entry. It handles the tuple\-format
  descriptor and the blob format. `indexScanOp.scanAppendEntry` applies the
  check before the TID is queued for a heap fetch, so a rejected entry is
  never fetched, as with nbtree's array keys. The same callback serves the
  backward cursor.
- **Siblings:**
  - `indexScanPredicate` adds the array to the predicate it rebuilds.
  - EXPLAIN shares the IOS splice \(`withTrailingSAOPCond`\).
  - The index\-only scan's predicate and decodability now come from the
    shared helpers \(`trailingSAOPPred`, `predAdmits`,
    `indexKeyDecodable`\).
- **A hidden fallback:** createIndexScanPlan's backward guard panicked on
  any plain SAOP path. The panic was recovered upstream, so the backward
  trailing path silently lost to Sort \+ forward. The trailing shape is now
  allowed there.

### Verification

- **Live server against a scratch PG 18.3:** the five plain statements
  \(forward, backward, the bound\-counted arm, a column qual, a NULL
  element\) have identical plans, costs and rows. Only `width` differs, as
  it already did for any `SELECT *` on this table.
- **`TestIndexScanSAOPBehindLeadingRange`:** 3 PG plans with rows.
- **Regress:** the 22 suites are unchanged from slice 3 \(join back at
  17626\).
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

## Item \(i\) slice 5 — the empty array literal

PG makes `= ANY ('{}')` an index qual like any other array. Merged into
another array, it leaves the intersection empty:
`((unique1 = ANY ('{42,43,7}'::integer[])) AND (unique1 = ANY ('{}'::integer[])))`.
On its own it is a probe with no elements.

### Change

- `arrayLiteralElements` returns an empty, non\-nil element list for
  `'{}'`. Before, it refused the literal, so the qual stayed in the Filter.
- **Primary array:** the producers \(`restrictionSAOPOnColumn`,
  `bitmapSAOPOnColumn`\) still refuse an empty list, because goopg's probe
  shapes read "no elements" as "no array".
- **Column quals:** a merged column qual \(`saopColumnQuals`\) takes it.
  `saopWindows` then keeps no element, and the scan reads nothing. This works
  on the plain, index\-only, bitmap and trailing\-array paths.
- **Selectivity:** the InExpr estimator splits the empty literal too, so it
  prices 0, as scalararraysel's merge over no elements does. Rows then clamp
  to 1, as PG's do.
- **EXPLAIN:** `inListArrayConst` prints an empty list as `'{}'::type[]`.
  Before, it declined, and the text came out as the invalid `= ANY ()`.

### Verification

- **Live server against a scratch PG 18.3:** the empty\-array statements
  have identical plans, costs and rows on the index\-only, plain and
  trailing\-array paths. The slice\-1 probe set's residue went from 21 to
  16 diff lines.
- **`TestSAOPArrayLiteralAndColumnQuals`:** the PG plan, with no rows.
- **Regress:** the 22 suites are unchanged from slice 4, and `arrays` is
  byte\-identical to HEAD \(run against a HEAD build\).
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **A lone empty array:** `unique1 = ANY ('{}')` alone is still a Filter.
  PG uses an Index \(Only\) Scan with `Index Cond: (unique1 = ANY
  ('{}'::integer[]))` at 4.30. goopg needs a zero\-element probe shape
  first.

## Item \(d\) — sub\-ORs inside AND arms

PG's generate\_bitmap\_or\_paths treats a sub\-OR inside an AND arm of the
OR in two ways:

- **In build\_paths\_for\_OR:** the sub\-OR stays one of the arm's
  clauses. match\_orclause\_to\_indexcol may take it as a ScalarArrayOp, as
  in `((thousand = 42) AND (tenthous = ANY ('{1,3}')))`.
- **By recursion:** the function recurses over the arm's items \("Recurse in
  case there are sub\-ORs"\), offering each sub\-OR's own BitmapOr to
  choose\_bitmap\_and.

goopg's `bitmapOrArmPath` refused any arm holding a sub\-OR, which dropped
the whole BitmapOr \(`thousand = 42 AND (tenthous = 1 OR tenthous = 3) OR
thousand = 41` became an Index Only Scan with a Filter\).

### Change

- `bitmapOrPathsFor(clauses, otherClauses)` is generate\_bitmap\_or\_paths
  proper. It returns BitmapOr paths, with all\_clauses as each arm's
  context. `bitmapOrHeapPaths` wraps its result for the leaf's conjuncts.
- `bitmapOrArmPath` keeps a sub\-OR among the arm's clauses
  \(`bitmapSAOPOnColumn` already matches an OR of equalities on a column\).
- For an AND arm, it recurses through `bitmapOrPathsFor` over the arm's
  items and appends the BitmapOr candidates to choose\_bitmap\_and's list.

### Verification

- **Live server against a scratch PG 18.3, regress tenk1:** the first case
  matches PG plan for plan and cost for cost \(48.05\). Only the
  bitmap\-node startup and width differ, which is item \(g\).
- **`TestBitmapOrSubORInAndArm`:** the PG plan on a fixture table, with
  rows.
- **Regress:**
  - create\_index 2856→2832.
  - Its one new mismatch line is a weaker Recheck Cond for `hundred = 42 AND
    (thousand = 42 OR thousand = 99 OR tenthous < 2) OR thousand = 41`. It
    was a Seq Scan before, so this is a shape improvement \(see Not
    covered\).
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered — item \(j\)

- PG 18 serves `tenthous < 2` from `(thousand, tenthous)` with a skip scan
  over the leading column and a range on the second. This is a bitmap index
  scan at 195.28, and an Index Only Scan too.
- goopg's skip scan takes only equality runs \(restrictionSkipRun\), so that
  arm has no index path.
- As a result, PG's `BitmapAnd(hundred, BitmapOr(thousand = ANY, tenthous <
  2))` cannot form. goopg keeps the `hundred = 42` arm alone, which is
  correct, with the full OR in the Filter.

## Item \(g\) — the bitmap node cost line

A bitmap tree node returns a bitmap, not tuples. create\_bitmap\_subplan
therefore sets:

- `plan_width = 0` on every Bitmap Index Scan, BitmapAnd and BitmapOr;
- a Bitmap Index Scan's `startup_cost = 0.0`;
- its `total_cost = ipath->indextotalcost`, without cost\_bitmap\_tree\_node's
  `0.1 * cpu_operator_cost * rows`, which the parent's cost carries.

goopg stamped the path's own figures instead, for example
`(cost=0.29..5.06 rows=100 width=244)` where PG prints
`(cost=0.00..5.04 rows=100 width=0)`.

### Change

- `costBitmapIndexScan` also returns indextotalcost alone. The two bitmap
  producers store it on `Path.BitmapIndexAMTotal`.
- `stampPlanCost` gives a Bitmap Index Scan startup 0, width 0 and that
  total, and gives BitmapAnd / BitmapOr width 0.
- Planning is untouched: paths keep their costs, and only the stamped plan
  cost changes.

### Verification

- **Regress tenk1 probe against a scratch PG 18.3:** every bitmap node line
  matches, apart from the shapes that wait on \(j\).
- **TPC\-DS SF0.25:** the plan A/B fired on the 7 queries with a Bitmap
  Index Scan \(Q3 Q32 Q53 Q54 Q55 Q63 Q92\), all on the cost line alone.
  - Their values pass.
  - 6 of the 7 Bitmap Index Scan lines now equal PG's text.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set PASS on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH;
  - regress unchanged \(COSTS OFF\).

### Not covered

- **A parameterized Bitmap Index Scan's indextotalcost:** on TPC\-DS Q53,
  goopg's is 4.75 where PG's is 4.64. PG's amcostestimate pro\-rates by
  loop\_count inside indextotalcost.
- **The Bitmap Heap Scan's width:** it is the scan's full row width
  \(`width=244` under `count(*)`\), where PG prices the reltarget. This is
  the same gap as the index\-only and plain scan widths.

## Item \(j\) — skip scan with a range on a later column

PG 18 makes `tenthous < 2` on `(thousand, tenthous)` an index qual. Skip
arrays fill the unbound leading column \(nbtpreprocesskeys.c
`_bt_skiparray`\), so the plan is `Index Cond: (tenthous < 2)` on an Index
Only Scan, Index Scan \(Backward\) or Bitmap Index Scan.

goopg's skip scan took only equality runs \(restrictionSkipRun\). Without
this, the predicate was a Filter on a full index scan, and as a BitmapOr arm
it had no path at all. That blocked create\_index's `BitmapAnd(hundred,
BitmapOr(thousand = ANY, tenthous < 2))`.

### Change

- **Clauses:** `restrictionSkipRange` takes the first column after the
  leading one that carries bounds, when no earlier column carries any qual.
  The skipped columns must be ascending and NULL\-safe, and no interval
  column may appear.
- **Producers:**
  - the plain producer, when there is no skip run;
  - the index\-only producer, when nothing else binds;
  - the bitmap producer, with constant bounds only.
- **Costing:** each producer prices it with `skipScanDescents`
  \(btcostestimate's skip arm\). When the estimate reverts,
  boundSelectivity is 1 and the index side covers the whole index. For
  tenk1 that gives 195.28 / 195.31 / 203.30, which are PG's figures.
- **Plan:** `SkipPrefix` with no Keys, and LowKey/HighKey on
  `Columns[SkipPrefix]`, on IndexScan, IndexOnlyScan and BitmapIndexScan
  \(a new field there\). `clausesSkipRange` recognises the shape at
  lowering.
- **Executor:** all three scans read the whole index and check each entry's
  key column with `newIndexColumnCheck`. That is the trailing\-array check
  from slice 3, generalised to any column, with `skipRangePred` as the
  qual. The check runs before any heap fetch, and backward reads use the
  same callback. The bitmap scan needs no recheck.
- **Siblings:**
  - `indexScanPredicate` rebuilds the bounds on the right column.
  - EXPLAIN prints them under that column for all three node types.
  - The plain scan's equality\-skip dispatch \(rescanSkip, walkLeaf,
    skipNextGroup\) now requires Keys \(`skipRangeScan`\).

### Verification

- **Live server against a scratch PG 18.3, regress tenk1:** 10 statements
  have identical plans, costs and rows:
  - IOS, plain, backward, two bounds;
  - the item \(d\) BitmapAnd and BitmapOr cases \(248.78, 213.31\).
  Only `width` differs \(ledgered\).
- **`TestSkipScanRangeOnLaterColumn`:** 4 PG plans with rows.
- **Regress:** create\_index 2832→2801 with no new mismatch line; join
  \+5 is the flap.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Real skip descents:** the scan reads the whole index rather than
  repositioning per skipped value \(\_bt\_advance\_array\_keys\). Results
  and order are the same. PG's own cost reverts to a whole\-index read when
  the descents would exceed the index's pages, as they do for tenk1.
- **Narrower shapes not taken:**
  - a range on the skipped column itself, which feeds indexSkipQuals into
    ndistinct;
  - a range followed by an equality run;
  - a skip range behind a partial skip run.

## Item \(e\) — partial indexes proved by predicate\_implied\_by

PG decides whether a partial index is usable \(predOK,
check\_index\_predicates\) with `predicate_implied_by(indpred, the
restriction list)`. goopg's partialPredicateProvenBy needed one conjunct
that matched the predicate exactly \(`provePartialIndexPredicate`\), and
that failed for a `name` literal. So the regress select suite's
`onek2_u2_prtl` \(`WHERE stringu1 < 'B'`\) and `onek2_u1_prtl` \(`WHERE
unique1 < 20 OR unique1 > 980`\) were never used. Plain scans and BitmapOr
arms \(useful\_predicate's proven\-predicate route\) were affected alike.

### Change

- **predOK:** `partialPredicateProvenBy` runs `predicateImpliedBy`, the
  predicate\_implied\_by\_recurse port, over the conjuncts as one AND. So
  `unique1 = 0` proves the OR predicate by operator inference.
- **The other three sites** use the same prover:
  - the Filter drop of quals the predicate implies
    \(`partial_index_implied_quals.go`, `predicate_implied_by(clause,
    indpred)`\);
  - the BitmapOr arm subqual's predicate test;
  - the legacy planner.go guard.
- **name constants:** `operatorPredicateProof` now also compares `name`
  constants \(`nameComparison`\), bytewise, since a name column's collation
  is always C. So `stringu1 = 'ATAAAA'` implies `stringu1 < 'B'`. The
  implication table is now `btreeImplication`.
- **Recheck Cond:** for a single partial index, create\_bitmap\_subplan's
  order applies: the index quals first, then the predicate, and the
  predicate only when the quals do not already imply it.

### Verification

- **Probe against a scratch PG 18.3:** an onek2\-like fixture
  \(`TestPartialIndexPredicateProof`\) gives PG's 4 plans and rows: plain
  with and without the implied Filter, and both BitmapOr shapes.
- **Regress:**
  - select 256→45. Every partial\-index case now matches; what is left is
    the index\-only scan over a proven partial index \(M0148\-0001c3b item
    1\), `Index Searches` in EXPLAIN ANALYZE, and an unrelated One\-Time
    Filter.
  - portals\_p2 still passes, which is the 2026\-08\-07 wrong\-rows guard.
  - join: j2\_id1\_idx is now used, with PG's Merge Cond and Join Filter
    structure.
  - rowsecurity differs only by pointer\-address noise.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Operator inference for other types:** it covers integer and `name`
  constants only. Text, numeric and dates need the opfamily's comparator,
  and the collation for text.
- **Refutation:** `predicate_refuted_by` is not ported.
