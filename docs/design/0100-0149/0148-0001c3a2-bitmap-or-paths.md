# M0148\-0001c3a2 — BitmapOr paths over OR restriction clauses

**Parent:** M0148\-0001c3a1 · **Status:** slice 1 landed 2026\-11\-12 \(BitmapOr producer \+ parallel lossy page fix\); slice 2 landed 2026\-11\-12 \(bitmap ScalarArrayOp carrier, PG 18 OR\-arm grouping, predtest port\); slice 3 landed 2026\-11\-12 \(plain\-path OR→SAOP, qual NULL arms\); slice 4 landed 2026\-11\-12 \(choose\_bitmap\_and port, BitmapAnd costing fixes\); slice 5 landed 2026\-11\-12 \(exact range bounds behind an equality prefix, plain and bitmap\); the other c3a2 items stay open

## Problem

goopg had a BitmapOr plan node, an executor operator \(`bitmapOrOp`, a TBM
union\) and a cost function, but no path producer ever built a
`PathBitmapOr`. An OR restriction such as `unique1 = 1 OR unique2 = 3` was
always a Seq Scan with the OR as a Filter, where PG plans

```
Bitmap Heap Scan on tenk1
  Recheck Cond: ((unique1 = 1) OR (unique2 = 3))
  ->  BitmapOr
        ->  Bitmap Index Scan on tenk1_unique1
              Index Cond: (unique1 = 1)
        ->  Bitmap Index Scan on tenk1_unique2
              Index Cond: (unique2 = 3)
```

That accounted for 23 BitmapOr lines across the regress create\_index, join,
partition\_join and select diffs.

## PG behaviour \(the oracle\)

- `create_index_paths` \(indxpath.c\) calls `generate_bitmap_or_paths(root,
  rel, rel->baserestrictinfo, NIL)` and adds the results to `bitindexpaths`.
- `generate_bitmap_or_paths` goes through each OR restriction clause and
  works on the result of `group_similar_or_args`. For each arm:
  - **AND arm:** `build_paths_for_OR(andargs, all_clauses)`, plus a
    recursive call for the sub\-ORs.
  - **Grouped sub\-OR:** `make_bitmap_paths_for_or_group`, which builds a
    SAOP clause.
  - **Plain clause:** `build_paths_for_OR([arm], all_clauses)`.

  An arm that no index serves abandons the whole OR. Each arm's paths go
  through `choose_bitmap_and`, and the arms are combined by
  `create_bitmap_or_path`.
- `build_paths_for_OR`:
  - Skips an index unless the arm's own clauses match it
    \(`clauseset.nonempty`\) or the arm proves the index's partial predicate
    \(`useful_predicate`\).
  - Then adds the "other" restriction clauses to the clause set, so they can
    complete an index prefix: `thousand = 42 AND (tenthous = 1 OR …)` gives
    `Index Cond: ((thousand = 42) AND (tenthous = 1))`.
- `cost_bitmap_or_node` \(costsize.c\):
  - Selectivity is the sum of the arms' selectivities, capped at 1.
  - Cost is the sum of the arms' costs, plus `100 * cpu_operator_cost` per
    union **except where the input is an IndexPath**.
- `create_bitmap_subplan`'s BitmapOr arm \(createplan.c\):
  - Each child's qual becomes one AND \(`make_ands_explicit`\), and the arms
    are ORed together.
  - A Bitmap Index Scan child's qual is its index clauses, plus its partial
    predicate when those clauses do not imply it.
- `create_bitmap_scan_plan`: the Filter \(qpqual\) is every scan clause
  except those the recheck list \(indexquals\) contains or implies
  \(`predicate_implied_by`\):
  - a conjunct repeated in every arm is dropped;
  - the OR clause itself is dropped when every arm's qual covers its arm;
  - an arm the index serves only in part keeps the OR in the Filter.

## goopg implementation

- `optimizer/pathbitmap.go`:
  - `buildOneBitmapPath` is split in two:
    - `buildBitmapIndexChild` builds the Bitmap Index Scan path from an
      explicit conjunct list, i.e. build\_index\_paths' ST\_BITMAPSCAN arm;
    - `bitmapHeapPathOver` wraps any bitmap qual tree in the heap path,
      i.e. create\_bitmap\_heap\_path.
  - Plain bitmap paths behave exactly as before: the same conjuncts, and the
    index path keeps its lossy\-corrected row count.
  - `addBaseRelBitmapPaths` adds the OR heap paths beside the single\-index
    and AND paths.
