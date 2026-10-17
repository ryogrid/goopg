# M0146-0014 routing 2026-10-10 — join-order records (Q64 SF0.25, Q64 SF1, Q18 SF1)

This is a read-only analysis of the EXPLAIN captures in this directory. No
servers were started. Costs are quoted from `goopg-*.plans.txt` and
`pg-*.plans.txt`.

## 1. Q64 SF0.25 — depth=5 [qual-placement] under Nested Loop

**The divergence.** The heads are identical in both engines: cs_ui
GroupAggregate (7 rows), then store_returns, then item, then store_sales.
After the item probe, every join is estimated at `rows=1`. What follows is a
tail of 14 one-row index probes: ad1, cd1, cd2, store, d1, d2, d3, customer,
hd1, hd2, ib1, ib2, promotion and ad2. The two engines order this tail
differently:

- PG: ad1, cd1, store, d1, customer, d3, d2, hd1, hd2, ib2, ib1, promotion,
  cd2, ad2.
- goopg: ad1, promotion, customer, d2, d3, cd2, hd2, ib2, cd1, store, d1, hd1,
  ib1, ad2.

The `[qual-placement]` label is only a side effect of this order. The Join
Filter `cd1.cd_marital_status <> cd2.cd_marital_status` sits on whichever
join brings in the later of cd1 and cd2. That is depth 5 in PG and depth 8 in
goopg.

**The tail is an exact cost tie.** All rows are clamped to 1, so a nested
loop's cost is the sum of its inner probe costs, and any legal order costs the
same.

- PG: the tail rises from 13868.55 to 13874.00, which is 5.45. The sum of
  PG's 14 inner totals is also 5.45: 0.31 + 0.60 + 0.15 + 0.32 + 0.33 + 0.38
  + 0.38 + 0.30 + 0.30 + 0.16 + 0.16 + 0.17 + 1.53 + 0.36.
- goopg: the tail rises from 13833.20 to 13839.47, which is 6.27. The sum of
  goopg's inner totals is 6.25 (within rounding).
- Pricing goopg's order with PG's per-probe costs gives the same 5.45. The
  difference is 0.00%.

**What breaks the tie is GEQO, not add_path.** Q64's `cross_sales` has 18
FROM items, which is at least `geqo_threshold` (12). Both engines therefore
run the genetic optimizer: PG through `geqo_main.c`, goopg through
`internal/optimizer/geqo.go` `geqoSearch`, routed at `relfromjoinlist.go:726`.

- When tours tie on fitness, the tour that survives depends on the random
  stream and on how the pool sort orders ties.
- **PG's random stream:** `geqo_random.c` `geqo_set_seed` calls
  `pg_prng_fseed(&random_state, geqo_seed)`, and `geqo_rand` / `geqo_randint`
  use `pg_prng_double` / `pg_prng_uint64_range` (xoroshiro128**).
- **goopg's random stream:** `geqoRNG.rand` (`geqo.go:716`) is a 64-bit LCG
  (`state*6364136223846793005 + 1442695040888963407`), seeded `state=1`. Its
  sequence is entirely different from PG's.
- **Pool sort:** PG's `sort_pool` calls `qsort` (`geqo_pool.c:137`), which is
  unstable on equal worth. goopg's `sortPool` (`geqo.go:236`) is a stable
  insertion sort.

**Evidence that the tail position is arbitrary in PG itself.** PG's own tail
order changes between SF0.25 and SF1 (see record 2). goopg's does too. The
small cost changes between scale factors are enough to send the genetic
search to a different tied tour.

**What parity would need.** A pg_prng port would be necessary but not
sufficient. Every tour's fitness would also have to equal PG's closely enough
that the pool ranks the same. Given B-15/B8 on every probe, that is out of
reach. The plan's cost is unaffected (0.00%).

**Trace that would confirm it.** On a private PG clone, run Q64 with
`SET geqo_seed = 0.5`. The tail order should change while the total cost
stays at about 13874.

ROUTE: NEW (GEQO-RNG) — the 14 one-row probes cost the same in any order (an exact 0.00% tie, 5.45 = 5.45), so PG's `geqo_main` tour decides the order; goopg's GEQO uses an LCG rather than PG's `pg_prng` (xoroshiro128**) and a stable pool sort, so it cannot reproduce PG's choice. Waive like COSTTIE.

## 2. Q64 SF1 — depth=6 [join-order] under Nested Loop

**The first divergence is the same mechanism as record 1.** Above the
store_sales join everything is `rows=1`.

- PG's tail: hd1, ad1, d1, cd1, store, customer, d3, hd2, d2, ib2, cd2, promo,
  ad2, ib1. It rises from 49253.73 to 49259.04, which is 5.31 and equals the
  sum of its probes.
- goopg's tail: d1, store, ad1, promo, customer, d2, cd2, d3, cd1, ad2, hd2,
  ib2, hd1, ib1. It rises from 53264.70 to 53270.44, which is 5.74 and also
  equals its probe sum.
- PG's tail here also differs from PG's SF0.25 tail. The order is decided by
  GEQO's random search, not by cost.

**A second, deeper divergence: the head.** This one is not the first
divergence, but it is a real cost difference.

- **PG** probes item first, through Memoize. The cost is 34 × 7.39 = 251:
  `Memoize (cost=0.30..7.39)` over `Index Scan using item_pkey (cost=0.29..7.38)`.
  That leaves 1 row. Then store_returns at 5.10 (`Index Only Scan
  store_returns_pkey (cost=0.42..5.10 rows=18)`) and store_sales at 1.32.
  The head costs 278.8 above the aggregate (48974.91 to 49253.73).
