# M0148\-0001c3a5 — the plans behind the last `… Backward` regress lines

**Parent:** M0148\-0001c3a3 · **Status:** item \(d\) landed 2026\-11\-14 \(two
slices\); items \(a\)–\(c\) open

Backward scans landed in M0148\-0001c3a3
\([0148\-0001c3a3\-backward\-index\-scans.md](0148-0001c3a3-backward-index-scans.md)\).
The remaining `… Backward` regress lines each wait on another planner
feature, and the forward plan is wrong too. The items, from
`.ralph/fix_plan.md`:

- \(a\) inherit: the partition\\-ordered plain Append, and min/max over
  partitions;
- \(b\) partition\\_join: the fractional partitionwise join;
- \(c\) btree\\_index: row\\-comparison index quals;
- \(d\) create\\_index: boolean index quals and equality\\-prefix pathkeys.

## Item \(d\) — boolean index quals and constant\-column pathkeys

create\_index's `boolindex (b bool, i int, unique (b, i))` queries:
`WHERE b ORDER BY i LIMIT 10`, `WHERE NOT b`, `WHERE b IS TRUE / IS FALSE`
\(and DESC\). PG answers each one with an Index Scan \(Backward\) using
`Index Cond: (b = true)` and no Sort. goopg planned Seq Scan \+ Sort, for
two reasons:

- **The index qual:** match\_boolean\_index\_clause turns a boolean column
  alone, negated, or under IS TRUE / IS FALSE into the indexable `b = true`
  / `b = false`. goopg took only `col = const` binary operators, and it
  rejected a boolean constant as a key outright.
- **The ordering:** build\_index\_pathkeys skips an index column whose
  pathkey is redundant, i.e. its EquivalenceClass holds a Const, or the
  column is a boolean constant for the query
  \(indexcol\_is\_bool\_constant\_for\_query\). goopg's buildIndexPathkeys
  stopped at the first column the query neither sorts nor merges on. So an
  index on `(b, i)` with `b` pinned offered no ordering on `i`.

### Change

- **Index qual:** `booleanIndexQual` \(pathindexrestrict.go\) recognises the
  four boolean shapes. `restrictionEqualityOn` files them as `col = true` /
  `col = false` clauses, which carry their conjunct, so the Filter drops it.
  `restrictionKeyUsable` admits a boolean constant key on a boolean column.
  The restriction and index\\-only producers share both.
- **Ordering:**
  - `addConstEquatedColumns` \(pathkeysindex.go\) marks, in the per\\-rel
    `colExprs` map, every column the rel's restriction pins to one value:
    `col = const` \(not NULL\) and the boolean shapes, through
    `constEquatedColumn`, which reuses booleanIndexQual.
  - `buildIndexPathkeys` skips a marked column instead of stopping.
  - The restriction, index\\-only and ordered producers apply the marks.
  - Because every row the rel emits has that value, the index's later
    columns still deliver their order, in either direction.

### Verification

- **Live server against a scratch PG 18.3** \(2000 analysed rows\): the
  same plans, with Index Cond `(b = true)`.
- **`TestBooleanIndexQualAndConstantPrefixPathkeys`:** PG's 4 plans
  \(forward and backward\) and rows.
  - Its `i` is NOT NULL, because the unit fixture's cluster keeps no
    NULL\\-keyed index entries and so refuses a probe that leaves a
    nullable `i` unbound.
  - This was the cause of the fixture's Seq Scan, not costing.
- **Regress:**
  - create\\_index 2801→2750. `NOT b`, `IS TRUE` and `IS FALSE`, forward and
    backward, now match.
  - Its one new mismatch line is the `b = true` case's Recheck Cond \(see
    Not covered\).
  - No other suite moved.
- **Gates:**
  - units, tpch\\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\\-set fires=none on sf025, sf1 and TPC\\-H;
  - acceptance 24 MATCH.

### Not covered \(closed by slice 2 below\)

