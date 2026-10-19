# M0146-0227 — TPC-DS SF0.25 Q69: semijoin loop count over a multi-relation RHS

Status: landed 2026-10-19 (slice 1, the semijoin loop count; slice 2, join_collapse_limit over pulled sublinks). Filed by the S9 pass at
18d2c73b0 (`analysis/m0146/m0146-0014/routing-20261019c/`). Evidence: `analysis/m0146/m0146-0227/`.

## 1. Record

TPC-DS SF0.25 Q69, join-method, SHAPE-DIFF at depth 3 (under Sort: PG Hash Right Anti Join | goopg Nested Loop Anti
Join). It persists under PG statistics.

- PG (43130.36) anti-joins `catalog_sales` at the top with a Hash Right Anti Join. The `web_sales` NOT EXISTS is a
  nested Hash Right Anti Join below it.
- goopg (43183.05 before this slice) puts a Nested Loop Anti Join with a Join Filter on `web_sales` at the top.

## 2. Slice 1: the EXISTS leg's customer probe

On an SF0.25 clone with PG statistics, the EXISTS leg alone reproduces a gap
(`exists-leg-probe.sql`):

- PG: `Index Scan using customer_pkey on customer c (cost=0.29..0.63)`; Nested Loop 19132.67..19681.46.
- goopg: the same probe at 0.29..0.33; Nested Loop 19132.67..19430.53.

The statistics are identical on both engines: customer 2872 pages, 100000 tuples, 2853 all-visible; customer_pkey
276 pages; correlation 0.99975. The difference is `cost_index`'s `loop_count`.

- `get_loop_count` (indxpath.c) takes the probe's required-outer rel, store_sales, and passes its rows through
  `adjust_rowcount_for_semijoins`. store_sales sits on the RHS of the EXISTS semijoin, whose LHS holds customer.
  So the loop count is `estimate_num_groups(semi_rhs_exprs, nraw)` with `nraw = approximate_joinrel_size(RHS)`.
- `approximate_joinrel_size` is the plain PRODUCT of the RHS rels' rows, with no join selectivity. That makes
  `nraw` large, and the groups are ss_customer_sk's 39023 distinct values. Solving PG's cost_index for 0.63 gives a
  loop count of about 39000.
- goopg's `adjustRowcountForSemijoins` sized only a one-relation RHS. Q69's RHS is `store_sales ⋈ date_dim`, so the
  count stayed at store_sales' 719876 rows, and the per-probe heap and index IO amortised to half PG's.

The change, `multiRelSemiRhsUniqueRows` (pathparamindex.go):

- nraw is the product of the RHS level-1 rels' rows.
- Each uniq expr (a problem-space ColumnRef) is attributed to the RHS relation whose coordinate range holds it and
  sized there by `estimateNumGroups`. The per-relation counts multiply and are clamped to nraw, as
  `estimate_num_groups` does across relations.
- An expr it cannot attribute leaves the count unadjusted, as before.

The probe now costs 0.29..0.63, PG's number. The Nested Loop is 19692.38 against PG's 19681.46.

Pin: `TestLoopCountClampsMultiRelSemijoinRHS`, mutation-checked.

## 3. Measured (slice 1)

Census `analysis/m0146/m0146-0227/census/` (m27):

- No verdict moves: SF0.25 92/99, SF1 81/99, TPC-H 22/22.
- Plan text changes in Q10, Q35 and Q69 at SF0.25 and in Q10 and Q35 at SF1, all cost-only. The semijoin-driven
  probes in those queries now carry PG's loop count.
- Fire set: 5 fired results, all byte-identical.

## 4. Slice 2: join_collapse_limit over the pulled sublinks

With the EXISTS leg at PG's cost, goopg's Q69 costs 43444.91 against PG's 43130.36 and keeps a Nested Loop Anti Join
on web_sales at the top.

goopg's side, from the DPTRACE/DPPATH trace on the SF0.25 PG-statistics clone (`q69-trace-plan.txt`):

- PG's top IS generated: the `right_anti` hash join with catalog_sales probing and the other seven rels hashed costs
  32211.13..43141.28, against PG's 32200.20..43130.36.
