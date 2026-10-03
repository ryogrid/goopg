# M0146-0005 (part 6): slices 87+ — EXPLAIN constant and array rendering

Status: **HELD 2026-10-03** — the M0146-0005 root is `[!]` under the S4 lineage budget (five consecutive `Movement: none`: 0005dn, do, dt, dr, ds); escalation block in `.ralph/fix_plan.md`, awaiting the owner.

Continuation of [m0146-0005-join-order-burndown-5.md](m0146-0005-join-order-burndown-5.md)
(slices 71-86), split per the design-doc size rule (D3). Same task and census
family.

## Slice 87: M0146-0005ch — an IN list prints as PG's folded array Const

PG's transformAExprIn turns `a IN (1, 2)` into a ScalarArrayOpExpr over an
ArrayExpr. eval_const_expressions folds the ArrayExpr to one array Const,
so EXPLAIN prints `(a = ANY ('{1,2}'::integer[]))`. goopg printed the
element list, `(a = ANY (1, 2))`, in every IN-list qual. None of the 51
array Consts PG prints over TPC-DS (each scale) matched.

- `inListArrayConst` returns the Const text for an all-literal list. The
  element type is select_common_type's pick for the column and the
  literals:
  - int2/int4 → `integer[]`, int8 → `bigint[]`;
  - an integer column against a decimal literal → `numeric[]` with
    `(a)::numeric`;
  - text → `text[]`, char(n) → `bpchar[]`, varchar → `text[]` with
    `(f)::text`, and canonical ISO dates → `date[]`.

  Elements are quoted as array_out does: double quotes around empty, NULL,
  or any element holding `"`, a backslash, braces, a comma or whitespace.
  Embedded `"` and backslashes are escaped. Any other list (a NULL, a
  non-literal, an unmodelled type) keeps the old rendering.
- NOT IN prints `(a <> ALL (…))`, and `<> ANY` keeps its own operator.
  The UnaryOp arm pushes an explicit `NOT (b IN …)` into the list as
  negate_clause does.
- The Index Cond SAOP renderer (`formatIndexCond`) is the sibling. It uses
  the same helper with the index column's catalog type. Its
  bounds-on-the-second-column form now parenthesises each clause:
  `((a = ANY (…)) AND (b > 1))`.

Test: `TestInListPrintsFoldedArrayConst` (13 PG 18.3 oracle lines; all 13
fail with the branch disabled). `TestSAOPExplainRendersAnyCond` now
expects PG's `'{2,4}'::integer[]`.

Movement:

- TPC-DS: goopg prints 49 of PG's 51 array Consts per scale (was 0). The
  rest are the
  `substr(ca_zip, …)` lists of Q8/Q15/Q45. There PG also casts the
  argument (`(ca_zip)::text`, the ledgered argument-cast gap) and
  ExprResultType does not resolve `substr(bpchar, …)`. The classifier treats
  qual text as rendering inside MATCH, so CATEGORIES-EXCL-MATCH is
  unchanged (rendering 12 / 15).
- Regress, same-order A/B over 12 cases: btree_index 507 → 500,
  create_index 3310 → 3309. Five array lines now match PG exactly (was 0).
  Most other changed lines stay mismatched because of the plan shape or
  partition-child qualification around them.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): non-literal lists (`ARRAY[...]` and PG's
Var/non-Var split into OR arms), lists with NULL elements, float, timestamp
and other element types, and a function-call operand whose type
ExprResultType cannot resolve.

Evidence: `analysis/m0146/m0146-0005/slice87/`.

## Slice 88: M0146-0005ci — a literal prints as the Const it was coerced to

PG coerces a literal to the other operand's type during parse analysis
(make_op → coerce_type), so EXPLAIN prints the resulting Const through
get_const_expr: `(ca_gmt_offset = '-6'::numeric)`,
`(s_state = 'TN'::bpchar)`, `(revenue / '50'::numeric)`. goopg printed the
literal as written (`= -6`, `= 'TN'`).

- `formatCoercedLiteralOperands` covers `= <> < <= > >= + - * /` with one
  literal side and one side whose type ExprResultType resolves.
  `coerceLiteralText` applies the operator's input type:
  - int2/int4/int8 operand: the literal stays int4 (cross-type operators);
  - numeric: an integer becomes `'N'::numeric`, a decimal prints bare
    unless negative;
  - an integer operand against a decimal is cast, `((a)::numeric > 2.5)`;
  - char(n) → `'x'::bpchar`, text → `'x'::text`, varchar → `((f)::text =
    'x'::text)`.
