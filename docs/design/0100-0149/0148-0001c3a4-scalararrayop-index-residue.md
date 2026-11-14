# M0148\-0001c3a4 — ScalarArrayOp and OR index\-path residue

**Parent:** M0148\-0001c3a2 · **Status:** items \(a\), \(b\), \(c\) and \(h\) landed 2026\-11\-12; item \(i\) slices 1–5, \(d\) and \(g\) landed 2026\-11\-14; items \(e\), \(f\), \(j\) and the rest of \(i\) open

This item collects what M0148\-0001c3a2 \(BitmapOr paths\) left open. The items
are listed in `.ralph/fix_plan.md` under M0148\-0001c3a4:

- \(a\) inequality OR groups;
- \(b\) OR→SAOP behind an equality prefix on plain paths;
- \(c\) leading SAOP plus a range bound;
- \(d\) sub\-ORs inside AND arms;
- \(e\) useful\\\_predicate arms;
- \(f\) join OR bitmap paths;
- \(g\) the bitmap node cost line;
- \(h\) index\-only SAOP scans, and the bitmap SAOP carrier with bounds;
- \(i\) the array's own column: array literals, further arrays and bounds
  merged into the probe; an array behind a range or behind a prefix on an
  index\-only scan.

## Item \(c\) — a bound behind a leading ScalarArrayOp is an exact index qual

PG plans `thousand IN (182, 183) AND tenthous > 7550` on `(thousand,
tenthous)` with `Index Cond: ((thousand = ANY ('{182,183}'::integer[])) AND
(tenthous > 7550))` and no Filter. nbtree runs one `_bt_first` per array
element, and the second\-column bound is part of every descent's scan keys.

goopg already printed that Index Cond and bounded each element's descent
\(M0145\-0029\). But it kept the bound in the Filter as a recheck, because the
SAOP descent built its per\-element windows with its own code:

- **No NULL\-group stop.** In a cluster that stores NULL\-keyed entries, the
  open end of `tenthous > 7550` runs into the element's `(182, NULL)`
  entries.
- **No DESC swap.** On `(thousand, tenthous DESC)` the ascending window is
  empty or wrong. The descent then fell back to the element's whole prefix
  group and relied on the recheck.

### Change

- **Executor \(`indexScanOp.rescanSAOP`\):**
  - Each bounded element's window is now `indexRangeBounds` with the element
    as a one\-key equality prefix — the RangePrefix probe's own bound logic.
    The DESC swap and the NULL stop therefore apply, and each element keeps
    its own bound exclusivity \(`saopExcl`, beside `saopBounds`\).
  - A NULL bound empties the whole scan.
- **Planner:**
  - The SAOP producer files the bounds with
    `restrictionRangeBehindPrefix(…, 1, …)`, so each bound carries its
    conjunct as `local`.
  - `createSAOPIndexScanPlan` drops those conjuncts from the leaf's Filter,
    as it already did for the IN itself.

### Found on the way: the elements must descend in index order

The probe returned `IN (182, 183, 5) … ORDER BY 1, 2` out of order, and
`ORDER BY 1, 2 LIMIT 3` returned 182's rows instead of 5's. The SAOP path
claims the index's pathkeys, so the upper Sort is elided, but the descents ran
in list order. This was a wrong\-results bug that predates the change; HEAD
returned the same rows.

PG sorts the array with the column's ordering proc and removes duplicates
\(`_bt_preprocess_array_keys` → `_bt_sort_array_elements`,
nbtpreprocesskeys.c\). `rescanSAOP` now encodes every non\-NULL element first,
then sorts the probe keys with the tree's own comparator
\(`BTree.CompareLowBound`, so a DESC leading column descends high to low).
Equal neighbours are dropped. `saopSeen` still deduplicates TIDs, which is
redundant for distinct elements but kept for safety.

### Verification

- **Probe against PG 18.3:** 13 statements over `(thousand, tenthous)` and
  `(thousand, tenthous DESC)`, with NULLs in `tenthous`. Bounds were `>`,
  `>=`, `<`, `<=`, BETWEEN and a NULL bound, plus a NULL element and a
  duplicated element. Plans and rows were identical.
