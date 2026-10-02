# M0146-0005 (part 7): slices 105+ — EXPLAIN text parity

Continuation of [m0146-0005-join-order-burndown-6.md](m0146-0005-join-order-burndown-6.md)
(slices 87-104), split per the design-doc size rule (D3). Same task and
census family. Progress is measured with `scripts/tpcds-text-identity.py`:
plans whose EXPLAIN text equals PG's with costs ignored.

## Slice 105: M0146-0005cz — an IN list over a text-only function folds to an array Const

Slice 87 taught EXPLAIN to print an all-literal IN list the way PG does.
transformAExprIn builds a ScalarArrayOpExpr, and eval_const_expressions
folds its ArrayExpr into one array Const: `= ANY ('{a,b}'::text[])`. The
fold needs the operand's type, which comes from `optimizer.ExprResultType`.
For a function call, that is an exact pg_proc lookup on the argument types.

`substr(ca_zip, 1, 5)` over a char/varchar column has no exact match.
pg_proc only has `substr(text, …)`, and PG reaches it through an implicit
coercion of the argument. So the lookup failed and the list printed as
the raw `= ANY ('85669', '86197', …)`. This affected TPC-DS Q8, Q15 and
Q45 at both scales.

- `formatInExprPG` falls back to `stringTypeName` when ExprResultType
  cannot resolve the operand. That is the classifier slice 93 already
  uses to print the argument's `(x)::text` coercion. Text-only functions
  (substr, substring, upper, lower, initcap) return text, so the list
  folds to `'{…}'::text[]`.
- The fix stays in the EXPLAIN layer. A general implicit-coercion pass in
  `funcCallResultType` (PG's func_select_candidate) would also change
  planner-side typing, which is out of scope here. The deferral ledger
  records it.

Test: `explain_in_list_array_test.go` gains `substr(varchar)`,
`substr(text)`, `upper(char)` and `a + 1` cases. Each expected line is
PG 18.3's output. The varchar and char cases fail without the fix.

Movement:

- Unfolded IN lists in the TPC-DS captures go 3 → 0 at both scales.
- Text-identical plans go 24 → 25 at SF0.25 and 13 → 14 at SF1 (Q15).
  Aligned lines go 2211 → 2214 and 2013 → 2016.
- CATEGORIES-EXCL-MATCH is unchanged (rendering 12 / 15).
- Regress A/B is not applicable: no PG expected output has an IN list
  over a text-only function.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q8 Q15 Q45)
  and ea-ratchet (10).

Evidence: `analysis/m0146/m0146-0005/slice105/`.

## Slice 106: M0146-0005da — a non-canonical date literal prints as date_out's text

make_op coerces `d_date >= '2002-5-01'` to a date Const, and get_const_expr
prints that value with date_out: `'2002-05-01'::date`. goopg printed the
coerced literal only when its text was already ISO (`YYYY-MM-DD`), so
TPC-DS Q16, Q94 and Q95 (`'2002-5-01'`, `'2001-4-01'`) kept the bare
`'2002-5-01'` at both scales.

- `canonicalISODateText` returns the ISO text for a four-digit-year
  `Y-M-D` literal with one- or two-digit month and day. That form reads
  the same under every DateStyle field order, so the printed text does not
  depend on the session. Every other spelling declines, as before.
- It replaces the ISO-only check at all three date sites: a comparison
  operand (coerceLiteralText), an explicit cast (castLiteralConstText)
  and an IN-list array element (inListArrayConst).
- Side finding, filed and not worked: numeric literal arithmetic folds
  through float64 (Q21's `2.0/3.0` → `0.6666666666666666`), filed as
  M0146-0041 (S2). The `cast(... as date) + 60` bound in Q94 is
  M0146-0040's `date + integer` defect (S2), which this slice does not
  touch.

Test: `explain_coerced_literal_test.go` adds three date cases and
`explain_in_list_array_test.go` adds a date IN list, all with PG 18.3's
output. Three of them fail without the fix.

Movement:

- Uncoerced date literals in the TPC-DS captures go 3 → 0 at both
  scales.
- Text-identical plans go 25 → 26 at SF0.25 and 14 → 15 at SF1 (Q94).
- CATEGORIES-EXCL-MATCH is unchanged (rendering 12 / 15).
- Regress A/B is not applicable: no regress EXPLAIN uses a non-canonical
  date literal.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q16 Q94 Q95)
  and ea-ratchet (10).

Evidence: `analysis/m0146/m0146-0005/slice106/`.

## Slice 107: M0146-0005db — LIKE prints as its `~~` operator

PG's parser turns `x LIKE 'p'` into an OpExpr on the `~~` operator family:

| SQL | operator |
|---|---|
| LIKE | `~~` |
| NOT LIKE | `!~~` |
| ILIKE | `~~*` |
| NOT ILIKE | `!~~*` |

