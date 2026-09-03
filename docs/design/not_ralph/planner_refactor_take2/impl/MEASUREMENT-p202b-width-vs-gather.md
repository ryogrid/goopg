# P2-02b's cost, split between width and the lost Gather

**Date:** 2026-09-03. **Why:** P4-A §12 names two independent causes for
P2-02b's +23.1 % — tuple WIDTH (P4-01) and the lost Gather (Phase 5) — and
sequences P4-01 first without quantifying either. This measures the split, so
the sequencing rests on a number rather than on an assertion.

## Method

All four readings are ISOLATED single-query runs of TPC-H Q9 (`tpch-runner
-queries 9`), one per fresh server, so server age and GC state are held constant
— the confound recorded in CLAUDE.md's benchmark-timing hygiene and the reason
the full-sweep readings elsewhere in this bundle are not comparable to these.
`-parallel-workers` sets `max_parallel_workers_per_gather` on the query's own
session.

The two binaries differ only in the `work_mem` BootVal / `DefaultMemLimitBytes`
pair that P2-02b changes.

## Readings

| `work_mem` default | parallelism | Q9 |
|---|---|---:|
| 512MB (today) | as planned — parallel, 4 workers | **17.54 s** |
| 512MB | forced serial (`-parallel-workers 0`) | 22.50 s |
| 4MB (PG's, P2-02b) | 4 workers allowed | **51.50 s** |
| 4MB | as planned — serial | 55.42 s |

## Split

Comparing like for like, with parallelism AVAILABLE on both sides
(17.54 → 51.50):

- **width / batching: ~33.96 s, about 87 % of the gap.**
- **lost Gather: ~4–5 s, about 13 %** (17.54 → 22.50 at 512MB; 51.50 → 55.42 at
  4MB — the Gather is worth roughly the same absolute amount at either budget).

## The finding

**Width dominates by roughly 7:1, so P4-A's sequencing is correct and is now
measured.**

A second, less obvious result: at 4MB, *allowing* four workers recovers only
3.92 s (55.42 → 51.50). Parallelism cannot be restored by permitting it, because
at the smaller budget the plan moves onto index-scan-driven joins and goopg's
parallel post-pass only drives sequential scans. So Phase 5's Gather work does
NOT become available by fixing eligibility alone — it is downstream of the plan
shape, which is downstream of width. That reinforces the same ordering from the
other direction.

## What this does NOT establish

It does not show that fixing width recovers 33.96 s. It shows that 33.96 s is
attributable to the budget/width axis rather than to parallelism. A narrower
tuple should reduce the batching that drives it, but the amount recoverable is
only measurable once P4-01 exists — and P4-01b's revert is the standing warning
that a width change can be faster and wrong at the same time.
