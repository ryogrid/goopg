# M0146-0028 — pulling simple FROM-clause subqueries into the parent search

Status: slices 1 and 2 landed 2026-09-28 (§7 for slice 2). Code: `internal/optimizer/derivedpullup.go`
and hooks in `planner.go`. Evidence: `analysis/m0146/m0146-0028/`.

## 1. The gap

PG's `pull_up_subqueries` → `pull_up_simple_subquery` (prepjointree.c)
replaces an `is_simple_subquery` RTE_SUBQUERY with the subquery's FROM items
and quals, and rewrites references to its outputs with `pullup_replace_vars`.
The parent's join search then orders the body's relations together with its
own. goopg's `planSubqueryRangeVar` planned every FROM-clause subquery as a
separate scope, so TPC-DS Q59 was solved as `{wss,store,d}` twice plus
`{y,x}`. PG's order joins x's `wss ⋈ store` to all of y and adds x's
`date_dim` last. The M0145-0001 contract (§4.3) planned the mechanism and it
was never built (routing: M0146-0005ad).

## 2. Slice 1 scope

Only the shape whose `pullup_replace_vars` is a pure renaming:

- parent FROM: a comma list of at least two items, none LATERAL and none a
  table function, and no row locks;
- the derived item: non-LATERAL `(SELECT …) alias`, no column-alias list, no
  JOIN attached;
- the body: a bare SELECT (no WITH, set operation, grouping, HAVING,
  DISTINCT, ORDER BY, LIMIT/OFFSET, window clause, VALUES or locking) over a
  comma list of plain relation names (tables or CTE references), every target
  a bare column reference with a unique output name, and a WHERE with no
  sublink.

## 3. Mechanism

- **FROM walk.** `planFromClause` expands the list: each admitted derived
  item is replaced by its body's FROM items (`expandDerivedPullups`). The
  body relations become ordinary leaves, so the joinlist deconstruction, the
  jtScope table and the seam see one flat problem. The bindings stay 1:1 with
  the leaves.