- **`b = true` written out:** PG's eval\_const\_expressions rewrites it to
  `b` \(simplify\_boolean\_equality; the Filter prints `b`\). The column then
  gets boolvarsel's estimate, 0.5 without statistics. goopg keeps the
  `b = true` OpExpr and its 0.005 equality default, so the bitmap \+ Sort
  wins on an empty table.
- **Boolean selectivity in general:** a bare boolean column and IS TRUE /
  IS FALSE get goopg's 1/3 default. PG uses boolvarsel / booltestsel: 0.5
  without statistics, var\_eq\_const with them. Rows show 667 against PG's
  1000.

## Item \(d\) slice 2 — boolean equality simplification and boolean selectivity

`WHERE b = true ORDER BY i DESC LIMIT 10` still planned bitmap \+ Sort,
because goopg priced `b = true` at the equality default \(0.005\). PG's
eval\_const\_expressions rewrites it to `b` \(simplify\_boolean\_equality\),
and boolvarsel prices that at 0.5 without statistics, so the Index Scan
Backward wins.

### Change

- **simplifyBooleanEquality** \(foldconst.go\), over a boolean column in
  either operand order:
  - `b = true` and `b <> false` become `b`;
  - `b = false` and `b <> true` become `NOT b`.
  The forms agree on every input, NULL included.
- **Double negation:** tryFoldUnaryOp folds `NOT (NOT x)` to `x`, as
  negate\_clause does. This handles `NOT a = false`, which PG prints as
  `Filter: a`.
- **boolvarsel:** clauseSelectivity gives a bare boolean column 0.5
  without statistics. With statistics it gives the TRUE mass booltestsel's
  IS TRUE reads.
- **booltestsel without statistics:**
  - IS \[NOT\] UNKNOWN take DEFAULT\_UNK\_SEL and its complement;
  - IS TRUE / IS NOT FALSE recurse into the argument's selectivity;
  - IS FALSE / IS NOT TRUE take its complement.
  These no\-statistics estimates are reported unreliable. The old 1/3 has
  no PG source.
- **EXPLAIN:** a bare\-column qual prints without parentheses \(`Filter: b`,
  show\_qual's deparse; `qualText` in place of `wrapParen` at all 21
  qual sites\).

### Verification

- **create\_index:** 2750→2727; every boolindex hunk is gone.
- **partition\_prune:** 6783→6778. `boolpart` filters now print `NOT a` /
  `a` as PG does.
- **inherit / subselect:** only qual text changes, toward PG's form.
- **TestBoolTestSelectivityDefaults:** pins boolvarsel's 0.5,
  DEFAULT\_UNK\_SEL and the constant recursion.
- **Gates:**
  - units, tpch\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH;
  - the TestPort\_Isolation family \(a selectivity change\): ok.

### Not covered

- **Non\-column operands:** `simplifyBooleanEquality` takes only a bare
  boolean column. PG simplifies any boolean expression compared with a
  boolean Const, which needs the expression's type.


## Item (a) slice 1 — partition\-ordered plain Append

PG reference: allpaths.c generate\_orderedappend\_paths \(the
partitions\_are\_ordered arm\), pathkeys.c build\_partition\_pathkeys,
partbounds.c partitions\_are\_ordered, costsize.c cost\_append \(the
presorted arm\).

### Shape

- goopg has no child RelOptInfos for a partitioned table. The appendrel
  reaches the ORDERED upper rel as the inheritance SetOp chain, whose
  members are `Project → SeqScan` leaves.
- `addOrderedPartitionAppendPaths` \(new orderedpartitionappend.go\),
  called from createOrderedPaths after addOrderedAppendPaths, rebuilds
  that chain:
  - each member's SeqScan becomes an Index Only Scan \(the index covers
    every column, so the member Projects keep their positions\) or an
    Index Scan;
  - the members are re\-sequenced by lower bound, reversed for DESC, and
    each scan runs Backward;
  - the result is filed on the ORDERED rel with the sort pathkeys, so
    add\_path weighs it against Sort \+ Append.