- It is dominated. goopg's Nested Loop Anti Join with web_sales inner (26056.65..43444.91) is fuzzily equal on total
  (within STD_FUZZ_FACTOR 1.01) and better on startup.
- That NL anti's outer is the rel {c, ca, cd, EXISTS, catalog_sales anti}, built at 22880.00..34248.88.

PG's side, from the exact replica (`tmp/m21-refclone` under the tmp/pgdbg debug build, with an add_path probe;
`q69-pgprobe7.txt`):

- PG never builds that rel. Across all seven-member join rels, it builds only (c, ca, cd, EXISTS, web_sales anti).
- At the top it offers only catalog_sales-last splits. Its NL anti there costs 43738.81, more than 1% above the hash
  join, so the hash join wins.

The cause is `join_collapse_limit`, not join legality:

- `pull_up_sublinks` nests each converted sublink's JoinExpr over the previous one (`j->larg = *jtlink`).
- `deconstruct_recurse` folds each (larg, rarg) pair under `join_collapse_limit` (initsplan.c). EXISTS gives 3 + 2 =
  5, then NOT EXISTS(web_sales) gives 5 + 2 = 7, and NOT EXISTS(catalog_sales) gives 7 + 2 = 9 > 8.
- So PG plans the first seven relations as a sub-problem and joins catalog_sales last.
- goopg appends every pulled body's leaves to one flat joinlist (`jointreepullup.go`, the `ctx.joinlist` append) and
  searches a nine-relation problem.

The fix has three parts.

- **Fold.** `foldPulledBodyLeaves` (jointreepullup.go) appends each top-level pulled body's leaves through
  `combineJoinlists(jointype, false, joinlist, bodyLeaves, join_collapse_limit)`, in conjunct order.
  `deconstruct_recurse` does the same over the nested JoinExprs. Nested pull-ups (a body's own sublinks) keep the
  flat append.
- **Publication.** Item 1 (catalog_sales + date_dim) is wholly the RHS of the anti join, but its inner quals read
  five of its coordinates, so the root hole-filler refused them. The fold had panicked with "search root does not
  publish binding coordinate(s) [153 160 187 193 195]". `searchOneProblem` (relfromjoinlist.go) now treats an item
  lying wholly inside a SEMI/ANTI join's syntactic RHS like c11's synthetic semi/anti leaf: the join never
  projects RHS columns, so nothing above reads them.
- **Join-var statistics.** Once catalog_sales sat inside a sub-problem leaf, the top anti join's clause
  `c_customer_sk = cs_ship_customer_sk` lost the column's statistics: the leaf is table-less. Its rows halved to 46
  against PG's 91. PG's `examine_variable` reaches the base rel through `find_base_rel(varno)` whatever joinrel the
  Var is read from. A sub-problem leaf now carries `subproblemBaseVars` (table, tuples, rows per base relation,
  nested entries merged, beside `subproblemBaseRows`), and `examineJoinVar` resolves such a Var to its base
  relation.

On the SF0.25 PG-statistics clone, Q69 now prints PG's tree (`q69-pgstats-clone.txt`). The top Hash Right Anti Join
costs 32212.04..43146.66 against PG's 32200.20..43130.36, both at 91 rows, and the differ reports MATCH.

Pins: `TestPulledSublinksFoldUnderJoinCollapseLimit` and `TestSubproblemLeafVarKeepsBaseRelStats`, both
mutation-checked. The publication change is exercised end to end: without it Q69 panics in createPlan.

## 5. Measured (slice 2)

Census `analysis/m0146/m0146-0227/census2/` (m27b):

- Shipped SF1 PLAN-PARITY 81→82 (Q69 MATCH); CATEGORIES-EXCL-MATCH qual-placement 4→3, join-order 15→14.
- PG-statistics SF0.25 96→97 (Q69 MATCH).
- Shipped SF0.25 stays at 92/99. Q69's first divergence moves from depth 3 to depth 4 (PG Nested Loop Inner |
  goopg Nested Loop Left Anti). It matches under PG statistics, so the residual is statistics-driven and routes to
  the owner's stats import (M0146-0215).
- TPC-H 22/22.
- Q69 is the only plan whose text changed, at both scales. Fire set: both fired results byte-identical.
