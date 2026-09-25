# M0146-0005 slice 5 (not landed): stats-less hash keys get PG's 0.1 bucket

Witness: TPC-DS Q79, first divergence `join-method` at depth 2 at both
scales. The top join is the aggregated subquery `ms` (1131 rows) against
`customer`: PG runs a nested loop into `customer_pkey` (24560.8), goopg a hash
join (22509).

## PG's candidates (`pg-q79-top-join-plancand.txt`)

Taken from the private instrumented PG 18.3 (`debug_plan_candidates`,
database `tpcds025`):
- nested loop, `ms` outer: 19147.5..24560.8 (the winner);
- hash join, `ms` outer, `customer` hashed: 24269.2..24451.2. It is fuzzily
  equal on total (within 1%) and much worse on startup, so it is rejected
  with `via=cost cmp=cost:B2`;
- hash join, `customer` outer, `ms` hashed (goopg's orientation):
  19340.4..**37598.7**.

The about 15000 goopg does not charge is the bucket walk. `ms.ss_customer_sk`
is a GROUP BY output with no statistics, so `get_variable_numdistinct` is
isdefault, and `estimate_hash_bucket_stats` punts to
`Max(0.1, mcv_freq)` = 0.1 (ten entries per bucket). That gives
100000 × 113 × 0.0025 × 0.5 ≈ 14100.

goopg's `estimateHashBucketSize` has that isdefault arm, but a key with no
statistics at all never reaches it: `if v.stats == nil && !v.isBool {
continue }` makes it report 0, "no information", so the bucket-walk term is
skipped.

## The patch (`default-hash-bucket.wip.patch`) and why it is not landed

The patch drops the `continue` (a nil search context still reports 0) and
gives two unit-test fixtures ANALYZE-like key statistics.

- Q79: goopg now builds on `customer` (23499). It is still a hash join; the
  remaining gap is PG's fuzzy startup comparison against the nested loop.
- **Q47 times out at 310 s** (`sf025-with-patch.txt`,
  `q47-with-patch.plan.txt`). The CTE self-join (v1, v1_lag, v1_lead) now
  elects a Nested Loop over `CTE Scan v1_lead` (3823 rows) with an inner
  Merge Join estimated at 1 row, costed as cached (856 total). PG plans merge
  joins over `Materialize`d CTE scans.

So the PG-faithful bucket unmasks a nested-loop election over CTE scans that
is not PG's. The nested loop's cost for an unparameterised inner
(`nestLoopInnerRescanCost`: build plus cpu_operator_cost per row per rescan)
assumes goopg's executor cache. This is filed as M0146-0005a and must land
first.
