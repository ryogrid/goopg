# M0146-0005 slice 6 (M0146-0005e): PG's hash-join tuple counts

Witness: TPC-DS SF0.25 Q31, first divergence `join-method` at depth 3.
Inside the `ws` CTE (`q31-ws-cte.sql`), PG hash-joins `web_sales` to
`date_dim` (3067.6..10083.57). goopg ran a Memoize nested loop into
`date_dim_pkey` (11597) because its own hash join cost 11993
(`goopg-q31-ws-dppath-before.txt`).

## What PG charges (`pg-q31-ws-hash-plancand.txt`)

The trace comes from the private instrumented PG 18.3 (`debug_plan_candidates`,
`HJCOST` in `final_cost_hashjoin`). `date_dim` is unique on `d_date_sk`, so
`extra->inner_unique` holds and PG takes the semi/anti branch. The trace
shows `hashjointuples=2` for 179956 outer rows:
- `compute_semi_anti_join_factors` computes `jselec` with `JOIN_SEMI` but
  with the INNER SpecialJoinInfo that `make_join_rel` builds for a plain
  inner join. `eqjoinsel` switches on `sjinfo->jointype`, so `jselec` is the
  inner-join selectivity 1/73049. `outer_match_frac` is therefore 1/73049.
  `match_count` = nselec × inner rows / jselec = 73049.
- `outer_matched_rows = rint(179956 / 73049) = 2`. The matched walk and
  `cpu_tuple_cost × hashjointuples` are about 0. The other 179954 rows pay
  the unmatched virtual-bucket walk (22.5).

So 10083.57 = 3067.60 + 6543.56 + 449.89 + 22.50 + 0.02.
`TestHashJoinInnerUniqueReproducesPGQ31WebSalesDateDim` reproduces it
exactly.

goopg used `joinrel.Rows / outer.Rows` (≈ 1) as the fraction, a match count
of 1, and `cpu_tuple_cost × join rows` (1799). This is take2's B5 chain:
R98 ruled PG's inputs "unobservable" because a plain EXPLAIN does not show
them. The instrumented PG does.

## The non-unique arm (`tpch-census-diff-approx-missing.txt`)

With only the inner-unique fix, TPC-H Q10 lost its match (5 to 4, see
`tpch-plan-parity.txt`). Customer became a unique hashed inner, while PG
keeps customer as the probe side over a Parallel Hash of lineitem ⋈ orders.

PG's non-unique arm charges `approx_tuple_count(hashclauses)` = selectivity
× outer path rows × inner path rows. For a Parallel Hash both path rows are
per worker: 62500 × 14687 / 150000 = 6120 tuples. goopg charged the
joinrel's per-worker rows (24479). Porting that term restores Q10; the
TPC-H census is then identical to before.

## Results

- `census-diff-sf025.txt` / `census-diff-sf1.txt`: **Q96 matches PG at both
  scales.** Q31 now diverges at depth 1 and Q48 at depth 2. Nothing got
  shallower.
- `sf025-sweep.txt`: 96/96 PASS.
- `tpcds-fireset.txt`: SF1 Q74 timed out on the candidate at 600 s. The
  plan is identical to the baseline modulo cost, and fresh-server timings
  are 894 s (baseline) and 863 s (candidate) (`q74-sf1-timing.txt`). The
  query straddles the limit on this host. Re-executed on both arms at 1200 s
  through `FIRESET_RESUME=1`: PASS/PASS.
- TPC-H acceptance arm: 24 MATCH on values. Spotcheck: PASS.

## Not ported (ledgered)

- The LEFT join with a unique inner takes the same PG branch (over its
  non-pushed-down quals); goopg keeps the non-unique arm there.
- Nested loops with a unique inner (M0145-0008l's ledger row). The same
  factor semantics apply: for an inner join, `outer_match_frac` is the
  inner-join selectivity.
