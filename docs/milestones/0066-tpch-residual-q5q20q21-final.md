# Milestone 0066 — TPC-H Runtime Optimization (Pivoted from Q5/Q20/Q21)

## Goal

Reduce GC and per-row allocation overhead in the executor so
Q5, Q20, Q21 (and the broader 22-query suite) complete within
the 600 s budget. Pivoted from the original "fix Q5 / Q20 /
Q21 in the planner" approach after empirical findings showed:

1. **Q9's "7 rows" was silent false negatives** — Name-rebinding
   the chained-NLI keys (the planner-side fix) produces correct
   probes but exposes a pre-existing cardinality explosion that
   needs composite-NLI on `partsupp_pk` to absorb.
2. **Q5 is GC-dominated.** pprof CPU at SF=1 shows
   `runtime.gcBgMarkWorker` 64.85 % of total samples, with only
   ~30 % in application code. Planner-side build-time
   predicate pushdown to shrink the 1.5 M-row orders hash
   helps marginally but doesn't address GC.
3. **Q20's bottleneck is the single-shot non-correlated IN
   inner-plan execution** — 60 s+ scanning 6 M lineitems with
   GROUP BY aggregation. Predicate pushdown stalls on missing
   timestamp btree.
4. **Q21's Anti-side stays on hash join** even with the NLI
   walker fix because `Filter`-wrapped lineitem disqualifies
   the NLI rule.

These all benefit from reducing the per-row allocation cost,
which is the largest CPU user across the board.

## Sub-tasks

- **M0066-0001 GOGC tuning.** Bump `GOGC` to 400 in
  `bench/tpch/env_goopg.sh`. Single-line change. Expected: GC
  cycles drop ~4× under heavy MHJ build/probe loads; CPU
  shifts from `runtime.gcBgMarkWorker` to application code.
- **M0066-0002 Row buffer pool.** Add `sync.Pool` for
  `[]Datum` of common widths. Use in `multi_hash_join.go`'s
  `lazyOut` allocation and per-step buffers. Mirror M0059's
  BorrowRow contract for the new pool entries.
- **M0066-0003 String interning for repeating column values.**
  For `char` / `varchar` columns with low cardinality
  (n_name 25 distinct values, s_name patterns, brand strings,
  comment categories), intern the strings during scan/build
  to share underlying bytes. Reduces GC-scanned pointer
  density.
- **M0066-0004 Final 22-query SF=1 sweep + report.**
  Capture pprof CPU + heap before/after each phase. Document
  per-query wall-clock delta vs. M0065 baseline.

## Acceptance

- **Soft**: Q5 completes in < 600 s; Q21 / Q20 either complete
  or visibly closer to the budget. 22-query OK count ≥ 19.
- **Hard**: No regression on previously-OK queries
  (row-count parity); GC CPU share drops below 40 %.

## What this milestone does NOT do

- Planner-side fixes for Q21 / Q20 / Q9 — carried to M0067
  with the diagnostic findings from M0064/M0065/M0066's
  abortive planner attempts.

## Why pivot

The four queries cancelling at 600 s share a root cause: the
executor allocates too much per row, and at SF=1 the GC mark
phase consumes ~65 % of CPU on the long-running queries. Even
a perfect planner cannot escape this. Cutting GC overhead
delivers wins across the suite, not just on the named queries.