- `optimizer/pathbitmapor.go`:
  - `bitmapOrHeapPaths` is generate\_bitmap\_or\_paths over the leaf's
    conjuncts.
  - `bitmapOrArmPath` is build\_paths\_for\_OR followed by choose\_bitmap\_and.
    It builds the clause context as arm clauses first, then the rel's
    conjuncts, so an arm clause wins its index column. It keeps an index
    only when the path uses an arm clause and every clause is exact
    \(`local` set\).
  - `bitmapOrArmsWouldGroup` declines an OR that PG 18 would partly fold
    into a SAOP group \(see Not covered\).
  - `bitmapArmSubqual` is create\_bitmap\_subplan's `*qual`.
  - `bitmapOrRecheck` builds the Recheck Cond and the set of dropped
    Filter conjuncts.
  - `orOfAndsImplies` / `andItemsImply` are the AND/OR rules of
    predicate\_implied\_by over `exprEqual` and
    `provePartialIndexPredicate` atoms.
- `optimizer/costbitmap.go`: `costBitmapOrCost` now applies PG's IndexPath
  exemption, and reads an index arm's own `Cost.Total`.
- `optimizer/createplanbitmap.go`: a `PathBitmapOr` child takes the
  `bitmapOrRecheck` arm instead of the single\-index recheck logic.

## Defect found on the way \(fixed here\)

`bitmapHeapScanOp.nextParallel` lost rows on **lossy** pages:

- **Symptom:** it returned a page's first match and fell out, so the next
  `Next()` claimed a new page and the rest of the page was dropped. A
  Parallel Bitmap Heap Scan therefore returned one row per lossy page.
- **History:** this is the same bug the exact\-page arm had, which was fixed
  earlier with the `parOffsets` cursor.
- **Evidence:** regress bitmapops' `a = 1` under `work_mem = 64` gave 1273
  rows, which is the lossy page count, against the true 1321. This
  reproduced on HEAD without this change. BitmapOr made the passing
  bitmapops case `a = 1 OR b = 1` return 1273 instead of 2485.
- **Fix:** the lossy walk is now a cursor across calls \(`inLossyPage` /
  `lossyOff`\), resumed at the loop head, as the serial path already did.
- **Test:** pinned by `TestBitmapOrOverOrClauses`'s parallel\-lossy cases,
  which fail with 1273 without the fix.

## Slice 2 — bitmap ScalarArrayOp carrier and PG 18 OR grouping

- **Carrier:** `BitmapIndexScan.SAOPKeys` holds a `col = ANY (…)` index qual
  on `Columns[len(Keys)]`, behind the equality Keys.
  - `bitmapIndexScanOp.buildSAOPBitmap` probes once per element into the
    one TID bitmap. A NULL element matches nothing; repeated elements are
    probed once, as `_bt_preprocess_array_keys` deduplicates.
  - EXPLAIN prints the folded array Const (`formatBitmapSAOPIndexCond`):
    `((thousand = 42) AND (tenthous = ANY ('{1,3}'::integer[])))`.
- **Matcher** `bitmapSAOPOnColumn`, called from `buildBitmapIndexChild`
  after the equality prefix:
  - an `IN` / `= ANY` conjunct \(match\_saopclause\_to\_indexcol\);
  - an OR whose every arm is `col = const` on that column
    \(match\_orclause\_to\_indexcol\). The OR is priced as the
    ScalarArrayOp it becomes \(scalararraysel\), and `num_sa_scans` is
    charged per element.
  - The conjunct is the recheck \(`local`\), so the plain bitmap path for
    `unique2 = 3 OR unique2 = 7` prints `Recheck Cond: ((unique2 = 3) OR
    (unique2 = 7))` over `Index Cond: (unique2 = ANY ('{3,7}'::integer[]))`,
    as PG does.
  - Hash indexes \(`DeclaredHash`; no amsearcharray\) are skipped.