- `intConstText` is get_const_expr's int4 arm everywhere: a negative
  literal is `'-6'::integer`, and one past int4's range is
  `'N'::bigint`.
- Typed literals print format_type's names (`timestamp without time zone`),
  and a timestamp's ISO value goes through timestamp_out's form
  (`'2001-07-15 00:00:00'`).

Test: `TestLiteralPrintsAsCoercedConst` (13 PG 18.3 oracle lines; 8 fail
with the coercion disabled, the rest pin the int4 and timestamp arms).
Two older tests that pinned goopg's unlabelled text now expect PG's
`'3'::text` / `'a'::text`.

Movement (TPC-DS SF0.25; SF1 identical):

| label | PG | goopg before | goopg now |
|---|---|---|---|
| `::numeric` | 152 | 0 | 141 |
| `::bpchar` | 207 | 49 | 207 |
| `::text` | 68 | 6 | 33 |
| `::integer` | 28 | 19 | 28 |
| `::timestamp without time zone` | 28 | 0 | 28 |

No label goopg prints is missing from PG's plan for the same query
(per-query multiset check). CATEGORIES-EXCL-MATCH is unchanged (rendering
12 / 15). In regress the moves are small; literal lines in partition_prune
now match PG's `'a'::bpchar`, but their column qualification still
differs. Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and
ea-ratchet (10) pass.

Left (ledgered): string literals that are target or union-arm output
columns (`'store channel'::text`), LIKE patterns (`~~ 'Unknown%'::text`),
literals inside CASE / COALESCE results (Q39, Q78 `'0'::bigint`), function
arguments, and float or date-arithmetic operands.

Side finding (filed S2 as M0146-0040, not worked): `date + integer`
returns a timestamp-formatted value of type unknown, and errors with a
literal date operand.

Evidence: `analysis/m0146/m0146-0005/slice88/`.

## Slice 89: M0146-0005cj — a Sort key over a WindowAgg prints its window function

PG computes `sum(x) * 100 / sum(sum(x)) OVER (…)` in the WindowAgg's
targetlist, and the Sort above keys on that entry. EXPLAIN therefore prints
`((((sum(x)) * '100'::numeric) / sum((sum(x))) OVER w1))`:

- the window function is evaluated in place, so it prints bare with its
  `OVER w1`;
- each input column is an OUTER_VAR into the window's child, parenthesised
  around its non-Var referent (get_special_variable), so the aggregate
  prints as `(sum(x))`, also inside the window function's argument;
- the whole key is a non-Var referent and takes the outer pair.

goopg printed the output labels, `(((sum * 100) / sum))`, and
`Sort Key: rank` for a bare window result.

- `windowKeyText` rewrites the key's column references for display with
  `optimizer.CloneExprReplacingColumnRefs` (slice 92 replaced a duplicate
  wrapper this slice had added):
  - a window result becomes `windowFuncText` (`name(args) OVER wname`,
    with arguments chased through the window's child);
  - an input column is chased through `resolveKeySource` and wrapped
    unless it lands on a plain column.

  Stand-ins print through the `boundaryKeyName` map and keep the column's
  type, so slice 88's literal coercion still labels `'100'::numeric`.
- `windowUnderNarrowing` also finds the WindowAgg under a column-selecting
  Project, which goopg places where PG's WindowAgg emits the final
  targetlist (`ORDER BY rk` → `(rank() OVER w1)`).

Test: `TestSortKeyOverWindowAggDeparsesWindowFunc` (3 PG 18.3 oracle
lines; all fail with the arm disabled).

Movement:

- TPC-DS Q12/Q20/Q98 go from `MATCH [rendering]` to `MATCH []` at both
  scales. Rendering including MATCH goes 18 → 15 (SF0.25) and 19 → 16
  (SF1), and fully identical plans 32 → 35 and 24 → 27.
  CATEGORIES-EXCL-MATCH is unchanged (12 / 15).
- What is left in those three plans is a missing `Parallel Hash` line under
  a parallel hash join, which the classifier does not score.
- Regress groupingsets 1893 → 1880: the cube-over-window EXPLAIN hunk now
  matches PG completely.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): window functions with FILTER, keys over a WindowAgg
reached through other nodes (Filter, Subquery Scan), Group Key / Window
lines that read window results, and the missing `Parallel Hash` node line.

Evidence: `analysis/m0146/m0146-0005/slice89/`.

## Slice 90: M0146-0005ck — a hash join prints its Hash node

