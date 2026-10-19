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

## 4. Open: the anti-join order (slice 2)

With the EXISTS leg at PG's cost, goopg's Q69 plan costs 43444.91, 314 above PG's 43130.36, and keeps the Nested Loop
Anti Join on web_sales at the top. Either PG's order is not generated or goopg prices it higher:
`catalog_sales` Hash Right Anti Join over `(customer ⋈ ca ⋈ cd ⋈ EXISTS) ⟕anti web_sales`, with the web_sales anti
join itself a Hash Right Anti Join. Next step: instrument addPath on the top joinrel on the PG-statistics clone.