### Admission \(each guard declines\)

- RANGE parent \(found through PartitionParentOID; the statement catalog
  wrapper is unwrapped to reach LookupTableByOID\), single level, no
  default partition.
- Integer lower bounds; MINVALUE / MAXVALUE honoured.
- The ORDER BY is plain columns through column\-only Projects, leads with
  the partition key, and is all ASC NULLS LAST or all DESC NULLS FIRST.
- Every member has a plain ascending btree index led by the ORDER BY
  columns, with no predicate and no expression column, and no Filter on
  the member scan.

### Cost

- Each child is cost\_index at selectivity 1.
- The Append follows cost\_append's presorted arm: startup is the SUM of
  the child startups, total is the sum of totals plus
  cpu\_tuple\_cost × APPEND\_CPU\_COST\_MULTIPLIER × rows.

### Verification

- **inherit:** 3153→3118. Both range\_parted plans now match PG with
  costs off; the missing `Backward` lines drop from 9 to 7.
- **Scratch PG probe** \(3 partitions attached out of bound order,
  MINVALUE / MAXVALUE, 3000 rows\):
  - plans match PG apart from alias numbering;
  - ASC and DESC row digests are identical to PG;
  - LIMIT and tableoid output are identical to PG.
- **TestOrderedPartitionAppend** pins both directions and rows. It fails
  with the call removed.
- **Gates:**
  - units, tpch\-spotcheck, sf025;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Not covered

- **Empty\-btree size:** with costs on, each child scan costs 4.0 more
  than PG's. goopg's empty btree is 2 pages \(16384 bytes\) where PG's is
  the metapage alone \(8192\), so index pages read 2 against PG's 1.
- **Alias numbering:** collectAllPartitionLeaves expands the leaves in
  registration order, so aliases follow attach order where PG's follow
  bound order. This also affects plain Seq Scan Appends.
- **Append width:** the Append reports width 16 where PG reports 12.
  This predates the slice; the unordered Append shows it too.
- **Still missing:**
  - mcrparted \(expression index column `abs(b)`, 6 lines\);
  - parted\_minmax per\-child min/max \(1 line\);
  - Merge Append over child index paths;
  - LIST partitions;
  - multi\-level hierarchies;
  - non\-integer bounds.

## Item (a) slice 2 — expression keys and index column names

PG reference: parse\_target.c FigureIndexColname /
FigureColnameInternal, parse\_utilcmd.c transformIndexStmt \(sets
indexcolname\), indexcmds.c ChooseIndexColumnNames, index.c
ConstructTupleDescriptor.

### Index column names

- **The gap:** goopg named an expression index column `expr` in a clone
  \(partition child, ATTACH, LIKE\) and the empty string in a top\-level
  CREATE INDEX, giving `fz__idx`. Its pg\_attribute row was named
  `pg_expression_N`, a name PG never uses.
- **PG:** transformIndexStmt gives every expression column
  FigureIndexColname's name \(`abs(b)` → `abs`, `1::integer` → `int4`,
  CASE → `case`\). ChooseIndexColumnNames falls back to `expr` and
  suffixes conflicts \(`abs`, `abs1`\). Both the generated index name and
  the index's attribute names come from that list.
- **The fix:**
  - new `parser.FigureIndexColname` ports FigureColnameInternal over
    goopg's raw AST, including the grammar's SystemTypeName mapping for
    keyword types;
  - executor `indexNameColumns` feeds it to all four naming sites;
  - buildUserPGAttributeRowsForIndex names the attributes from the same
    deduplicated list, INCLUDE columns included.
- **Verification:** on the scratch PG, index names and attnames are
  identical for `abs(b)`, `(a+b)`, `lower(a::text)`, `(abs(a), abs(b))
  include (c)`, `(a, a)` and LIKE INCLUDING INDEXES. regress
  create\_index 2727→2708 \(`concur_heap_expr_idx`, the `textcat` column\).