- **Grouping** `groupSimilarOrArgs` is group\_similar\_or\_args.
  - Arms `indexkey op const` with the same column, operator and key literal
    kind fold into a sub\-OR placed at the first member's position.
  - The sub\-OR is marked `BinaryOp.Nested`. EXPLAIN keeps it as one
    argument \(`((unique1 = 1) OR ((unique2 = 3) OR (unique2 = 7)))`\),
    and the implication test treats it as one item.
  - `bitmapPathsForOrGroup` is make\_bitmap\_paths\_for\_or\_group: the
    group as one SAOP arm, or its arms one by one, compared by PG's
    `bitmapqual->total_cost`. For an index leaf that is IndexPath
    total\_cost, the whole `cost_index` scan, carried as
    `Path.IndexPathTotalCost`. With no other restriction clause a joint
    match is taken outright.
  - A non\-equality group \(`unique1 < 1 OR unique1 < 3`\) declines the
    OR, since goopg has no `< ANY` probe.
- **Implication:** `predicateImpliedBy` ports predicate\_implied\_by\_recurse
  verbatim \(strong form\). Its OR ⇒ OR rule needs each recheck arm to
  imply ONE Filter arm. That is why PG keeps `((unique1 = 1) OR (unique2 =
  3) OR (unique2 = 7))` in the Filter under a recheck whose second arm is
  the group, yet drops the OR when the whole OR is one SAOP.
  `bitmapOrRecheck` uses it.
- **Defect fixed before commit:** `buildSAOPBitmap` first assigned
  `indexProbeKey`'s concrete `*ExecError` into an `error`\-typed variable.
  The nil pointer read as a non\-nil error, and the backend panicked in
  `execErrCode`.
- **Probe against PG** \(scratch PG port 5545, ten queries\):
  - 7 plans identical, and values identical, exact and lossy;
  - the 3 differences are listed below.

## Slice 3 — plain\-path OR→SAOP and NULL arms in quals

- **OR→SAOP on plain paths:** `restrictionLeadingSAOP`, shared by the plain
  Index Scan and index\-only producers, accepts an OR of equalities on the
  index's leading column. This is match\_orclause\_to\_indexcol outside
  bitmaps, through the shared `orEqualitySAOP`.
  - The OR is the probe's exact recheck and leaves the Filter.
  - It is priced as the ScalarArrayOp it becomes (`saopPricedClause`).
  - Hash indexes are skipped.
  - With bitmap scans off, PG 18.3 and goopg both plan `unique2 = 3 OR
    unique2 = 7` as `Index Scan … Index Cond: (unique2 = ANY
    ('{3,7}'::integer[]))`.
- **Defect found and fixed \(wrong results, already on HEAD\):**
  `tryFoldBinaryOp` folded `x OR NULL` and `x AND NULL` to NULL. The
  generic "a NULL operand makes the result NULL" rule caught AND / OR too,
  despite its own comment.
  - `WHERE hundred = 3 OR NULL` planned as `One\-Time Filter: false` and
    returned 0 of 100 rows.
  - A select\-list `false AND NULL` gave NULL, not false.
  - AND / OR now keep a NULL arm beside a non\-constant one, as PG's
    simplify\_or\_arguments / simplify\_and\_arguments do.
- **Qual reduction:** `reduceQualConstArms`, applied by `foldQualConstants`,
  the qual\-context fold, is find\_duplicate\_ors' constant handling with
  is\_check = false.
  - An OR drops FALSE and NULL arms; a TRUE arm makes it TRUE.
  - An AND drops TRUE arms; a FALSE or NULL arm makes it FALSE.
  - So `3 = unique2 OR unique2 = 7 OR unique2 = NULL` becomes the
    two\-element SAOP PG plans.
- **Verification:**
  - `TestOrEqualitiesAsIndexSAOP` covers the plan, six NULL\-arm row counts
    and the select\-list values; it fails without the fold fix.
  - Regress over 22 suites \(the 16 plus boolean, case, predicate,
    aggregates, select\_distinct, select\_having\): unchanged against the
    HEAD baselines.
  - sf025 PASS=99 with 99/99 shapes identical; fire\-set fires=none;
    acceptance 24 MATCH.