- **Names.** The body leaves' bindings are `pulledHidden`: skipped by every
  name lookup (qualified, unqualified, whole-row, `*`, hints, the analyzer's
  outer scope, GROUP BY visibility). PG's pulled-up RTEs are likewise not
  nameable from the parent. The derived alias lives on
  `resolveContext.pulledDerived`: per output name, the resolved body
  `ColumnRef` (parent coordinates). `resolveColumnRefAt`, `expandStarTarget`
  (emitting at the derived item's FROM position) and whole-row references
  consult it; HAVING's parent context carries it.
- **Quals.** The body's targets and WHERE are resolved against the body's own
  bindings, with `planParent` as the scope chain, at the parent's level. The
  WHERE is constant-folded and ANDed into the parent's WHERE Filter, which
  planSelect now also builds when only pulled quals exist.
- **Naming.** A pulled reference keeps the body column's `Name` and
  `SourceTableIdx`, because the schema at its slot is the body column's and
  several rebinds match on (Name, SourceTableIdx). The output name the user
  wrote (`s_store_name1`) comes back through `targetMeta`, which now names a
  bare column target by its written name when that differs from the resolved
  one. That is PG's `FigureColname` rule, and a no-op everywhere else.
- **Needed columns.** The name-based needed/output sets
  (pathindexonlyneed.go) gain each body's names, so an index-only path over a
  pulled leaf still covers what the body reads.
- **Fallback.** If a body target does not resolve to a plain current-level
  column, or anything in the body fails to resolve, the attempt is discarded:
  the scope's RTID counter and derived-subtree registry are restored, and the
  FROM clause is re-planned without pull-up.

## 4. Latent bug fixed on the way

A positional `ORDER BY n` over `SELECT *` indexed the FROM-tree's input schema.
That is right only when every star expands to contiguous binding columns; a
pulled body (and a JOIN USING's hidden copy) breaks it. Before the fix,
`SELECT * FROM (SELECT …) y, reason ORDER BY 3` sorted by
`store.s_rec_start_date`. `ordinalThroughStarTargets` now walks the target
list through the star expansions; the input-schema index remains the
fallback.

## 5. Deferred (ledgered)

- function-call targets (landed for call-free expressions in slice 2, §7),
  and PlaceHolderVar-wrapped pull-up (grouping sets, outer joins);
- JOINs inside the body, a derived item with a JOIN attached, and outer
  joins around it;
- sublinks in the body WHERE (PG hands them to the parent's
  `pull_up_sublinks`);
- LATERAL items and column-alias lists.

## 6. Movement

Fire set Q2 and Q59 (no introduced timeouts). SF0.25 `join-method` 39 → 38
and `rendering` 25 → 24. SF1 `join-method` 42 → 41, `rendering` 21 → 20,
`sort-strategy` 50 → 51. Match unchanged (16/14). Q59 at SF0.25 now searches
all six relations. Its first divergence is PG's nested loop against goopg's
hash join at the same depth, a costing difference inside a shared search
space. Q59 output is byte-identical to PG's. 12 hand-written edge queries
(star, `y.*`, whole-row, ORDER BY ordinal, GROUP BY on derived names,
correlated sublink on a derived column, ambiguity and missing-FROM errors)
match PG. The regress runner shows 0 changed diffs across 11 cases against
a HEAD baseline.

## 7. Slice 2 (M0146-0028b, 2026-09-28): expression targets and the lone FROM item

- **Expression targets.** A body target may be any expression with no
  function call and no sublink (`parserExprHasNode`). This excludes, without
  a catalog lookup, everything PG's `is_simple_subquery` refuses in a target
  list: aggregates, window functions, SRFs (`hasTargetSRFs`) and volatile
  functions (`contain_volatile_functions`, prepjointree.c:1926). The parent
  is a plain comma list, so no outer join can null the derived item and
  `pullup_replace_vars` needs no PlaceHolderVar: `pulledDerivedRef` hands
  every reference its own deep copy of the resolved body expression
  (`cloneExprRefs`), with every column rebased to an `OuterColumnRef` of the
  referencing level. Output names are `targetMeta`'s on the resolved body
  target — what the body's own projection would have named them. A bare
  column reference that resolves to an expression is named as written
  (FigureColname).
- **The lone FROM item.** PG pulls it up too; the parent gate no longer
  needs a second item. The one-relation index arm (`isSimpleSingle`) is
  skipped for a pulled scope, because it rebuilds the scan from the
  statement's own WHERE alone and would drop the body's quals.
- **Grouping sets decline.** PG pulls up under grouping sets but wraps every
  substituted output in a PlaceHolderVar (`REPLACE_WRAP_ALL`), so two
  outputs renaming one column stay two grouping columns. goopg has no
  PlaceHolderVar, and substituting there merged them: regress `groupingsets`
  raised `column ref four/3 out of Slot range 2`. The parent gate declines
  statements with grouping sets.
- **Movement.** TPC-H Q7/Q8/Q9 wrap their whole join in a lone subquery with
  `extract(…)` and arithmetic outputs. TPC-H PLAN-PARITY match 6 → 7 (Q9 now
  matches PG) and `aggregation-strategy` 7 → 5; Q7/Q8 moved to
  `sort-strategy`/`parallelism` (+1 each). TPC-DS unchanged (no fires). The
  regress runner shows 0 of 22 case diffs changed against the HEAD baseline.
  A like-for-like list is required: runs over different case lists leave
  different catalog/statistics state and show spurious plan differences.

## 8. Slice 3 (M0146-0028c, 2026-09-28): INNER / CROSS joins inside the body

A body FROM item may carry INNER or CROSS joins to further plain relations.
PG pulls the body's whole join tree up with it: only an outer join *around*
the pulled subquery needs PlaceHolderVars, and an inner join's ON clause is a
WHERE qual in all but name. `planFromItem` already plans a FROM item with
its join chain and resolves the ON clauses against the item's own bindings.
The pull-up only has to hide every relation of the chain and hand the chain
to the parent's walk, where the seam flattens the INNER links into the one
search problem.

Still declined, each for a concrete reason:

- **outer joins in the body.** goopg's outer-join demotion
  (`demotedForPlan`, `reduceOuterJoins`) reads the parent's WHERE by column
  name, and its IS-NULL strip for LEFT→ANTI runs on the parent's WHERE. For
  a body join those must be the body's WHERE; wiring that is its own slice.
- **USING / NATURAL.** They merge columns through per-join resolve contexts
  (`usingHidden` lives on the merged context, not the binding), which the
  body re-resolution does not rebuild.
- **derived legs** (a subquery as a join operand).

Movement: none on TPC-H or TPC-DS (no corpus body has an inner JOIN);
regress runner 0 of 22 case diffs changed vs HEAD; live queries with joined
bodies return PG's rows and search the joined relations as one problem.