### Expression ORDER BY keys in the partition arm

- addOrderedPartitionAppendPaths now carries each ORDER BY key as an
  expression:
  - substituteProjectTargets maps it through the top Project and each
    member's Project chain;
  - exprOverSchema bounds it, as a walkExprRefs callback.
- A key matches a partition key column, or an index column, when it is
  the plain column. When the catalog stores an expression there
  \(PartitionKeyExprs, ColExprs\), the key must instead equal that
  expression resolved against the member \(keyMatchesTableColumn,
  exprEqual\).
- **Not yet reachable end to end:** indexUnboundKeysNotNull refuses an
  expression key column. Expression index keys keep the blob format
  \(buildPGIndexKeyDesc refuses them\), which stores no NULL\-keyed
  entries, so a full ordered scan would lose rows whose expression is
  NULL. The mcrparted witness therefore still plans Sort \+ Append.
  New task M0148\-0001c3a8 covers tuple\-format expression keys.
- **Test:** TestKeyMatchesTableColumnExpression pins the matching;
  TestOrderedPartitionAppend still covers the column path.

### Gates

- units, tpch\-spotcheck, sf025;
- fire\-set fires=none on sf025, sf1 and TPC\-H;
- acceptance 24 MATCH;
- regress A/B: only create\_index moved.

## Item (a) slice 3 — no Filter\-only full ordered scan of a qualified index

PG reference: indxpath.c create\_index\_paths → build\_index\_paths, which
builds ONE path per index from every clause
match\_restriction\_clauses\_to\_index found, and makes it index\-only when
it can.

### The gap

- goopg's `index.ordered` producer \(addOneOrderedIndexPath\) filed a full
  ordered scan of every orderable index and kept every local qual as a
  Filter, even when the index could bind one of them.
- When the bindable clause is barely selective \(`a IS NOT NULL`, `a >=
  -100`\), that Filter\-only twin out\-costed the index\-only path that
  carries the clause in Index Cond. goopg then planned `Index Scan …
  Filter: (a IS NOT NULL)` where PG plans `Index Only Scan … Index Cond:
  (a IS NOT NULL)`.
- This is the plain\-table half of the parted\_minmax witness.

### The fix

- indexBindsLocalRestriction: an equality prefix, or anything
  plainRestrictionBindsOnlyPrefix\(n=0\) reports — a leading
  ScalarArrayOp, range or NullTest, or a skip run.
- When it holds, the ordered full\-scan producer declines that index. The
  restriction and index\-only producers file the clause\-carrying path,
  with the same pathkeys.

### Verification

- **Scratch PG probes:** `select a from t where a is not null order by a
  limit 1` and `select min(a) from t` match PG.
- **TestOrderedFullScanYieldsToIndexClausePath:** pins PG's plan for
  `a >= -100 … order by a limit 3`. It fails with the decline removed.
- **Regress A/B:** every suite unchanged; the rowsecurity lines differ
  only in pointer addresses.
- **Gates:**
  - units, tpch\-spotcheck, sf025 \(PLAN\-SHAPE same=99\);
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH.

### Still missing for parted\_minmax

- **Trailing\-column index quals:** a qual on a later index column behind
  a leading range or NullTest — `((a IS NOT NULL) AND (b = 5))`, `((a >
  0) AND (b = 5))` — stays a Filter. PG 18 makes every qual on any index
  column an index qual. goopg carries only the `= ANY` form
  \(TrailingSAOP\).
  - The natural generalisation: TrailingSAOP with no Elems and only
    ColQuals, across IndexScan, IndexOnlyScan and BitmapIndexScan.
  - btcostestimate's indexBoundQuals stop at the first non\-equality
    column, so the trailing qual narrows indexSelectivity, not the
    descent.
