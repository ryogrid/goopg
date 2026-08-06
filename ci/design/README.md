# Nightly Whole-Suite Regression Batch — Design

Design date 2026-07-06. **This directory contains the DESIGN only** — the batch
itself (scripts under `ci/batch/`, the `~/.ralph/ralph_loop.sh` hook, the
Makefile target) is a later implementation task. Nothing in `ci/batch/` exists
yet.

## Purpose

Detect regressions across the *entire* implemented goopg test surface once per
day, while a Ralph autonomous loop keeps developing on the same host. The batch
must therefore be resource-conscious (shared 32 GiB WSL2 box), collision-free
(distinct ports / cgroup units / data dirs), and tolerant of load-induced
timing noise (only *drastic* performance changes are flagged).

The test landscape this design draws from was surveyed in
[`analysis/tests-overview-260706/`](../../analysis/tests-overview-260706/README.md)
(inventory, scripts, constraints, baselines, dedup policy). Authority inputs:

- `docs/test-port/postgres-oracle-port-status.csv` — the must-pass roll-up
  (60 `port/yes` rows; 8 promotable `defer` rows).
- `docs/test-port/regress-diff-baseline.csv` — per-regress-case baseline.
- `bench/tpch/spotcheck_expected.env` — TPC-H Q12/Q13 row-count tripwire.
- `bench/tpch/logs/tpch_power_test_20260526.md` — 22-query time baseline.

## One-paragraph summary

A single entrypoint (`ci/batch/run-nightly.sh`, wrapped by `make
nightly-batch`) runs: **S0 preflight** (build + environment checks) → **S1 two
parallel lanes** (Lane L: server-less unit + race tests; Lane H: server-based
testport/regress/isolation + pgbench (s=50, c=100, j=20, 180 s × 3 workloads)
— every stage in both lanes runs
inside its own cgroup memory cap) →
barrier → **S2 TPC-H solo** (spotcheck → EXPLAIN capture → 22 queries under a
2-hour total budget) → **S3 summary** (`summary.md`/`summary.json`, non-zero
exit on any must-pass failure). Every run writes to
`ci/logs/<YYYYMMDD-HHMMSS>/` with a real-time `progress.log`, and regenerates
the agent-facing `ci/logs/action-items.md`, which the Ralph loop consumes as
its highest-priority work source (standing `M-NIGHTLY` milestone in
`.ralph/fix_plan.md` — doc 07). A resident scheduler
(`ci/batch/nightly-scheduler.sh`), spawned once from `~/.ralph/ralph_loop.sh`
and guarded by `flock` against duplicates, fires the batch daily at ~00:00
local time.

## Document map

| Doc | Contents |
|-----|----------|
| [01-architecture.md](01-architecture.md) | Components, stage DAG, entrypoints, `ci/batch/` layout, reuse-vs-consolidate table |
| [02-test-selection.md](02-test-selection.md) | Exactly what runs / skips; data-driven must-pass; expected-fail handling; promotion workflow; **regress wedge-recovery rule** (recovery must not re-bootstrap the shared fixtures — one wedged case used to file 9 items); **regress wedge-probe rule** (at 60 s a case captures live pg_stat_activity/pg_locks/goroutine-dump/RSS; host-overload and GC-thrash are already refuted by arithmetic) |
| [03-resources-and-parallelism.md](03-resources-and-parallelism.md) | Memory budget, cgroup units, parallelism policy, `mem_guard.py` interaction, port isolation |
| [04-logging-and-reporting.md](04-logging-and-reporting.md) | `ci/logs/<ts>/` layout, progress log, summary schema, perf-tolerance policy, §C.1 mid-run build breaks (`build_kills`, source fingerprints), retention |
| [05-tpch-stage.md](05-tpch-stage.md) | The 2-hour-bounded TPC-H sweep: budget algorithm, EXPLAIN capture, comparisons |
| [06-scheduler.md](06-scheduler.md) | Resident daemon, `flock` single-instance control, the `ralph_loop.sh` hook patch |
| [07-ralph-feedback.md](07-ralph-feedback.md) | Failures → `ci/logs/action-items.md` → standing top-priority `M-NIGHTLY` milestone in `.ralph/fix_plan.md` |

## Design invariants (the short list)

1. **Never fight the Ralph loop for resources it already holds** — distinct
   ports, distinct `GOOPG_CG_UNIT` names, distinct data dirs; busy resource ⇒
   wait-then-SKIP, never kill.
2. **Every batch stage runs memory-capped** via `scripts/goopg-test-run.sh`
   (server-less stages included); TPC-H runs with nothing else in parallel.
3. **Must-pass sets are data-driven** — read the authority CSVs at run time;
   promoting a test never requires a batch-code change.
4. **Time is informational, correctness is gating** — row counts, diff
   baselines, and 0-failed-transactions gate; elapsed time only flags on
   drastic events (timeout, budget exhaustion, >2× total baseline).
5. **One command starts everything**; the scheduler is idempotent to respawn.
6. **Failures flow back to the loop one-way** — batch writes
   `ci/logs/action-items.md`; the in-loop agent (never the batch) turns items
   into top-priority fix_plan tasks (doc 07).