The left operand picks the implementation: textlike, bpcharlike or
namelike. The pattern is always text. ruleutils deparses the operator
symbol and the text Const make_op coerced the pattern to:
`(hd_buy_potential ~~ 'Unknown%'::text)`. goopg printed the SQL keyword
and the bare literal (`LIKE 'Unknown%'`), which affected TPC-DS Q91 and
four regress cases.

- `formatLikeOpExpr` renders the family with PG's operator names:
  - A string-literal operand prints as `'…'::text`.
  - A varchar operand on either side shows its implicit `(x)::text`.
  - bpchar and name operands print bare, since they have their own
    left-operand implementations.
- A pattern with an ESCAPE clause declines and keeps the old text. PG
  folds `like_escape()` into the pattern Const (`'a\%'::text`), which is
  ledgered.

Test: `explain_like_op_test.go` has nine cases (char, text, varchar,
name, negated, ILIKE, column pattern, function operand), each with PG
18.3's output.

Movement:

- TPC-DS LIKE lines go 1 → 0 (now `~~`) at both scales.
- Text-identical plans go 15 → 16 at SF1 (Q91). At SF0.25 they stay 26,
  because Q91 there still differs by its Join Filter operand order.
- Regress A/B over btree_index, partition_prune, rowsecurity and
  select_parallel:
  - btree_index: the diff goes 500 → 491 lines.
  - partition_prune and rowsecurity: the LIKE lines now print PG's text,
    and nothing else moved.
- CATEGORIES-EXCL-MATCH is unchanged.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q91) and
  ea-ratchet (10).

Evidence: `analysis/m0146/m0146-0005/slice107/`.

## Slice 108: M0146-0005dc — `||` and a target-list literal print as text

TPC-DS Q80 unions three channel subqueries. Each arm has a literal channel
name and an id built with `'store' || store_id`, and the union is grouped
and sorted on both. PG prints:

    Sort Key: ('store channel'::text), (('store'::text || (ssr.store_id)::text))

goopg printed `('store channel'), (('store' || ssr.store_id))`. There
are three PG rules involved:

- **Concatenation is textcat / textanycat.** Both operands are text, so:
  - a literal prints as its text Const;
  - a char(n) or varchar operand shows its implicit `(x)::text`;
  - a non-character scalar (int, numeric, date) shows the `::text` cast
    that the inlined textanycat SQL function writes.

  `formatTextConcatExpr` renders this, classifying operands with
  `textConcatKinds`. Array, jsonb and bytea concatenation decline.
  `stringTypeName` now types a text concatenation as text, so a literal
  compared with one is coerced too (`= 'q'::text`).
- **A target-list literal is text.** resolveTargetListUnknowns turns an
  unknown output literal into text, so a key that is a literal prints
  `'x'::text`. `formatKeyExprQual` applies this at the Sort, Group,
  Hash and grouping-set key sites. Quals keep their own coercion path.
- **An Append cannot project.** The Agg's group key above a UNION ALL is
  an OUTER_VAR that resolves through the Append's first child's
  targetlist, so a non-Var key prints parenthesized, as for Sort and
  Gather. `keyChildPassesThrough` now admits a SetOp that renders as
  (Merge) Append.

Test: `explain_text_concat_test.go` has six cases, each with PG 18.3's
output. All six fail without the change.

Movement:

- Typed literal keys in the TPC-DS captures go 0 → 21 of PG's 24 at both
  scales.
- Text-identical plans go 26 → 27 at SF0.25 (Q80). SF1 stays 16, though
  aligned lines go 2018 → 2025.
- Regress A/B over the ten cases whose expected output has `||` or
  text-literal keys: no diff grew. In pg_lsn and window every moved line
  now has PG's concat form.
- CATEGORIES-EXCL-MATCH is unchanged.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q5 Q14 Q49
  Q66 Q76 Q77 Q80) and ea-ratchet (10).

Evidence: `analysis/m0146/m0146-0005/slice108/`.

## Slice 109: M0146-0005dd — an expression key over a kept Subquery Scan qualifies its columns

TPC-DS Q89 sorts on `sum_sales - avg_monthly_sales` above a Subquery Scan
`tmp1` whose qual keeps it in the plan (setrefs.c trivial_subqueryscan).
The key's Vars are OUTER_VARs into the scan's own columns, so PG prints
`((tmp1.sum_sales - tmp1.avg_monthly_sales)), tmp1.s_store_name`. goopg
already stopped a bare column key at that scan (slice 0005ca,
`boundaryKeyName`), but an expression key printed its columns bare.

- `chaseJoinKeyExprColumns` (slice 103's per-column chase for expression
  keys) now also admits a Filter over an aliased SubqueryScan. That is
  goopg's shape for a kept Subquery Scan. Each column then resolves
  through resolveKeySource to the same `alias.col` a bare key gets.

Test: `explain_subquery_key_test.go` has two Sort Key shapes, both with
PG 18.3's text. Both fail without the change.

Movement:

- Text-identical plans go 27 → 28 at SF0.25 (Q89). SF1 stays 16
  (aligned lines +1).
