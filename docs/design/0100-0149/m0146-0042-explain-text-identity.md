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

## Remaining classes (census of MATCH queries, 2026-10-05)

| class | queries | PG | goopg |
|---|---|---|---|
| ~~a BETWEEN bound is not folded~~ (done, `7eed1a031`) | Q10 (both scales), Q69 (SF1) | `(d_moy >= 3) AND (d_moy <= 6) AND (d_year = 2001)` | `(d_moy >= 3) AND (d_year = 2001) AND (d_moy <= (3 + 3))` |
| ~~order of two constant EC equalities~~ (done, `7b5892287`) | Q31 | `(d_year = 1999) AND (d_qoy = 3)` | reversed |
| ~~column qualification missing~~ (done, `fa61c41a7`; Q8 keeps alias numbering) | Q8 (both), Q46, Q79 (SF1) | `a1.ca_zip`, `customer.c_customer_sk`, `store.s_city` | bare |
| a reference through an elided subquery / CTE / Append (Q75's CTE group key done, `95c4f5581`; Q56's aggregate argument open) | Q56, Q75 | `sum((sum(store_sales.ss_ext_sales_price)))`, `date_dim.d_year` | `sum(ss.total_sales)`, `curr_yr.d_year` |
| alias suffix numbering (Q8 done, `eef2d1762`; Q75 skipped numbers done, `f894f0c27`) | Q8, Q56, Q58, Q75 | numbered over the final flattened range table (`set_rtable_names`) | numbered in RTID allocation order |
| ~~a Sort Key detail~~ (typmod'd CASE NULL, done `2f9990d50`) | Q43 (SF1) | | |