- **`TestSAOPRangeBoundsExactAndElementOrder`:**
  - covers both cluster capabilities \(NULL\-keyed entries stored or not\);
  - covers three indexes: ASC/ASC, ASC/DESC and DESC/ASC;
  - checks 8 WHERE shapes under `ORDER BY 1, 2 LIMIT 7`, with rows equal to
    a `+ 0` twin that keeps the index out;
  - requires no `tenthous` Filter recheck;
  - pins PG's plan on the ASC index.

  With the element sort disabled, 10 cases fail.
- **Updated tests:** `TestRestrictionSAOPIndexScanOnJointreePipeline` and
  `TestRestrictionSAOPPlusRangeOnJointreePipeline` now expect no Filter for
  the bound.
- **Regress:** btree\_index 457→453 lines. join \+5 is the ledgered Hash Right
  Join unmatched\-row order flap.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Index\-only SAOP:** PG uses an `Index Only Scan` for the btree\_index
  case. goopg's index\-only path and `indexOnlyScanOp` carry no SAOP probe,
  so the plain Index Scan stands in.
- **Bitmap SAOP:** the bitmap SAOP carrier still takes no bound.
- **Backward and parallel SAOP scans** are still refused.

## Item \(b\) — an array behind an equality prefix

PG plans `thousand = 42 AND (tenthous = 1 OR tenthous = 3 OR tenthous = 42)`
\(or `tenthous IN (…)`\) on `(thousand, tenthous)` as a plain
`Index Scan` with `Index Cond: ((thousand = 42) AND (tenthous = ANY
('{1,3,42}'::integer[])))`. build\_index\_paths takes the second column's
ScalarArrayOp clause behind the first column's equality. goopg's plain SAOP
probe bound only the leading column, so the bitmap path won.

### Change

- **Plan shape:** `IndexScan.RangePrefix` \+ `SAOPKeys`. The array binds
  `Columns[len(RangePrefix)]`, and LowKey/HighKey bound the column after it.
  RangePrefix was chosen over a new field because every IndexScan
  expression walker already visits it — subplan lowering, unnest, parallel
  safety, deform, read sets and the NLI param quals.
- **Producer \(`addOneRestrictionIndexPath`\):**
  - With an equality prefix of n columns and no bound on column n, it looks
    for an array on column n \(`restrictionSAOPOnColumn`, the leading\-column
    builder generalised: an IN, `= ANY`, or an OR of equalities\).
  - It also files bounds on column n\+1.
  - Selectivity is the array's times the prefix equality's; one descent per
    element.
  - `plainRestrictionBindsOnlyPrefix` reports such an array, so the
    index\-only producer, which has no SAOP probe, does not stand in for
    the plain path.
- **Lowering \(`createSAOPIndexScanPlan`\):** dispatched on any clause
  carrying an array. It takes the prefix equalities into RangePrefix and
  drops every consumed conjunct from the Filter.
- **Executor \(`rescanSAOP`\):**
  - Each element's probe key is the prefix's parts plus the element. A NULL
    prefix value empties the scan.
  - A bound goes through indexRangeBounds with prefix \+ element.
  - The elements are still sorted and de\-duplicated in index order.
- **EXPLAIN:** prints the prefix equalities, then the array, then the
  bounds.
- **`indexScanPredicate`** \(the DML fallback\) rebuilds the prefix
  equalities before the IN. `lockRows` requires the prefix to be row\-local
  too.

### Verification