- **goopg** probes store_returns first. That is 37 × 8.64 = 327: `Index Only
  Scan store_returns_pkey (cost=0.38..8.64 rows=18)`. It yields 652 rows,
  then item costs 652 × 0.30 = 206, then store_sales costs 2.27. The head
  costs 536.1 (52728.58 to 53264.70).
- goopg's multi-page probes are about 1.7x PG's: store_returns 8.64 vs 5.10,
  store_sales 2.27 vs 1.32, cd2 2.22 vs 1.53. That is the B8 multiplier on the
  I/O part.
- At about 2x PG's 7.38, a loop_count-37 item probe would cost about 14.4.
  goopg's item-first head would then cost about 37 × 14.4 + 8.64 + 18 × 2.27,
  roughly 580, which is more than 536. So goopg keeps store_returns first.
- At PG's price, item-first costs about 279. That is far cheaper than goopg's
  current head.
- Deciding trace: instrument `addPath` for the item_pkey path parameterized by
  cs_ui (loop_count 37) and confirm its total is about 14.

ROUTE: NEW (GEQO-RNG) — the first divergence is the same exact tie among one-row probes as SF0.25 (PG 5.31 vs goopg 5.74, both equal to their probe sums, and PG's own order differs from its SF0.25 one), resolved by GEQO's random stream; separately, the deeper head difference (item/Memoize vs store_returns first, 279 vs 536) is B8.

## 3. Q18 SF1 — depth=8 [join-order] under Nested Loop

Q18 has 7 relations, so this is ordinary dynamic-programming search, not
GEQO. Both engines put the Parallel Hash Join of catalog_sales and date_dim
at the bottom. They differ in how they order the three filtered probes:

- PG: cd1, then customer, then customer_address (ca). The added cost is
  1183.9, from 43962.24 to 45146.16.
- goopg: customer, then ca, then cd1. The added cost is 987.1, from 47678.74
  to 48665.81.
- At SF0.25 both engines pick customer, ca, cd1, and the record matches.

**Per-probe costs (per-loop totals derived from the nested-loop deltas):**

| probe | goopg (startup..total) | per-loop | PG (startup..total) | per-loop |
|---|---|---|---|---|
| customer_pkey | 0.25..0.29 | 0.285 | 0.29..0.33 | 0.326 |
| customer_address_pkey | 0.25..0.45 | 0.449 | 0.29..0.42 | 0.422 |
| customer_demographics_pkey cd1 | 0.38..0.52 | 0.522 | 0.43..0.53 | 0.525 |

Selectivities are nearly the same in both engines: customer about 0.48, ca
0.197, cd1 0.070 to 0.076.

**Both orders priced per outer row, using each engine's own costs:**

- **goopg:** customer-first is 0.5524 × 1787 = 987.2, matching the plan.
  cd1-first is 0.5599 × 1787 = 1000.5. goopg's margin is 13.4, which is 0.03%
  of 48683.
- **PG:** cd1-first is 0.5625 × 2105 = 1184.0, matching the plan.
  customer-first is 0.5783 × 2105 = 1217.4. PG's margin is 33.4, which is
  0.07%.

The margin is near-tie sized. Two identified terms, each enough on its own,
move it toward PG's order:

1. **B-15, the missing descent term.** goopg's startup costs are 0.25, 0.25
   and 0.38. PG's are 0.29, 0.29 and 0.43. The gap is exactly
   `ceil(log2(N)) * cpu_operator_cost` from btcostestimate
   (`selfuncs.c:7764`):
   - customer: 100000 rows gives 17 × 0.0025 = 0.0425.
   - ca: 50000 rows gives 0.04.
   - cd: 1.92M rows gives 21 × 0.0025 = 0.0525.

   The customer-first order makes 1.58 probes per outer row, against 1.11 for
   cd1-first. Adding the term shifts 17.3 toward cd1-first, which beats the
   13.4 margin. goopg would then choose cd1-first by about 4.
2. **B8, the per-loop I/O charged at 2x.** The run portion (total minus
   startup) is:
   - customer: goopg 0.035, PG 0.036 (I/O is negligible, so no change).
   - ca: goopg 0.199, PG 0.132.
   - cd1: goopg 0.142, PG 0.095.

   Both fit "PG cpu part + 2 × PG I/O part": cd1 0.048 + 2 × 0.047 = 0.142.
   For ca, 0.04 + 2 × 0.092 × (955/1136 pages) ≈ 0.195, so RELPAGES slightly
   offsets the multiplier there. Removing the excess shifts 22.3 toward
   cd1-first, which also flips the choice.

With both corrected (PG's cost vector), cd1-first wins by about 26, close to
PG's own 33. The rest is ordinary stats drift: the hash join estimates 1787
rows against PG's 2105, and date_dim's `d_year = 2001` estimate is 214 against
252.

The earlier note, "the cd1/customer probe order", is correct. The cause is
these two per-probe terms, not a new mechanism.

ROUTE: B8 (+B-15) — a near-tie (goopg's margin is 13.4 = 0.03%); goopg's 2x per-loop I/O on the ca/cd1 probes (0.199/0.142 vs PG 0.132/0.095, a 22-unit shift) and its missing log2(N) descent term (a 17-unit shift) each alone flip it to PG's cd1-first order.