## Slice 4 — choose\_bitmap\_and and BitmapAnd costing

goopg never planned the BitmapAnd PG builds for `hundred = 42 AND unique2 <
3000`, nor the BitmapAnd over a BitmapOr for `hundred = 42 AND (thousand =
42 OR thousand = 99 OR unique1 < 2)`. Three defects, all fixed:

- **Redundancy model:** `chooseBitmapAnd` is now PG's choose\_bitmap\_and.
  - `findIndexpathQuals` is find\_indexpath\_quals. An OR path's quals are
    its arms' index clauses, plus each index's partial predicate as AND
    items.
  - The clauses are numbered by `exprEqual` position, as
    classify\_index\_clause\_usage does.
  - Candidates with the same clause set keep the cheaper.
  - path\_usage\_comparator sorts by cost, then selectivity.
  - A candidate is skipped if it shares a clause with the group, or if the
    group's quals imply one of its predicates.
  - The old test skipped any pair of indexes sharing a COLUMN, and read
    `Children[0].IndexInfo`, which a BitmapOr lacks.
  - OR paths are now candidates: `addBaseRelBitmapPaths` passes
    singles plus ORs, as create\_index\_paths hands choose\_bitmap\_and
    every bitindexpath.
- **Estimator tuple base:** bitmap\_scan\_cost\_est / bitmap\_and\_cost\_est
  price the heap side on `selectivity * baserel->tuples`. goopg passed
  `rel.Rows`, the post\-restriction rows: the leader fetched 0.3 tuples,
  cost 9.06 against its real 143, and no AND could win.