- **Probe against PG 18.3:** 12 statements, plans and rows identical. They
  cover OR and IN arrays, a duplicated array, a NULL element, a NULL
  prefix, prefix \+ array \+ bound on `(thousand, tenthous, c)`, and DELETE
  / UPDATE … RETURNING.
  - One plan differs and predates this change: `thousand = 42 AND tenthous
    IN (…) ORDER BY tenthous LIMIT 2` keeps a Sort.
  - goopg's index pathkeys do not drop a leading column fixed by a constant
    \(PG's pathkey\_is\_redundant\). Plain `thousand = 42 ORDER BY tenthous`
    behaves the same way. This is M0148\-0001c3a5 \(d\).
- **`TestSAOPBehindEqualityPrefix`:**
  - covers both cluster capabilities;
  - pins PG's two plans;
  - compares rows with a `+ 0` twin under ORDER BY … LIMIT;
  - checks that `indexScanPredicate` keeps the prefix equality. With the
    prefix removed, that check fails.
- **Regress:** create\_index 3137→3099 lines, with no new mismatch line.
  join −5 is the Hash Right Join flap.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Arrays elsewhere in the key:** goopg binds an array only on column
  len\(prefix\). PG takes several arrays on several columns, and an array
  ahead of further equalities \(`a IN (…) AND b = 1`\).
- **DML:** DML plans a Seq Scan for these quals, so the rebuilt predicate is
  defensive today.

## Item \(h\) — index\-only ScalarArrayOp scans

PG plans `thousand IN (182, 183) AND tenthous > 7550` as `Index Only Scan
… Index Cond: ((thousand = ANY ('{182,183}'::integer[])) AND (tenthous >
7550))`, and `unique1 IN (7, 1, 42) ORDER BY unique1 DESC` as `Index Only
Scan Backward`. build\_index\_paths builds one path per index and makes it
index\-only when the index covers the query. goopg's index\-only path
refused any SAOP, so it used the plain Index Scan, or a bitmap path.

### Change

- **Shared windows.** The per\-element window computation moved out of
  `indexScanOp.rescanSAOP` into `Context.saopWindows` \(`saopProbeSpec` →
  `[]saopWindow`\). It covers the prefix, the encoding, the index\-order sort
  and de\-duplication, and the per\-element `indexRangeBounds`. Both scans
  call it, so the siblings cannot disagree.
- **Plan:** `IndexOnlyScan.SAOPKeys` — the leading\-column array, with
  LowKey/HighKey bounding the second column. It is added to every IOS
  expression walker: plan exprs, subplan lowering, unnest, parallel safety,
  deform keys, and the outer\-ref rebase.
- **Executor:** `indexOnlyScanOp.Rescan` drains each window in order through
  `RangeScanWithPosLeafFilter`. A backward IOS reverses the materialised
  rows, so a backward SAOP is correct too, and `createIndexScanPlan` no
  longer refuses an index\-only one.
- **Producer:**
  - `indexOnlyLeafClauses` takes a leading SAOP plus second\-column bounds,
    in the plain producer's precedence \(ahead of a leading range\). The
    index\-only path therefore stands in for the plain one when the index
    covers the query, as PG's single path does.
  - `addOneIndexOnlyPath` prices it as the plain SAOP path:
    scalararraysel × the bounds, and num\_sa\_scans = the element count.
  - It also carries the index pathkeys, so the backward twin follows.
- **EXPLAIN:** the IOS renders through the Index Scan's SAOP formatter.

### Verification

- **Probe against PG 18.3:** 7 statements with plans and rows identical, and
  costs equal \(24.14\). One difference predates this change:
  - `SELECT DISTINCT hundred … IN (…)` keeps a HashAggregate where PG puts
    Unique over the ordered IOS;
  - a range IOS does the same, so it is the upper\-rel distinct election,
    not this probe.
- **`TestIndexOnlySAOP`:**
  - pins 4 of PG's plans \(forward, two\-column bounded, backward, backward
    under LIMIT\);
  - checks rows against a `+ 0` twin, and an all\-NULL array.
  - With the element sort disabled, 3 cases fail.
- **`saop_index_test.go`'s helper** accepts the index\-only form. `SELECT id`
  is covered, which is PG's own choice.
- **Regress:**
  - btree\_index 453→437, create\_index 3099→3063 lines.
  - The only new lines are row\-count lines of existing mismatches whose plan
    moved toward PG's. join \+5 is the flap.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Index\-only SAOP behind an equality prefix:** IndexOnlyScan has no
  RangePrefix yet.