PG's create_hashjoin_plan puts a Hash node (Parallel Hash for a
parallel_hash join) over the build input, so every hash join prints
`->  Hash` between the join and its inner scan. goopg's join operator
builds the table itself and had no such node. EXPLAIN printed the inner
scan directly under the join: 0 of PG's 241 Hash lines over TPC-DS SF0.25
appeared. The plan-parity classifier strips PG's Hash nodes, so this never
showed in its counts, but no TPC-DS plan with a hash join could match PG's
text.

- `hashBuildChild` names the build side (`Right`, or `Left` under
  BuildLeft) and the node label. Every renderer synthesises the node over
  that child:
  - the text walker (`emitHashNodeLine`);
  - the text ANALYZE walker, which states the input's actual rows and loops
    and its total time at both ends;
  - FORMAT JSON plain and ANALYZE (`hashNodeJSON`).
- The Hash node's estimate is create_hashjoin_plan's: the input's rows and
  width, with startup = total = the input's total cost. `hashNodeCost`
  reads it through Project wrappers, the node the input's own line prints.
- ANALYZE's hash-table line (`Buckets: …`) moves from the join to the Hash
  node, where show_hash_info prints it.

Tests: `TestHashJoinPrintsHashNode` (PG 18.3's COSTS OFF block
byte-for-byte, the cost identity, and the ANALYZE Buckets placement) and
`TestHashJoinJSONPrintsHashNode`.

Movement:

- TPC-DS plans whose text equals PG's with costs ignored: 1 → 8 at SF0.25
  (Q9 Q12 Q18 Q20 Q22 Q84 Q97 Q98) and 1 → 5 at SF1.
- goopg now prints 270 Hash lines to PG's 241 (SF0.25), because it chooses
  more hash joins. The classifier counts are unchanged.
- Regress, same-order A/B over 12 cases: 47737 → 47567 lines. join_hash
  goes 1022 → 899 and partition_join 6381 → 6293. In join, subselect and
  inherit the new lines sit in hunks where goopg already chose a different
  plan.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set (all queries
  fire; it ran about 70 minutes) and ea-ratchet (10) pass.

Left (ledgered):

- a BuildLeft join still prints its build side first, where PG's plan has
  it as the second child;
- the Hash node's ANALYZE actual time is approximated from the input's
  total;
- ANALYZE still reports the build input as `rows=0 loops=0` (the existing
  M0145-0030 row);
- the JSON Hash object lacks PG's other per-node properties (goopg's JSON
  carries none of them).

Evidence: `analysis/m0146/m0146-0005/slice90/`.

## Slice 91: M0146-0005cl — AND / OR chains print flat

PG's AND and OR are N-ary BoolExprs, and eval_const_expressions
(simplify_and_arguments / simplify_or_arguments) flattens nested
same-kind arms. A planned qual therefore prints
`((a = 1) AND (b = 2) AND (c = 3))`. goopg's planner keeps binary
BinaryOp chains, and EXPLAIN printed `(((a = 1) AND (b = 2)) AND (c = 3))`.
This was the most frequent remaining text gap inside the TPC-DS MATCH
plans (Q7/Q27/Q28/Q53/Q63/Q82/Q88 filters).

- formatExprQual's BinaryOp arm prints an AND or OR node as one
  parenthesised list of its arms, with `flattenBoolArms` descending into
  nested nodes of the same operator. Arms of the other operator keep
  their own parentheses (`((a) AND (b)) OR (c)`).

Instrument: `scripts/tpcds-text-identity.py` (new) counts the fire-set
captures whose EXPLAIN text equals PG's with costs ignored, plus the
position-aligned identical lines. It is the measure for this text-parity
work, which the plan-parity classifier normalises away.

Test: `TestBoolChainsPrintFlat` (5 PG 18.3 oracle lines; all fail with the
flattening disabled).

Movement:

- TPC-DS text-identical plans go 8 → 12 at SF0.25 (Q7, Q27, Q28, Q82) and
  5 → 8 at SF1 (Q27, Q28, Q41). Aligned identical lines go 1891 → 1916 and
  1871 → 1895. The classifier counts are unchanged.
- Regress, same-order A/B over 19 cases: 58524 → 58506.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Next gaps by count in the MATCH plans:

- outer references in a parameterised Index Cond / Recheck Cond print
  unqualified (`ss_customer_sk` for PG's `store_sales.ss_customer_sk`);
- varchar operands lack PG's `(x)::text` cast;
- PG orders a scan's quals by cost (order_qual_clauses), which goopg does
  not.

Evidence: `analysis/m0146/m0146-0005/slice91/`.

## Slice 92: M0146-0005cm — outer references in a parameterised scan's quals print qualified

An outer column in the inner scan of a parameterised nested loop is PG's
NestLoop param. get_parameter deparses it against the NestLoop's outer
plan with the relation prefix forced, so PG prints
`Index Cond: (c_customer_sk = store_sales.ss_customer_sk)`, and the
bitmap scan's `Recheck Cond` the same way. goopg printed the bare name
in two cases:

- a reference whose binding id could not be named, inside a CTE body where
  ids restart (TPC-DS Q24);
- every Recheck Cond, which is a scan qual and therefore unqualified.

There were 75 such Index / Recheck lines over TPC-DS SF0.25; PG has none.

- `subPlanReg.enterParamInner` makes a parameterised nested loop's outer
  input the deparse ancestor while its inner side prints. This covers a
  NestedLoopIndexJoin's inner and a Join whose inner `paramInnerChild`
  accepts, in both text walkers. The OuterColumnRef arm's existing
  `resolveInAncestor` fallback then names the relation.
- `qualifyForeignColumns` pins every Recheck Cond column the scan does not
  produce to its qualified name.
- `indexCondAndText` joins a multi-clause Index Cond as PG's implicit-AND
  list, `((a = 1) AND (b > 2))`. The five former
  `wrapParen(strings.Join(parts, " AND "))` sites printed
  `(a = 1 AND b > 2)`.
- The executor's duplicate `optimizer.CloneExprMapColumnRefs` (slice 89)
  is removed in favour of the existing `CloneExprReplacingColumnRefs`.

Tests: the three `TestNLIBitmapProbe*` expectations now hold PG's text
(`Recheck Cond: (l_key = ord.o_key)`, checked on PG 18.3). A minimal unit
reproduction of the CTE-body case did not trigger: it needs Q24's
OuterColumnRef under a parallel join. The ancestor change is therefore
evidenced by the TPC-DS captures.

Movement:

- Unqualified outer-reference Index / Recheck lines go 75 → 8 at SF0.25 and
  63 → 6 at SF1.
- Text-identical plans go 12 → 13 at SF0.25 (Q3) and 8 → 9 at SF1 (Q87).
  Aligned identical lines go 1916 → 1941 and 1895 → 1918.
- The classifier counts are unchanged. Regress, same-order A/B over 17
  cases: 52409 → 52402.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice92/`.

## Slice 93: M0146-0005cn — string operands show their text coercion

varchar has no comparison operators of its own, and substr / upper /
lower / initcap take text only. Parse analysis therefore coerces a varchar
operand, or a char(n) / varchar argument, to text. get_oper_expr and
get_func_expr deparse their arguments with showimplicit, so EXPLAIN shows
the coercion:

- `((ss1.ca_county)::text = (ws2.ca_county)::text)`;
- `substr((ca_zip)::text, 1, 5)`;
- `((c_birth_country)::text <> upper((ca_country)::text))`.

goopg printed the bare columns. Slice 88 had covered only the varchar
column against a literal: 19 of PG's 73 `(x)::text` over TPC-DS.

- `formatTextCastOperands` handles a comparison whose operands are
  varchar/text (at least one varchar): each varchar side takes `::text`.
  char(n) against char(n) keeps bpchar's operators and is left alone.
- The FuncCall arm casts a char(n) / varchar first argument of a
  `textOnlyFuncs` function.
- `stringTypeName` classifies operands, and treats a text-only function's
  result as text when pg_proc lookup cannot type it (`substr(char, …)`).
  The literal-coercion path uses the same fallback, so
  `substr((b)::text, 1, 2) = 'ab'::text`.

Test: `TestStringOperandsShowTextCast` (6 PG 18.3 oracle lines; 5 fail with
the casts disabled, the char(n) join pins the no-cast case). The plain
`b = b` probe was dropped: PG rewrites it to `b IS NOT NULL`.

Movement:

- TPC-DS `(x)::text` casts: 19 → 57 of PG's 73 at both scales. No cast
  goopg prints is missing from PG's plan for the same query.
- Aligned identical lines go 1941 → 1952 and 1918 → 1929. Text-identical
  plans stay at 13 / 9.
- Regress, same-order A/B: 57036 → 57026.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): `||` operands (Q5/Q80 `(ssr.store_id)::text`), and varchar
columns read through CTE / subquery outputs whose type goopg does not carry
as varchar (Q47/Q57).

Evidence: `analysis/m0146/m0146-0005/slice93/`.

## Slice 94: M0146-0005co — a scan's restriction list follows PG's order

PG builds a relation's baserestrictinfo in two passes:

- distribute_qual_to_rels adds every qual except the equalities
  process_equivalence absorbs into an EquivalenceClass (`col = const`, or
  two columns of the relation);
- generate_base_implied_equalities appends those afterwards.

order_qual_clauses then stable-sorts the list by per-tuple cost. So
`t_hour = 8 AND t_minute >= 30` filters as
`((t_minute >= 30) AND (t_hour = 8))`, while `a = 1 AND (b = 2 OR b = 3)`
keeps the cheap equality ahead of the three-operator OR. goopg kept the
written order. This was the largest remaining text gap in the TPC-DS
MATCH plans (Q74/Q88/Q96, 10 Filter lines).

- `equivalenceClausesLast` (local_filters.go) reorders each relation's
  local conjuncts in `partitionConjunctsForJoinPlanning`:
  - `isEquivalenceClause` arms (an `=` between a column and a plain
    constant, or two columns; type assertions only, no new walker switch)
    move after the rest;
  - a stable sort by `qualEvalOps`' per-tuple cost follows.

  This is a plan change, not a rendering one: the scan evaluates its quals
  in the printed order, as PG's does.

Test: `TestRestrictionQualOrder` (6 PG 18.3 oracle lines; 5 fail with the
reorder disabled).

Movement:

- TPC-DS text-identical plans go 13 → 14 at SF0.25 (Q96) and 9 → 11 at SF1
  (Q88, Q96). Aligned identical lines go 1952 → 1963 and 1929 → 1941.
- The plan-parity classification is identical before and after at both
  scales, and the TPC-H census is unchanged (11/22 match, same categories).
- Regress, same-order A/B over 21 cases: 63877 → 63865.
  - In join.sql a whole hunk now matches.
  - create_index's Recheck / Filter lines take PG's
    `(hundred = 42) AND (…OR…)` order.
  - One create_index plan moves from a Parallel Seq Scan to an Index Scan
    on the cheaper ANY. PG plans an Index Only Scan with both quals; goopg
    diverged before and after.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set, TPC-H census
  and ea-ratchet (10) pass.

Left (ledgered): EC creation order among several EC equalities (Q31's
`d_year` before `d_qoy`), joins' Join Filter clause order (Q8/Q31/Q74),
and qualEvalOps' cast and sublink costs.

Evidence: `analysis/m0146/m0146-0005/slice94/`.

## Slice 95: M0146-0005cp — a repeated CTE reference takes its `_N` suffix

A CTE referenced twice without aliases is two range-table entries sharing
one name. set_rtable_names suffixes the later one, so PG prints
`CTE Scan on ssales` and `CTE Scan on ssales ssales_1` (TPC-DS Q24). goopg
already computed the disambiguated name in `explainNames.nodeLabels` and
used it for table scans, but the CTE Scan arm never consulted it.

- The CTE Scan arm, and the inlined-CTE `Subquery Scan` arm, print
  `disambiguatedName` when one is assigned.
- The first attempt suffixed the outer reference of a recursive CTE.
  goopg numbers the CTE body, with its WorkTable self-reference, before
  the outer query, so the self-reference claimed the bare name. PG names
  the outer reference first (`CTE Scan on x`,
  `WorkTable Scan on x x_1`). The label pass in `explain_names.go` now
  skips recursive self-references, so the outer scan keeps the bare name.
  The working table's own `_1` is ledgered.

Test: `TestRepeatedCTEReferenceTakesSuffix` (PG 18.3 oracle
`CTE Scan on c` / `CTE Scan on c c_1`; fails with the arm disabled). The
regress A/B caught the recursive-CTE regression before commit
(subselect.sql); after the fix the 8-case A/B is neutral.

Movement:

- The CTE Scan labels of every fired TPC-DS query (Q2/Q14/Q23/Q24/Q59/Q95)
  equal PG's at both scales.
- Text-identical plans go 14 → 15 at SF0.25 (Q24).
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): the recursive working table's `x_1`, and the
`(x)::numeric` cast PG shows comparing an int column with a numeric
InitPlan result.

Evidence: `analysis/m0146/m0146-0005/slice95/`.

## Slice 96: M0146-0005cq — a NestLoop param names its relation by its printed label

Q88 and Q90 cross-join subqueries that each scan the same table, which
set_rtable_names prints as `web_sales`, `web_sales_1`, …. Each subquery's
parameterised index scan keys on its own loop's outer relation:
`Index Cond: (t_time_sk = web_sales_1.ws_sold_time_sk)`. goopg printed
`web_sales.` in every subquery. The key is an OuterColumnRef whose binding
id restarts per subquery, so `names().column(id)` resolved every one to the
first level's relation.

- `subPlanReg.paramInner` is set by `enterParamInner` (slice 92) while a
  parameterised loop's inner side prints, and cleared while a sublink's
  subtree prints. Under it, the OuterColumnRef arm first resolves the name
  against the loop's outer input with `explainNames.resolveLabelInAncestor`.
  That is resolveInAncestor naming each relation by its disambiguated label,
  as get_parameter deparses a NestLoop param against the outer plan.

No unit test: a reproduction needs Q88/Q90's parallel subquery shape. A
two-subquery probe resolved correctly through binding ids and was planned
differently by PG, so the TPC-DS captures are the evidence.

Movement:

- Q90's second Index Cond now names `web_sales_1`. Q61's eight Index Conds
  equal PG's (`store_sales_1` / `customer_1`), where four printed the
  unsuffixed name.
- Text-identical plans go 15 → 16 at SF0.25 (Q88). Aligned identical lines
  go 1965 → 1973 and 1943 → 1944.
- Q14 moves too, but its plan shape differs from PG's, so its suffix
  numbers are not comparable.
- Regress, same-order A/B over 7 cases: flat (join.sql row-order flap
  only).
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice96/`.

## Slice 97: M0146-0005cr — a group key over a pass-through child is parenthesised

show_agg_keys deparses each group key against the Agg's child targetlist.

- A scan or join computes a key expression in its own targetlist, so it
  prints bare: `Group Key: (a % 10)` over a Seq Scan, checked on PG 18.3.
- A Sort or Gather Merge only passes its input's columns through. The key
  then reaches the Agg as an OUTER_VAR, and get_special_variable wraps the
  non-Var referent: `Group Key: (substr((b)::text, 1, 2))`.

goopg applied the wrap (S18) only when the child was a Sort, so the
Finalize GroupAggregate over Gather Merge in TPC-DS Q62/Q99 printed the
bare form.

- `keyChildPassesThrough` names the non-projecting children (Sort,
  IncrementalSort, Gather, GatherMerge, Materialize). The plain-grouping
  Group Key arm uses it in place of the Sort-only test.

Test: `TestFinalizeGroupKeyOverGatherMergeIsParenthesised` (PG 18.3 output
for the same statement and settings; fails with the Sort-only test).

Movement:

- Q62/Q99 Group Keys equal PG's at both scales. Text-identical plans go
  16 → 17 at SF0.25 (Q62).
- Regress A/B (groupingsets / aggregates / select_parallel) is flat.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice97/`.

## Slice 98: M0146-0005cs — a Subquery Scan's filter names its columns by the scan's alias

show_scan_qual sets `useprefix = IsA(plan, SubqueryScan) || es->verbose`,
so a Subquery Scan's quals print each column as the scan's own alias
column: `Filter: (y.web_cumulative > y.store_cumulative)` (TPC-DS Q51),
`(tmp1.avg_quarterly_sales > …)` (Q53/Q63/Q89). goopg's generic detail arm
resolved the columns through binding ids. Those restart per query level, so
the columns could not be named and printed bare.

- A `*optimizer.SubqueryScan` arm in emitNodeDetailLines maps every
  ColumnRef that is a position in the scan's output (same name) to
  `alias.col` through `displayColumn`, then prints the Filter.

Test: `TestExplainDoesNotQualifyDerivedColumns` now expects PG 18.3's
`Filter: (t.s1 <> t.s2)` (checked on PG; it fails with the arm disabled).
Its wrong-relation guard is kept.

Movement:

- Q51 becomes text-identical at both scales: 17 → 18 (SF0.25) and
  11 → 12 (SF1).
- Q53/Q63/Q89's filters now match PG's except `ELSE NULL::numeric`
  (ledgered: a NULL CASE arm takes the CASE's type label).
- Q44/Q67 print a Subquery Scan filter where PG's plan shape has none (a
  window run condition).
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice98/`.

## Slice 99: M0146-0005ct — a NULL CASE arm prints as a typed NULL

PG's transformCaseExpr coerces every result arm to the CASE's common type,
so a NULL arm becomes a typed NULL Const. get_const_expr labels it
(`ELSE NULL::numeric`). An omitted ELSE is the parser's NULL defresult,
which ruleutils always prints (`CASE WHEN (a > 0) THEN a ELSE
NULL::integer END`). goopg printed `ELSE NULL END` and dropped the
implicit ELSE. Over TPC-DS SF0.25, PG prints 26 such labels and goopg 0.

- The CaseExpr arm labels NullConst results with `nullConstTypeLabel`
  (format_type's name for the CASE's ExprResultType: numeric, integer,
  bigint, smallint, text, boolean, double precision, date). It appends
  `ELSE NULL::type` when no ELSE was written. Typmod-carrying and
  unmodelled types keep the old text.

Test: `TestCaseNullArmIsTyped` (4 PG 18.3 oracle lines; 3 fail with the
label disabled, the fourth has no NULL arm).

Movement:

- TPC-DS `NULL::type` labels go 0 → 26 of 26 at SF0.25 and 0 → 19 of 26 at
  SF1. No label goopg prints is missing from PG's plan for the same query.
- Text-identical plans go 18 → 19 at SF0.25 (Q43). Aligned identical lines
  go 1977 → 1985 and 1948 → 1955.
- Gates: units, spotcheck, sweep 96/96, fire set, arm 24/24 (run after the
  nightly batch) and ea-ratchet (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice99/`.

## Slice 100: M0146-0005cu — an integer operand against a numeric one shows its numeric cast

There are no mixed integer/numeric operators. make_op coerces the integer
operand to numeric (int4_numeric), and get_oper_expr shows the coercion
because operator arguments deparse with showimplicit:
`sum((ss_sales_price * (ss_quantity)::numeric))`, `(n = (a)::numeric)`,
`(((a - b))::numeric < n)`. goopg printed the bare integer operand. Over
TPC-DS (Q14/Q23/Q93 …) PG prints 18 such casts and goopg printed 3.

- `formatNumericPromotedOperands` handles comparison and arithmetic
  operators whose operands are integer (int2/int4/int8) and numeric, with
  neither a literal (literals are slice 88's coercion). The integer side
  prints `(…)::numeric`. `numericKind` classifies by ExprResultType.

Test: `TestIntegerOperandPromotedToNumeric` (5 PG 18.3 oracle lines; 4 fail
with the arm disabled, the int-vs-int case pins no cast).
`TestExplainHavingFilterExpandsAggOutput` now expects PG's
`(r65h.availqty)::numeric`.

Movement:

- TPC-DS `)::numeric` casts go 3 → 15 of PG's 18 at both scales.
- Text-identical plans go 19 → 20 at SF0.25 (Q93).
- The one query where goopg prints more casts than PG is Q67, where goopg
  chases a window key into the aggregate PG prints as `dw1.sumsales` (the
  ledgered subquery-alias window-key gap). The cast itself is PG's.
- Regress A/B over 9 cases is flat (join.sql row flap).
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): the cast on a non-literal whose type ExprResultType cannot
resolve, such as `((InitPlan 1).col1)::numeric` (a bigint InitPlan result).

Evidence: `analysis/m0146/m0146-0005/slice100/`.

## Slice 101: M0146-0005cv — a scalar sublink's value has its subplan's type

`ExprResultType` declined a SubqueryExpr. Slice 100's numeric promotion
therefore could not see that `(SELECT sum(int_col) …)` is bigint, and
printed `> (InitPlan 1).col1` where PG prints
`> ((InitPlan 1).col1)::numeric`.

- `ExprResultType` types a SubqueryExpr by its subplan's single output
  column, as exprType does for a SubLink or its Param. The other callers
  are unaffected:
  - the min/max const-arg branch admits only `isConstantExpr` arguments;
  - the index-key callers cannot hold a sublink.

Test: `TestExplainHavingFilterExpandsAggOutput` now pins PG 18.3's whole
line, `Filter: (sum((r65h.supplycost * (r65h.availqty)::numeric)) >
((InitPlan 1).col1)::numeric)`. It fails with the case disabled. The probe
(`slice101/probe.sql`) also matches PG on an avg (numeric) and a max (int)
InitPlan.

Movement: none on TPC-DS (the fire set shows no changed plan at either
scale) or on the subselect / aggregates / join regress cases. The rule is
PG's and is pinned by the test.

Evidence: `analysis/m0146/m0146-0005/slice101/`.

## Slice 102: M0146-0005cw — an explicit cast prints as PG's coercion

A cast written in the query is a COERCE_EXPLICIT_CAST node, which ruleutils
always shows through get_coercion_expr as `(arg)::type`, using
format_type_with_typemod's name:
`((a)::numeric(15,4) / (n)::numeric(15,4))`, `(((n / '50'::numeric))::integer`.
goopg's CastExpr arm printed only the operand. CastExpr could not tell a
written cast from one the planner inserts (set-operation coercion,
index-key alignment), which PG never shows.

- `CastExpr.Explicit` is set where a parser cast is resolved: the
  after-aggregate, after-window and main resolve arms, and
  NewCastExprFromParser. Every clone carries it.
- `explicitCastText` handles three cases:
  - a cast of a literal is the Const parse analysis folds it into, printed
    by `castLiteralConstText` with get_const_expr's rules (int4 bare,
    int8/int2 labelled, numeric rescaled to its typmod and printed bare with
    the label, e.g. `5.00::numeric(10,2)`, everything else quoted and
    labelled);
  - a cast to the operand's own type without a modifier prints nothing,
    since coerce_type returns its input;
  - anything else prints `(arg)::type`.

  `castTypeName` spells numeric/varchar/bpchar with typmods and the common
  scalar types.
- `coercibleLiteral` treats a modifier-free explicit cast of a literal as
  that literal. eval_const_expressions folds the operator's coercion of it
  too, so `n > cast(7 as bigint)` prints `'7'::numeric`.
- A string literal against a date column prints `'…'::date` (canonical ISO
  text only).

Test: `TestExplicitCastPrints` (9 PG 18.3 oracle lines; 5 fail with the
arm disabled, the others pin the folding rules).

Movement:

- TPC-DS non-text cast / typed-constant tokens go 41 → 55 of PG's 58
  (SF0.25). The only goopg-only cast is still Q67's (slice 100).
- Q90's sort key now carries `::numeric(15,4)`. What is left there is the
  chase of `amc`/`pmc` through the cross-joined subqueries to `(count(*))`.
- Text-identical counts are unchanged (20 / 12). Regress A/B over 13
  cases is flat.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice102/`.

## Slice 103: M0146-0005cx — an expression key over a join chases its columns into the aggregates

PG evaluates an expression Sort key over a join in the join's
targetlist. Each column there is an OUTER/INNER_VAR into the join's
inputs; get_variable follows it and parenthesises a non-Var referent.
TPC-DS Q90's cross join of two `count(*)` subqueries therefore prints
`((((count(*)))::numeric(15,4) / ((count(*)))::numeric(15,4)))`, where
goopg printed `(((amc)::numeric(15,4) / (pmc)::numeric(15,4)))`.

- `chaseJoinKeyExprColumns`: when a non-column Sort key's child is a Join
  or NestedLoopIndexJoin, each column reference is chased through
  `resolveKeySource`. One that lands on a non-column expression prints in
  parentheses via `displayColumn`.
- `resolveKeySource`'s Aggregate arm declined every non-simple aggregate
  (slice 84, against Q59's Finalize group key read through the Partial's
  transport layout). A Finalize aggregate's aggregate-result position is
  its final call with no descent, so it now returns that call. Group
  positions still decline.

Test: `TestSortKeyExprOverJoinChasesAggregates` (PG 18.3's line for the
serial and the parallel plan). Disabling the Finalize branch fails the
parallel case; disabling the chase fails both.

Movement:

- Q90 becomes text-identical at both scales: 20 → 21 (SF0.25) and
  12 → 13 (SF1).
- Rendering including MATCH goes 15 → 14 and 16 → 15. The fire set
  touched Q90 only.
- Regress A/B over 9 cases is flat.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice103/`.

## Slice 104: M0146-0005cy — a Memoize node prints its Cache Mode

show_memoize_info prints `Cache Mode: logical|binary` on the line after
`Cache Key:`. paraminfo_get_equal_hashops sets binary mode only for a join
operator without a hash equality, or for lateral Vars. goopg printed the
key and no mode, so every TPC-DS plan with a Memoize lost one line against
PG. The text-identity census put that single missing line at the end of
seven otherwise identical MATCH plans.

- The Memoize arm prints `Cache Mode: logical` after the Cache Key. goopg
  builds a Memoize only over an equality index probe (memoizeNodeFor /
  maybeAttachMemoize), which is hashable and has no lateral Vars, so
  logical is PG's answer for every node goopg produces. Every Cache Mode PG
  prints over TPC-DS is `logical` too.

Test: `memoize_exec_test.go` now requires `Cache Mode: logical` right after
the Cache Key line, and fails with the line removed.

Movement:

- Text-identical plans go 21 → 24 at SF0.25 (Q13, Q48, Q55). Aligned
  identical lines go 1987 → 2211 (SF0.25) and 1957 → 2013 (SF1), since the
  missing line had shifted every line after it.
- Regress memoize.sql has no goopg Memoize nodes for these queries; its
  Cache Mode lines are PG-only either way.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Evidence: `analysis/m0146/m0146-0005/slice104/`.