- **Binary\-compatible relabel:** the witness's `b varchar(16)` compares
  as `(b)::text = '12345'::text`. PG's match\_index\_to\_operand strips the
  RelabelType, so the qual binds the index column and prints as `b =
  '12345'::text`. goopg's normalizeColumnIndexKey sees a CastExpr and
  declines.
- **Partitioned min/max:**
  - a one\-member appendrel loses its parent alias \(`parted_minmax1
    parted_minmax`\);
  - a multi\-member one needs the per\-InitPlan ordered Append of member
    IOSs.

## Item (a) slice 4 — plain comparisons on the second index column

PG reference: indxpath.c match\_clause\_to\_indexcol, which makes every
qual on any index column an index qual; selfuncs.c btcostestimate's skip
arm; nbtree checks the non\-leading key on every entry it reads.

### Shape

- An index clause list `((a > 90) AND (b = 5))` or `((a IS NOT NULL)
  AND (b > 11))`: a leading range or NullTest, then plain comparisons on
  column 1. goopg had carried only the `= ANY` form there
  \(TrailingSAOP, c3a4 \(i\)\); a plain comparison stayed a Filter.
- **Path:** new `trailingColumnQuals` collects the `=` `<` `<=` `>` `>=`
  constant comparisons on column 1 into one `indexPathClause` with
  `trailingQuals` set. The first comparison sits in op / key / local and
  the rest in saopColQuals. trailingSAOPOnRange returns it when column 1
  has no array. A leading NullTest admits it, while still refusing an
  array.
- **clausesTrailingSAOP:** finds either clause kind, behind a leading
  inequality or NullTest.
- **Plan:** both lowering sites \(IndexScan, IndexOnlyScan\) build
  `TrailingSAOP{Elems: nil, ColQuals: …}`; trailingClauseColQuals
  restores the first comparison ahead of the rest.
- **Executor:** trailingSAOPPred is the column quals alone when Elems is
  empty. The per\-entry key check is unchanged.
- **EXPLAIN:** withTrailingSAOPCond now takes the leading part count and
  also wraps the NullTest renderer, which had returned early.
- **Cost:** trailingSAOPSelectivity unchanged \(one descent per skip
  value, saopDescents = 1 with no array\). The IOS producer had passed
  the SAOP position instead of the trailing position \(a −1 slice
  panic\); fixed.

### Verification

- **Scratch PG:** 5000\-row probe with NULLs in both columns. Plans and
  costs are identical, e.g. `0.28..96.96` for `a IS NOT NULL AND b = 5
  ORDER BY a LIMIT 1`. Row digests are identical for every shape,
  including DESC.
- **TestTrailingColumnQualsBehindLeadingBound:** pins 3 plans and 4 row
  results; all 3 plans fail without the collector.
- **Regress create\_index:** two hunks move toward PG \(`tenthous = 3000`
  joins the Index Cond\). The count is unchanged, because PG keeps the
  column's second redundant bound in Index Cond and goopg keeps one.
- **Gates:**
  - units, tpch\-spotcheck, sf025 same=99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH;
  - TestPort\_Isolation family ok \(it needs `-timeout` above the 10m
    default\).

### Not covered

- **Bitmap form:** BitmapIndexScan still keeps the column\-1 comparisons
  as Filter and Recheck. PG: `Recheck Cond: ((a > 90) AND (b = 5))`.
- **min/max:** rewriteMinMaxAggregates is a rule\-based builder. Its IOS
  arm admits a WHERE only over the aggregated column
  \(wherePredSafeForIOS\), so `min(a) … where b = 5` falls back to Sort
  \+ Seq Scan. PG's build\_minmax\_path plans the subquery through the
  path search.
- **Redundant bounds:** a second bound on the leading column \(`thousand >
  -1 AND thousand >= 0`\) — PG lists both in Index Cond; goopg keeps one
  and filters the other.
- **Later columns:** quals on column 2 and beyond.
