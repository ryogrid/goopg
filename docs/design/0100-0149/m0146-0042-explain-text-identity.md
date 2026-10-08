# M0146-0042 — EXPLAIN text-identity burn-down

Umbrella for the `rendering` class: EXPLAIN text differences on plans whose
shape already matches PG 18.3. The instrument is text-identical, meaning
plans equal to PG's line for line once costs are stripped. Children 0042a
(`m0146-0042a-rebuilt-candidate-ec-orientation.md`) and 0042b (same doc)
are done.

## Slice — varchar keys in Hash / Merge Cond (2026-10-05, `fb26f7dc6`)

- varchar has no `=` of its own, so `make_op` compares two varchar keys as
  text, and `get_oper_expr` shows each side's RelabelType:
  `((v1.s_store_name)::text = (v2.s_store_name)::text)`.
- goopg's Join Filter already printed this (`formatTextCastOperands`), but
  the key-pair renderer `formatJoinKeyCond` printed bare columns. It now
  applies the same rule; char(n) keys keep bpchar's operator and stay bare.
- `TestHashJoinBpcharRendersHashCondOnly`'s varchar expectations were not
  PG's and are corrected.
- Result: TPC-DS Q47 and Q57 are text-identical at both scales. SF0.25
  35 → 37, SF1 25 → 27.

## Slice — constant folding of a pulled sublink body (2026-10-05, `7eed1a031`)

- PG runs `eval_const_expressions` over the whole query, sublinks included,
  before `pull_up_sublinks`. A pulled-up EXISTS or IN body's WHERE
  therefore reaches `order_qual_clauses` already folded: `d_moy BETWEEN 3
  AND 3+3` is `(d_moy >= 3) AND (d_moy <= 6)`.
- goopg folded the statement's own WHERE (`planSelect`) but not a body
  resolved by the jointree sublink pull-up (`jointreepullup.go`, the EXISTS
  and ANY arms). Unfolded, `(d_moy <= (3 + 3))` cost two operators, so the
  stable cost sort put it after `(d_year = 2001)`. Both arms now run
  `foldQualConstants` over the resolved body WHERE.