- Regress A/B over union, subselect, window and with: unchanged.
- CATEGORIES-EXCL-MATCH is unchanged.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q89) and
  ea-ratchet (10).

Not changed, but found while probing:

- A query holding a subquery RTE qualifies columns inside the subquery
  too (`PARTITION BY zsq.a`), since useprefix is `rtable_size > 1`.
  goopg prints them bare.

### Finding (filed as M0146-0005de, not landed): EC join-clause orientation

Five TPC-DS queries (Q8, Q50, Q53, Q63, Q74, Q91) print an inner-join
equality with its operands swapped relative to PG. PG never reuses the
written clause for a join. generate_join_implied_equalities calls
create_join_clause with `parent_ec = ec`, while source clauses carry
`parent_ec = NULL`, so ec_search_clause_for_ems never matches them. The
derived clause is cached in ec_derives, so **the first code path to create
it fixes its operand order**:

1. In set_base_rel_pathlists (range-table order),
   match_eclass_clauses_to_index → generate_implied_equalities_for_column
   creates `indexed_rel.col = other.col` for each index key column that
   is an EC member.
2. That index's parameterized paths then call get_baserel_parampathinfo,
   which creates every remaining EC clause to the parameterizing rel as
   `outer.col = rel.col`.
3. Otherwise, the first join pair creates it (make_join_rel(rel1, rel2)),
   lower range-table index first.

On scratch PG 18.3, j1(a,x)/j2(b,y) checks the model in four variants:

| setup | PG prints |
|---|---|
| no index | `(j1.a = j2.b)` whatever the outer side or written order |
| index on j2.b | `(j2.b = j1.a)` |
| index on j1.x | `(j2.b = j1.a) AND (j1.x = j2.y)` |
| `FROM j2, j1` | `j2` first |

All the TPC-DS cases agree with the model. A render-time "outer side
first" rule matched TPC-DS but contradicts the no-index case, so it was
discarded. Implementing the model needs planner-side state (range-table
order, per-relation index key columns, equivalence classes), which goopg
lacks in one place.

## Slice 110: M0146-0005df — relation suffixes follow PG's flattened range-table order

EXPLAIN's set_rtable_names numbers repeated relation names (`date_dim_1`,
`item_2`) in glob->finalrtable order. goopg claimed them in RTID order,
which is planning first-encounter order: a FROM subquery's relations sit
inside its parent's, and a CTE body comes before its consumer. PG
flattens the range table in setrefs.c, in three steps:

1. set_plan_references(root, top_plan) adds the top query's whole range
   table first, including subqueries pull_up_subqueries flattened into it.
2. Walking the top plan, each SubqueryScan PG kept recurses into its
   subroot and adds that level's table at that point, outer input first.
3. The subplans (CTE bodies, sublinks) follow in glob->subplans order.
   That is post-order: a sublink inside a CTE body finishes planning, and
   is appended, before the body.

- `renumberRTIDsFlatRtableOrder` (internal/optimizer/rtid_flat_order.go)
  rebuilds that order on the finished plan and re-stamps every scan's
  RTID. RTID feeds only EXPLAIN naming, so no plan or result changes. It
  runs in Plan() just before stripTrivialSubqueryScans, while the
  SubqueryScan wrappers (FROM subqueries and wrapInlinedCTEScans'
  inlined CTEs) still mark the levels PG keeps.
- A level's own relations keep allocation order (rtindex order). An
  inlined CTEScan is a subquery RTE: pulled up into the level, or a kept
  level when wrapped. A materialized CTE body or sublink is a subplan.

Test: `explain_rtable_order_test.go` uses a CTE referenced twice over the
same table as the main query. PG prints `Seq Scan on zrt` for the main
query and `zrt zrt_1` for the CTE body. It fails without the pass.

Movement:

- Alias-only diff lines (lines equal to PG's once `_N` suffixes are
  ignored): 42 → 13 at SF0.25 and 49 → 8 at SF1.
- Text-identical plans go 16 → 17 at SF1 (Q83, whose grouped CTE
  subqueries take suffixes in join order). Aligned lines go 2225 → 2263
  (SF0.25) and 2026 → 2075 (SF1).
- No line that matched PG before differs now, at either scale.
- Regress A/B over with, subselect, union, join, aggregates and window:
  unchanged apart from join.sql's known row flap.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (12 queries)
  and ea-ratchet (10).

Left over:

- 13 / 8 alias lines in Q8, Q14, Q56, Q58, Q69 and Q75.
- The exact interleaving of a level's CTE subplans, its sublinks and the
  subplans of its kept subqueries is ledgered. glob->subplans appends
  them as planning reaches them.
- Pulled-up subquery relations should follow the parent's own FROM
  entries in rtindex order; goopg uses allocation order.
- Seen while probing: goopg's min/max InitPlan rewrite prints
  `Seq Scan on zrt` where the query wrote the alias `zrt z`.
