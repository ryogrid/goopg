# Practice card — TPC-H / performance / benchmarking work

**Load when** the task involves TPC-H queries, pgbench/HammerDB, pprof, heap or
CPU optimisation, or the cost model.

**Why:** perf loops are long and expensive (the costliest loops in the history
run 100s–1000s of turns); a wrong cost-model edit can materialise a 300M-row
intermediate and burn a whole run.

## Environment (do this first — see [[server-test]])

- **Always memory-cap** the server (`scripts/goopg-test-run.sh` / `make start`).
  An unbounded TPC-H intermediate thrashes swap and trips the WSL2 OOM killer.
- **Isolation:** ports **5533/5534** + `tmp/perf-optimize/`, distinct
  `GOOPG_CG_UNIT` per concurrent run, to avoid colliding with Ralph's own
  5433/5434 + `tmp/pgbench-compare/` ([[pattern_ralph_isolation_ports_paths]]).
- **pprof:** mutex/block profiles are OFF by default — set
  `GOOPG_MUTEX_PROFILE_RATE=1` and `GOOPG_BLOCK_PROFILE_RATE=1` before the run
  ([[pattern_pprof_env_var_enablement]]). Use inuse_space (not alloc_space) for
  retained-heap comparisons.

## Correctness gate (perf must not change results)

- **Row counts are the gate.** A perf change that alters any query's row count
  is a regression — re-check the canonical counts, especially the
  Q12/Q13 silent-regression tripwires ([[feedback_tpch_pre_commit_gates]],
  see [[executor-planner-change]]).
- Never `-count=1` in a gate run (cache policy:
  ci/design/test-gate-speedups/05 §1).

## Known traps

- **Cost model ignores build-side memory:** `estimateJoinCost ≈ (L*R)/NDistinct`
  is blind to hash build-side memory; a synthesised edge produced a 303M-row
  intermediate (M0076). Validate the chosen plan shape, not just the cost number.
- **Datum/arena aliasing:** arena slot reuse can alias values; cross-Kind
  equivalence (String↔StringArena) must hold in every comparison site
  ([[m0073_arena_q5_heap_drop]]).
- **GC hot-path:** avoid `ReadMemStats` (STW) on the per-query path — it cost 43%
  in gcBgMarkWorker once ([[m0107_gc_hotpath_fix]]).

## Measure, then attribute

Capture a baseline before the change and diff (TPS / heap / per-op). Record the
result in `analysis/` with the commit hash so the next loop doesn't re-measure.
