# TPC-DS warm-start query timings (half-scale-era archive)

Captured beside the plan fixtures in `../plans-pg/`, so a plan and the runtime
it produced can be read as a matched pair.

> Archive status: these captures predate the quarter-scale migration
> (2026-09-11) and were taken on the half-scale gate dataset. The numbers
> below are not comparable to the current gate; they are kept because the two
> caveats (concurrent-capture inflation, the 1.1×-not-1.9× Q4 analysis) and
> the shared_buffers history are still the reference for reading any TPC-DS
> timing of that era.

## Protocol

- **Warm start**: one full pass over queries 1..99 is run and discarded, then
  **one** measured pass. Only one, because a pass costs ~20 min per engine —
  so unlike `../../tpch/timings/`, **these files carry no error bar** and a
  single query's number must not be read as reproducible on its own.
- `psql -f` over `../runtime_goopg/tpcds-data/queries/`, 300 s per-query budget.
- Both engines at the settings aligned 2026-09-06: `shared_buffers = 2048MB`,
  `work_mem`, `effective_cache_size`, `autovacuum` as recorded in each header,
  read back from the live server rather than from the config file.

## Read the caveats in the file headers before quoting a number

Both files carry two, and the second one inverts the obvious reading:

1. **The two captures overlapped on one host.** Both totals are inflated by CPU
   contention. The ratio survives better than the absolutes. A quiet-host
   re-run is owed — ledger `tpcds-timing-concurrent-capture`.
2. **The printed totals are not comparable.** PG's Q4 exceeded the budget and
   is recorded `FAIL`, so PG's total covers 98 queries and goopg's covers 99.
   In a clean re-run PG's Q4 still did not finish inside 400 s; goopg runs it
   in **25.15 s**. Crediting PG a conservative 400 s gives goopg 1114 s vs PG
   ≥985 s over the same 99 — about **1.1×**, not the 1.9× the totals suggest.

A comparison that silently drops a timeout credits the engine that timed out.

## Correctness is gated elsewhere

These files record **timing only**. Result correctness is
`scripts/tpcds-sf025-regression.sh` against the git-tracked value oracle at
`../runtime_goopg/tpcds-results-sf025/oracle.txt`, which is unaffected by
timing, residency or host load.

## Historical note

Before 2026-09-06 both TPC-DS goopg clusters ran on the **128MB
`shared_buffers` default** against a 1.1 GiB working set, so any TPC-DS timing
older than that date is I/O-bound and not comparable to these. See
`../README.md`.
