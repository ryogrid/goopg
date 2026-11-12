# M0148\-0001c3a4 — ScalarArrayOp and OR index\-path residue

**Parent:** M0148\-0001c3a2 · **Status:** items \(c\) and \(b\) landed 2026\-11\-12; items \(a\), \(d\)–\(h\) open

This item collects what M0148\-0001c3a2 \(BitmapOr paths\) left open. The items
are listed in `.ralph/fix_plan.md` under M0148\-0001c3a4:

- \(a\) inequality OR groups;
- \(b\) OR→SAOP behind an equality prefix on plain paths;
- \(c\) leading SAOP plus a range bound;
- \(d\) sub\-ORs inside AND arms;
- \(e\) useful\\\_predicate arms;
- \(f\) join OR bitmap paths;
- \(g\) the bitmap node cost line;
- \(h\) index\-only SAOP scans, and the bitmap SAOP carrier with bounds.

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
