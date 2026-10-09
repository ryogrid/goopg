# M0146-0065 — an inlined single-reference CTE over a UNION ALL is an appendrel

Status: done 2026-10-07 (`21727437d`). Parent: M0146-0007 (`inline_cte`).

## Symptom

TPC-DS Q2:

```sql
with wscs as
 (select sold_date_sk, sales_price
  from (select ws_sold_date_sk sold_date_sk, ws_ext_sales_price sales_price
        from web_sales
        union all
        select cs_sold_date_sk sold_date_sk, cs_ext_sales_price sales_price
        from catalog_sales)),
 wswscs as
 (select d_week_seq, sum(...) ... from wscs, date_dim
  where d_date_sk = sold_date_sk group by d_week_seq)
select ...
```

PG 18.3 plans `CTE wswscs` as `Finalize HashAggregate → Gather → Partial
HashAggregate → Parallel Hash Join → Parallel Append`. goopg planned a
serial `HashAggregate → Hash Join → Append`, at both scales. The same UNION
ALL written as a FROM subquery outside a CTE already went parallel.

## Cause

PG's `inline_cte` (subselect.c) turns a single-reference CTE into an
`RTE_SUBQUERY` before `pull_up_subqueries` runs. `pull_up_simple_union_all`
(prepjointree.c) then flattens a UNION ALL into an appendrel, whether the
UNION ALL is the CTE body itself or a FROM item of a simple body. That is
because `pull_up_simple_subquery` first runs `pull_up_subqueries` on the
subquery.

goopg's version of this is the FROM pull-up (M0146-0007e,
`cteAsDerivedItem` → `expandDerivedPullups`). It splices only a simple
SELECT body over plain relations. Everything it declined stayed a
`CTE Scan` leaf, which the search cannot mark appendrel
(`addAppendRelPartialPaths`: `unmarked`). Three gaps:

1. **The body is the UNION ALL itself** (`WITH w AS (A UNION ALL B)`). The
   pull-up declines any set-operation body.
2. **The body selects FROM a UNION ALL subquery** (Q2's `wscs`).
   `simpleDerivedPullupBody` required every body FROM item to be a plain
   relation or a nested simple subquery.
3. **The reference sits in a later sibling's body** (Q2's `wswscs`
   reading `wscs`). The single-reference gate's inputs, `selectOwned` and
   `astRefs` (cterefcount), were stamped by `markSelectOwnedCTEs` /
   `stampCTEReferenceCounts` only after `preplanWithClause` returned. A
   sibling body is planned inside that call, so it saw the earlier CTE
   as un-inlinable. PG runs `inline_cte` over the whole WITH list before
   planning any body. (`eachRef`, the multi-reference NOT MATERIALIZED
   gate, was already stamped at entry creation for the same reason.)

## Change

- **derivedpullup.go.** `pullupUnionAllLeaf`: a non-LATERAL, join-free
  `(… UNION ALL …)` body FROM item that passes
  `subqueryChainIsSimpleUnionAll` no longer makes the body unsimple. The
  expansion keeps it as a derived item of the parent's FROM list, and
  `planSubqueryRangeVar` plans it through the existing appendrel mark
  (M0145-0004).
- **with.go / planner.go.** `plannedCTE.inlinesAsUnionAll`: a
  single-reference CTE whose body passes `subqueryChainIsSimpleUnionAll`,
  under `cteAsDerivedItem`'s single-reference gates. Its reference is
  planned through `planCTEReferenceAsSubquery`, the M0146-0007f path for
  inlined NOT MATERIALIZED references. That path plans the written body as
  an ordinary subquery under the declaration's CTE scope and takes back
  the preplanned body's references. A correlated body (`planHasOuterRef`)
  stays shared, as for `inlinesEachReference`: re-planning it at a deeper
  level would shift its outer-reference levels. A CTE with a column-alias
  list is declined too.
- **with.go (`preplanWithClause`).** `selectOwned` and `astRefs` are
  stamped when the entry is created, whenever the WITH has a SELECT owner.
  The later passes stay and are idempotent.

## Verification

- `TestInlinedCTEUnionAllIsAppendrel` (executor) covers the three shapes:
  no `CTEScan` for the CTE survives in the plan, and each returns PG
  18.3's values. All three fail without the change.
- The M0146-0027 closure repro
  (`analysis/m0146/m0146-0027/closure/q2-inlined-cte-union-all-repro.sql`):
  all five queries plan PG's node shapes.
- Seven edge cases returned PG's rows: two references, a sublink
  reference, NOT MATERIALIZED with a sibling, a column list, MATERIALIZED,
  and a member with ORDER BY/LIMIT.
- TPC-DS Q2: the `CTE wswscs` subtree is node for node PG's at SF0.25 and
  SF1.
- Fire set: fires are Q2, Q54 and Q64, with values PASS in both arms at
  both scales.
  - SF0.25 CATEGORIES-EXCL-MATCH: parallelism 29→28, aggregation-strategy
    8→7, scan-type 29→28.
  - SF1: parallelism 42→41, aggregation-strategy 15→14.
  - Q2's first divergence moves from D3 to D6.
  - Q64 keeps its shape; its estimate moves 81.68 → 13839.63 at SF0.25
    (PG 13874.27) and 545 → 53270 at SF1 (PG 49259).
- Gates:
  - units, tpch-spotcheck, arm 24/24 and ea-ratchet (9 → 9) all PASS;
  - sf025 96/96; plan shapes changed for Q2, Q54 and Q64 only;
  - regress with/subselect/union byte-identical A/B; `join` differs by
    one unordered row whose order varies from run to run on the HEAD
    binary too.

## Not covered

- Q2's next divergence (D6): the main query's join order over the two
  `CTE Scan`s of the multiply-referenced `wswscs`. PG joins
  `wswscs ⋈ (date_dim ⋈ (date_dim_1 ⋈ wswscs_1))`, goopg
  `date_dim_1 ⋈ (wswscs_1 ⋈ (date_dim ⋈ wswscs))`. Ledgered.
- A CTE with a column-alias list (`WITH w(a, b) AS (… UNION ALL …)`) is
  still a CTE Scan; PG inlines it. `planCTEReferenceAsSubquery` would
  carry the names, but the appendrel route's column naming was not
  verified. Ledgered.
- `tlist_same_datatypes` stays unported, as in M0145-0004. Mismatched
  member types are refused at hoist time (`TlistTypesDiffer`), not at
  mark time.