- **Other key positions:**
  - an array behind a leading range \(`thousand < 2 AND tenthous IN (…)`,
    PG `Index Cond: ((thousand < 2) AND (tenthous = ANY (…)))`\);
  - a bound on the array's own column \(`unique1 IN (1, 42, 7) AND unique1
    >= 42`\);
  - two arrays on one column.
- **Parameterised arrays:** `thousand = ANY (ARRAY[t1.tenthous, …])` under a
  nested loop.

## Item \(a\) — inequality OR groups as `col < ANY (…)`

PG 18 matches `thousand < 42 OR thousand < 99 OR 43 > thousand` as the
single index clause `thousand < ANY ('{42,99,43}')`. Two routes lead there:

- `match_orclause_to_indexcol` for a whole OR conjunct;
- `group_similar_or_args` for a group of OR arms inside a BitmapOr.

PG also matches an explicit `id1 >= ANY (array[1,5])`. nbtree runs such an
array as one range, up to the extreme element
\(`_bt_preprocess_array_keys` → `_bt_find_extreme_element`\).
`btcostestimate` still counts one descent per element. goopg declined every
inequality group.

### Change

- **Clause:** `indexPathClause.saopOp` carries the operator.
  - `orInequalitySAOP` matches an OR of one inequality on a column,
    commuting `43 > thousand` to `thousand < 43`.
  - `saopAnyInequality` matches an explicit `col op ANY (…)`.
  - Both `restrictionSAOPOnColumn` and `bitmapSAOPOnColumn` take them.
  - An inequality array files no bound on the next column; that qual stays
    in the Filter.
  - `groupSimilarOrArgs` now folds an inequality group into a Nested
    sub\-OR, as it does an equality group.
- **Plan:** `SAOPOp` on IndexScan, IndexOnlyScan and BitmapIndexScan,
  printed as `col < ANY (…)`. `indexScanPredicate` rebuilds
  `col op ANY (…)`.
- **Executor:** `saopWindows` reduces an inequality array to one window
  through indexRangeBounds, bounded by the extreme element in value order.
  That is the last element in index order, or the first on a DESC column.
  The bitmap ScalarArrayOp probe now uses `saopWindows` too, so all three
  probes share one implementation.
- **The Filter under a BitmapOr recheck.** For `unique1 < 1 OR unique1 < 3
  OR hundred < 2`, PG drops the restriction OR and goopg kept it.
  - PG's `predicate_implied_by` proves the OR item by item. The array arm
    `(u < 1) OR (u < 3)` implies the single predicate item `u < 3`, by btree
    operator inference \(`operator_predicate_proof`, BT\_implic\_table\).
  - goopg's atom test only knew same\-operator, equal\-constant proofs.
  - `operatorPredicateProof` adds the implication table for integer
    constants on integer\-typed columns. It is sound for any total order, and
    a text column is never inferred.
  - A first attempt flattened the Nested sub\-ORs instead. It dropped
    Filters PG keeps \(`thousand = 42 OR thousand = 41 OR …`, and the join
    OR\-extraction cases\), because PG matches the sub\-OR item by item. It
    was reverted.

### Verification

- **Probe against PG 18.3:** 12 statements identical, plans and rows. They
  cover:
  - the BitmapAnd and BitmapOr regress plans, and the explicit `>= ANY`;
  - NULL elements, all\-NULL arrays, `<=` / `>` / `>=`;
  - a DESC index.
- **`TestInequalitySAOP`:**
  - pins the three PG plans;
  - compares counts with `+ 0` twins on ASC and DESC indexes.
  - With the extreme\-element choice broken, 3 cases fail.
- **`TestOperatorPredicateProof`:** the implication table, its boundary
  cases, commuted constants, the text\-column refusal, and the OR\-clause
  rule over it.
- **Regress:** create\_index 3063→3015 lines, no new mismatch line. join
  −5 is the flap.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Wider operator inference:** `operatorPredicateProof` compares only
  integer literals. PG compares any btree\-comparable constants through the
  opfamily's comparison proc \(numeric, text with collation, dates\). It also
  refutes, which goopg does not.
- **Bounds behind an inequality array:** the next column's bounds stay in
  the Filter.

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

