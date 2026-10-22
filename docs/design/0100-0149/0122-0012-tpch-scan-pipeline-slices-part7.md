# M0122-0012 — scan pipeline slices, part 7 (TPC-DS executor gaps)

Continues [0122-0012-tpch-scan-pipeline-slices-part6.md](0122-0012-tpch-scan-pipeline-slices-part6.md) (slices
58–68 and M0122-0016). It was split out on 2026-10-23 under the 800-line design-doc limit; slice numbering is
unchanged.

**How this part measures.**

- **The bench clone holds SF0.25.** `tmp/q64-clone` (port 5592, 2 GB shared\_buffers) is the **SF0.25** corpus, not
  SF1: Q75's output on it is byte-identical to the `:65438` reference's `tpcds025` database and differs from
  `tpcds`. Earlier parts' "PG warm time" comparisons should be read against `tpcds025`.
- **Instruction counts are the A/B of record.** Per-query `instructions:u` (`tmp/ds-instr.sh`) is the measure. A
  fresh server spends its first ~7 heavy runs growing its heap toward the memory limit, page-faulting fresh pages
  (part 6, "M0122-0016"). Concurrent sessions on this host also run CPU-heavy fuzz tests, which swing user CPU per
  query by ±50 %. Wall and CPU numbers are reported only alongside a warm, quiet A/B.
- **Warm timings.** `tmp/ds-warm.sh <bin> <warm-q> <warm-runs> <q...>` warms one server past its heap plateau, then
  times each query (3 runs, wall plus user and sys CPU) on that same server.

## Slice 69 (2026-10-23): a hashed DISTINCT feeding an order-insensitive hashed aggregate stops sorting its output — TPC-DS Q75

**Target.** Ranked on warm timings, Q75 (0.56 s against PG's 0.24 s) is the largest untouched gap. Its CTE runs
`HashAggregate (sum) → HashAggregate (the UNION's de-duplication) → Gather → Parallel Append` over three sales
and returns join branches, about 485 K rows. A leader-split instruction profile put 90 % of the leader in
`distinctOp.Open`. A third of it was a `sort.Slice` over every de-duplicated row (`pdqsort`, `compareDatumPlain`).
`distinctOp`, the executor of the planner's hashed `Distinct` (EXPLAIN `HashAggregate`), de-duplicates through a
hash map and then sorts its output "for deterministic output". PG's hashed DISTINCT emits hash-table order and
never sorts. The sort makes an unordered DISTINCT's own output reproducible, but an aggregate whose result
cannot depend on its input order can never observe it.

**Change.**

- **`distinctOp.unordered`** skips the output sort. The rows come out in first-seen order, with the same rows
  and keys as before.
- **`markDistinctUnordered`**, called from `buildNode`'s Aggregate arm, sets it on a `distinctOp` that feeds the
  aggregate directly or through projections (and the EXPLAIN-ANALYZE instrumentation wrapper). It only does so
  when `aggregateIgnoresInputOrder` holds:
  - the aggregate is hashed (or ungrouped), not a Finalize, and has no grouping sets;
  - every aggregate is `count`, `min` or `max`, or `sum`/`avg` over a non-float input;
  - no aggregate has an ORDER BY or WITHIN GROUP.
  
  Float sums and averages round differently in a different order, and `array_agg`, `string_agg` and
  user-defined aggregates keep their input order, so any of those keeps the sort. A top-level DISTINCT, or any
  other consumer, sorts as before.

**Pins.**

- `TestAggregateIgnoresInputOrder` covers the allow list and every exclusion: float sum and avg, collecting and
  user aggregates, an ordered aggregate, the sorted strategy, Finalize, and grouping sets. Admitting floats
  fails it.
- `TestDistinctUnderOrderFreeAggregateSkipsSort`: a DISTINCT under `sum(int4)` through a Project is marked; one
  under `array_agg`, and a top-level one, are not. Stopping the walk at the Project fails it.
- `TestDistinctUnorderedEmitsFirstSeenOrder`: the sorted mode still emits ascending order with NULLs last, and
  the unordered mode emits first-seen order with the same rows. Ignoring the flag fails it.
- Q75's output is byte-identical before and after, and identical to the `:65438` reference's `tpcds025` output.

**Measured** against `32a6ac752`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q75 | 14.45–14.49 | 12.29–12.39 (−14.3 % to −15.2 %) |
| Q87, Q38, Q16, Q94 | — | within ±0.4 % |

Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH; RegressSuite PASS), and plan shapes are
identical.

**Also in this commit.** `internal/utils/mmgr/mctx.go`'s `permanentContext` comment no longer says big numerics
allocate there (M0122-0016's leftover). `mmgr.Perm()` now has no production caller in the executor.

**Left.** goopg's other hashed DISTINCT consumers (a hash join's build side, a hashed SetOp, a semi-join's
unique-ified inner) are also order-insensitive but keep the sort. Each needs its own proof that nothing above it
depends on the order, as the aggregate's does here.