- Test: `TestPulledSublinkBodyFoldsConstants` (EXISTS; EXISTS AND (EXISTS OR
  EXISTS); IN). Each expects PG 18.3's `Filter: ((m >= 3) AND (m <= 6) AND
  (y = 2001))`.
- Result: Q10 is text-identical at both scales (SF0.25 37 → 38, SF1
  27 → 28). The EC-equality ordering needed no change; the cost sort
  orders correctly once the bound is folded.
- Estimates: folding also fixes the scan's selectivity. The Q69 date\_dim
  filter now estimates 52 rows (PG 54; previously 71).
- Q69 lost its SF1 match (34 → 33). Both scales now put the catalog\_sales
  anti-join below the web\_sales one, where PG does the reverse.
  - With anti-join match fractions of `inner_rows / N`, the leading
    nested-loop term of the two orders is identical:
    `o·(i_web·i_cat/N − i_cat·i_web/N) = 0`. The election turns on
    second-order rounding.
  - The old 71-row estimate tipped it PG's way. The same tie at SF0.25
    was already routed by M0146-0005 to M0146-0014 as COSTTIE (ledger row
    2026-10-05).
  - CATEGORIES-EXCL-MATCH moved for Q69 only: SF0.25 join-method 24→25,
    parameterisation 25→26, parallelism 28→29 (the web anti-join becomes a
    Nested Loop over Materialize); SF1 join-order 52→53, qual-placement
    10→11.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025 (Q10, Q69
  changed), fire set, ea-ratchet, regress A/B over 14 files (pointer
  noise only).
- Evidence: `analysis/m0146/m0146-0042/slice-const-fold-q10-q69.txt`.

## Slice — EC-derived restriction order and orientation (2026-10-06, `7b5892287`)

- PG takes a `var = const` (or same-relation `var = var`) equality out of
  the restriction list (`process_equivalence`). It hands it back from
  `generate_base_implied_equalities`, which walks `root->eq_classes` in
  order. So a relation's EC equalities come out in EC creation order, not
  written order.
- The EC list is built in WHERE order, as `process_equivalence` does:
  - A new EC is appended.
  - An item already in an EC joins it. A constant matches an equal
    constant of the same type.
  - When the two items sit in different ECs, the right one's EC merges
    into the left one's and leaves the list.
- In TPC-DS Q31, `ss2.d_year = 1999` joins the EC opened by
  `ss1.d_year = 1999` before `ss2.d_qoy = 2` opens its own. PG filters ss2
  on `(d_year = 1999) AND (d_qoy = 2)`; goopg kept written order.
- `generate_base_implied_equalities_const` hands back the written clause
  only for an EC with two members and one source. Otherwise it builds
  `member = const` for every member, so a regenerated `3 = c` prints
  `(c = 3)`. A two-member `2 = a` stays as written.
- goopg (`local_filters.go`):
  - `equivalenceClassPlaces` replays that list over the scope's
    conjuncts.
  - `equivalenceClausesLast` sorts a relation's EC equalities by EC
    position and flips a regenerated `const = col`.
  - An EC with more than one distinct constant keeps the written form.
  - Items are keyed by `exprIdentityKey`; a constant's key carries its
    column's type, standing in for `em_datatype`.
- Test: `TestRestrictionQualECOrder` compares whole plans with PG 18.3's
  for EC order, constant matching, and regress join.sql's "Don't remove
  SJ" orientation.
- Results:
  - Q31 is text-identical at SF0.25 (38 → 39). At SF1 its filters match;
    the join order inside CTE `ss` still differs (structural).
  - The regress join.sql "Don't remove SJ" plan now equals PG's.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025, fire set
  (per-query records identical), ea-ratchet, regress A/B over 14 files.
- Evidence: `analysis/m0146/m0146-0042/slice-ec-order-q31.txt`.
- Not covered:
  - Join-level clause order (the M0146-0005co ledger row's part 2).
  - Orientation of the EC-derived join clauses.

## Slice — qualification through an unpulled subquery (2026-10-06, `fa61c41a7`)

Background:
- A grouped FROM subquery is not pulled up. When its Subquery Scan is
  trivial, PG removes it (`trivial_subqueryscan`), and so does goopg's
  plan.
- goopg then has no level boundary in the tree, while SourceTableIdx
  restarts at every query level. So the subquery's relations reuse the
  parent's source indexes, and its outputs carry a binding no scan owns.
- PG resolves each Var through the plan instead (`resolve_special_varno`
  → child target list → scan).

Fixes:
- `explainNames.columnIn` keeps the scope's first relation for the source
  index. When that relation lacks the column, it tries the scope's other
  relations for that index (`scopeLists`, walk order) and takes the first
  that has it. Q46/Q79's `customer.c_customer_sk` had met the subquery's
  `date_dim` first and printed bare. Relations below a SetOp stay out of
  the list: an Append-level column names the parent, never one child
  (regress union's `t2.ab` and partition\_prune's `part_abc_3_1.d` showed
  the risk).
- A Sort or Incremental Sort key column that still has no relation is
  walked positionally into the Sort's input (`resolvedColumn`), giving
  Q79's `(substr((store.s_city)::text, 1, 30))`.
  - The walk must land on a column with the reference's own name: a
    column inside an aggregate argument indexes the aggregate's input.
  - Without that check, Q71's `sum(ext_price)` read as
    `sum(time_dim.t_hour)`; a guard case pins it.
- The join-residual walk (`resolvedColumn`, not the set-op mode):
  - It follows an INTERSECT or EXCEPT through its first input
    (`set_deparse_plan`); neither is ever an appendrel.
  - It stops at a kept Subquery Scan with that scan's alias, as
    `get_variable` does (`varno` is the subquery RTE). This gives Q8's
    `substr(a1.ca_zip, 1, 2)`.

Results:
- Test: `TestExplainNamesThroughUnpulledSubquery` checks PG 18.3's lines
  for the three shapes, plus the aggregate guard.
- Text-identical: SF1 28 → 30 (Q46, Q79). SF0.25 stays 39; Q79 there
  also differs in join method.
- Q8, Q14 and Q23 move closer to PG at both scales.
- CATEGORIES-EXCL-MATCH `rendering` 11 → 10 at SF0.25.
- Regress: union's INTERSECT Sort Key now equals PG's, and join.sql gains
  PG's `nt3.nt2_id`.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025, fire set,
  ea-ratchet, regress A/B over 14 files.
- Evidence: `analysis/m0146/m0146-0042/slice-qualification-q46-q79-q8.txt`.

Not covered (ledger row 2026-10-06):
- A Hash Cond key through an INTERSECT prints bare where PG walks into the
  scan (`s2.city`). That is the key renderer, `formatJoinKeyCond`, not the
  join-residual walk.
- An aggregate-argument column over an unpulled UNION ALL prints bare. PG
  prints the appendrel's first child (`sum(ss.np)`), or for Q71
  `sum("*SELECT* 3".ext_price)`.

## Slice — keyword expressions and a CTE group key through a UNION (2026-10-06, `95c4f5581`)

- COALESCE, NULLIF, GREATEST and LEAST are grammar keywords that parse to
  their own nodes (`T_CoalesceExpr`, `T_NullIfExpr`, `T_MinMaxExpr`).
  `get_rule_expr` prints them by keyword in capitals; goopg printed the
  lowercase call spelling (TPC-DS Q67, Q75, Q78; regress join.sql).
  `explainKeywordFuncs` maps the call names.
- TPC-DS Q75: `all_sales` is referenced twice, so its body is a CTE. The
  body's outer group key reads the UNION's dedupe output.
  - goopg's scope lookup found no relation there, and the statement-wide
    fallback (`bySrc`, first registration wins across levels) handed it
    the consumer's alias `curr_yr`.
  - The ColumnRef arm now tries, in order:
    1. `columnInScope`, the level's own scopes;
    2. the name-guarded positional walk, into a Sort's input and now an
       Aggregate's;
    3. the statement-wide fallback.
  - `resolvedColumn` treats a `Distinct` (the UNION's dedupe) as
    transparent, and crosses the SetOp a UNION (distinct) dedupes, which
    is never an appendrel.
  - The key now names `date_dim`/`item` (goopg's `_1` suffixes are the
    alias-numbering class).
- Results:
  - CATEGORIES-EXCL-MATCH `rendering` SF1 12 → 11.
  - Q49's sort key gains PG's `web.return_rank, web.currency_rank`.
  - Text-identical unchanged (SF0.25 39, SF1 30).
- Tests: `TestExplainKeywordFuncsPrintUppercase` and the Q75 case in
  `TestExplainNamesThroughUnpulledSubquery`.
- Gates: units, tpch-spotcheck, arm 24/24, sf025, fire set, ea-ratchet,
  regress A/B over 14 files.
- Evidence: `analysis/m0146/m0146-0042/slice-keyword-funcs-cte-groupkey-q75.txt`.
- Not covered (ledger 2026-10-06):
  - A column renamed on the way (`dd.dow yr`) stays bare: the name guard
    declines it, where PG prints the source.
  - A group key that reads a computed column of its input prints as
    `(expr)`, where PG prints `((expr))` with the coercion shown
    (`((ss.np - (COALESCE(ss.ck, 0))::numeric))`).
  - Q49's `item` / `return_ratio` stay bare.

## Slice — set-operation branches as range-table levels (2026-10-06, `eef2d1762`)

- EXPLAIN's `_N` suffixes follow PG's flattened range table, which
  `renumberRTIDsFlatRtableOrder` (M0146-0005df) reproduces level by level.
- A genuine set operation is planned by `plan_set_operations` as a query
  whose range table holds one subquery RTE per leaf. That covers an
  INTERSECT, an EXCEPT, a UNION distinct, and a link of the UNION ALL
  chain a UNION dedupes. setrefs.c adds each leaf's relations when the
  plan walk reaches its SubqueryScan.
- `collectLevel` now makes each branch a kept level, in plan-walk order;
  nested set-operation nodes belong to the same tree.
  - Before, the branches' relations joined the enclosing level, so Q8's
    second INTERSECT branch was numbered ahead of the first branch's
    grouped subquery.
  - Plan-walk order is Parallel Append's cost order, so Q75's item
    suffixes now follow the store, catalog and web branches.
- A UNION ALL that may be a pulled-up appendrel (members join the parent
  level in written order) keeps the level walk.
- Results:
  - Q8 is text-identical at both scales (SF0.25 39 → 40, SF1 30 → 31).
  - Q38 and Q75 move closer to PG.
  - Regress union.sql's INTERSECT plans equal PG's.
- Test: `TestSetOpBranchSuffixesFollowPlanWalk`.
- Evidence: `analysis/m0146/m0146-0042/slice-setop-branch-levels-q8.txt`.
- Not covered (ledger 2026-10-06):
  - A top-level UNION ALL query (not a FROM subquery) is a set operation
    in PG too; goopg's SetOp does not record whether it was pulled up.
  - Q75's suffixes skip numbers (`item_2`, `item_4` where PG has
    `item_1`, `item_2`): some scan that EXPLAIN does not print claims a
    name.

## Slice — an unprinted CTE body copy claims no labels (2026-10-06, `f894f0c27`)

- A CTE referenced twice can hang a separate copy of its body under each
  reference (TPC-DS Q75's `all_sales` under `curr_yr` and `prev_yr`).
  - An instrumented binary on the SF0.25 dataset showed every `item`
    RTID claimed by two scan nodes, one per copy.
  - EXPLAIN prints one `CTE <name>` section, the first reference's body
    (`collectCTEHoist`).
  - Column qualifiers dedupe per RTID and came out right. Node labels are
    claimed per node pointer, so the copies took suffixes alternately:
    the printed body read `item`, `item_2`, `item_4` beside its own
    `item_1.i_item_sk`.
- `explainNames.collect` now follows `collectCTEHoist`: a second
  reference to a non-inlined CTE registers its own scan but does not
  descend into its body.
- The copy has no result effect: `cteScanOp` shares rows per CTE
  declaration (`ctx.CTERowCache`, M0097-0099), so the body runs once,
  as in PG.
- Results:
  - Q75 SF0.25 differing lines 6 → 2 (the `((expr))` group key remains).
  - Q14 128 → 116 (SF0.25) and 160 → 148 (SF1).
- Test: `TestExplainNamesSecondCTEBodyCopyClaimsNoLabel` (hermetic). A
  SQL probe did not reproduce the copy.
- Evidence: `analysis/m0146/m0146-0042/slice-cte-body-copy-labels-q75.txt`.

## Slice — typmod'd CASE NULL and an ambiguous NestLoop param (2026-10-06, `2f9990d50`)

Census first (`tmp/fireset-m42i`, MATCH queries not text-identical):
- SF0.25: Q56 (aggregate argument; NestLoop param names) and Q75
  (`((expr))` key).
- SF1: Q43 (`ELSE NULL` label) and Q45 (SubPlan block position).

Fixes:
- `nullConstTypeLabel`: a CASE's results are coerced to the common type
  with typmod -1 (`coerce_to_common_type`), so a `numeric(7,2)` THEN
  column still labels its NULL `NULL::numeric`. goopg declined any type
  with modifiers; Q43 at SF1 (a sort key over a Finalize aggregate)
  printed `ELSE NULL END`.
- NestLoop params: `get_parameter` deparses one against the loop's outer
  plan, which goopg resolves by column name (`resolveLabelInAncestor`).
  When two outer relations expose the name, `resolveLabelInAncestorSrc`
  now narrows by the reference's binding id before the statement-wide
  fallback.
  - In Q56's second and third UNION branches, the outer plan holds both
    the branch's `item` and the IN subquery's pulled-up `item`.
  - The fallback had printed the first branch's `item.i_item_sk` instead
    of PG's `item_2` / `item_4`.

Results:
- Q43 is text-identical at SF1 (31 → 32).
- Q56 SF0.25 differing lines 6 → 2; the aggregate argument remains
  (ledgered).
- Tests: `TestCaseNullArmIsTyped` (numeric(7,2) case) and
  `TestNestLoopParamNamesItsOwnBranch`.
- Gates: units, tpch-spotcheck, arm 24/24, sf025, fire set, ea-ratchet,
  regress A/B over 14 files.
- Evidence: `analysis/m0146/m0146-0042/slice-case-null-typmod-nestloop-param-q43-q56.txt`.

## Slice — SubPlans print after the node's children (2026-10-06, `ad7a20114`)

- `ExplainNode` prints a node's initPlan list before its children and its
  subPlan list after them (explain.c: initPlan, lefttree, righttree,
  special child plans, subPlan).
- goopg emitted both before the children, so TPC-DS Q45's Join Filter
  `hashed SubPlan 1` printed above the join's inputs.
- `deferSubPlans` / `requeueSubPlans` take a node's pending `SubPlan N`
  entries off the queue after its detail lines, so a child's drain cannot
  emit them early. They are re-queued and drained after the children;
  InitPlans keep their place. Both renderers changed together.
- Results:
  - Q45 is text-identical at SF1 (32 → 33). Q6, Q32 and Q92 move closer.
  - Regress subselect mismatch lines 1683 → 1665, join 15600 → 15596.
- Test: `TestSubPlanPrintsAfterChildren`.
- Evidence: `analysis/m0146/m0146-0042/slice-subplan-after-children-q45.txt`.
- Not covered (ledger 2026-10-06): goopg's structured EXPLAIN formats
  (JSON, `planToJSON`) carry no `Subplan Name` plans at all.

## Slice — join conditions through a grouped subquery (2026-10-08, `1ff32da82`)

- **Problem.** TPC-DS Q65 was PG's plan node for node, but its join
  conditions over two GroupAggregate'd derived tables printed output names
  bare: `Merge Cond: (ss_store_sk = ss_store_sk)` and
  `Join Filter: (revenue <= (0.1 * ave))`. PG prints
  `(store_sales.ss_store_sk = store_sales_1.ss_store_sk)` and
  `((sum(store_sales.ss_sales_price)) <= (0.1 * (avg((sum(store_sales_1.ss_sales_price))))))`.
  `resolve_special_varno` deparses each Var through the child plan's target
  list. A non-Var target prints parenthesised (get_variable), and an
  aggregate over another aggregate's result nests.
- **Change.**
  - **Hash/Merge Cond keys.** A key whose name-based text is bare is
    re-rendered with the join row in scope, so the positional walk resolves
    it through the group key. A name the scopes already qualify stays. On
    TPC-DS Q51/Q97's FULL joins the walk misread the merged columns, and
    the qualified names were already PG's.
  - **Computed columns.** A join-row column a child computes is chased with
    `resolveKeySource` and printed as `(expr)`, rendered with `joinRow`
    cleared. `resolveKeySource` now steps through a Materialize.
  - **Nested aggregates.** `chaseAggregateResultArgs` chases each
    bare-column argument into the aggregate's input
    (`resolveKeySourceAt`, pinned). It substitutes a uniquely named display
    column whose text survives `pinKeyExprNames`' clone, which
    `synthAggCall` had declined.
- **Results.**
  - Q65 is a full MATCH at both scales: PLAN-PARITY SF0.25 42 → 43, SF1
    33 → 34, and qual-placement 12 → 11.
  - Q44 (`v11.rnk = v21.rnk`), Q78 (`cs.cs_customer_sk`) and Q95
    (`ws_wh_1.ws_order_number`) now print PG's text.
  - TPC-H EXPLAIN text and ten regress files are unchanged.
- Test: `TestExplainJoinCondsThroughGroupedSubqueries`, which reproduces
  PG 18.3's three lines.

## Slice — a Sort Key aggregate over a UNION ALL of grouped CTEs (2026-10-08, `3b9456d12`)

- **Problem.** TPC-DS Q33/Q56/Q60 sort on `sum(total_sales)` over a UNION ALL
  of three inlined, grouped CTEs. PG follows the argument through the
  Append's first branch into that member's aggregate and prints
  `(sum((sum(store_sales.ss_ext_sales_price))))`. goopg printed the CTE
  label column, `(sum(ss.total_sales))`.
- **Change.**
  - `sortKeyParts` chases the expanded call's arguments with
    `chaseAggregateResultArgs`.
  - `resolveKeySource`'s Project arm also takes a COMPUTED nested result
    when the nested chase crossed a query-level boundary (an inlined CTE
    body or a UNION arm). Before, it fell back to the label column.
- **Results.**
  - Every changed line in Q33/Q56/Q60 equals PG's, and Q56 is
    text-identical at SF0.25.
  - The rendering category drops SF0.25 10 → 9 and SF1 11 → 9.
  - TPC-H text and ten regress files are unchanged.
- Test: `TestExplainSortKeyAggregateOverUnionAllMembers`.

## Slice — a UNION dedupe's computed group key (2026-10-08, `bce8f29c0`)

- **Problem.** A UNION's dedupe groups on the Append's output, and the
  Append's target entries are Vars of its first branch. `show_agg_keys`
  therefore deparses a Var, and `get_variable` wraps the branch's computed
  target in parentheses of its own. TPC-DS Q75's CTE prints
  `((store_sales.ss_quantity - COALESCE(...)))`; goopg printed one pair.
- **Change.** The Distinct arm adds that pair when the key's chase crossed
  a set operation and the dedupe's input is a SetOp. `inputIsSetOp` looks
  through Gather, Gather Merge, Sort and Materialize. A DISTINCT over a
  plain scan keeps one pair, as in PG.
- **Results.** Q75 is text-identical at SF0.25 (1 line → 0) and one line
  closer at SF1. TPC-H text and ten regress files are unchanged.
- Test: `TestExplainUnionDedupeComputedKeyParens`.
- Not covered (ledger): the same wrap for Aggregate/Sort keys read through
  an Append.

## Slice — a Finalize aggregate's Group Key (2026-10-08, `6340c1671`)

- **Problem.** A Finalize aggregate's keys index the Partial's transport
  row, which `resolveKeySource` refuses to read (M0146-0005ce). TPC-DS
  Q76's Finalize printed `Group Key: channel, col_name, d_year, …` bare.
  PG's `show_agg_keys` deparses them through the Gather into the Partial's
  target list, so both nodes print the same text.
- **Change.** `aggGroupKeyText` (factored out of the Group Key loop)
  renders a Finalize key that names a Partial group position as the
  Partial's own key. `partialAggregateBelow` finds the printed Partial
  through Gather, Gather Merge, Sort and Incremental Sort.
- **Results.** The rendering category drops 9 → 8 at both scales (Q76).
  Q76's remaining key difference is the Append's branch order, which is a
  shape difference.
- Test: `TestFinalizeGroupKeyOverUnionAllPrintsPartialText`.
- **Census of the rendering class, 2026-10-08**, SHAPE-DIFF queries at
  SF0.25:
  - Q39's Sort Key labels `inv2.mean` as `inv1.mean` and keeps a
    `d_moy` key that PG drops;
  - Q54/Q67 chase past a Subquery Scan that PG keeps (`my_revenue.revenue`,
    `dw1.*`);
  - Q66's `year` prints bare where PG prints `date_dim.d_year`;
  - Q71's appendrel member label `"*SELECT* 3".ext_price` is missing;
  - Q77 prints `ss.s_store_sk` where PG prints `store.s_store_sk`.
- Not covered (ledger): an explicit `'x'::text` literal prints `('x')`.

## Slice — a key through a pruned GROUP BY column (2026-10-08, `c7063a0a3`)

- **Problem.** A grouped UNION ALL member whose `d_year` key is pinned by a
  WHERE constant has it pruned from its group keys. goopg publishes the
  column as an Aggregate Passthrough; PG keeps it as a plain Var in the
  Agg's target list and deparses an upper key through it. TPC-DS Q66
  printed the alias `year` where PG prints `date_dim.d_year`.
- **Change.** `resolveKeySource`'s Aggregate arm treats a passthrough
  position (the output's tail, outside grouping sets) like a group key.
- **Results.** The rendering category drops 8 → 7 at both scales (Q66).
  TPC-H text and eleven regress files are unchanged.
- Test: `TestExplainKeyThroughPrunedGroupColumn`.

## Slice — a key chase through a Finalize aggregate (2026-10-08, `413c69918`)

- **Problem.** TPC-DS Q77's key `ss.s_store_sk` comes from a UNION ALL
  branch over a LEFT JOIN of two grouped, inlined CTEs, each split into
  Finalize over Partial. PG deparses through the Finalize and the Gather
  into the Partial's group key and prints `store.s_store_sk`; its Merge
  Cond is `(store.s_store_sk = store_1.s_store_sk)`. goopg's
  `resolveKeySource` declined Finalize group positions outright.
- **Change.** Finalize group position j continues as the Partial's group
  key j (`finalizeGroupPairs`), checking only that the output names agree
  at j. A Finalize's own key expression can index the pre-split input row
  (Q77's CTE body: Index 51), so the previous slice's `aggGroupKeyText`
  now uses the same positional pairing.
- **Measured and dropped.** Continuing past a CTE-qualified Project target,
  and a positional fallback in `pinKeyExprNames`. An A/B on a private
  SF0.25 clone showed neither changes Q77.
- **Results.** The rendering category drops 7 → 6 at both scales (Q77).
- Test: `TestSortKeyThroughFinalizeGroupKey`.

## Slice — a key chase stops at a Subquery Scan PG keeps (2026-10-08, `666a5a87a`)

- **Problem.** A Subquery Scan still in goopg's printed plan is one PG
  keeps (the strip pass mirrors `trivial_subqueryscan`), and PG deparses an
  upper Var to that scan's alias-qualified column. `resolveKeySource`
  stepped through every Subquery Scan, so goopg printed:
  - TPC-DS Q71: `sum(ext_price)`, where PG prints `sum("*SELECT* 3".ext_price)`;
  - Q44: `rank_col`, where PG prints `v1.rank_col`;
  - Q49: `return_ratio`, where PG prints `in_web.return_ratio`;
  - Q67: `item.i_category`, where PG prints `dw1.i_category`.
- **Change.**
  - The chase stops at a Subquery Scan with an alias and names the column
    `alias.column`, quoting both parts.
  - `chaseAggregateResultArgs` keeps such a named argument.
  - A pinned chase skips the relation-identity check, because an aggregate
    argument's id belongs to its input's numbering.
  - The Join arm compares ids only when the join's own output carries the
    reference's numbering.
- **Results.**
  - Every changed line equals a line of PG's plan. The exception is SF1
    Q78's `cs.cs_customer_sk`, which follows from the ws/cs join-order
    difference.
  - The rendering category drops 6 → 4 at both scales.
  - TPC-H text and eleven regress files are unchanged.
- Test: `TestExplainAggregateArgThroughUnionMemberWrapper`. The transitive
  group-key test now follows PG's kept-scan rule.
- Not covered (ledger): goopg names an anonymous subquery `__sq_<pos>`
  where PG says `unnamed_subquery`. A join key over a UNION ALL member
  wrapper prints its name-based `m1.a` where PG prints `"*SELECT* 1".b`.

## Slice — a computed group key through a kept Subquery Scan (2026-10-08, `d20998581`)

- **Problem.** TPC-DS Q54 groups on `cast(revenue/50 as int)` over the
  grouped CTE `my_revenue`, which PG keeps as a Subquery Scan. PG prints
  `(((my_revenue.revenue / '50'::numeric))::integer)` on the Sort, Group
  and inner Sort keys. goopg expanded `revenue` through the inlined CTE's
  body into `sum(...)`.
- **Change.**
  - `chaseKeyExprColumns` (factored out of `chaseJoinKeyExprColumns`)
    chases each column of an expression key.
  - The Group Key line (`aggGroupKeyText`) and `sortKeyParts` take that
    chase for a computed key only when it reaches a kept scan
    (`keyExprReachesKeptScan`). This covers the Sort over the aggregate's
    named group key and the Sort over goopg's unprinted Project.
- **Measured and dropped.** A column-wise chase in `resolveKeySource`'s
  Project arm, and a Subquery Scan case in `chaseJoinKeyExprColumns`.
  Neither changes Q54.
- **Results.** Q54's three key lines equal PG's at both scales. Its
  rendering record remains on `my_customers.c_customer_sk` (PG
  `customer.c_customer_sk`, another inlined CTE).
- Test: `TestExplainComputedGroupKeyThroughKeptSubqueryScan`.
- Not covered (ledger): goopg leaves a plan unqualified when the only
  extra range-table entry is a subquery. PG's `rtable_size > 1` counts the
  subquery RTE, so `rev` prints as `my_revenue.rev`.

## Slice — key chases through a DISTINCT dedupe; Recheck params as Index Cond (2026-10-08, `1745ad42f`)

- **Problem.**
  - The key chase had no arm for a DISTINCT's dedupe (goopg's `Distinct`
    / `DistinctOn`), so TPC-DS Q54's inlined DISTINCT CTE `my_customers`
    printed as `my_customers.*` on five lines, where PG prints
    `customer.*`. Q49's sort key over the UNION's dedupe printed
    `channel`, where PG prints `('web'::text)`.
  - A bitmap scan's Recheck Cond qualified its NestLoop param by name
    lookup, while its sibling Index Cond used the full column path, so
    the two disagreed.
- **Change.**
  - `resolveKeySource` passes through `Distinct` / `DistinctOn`, which
    republish their input position for position.
  - `qualifyForeignColumns` renders a param through `formatExprQual`, the
    path the Index Cond key uses, and falls back to the name lookup.
- **Results.** The rendering category drops 4 → 3 at both scales. Q49's
  first key and Q54's `customer.*` lines now follow PG.
- Test: `TestExplainSortKeyThroughUnionDedupe`.
- Not covered (ledger): on a hash join's hashed side PG keeps `Subquery
  Scan` over an inlined DISTINCT CTE, and goopg strips it. That is a
  shape gap in the strip pass for inlined CTE leaves.

## Remaining classes (census of MATCH queries, 2026-10-05)

| class | queries | PG | goopg |
|---|---|---|---|
| ~~a BETWEEN bound is not folded~~ (done, `7eed1a031`) | Q10 (both scales), Q69 (SF1) | `(d_moy >= 3) AND (d_moy <= 6) AND (d_year = 2001)` | `(d_moy >= 3) AND (d_year = 2001) AND (d_moy <= (3 + 3))` |
| ~~order of two constant EC equalities~~ (done, `7b5892287`) | Q31 | `(d_year = 1999) AND (d_qoy = 3)` | reversed |
| ~~column qualification missing~~ (done, `fa61c41a7`; Q8 keeps alias numbering) | Q8 (both), Q46, Q79 (SF1) | `a1.ca_zip`, `customer.c_customer_sk`, `store.s_city` | bare |
| ~~a reference through an elided subquery / CTE / Append~~ (Q75's CTE group key done, `95c4f5581`; Q56's aggregate argument done, `3b9456d12`) | Q56, Q75 | `sum((sum(store_sales.ss_ext_sales_price)))`, `date_dim.d_year` | `sum(ss.total_sales)`, `curr_yr.d_year` |
| alias suffix numbering (Q8 done, `eef2d1762`; Q75 skipped numbers done, `f894f0c27`) | Q8, Q56, Q58, Q75 | numbered over the final flattened range table (`set_rtable_names`) | numbered in RTID allocation order |
| ~~a Sort Key detail~~ (typmod'd CASE NULL, done `2f9990d50`) | Q43 (SF1) | | |
