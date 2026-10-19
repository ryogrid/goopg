# M0146-0227 — TPC-DS SF0.25 Q69: semijoin loop count over a multi-relation RHS

Status: slice 1 landed 2026-10-19; the task stays open for the anti-join order (§4). Filed by the S9 pass at
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

## 4. Slice 2 diagnosis: join_collapse_limit over the pulled sublinks (in progress)

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

Attempted fix: fold each top-level pulled body with `combineJoinlists(jointype, false, joinlist, bodyLeaves,
join_collapse_limit)`. The patch is saved at `tmp/m0227/slice2-collapse-fold-wip.patch`; it is not committed.

- It splits Q69 exactly as PG does.
- It panics in createPlan: "search root does not publish binding coordinate(s) [153 160 187 193 195]".
- A sub-problem leaf that holds pulled (non-emitting) semi/anti leaves does not publish the binding coordinates the
  enclosing tree reads.
- Next: make the sub-problem leaf publication account for pulled leaves (createplanroot.go boundaryMap, the
  sub-problem leaf's output layout), then re-apply the fold.