- **Per\-row bitmap charge:** cost\_bitmap\_tree\_node's `0.1 *
  cpu_operator_cost * path->rows` is on the index PATH's rows, which
  cost\_index sets to `baserel->rows` \(ppi\_rows when parameterised\).
  goopg charged the fetched tuples, 100x on a wide range. `costBitmapTree`
  then charged it a second time; both are fixed. The leaf now takes
  `pathRows`, and the parameterised probe passes its row count.
- **Recheck for BitmapAnd trees:** a BitmapAnd tree now gets its Recheck
  Cond as its children's quals, through `bitmapOrRecheck`. The Filter drops
  what the list implies.
- **Result:**
  - The four probes plan as PG's, costs identical
    \(`cost=64.08..139.37`\).
  - Regress create\_index 3235→3166.
  - The TPC corpora are unmoved: sf025 PASS=99 99/99, fire\-set
    fires=none on sf025 \+ sf1, acceptance 24 MATCH.
  - join \+5 is a row\-order flap of a Hash Right Join's unmatched rows. It
    shows on HEAD too, 5 of 6 runs \(ledgered\).

## Slice 5 — range bounds behind an equality prefix \(c3a2 item 2\)

- **The rule it replaces:** behind an equality prefix, the plain Index Scan's
  bounds (`IndexScan.RangePrefix` \+ LowKey/HighKey) recorded no `local`, so
  the bound stayed as a Filter beside the Index Cond. The reason given: "an
  open bound runs to the prefix's padded upper bound, past NULLs in the
  bounded column". That rule \(2026\-09\-23\) predates the prefix\-aware NULL
  stop \(M0148\-0001c3a, 2026\-11\-11\).
- **Why the bounds are now exact:** the probe bounds through
  `indexRangeBounds`, which swaps a DESC column and, with one open end,
  stops at the column's NULL group within the prefix
  \(`nullStopRangeBound`\). A cluster without NULL\-keyed entries stores
  none to run into.
  - `restrictionRangeBehindPrefix` records `local`, and the guard in
    `createRangeIndexScanPlan` is gone.
  - PG prints `Index Cond: ((a = 3) AND (b > 900))` and no Filter, and so
    does goopg.
  - The leading\-SAOP \+ range form keeps the old rule. Its per\-element
    bounds \(indexScanOp's SAOP descent\) have neither the NULL stop nor
    the DESC swap.
- **Bitmap:**
  - `buildBitmapIndexChild` adds the same constant bounds, and a NullTest,
    on the column after its equality prefix.
  - `createBitmapIndexScanPlan` now classifies its clause list once: an
    equality prefix, then a SAOP, or bounds and/or a NullTest on the next
    column. That shape lowers onto `Keys` \+ `LowKey`/`HighKey`.
  - The executor passes `Keys` as the `indexRangeBounds` prefix.
  - `formatPrefixRangeIndexCond` is now shared by IndexScan and Bitmap
    Index Scan.
- **Verification:**
  - Probe against PG 18.3, 25 statements over a table whose bounded column
    is 1/7 NULL, on (a, b), (a, b DESC) and (a, b NULLS FIRST), index and
    bitmap paths: every plan and value identical.
  - `TestRangeBehindEqualityPrefix` compares every answer to a
    non\-indexable twin, with and without NULL\-keyed entries, plus PG's
    plan text.
  - The two producer tests that pinned the old recheck rule now pin the
    exact form, bitmap\-off; the nullable\-unbound decline is checked on the
    bitmap path too.
  - Regress over 22 suites: unchanged.
  - sf025 PASS=99 with 99/99 shapes identical; fire\-set fires=none;
    acceptance 24 MATCH.

## Not covered \(ledgered, M0148\-0001c3a2 stays open\)

- **Inequality groups:** `col < ANY (…)` and the like. PG matches the group
  as one SAOP; goopg declines the whole OR, because its probe is
  equality\-only.
- **OR→SAOP behind an equality prefix on plain paths:** the plain Index
  Scan's SAOP carrier is leading\-column only. PG plans `thousand = 42 AND
  (tenthous = 1 OR tenthous = 3 OR tenthous = 42)` as an Index Scan with
  `((thousand = 42) AND (tenthous = ANY (…)))`; goopg uses a bitmap.
- **SAOP followed by a range bound on the next column, under a bitmap:**
  `(thousand = ANY (…)) AND (tenthous > 500)`. The bound stays a Filter.
- **`TestSAOPWithConjunctMoves` fixture:** the catalog\-only fixture prices
  the plain index path at 12.32 on item\_pkey without the i\_flag bound,
  where a server and PG price 8.17 on idx\_item\_sk\_flag. The test now
  plans with bitmap scans off.
- **Sub\-ORs inside an AND arm:** the recursive `generate_bitmap_or_paths`
  call.
- **useful\_predicate:** an arm that proves a partial predicate without any
  index clause.
- **Join OR clauses:** `bitjoinpaths`, i.e. generate\_bitmap\_or\_paths over
  `joinorclauses`.
- **Bitmap node cost line:** Bitmap Index Scan EXPLAIN shows startup 0.29
  rather than PG's 0.00, a total that includes the per\-tuple charge, and
  width 84 rather than 0. BitmapOr width is also 84 rather than 0.
- **One bitmap heap path per rel:** PG adds only one, from choose\_bitmap\_and
  over every bitmap path. goopg adds each candidate separately.
- **subselect:** goopg rewrites `(a, b) IN (VALUES …)` into an OR, which now
  plans as a BitmapOr. PG never makes that rewrite and plans a Nested Loop
  over the VALUES. The divergence is older; the diff only changed shape.

## Verification

- `TestBitmapOrOverOrClauses`:
  - five PG 18.3 plans, byte\-for\-byte;
  - row equality against a non\-indexable twin, both exact and lossy;
  - parallel lossy cases.
- Probe on scratch PG \(port 5545\) against goopg: six EXPLAIN \(COSTS OFF\)
  shapes identical. Costs identical too on `unique1 = 1 OR unique2 = 3`
  \(8.59..15.91\).
- Regress A/B over 16 suites, HEAD worktree against candidate:
  - create\_index 3286→3271, join 17792→17724;
  - subselect 2452→2462 \(the VALUES divergence above\);
  - bitmapops still passes.
- TPC\-DS SF0.25 sweep: 99/99 plan shapes identical, PASS.
- tpch\-spotcheck: Q12/Q13 PASS.
- Unit gate: PASS.
- Slice 2:
  - regress create\_index 3271→3235, join 17724→17655, no result change;
  - TPC\-DS SF0.25 99/99 plan shapes identical;
  - fire\-set fires=none on sf025 \+ sf1;
  - acceptance 24 MATCH; tpch\-spotcheck PASS; unit gate PASS.
