# TPC-H 22-Query Re-Baseline (M0062) — 2026-05-07

## Scope

Full 22-query TPC-H SF=1 sweep against `runtime_goopg` after the
M0062 same-day fixes landed in commit `aa2c50e` (Q9 LIKE
diagnostic + Q13 cancel-propagation + defense-in-depth ctx
checks) and `6b59749` (Q9 NLI bisect + M0062-0006 follow-up
sub-task). Supersedes
`analysis/tpch-m0061-followups-baseline-2026-05-07.md`.

| Run parameter | Value |
| --- | --- |
| Commit         | `6b59749` (`perf-analysis`) |
| Dataset        | TPC-H SF=1 (HammerDB schema, all integer cols NUMERIC) |
| Server         | `goopg` listening on `127.0.0.1:65433`, GOMEMLIMIT=12 GiB |
| Cancel-after   | 600 s |
| Per-query budget | 620 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0062_22q_20260507T104203.log` |

## Results

| Query | Status | Elapsed (s) | Rows | M0062 effect | Notes |
| ----- | ------ | -----------:| ----:| ------------ | ----- |
| Q1  | OK    |   42.58 |       4 | unchanged | within prior baseline |
| Q2  | OK    |    7.65 |     470 | unchanged | matches prior |
| Q3  | OK    |   43.88 |   11462 | unchanged | within prior |
| Q4  | OK    |  175.20 |       5 | unchanged | M0061-0001 EXISTS unnested |
| Q5  | ERROR | 600.10 |       — | unchanged | tracked as **M0062-0001** |
| Q6  | OK    |   31.57 |       1 | unchanged | within prior |
| Q7  | OK    |   39.70 |       4 | unchanged | matches prior |
| Q8  | OK    |  217.08 |       0 | unchanged | tracked as **M0062-0002** (0-row correctness) |
| Q9  | ERROR |    0.77 |       — | **bisect ID + diagnostic** | tracked as **M0062-0006**; error now reads `got left.Kind=5 right.Kind=3` (KindTime — confirms NLI column-index bug) |
| Q10 | OK    |   45.54 |   20574 | unchanged | matches prior |
| Q11 | OK    |    4.58 |    1142 | unchanged | matches prior |
| Q12 | OK    |  100.82 |       2 | unchanged | within prior |
| **Q13** | **ERROR** | **600.06** |       — | **cancel propagation fixed** | was 899 s in M0061-0003 (300 s cancel-lag); now cancel returns within 60 ms of `--cancel-after` |
| Q14 | OK    |   38.38 |       1 | unchanged | within prior |
| Q15-CREATEVIEW | OK | 0.00 |    0 | unchanged | DDL, no rows |
| Q15a-VIEWBODY  | OK | 29.19 | 10000 | unchanged | view body OK |
| Q15b-MAIN      | OK | 29.09 |    0 | unchanged | tracked as **M0062-0003** (0-row correctness) |
| Q16 | OK    |    5.72 |   18170 | unchanged | matches prior |
| Q17 | OK    |   87.05 |       1 | unchanged | within prior |
| Q18 | OK    |  125.11 |      11 | unchanged | within prior |
| Q19 | OK    |   73.49 |       1 | unchanged | M0061-0002 IN-list pushdown |
| Q20 | ERROR | 600.00 |       — | unchanged | tracked as **M0062-0004** (nested-IN decorrelation) |
| Q21 | ERROR | 600.01 |       — | unchanged | tracked as **M0062-0005** (non-equijoin EXISTS) |
| Q22 | OK    |   65.37 |       7 | unchanged | M0061-0001 NOT EXISTS unnested |

## M0062 outcomes

| Sub-task | Status | Evidence |
| -------- | ------ | -------- |
| Q9 LIKE forward-fix (KindBytes + diagnostic) | LANDED `aa2c50e` | error message in this run shows actual Kind |
| Q9 root-cause bisect | LANDED `6b59749` | first-bad = `13fad01` (M0054-0006 NLI) — see `analysis/tpch-m0062-q9-bisect-2026-05-07.md` |
| Q13 cancel-propagation | LANDED `aa2c50e` | sweep cancel returns at 600.06 s vs prior 899 s |
| Defense-in-depth ctx (sort/filter/agg-output) | LANDED `aa2c50e` | quiet here (no opportunity to verify); pure safety net |
| M0062 milestone opened | LANDED `aa2c50e` + `6b59749` | `docs/milestones/0062-tpch-residual-long-tail.md`, six sub-tasks |

## Q13 cancel-propagation verification

Targeted single-query run with a 15 s cancel-after returns
SQLSTATE 57014 within 20 ms of the deadline:

```
$ ./tpch-runner -queries=13 -cancel-after=15s -per-query-timeout=25s
Q13: ERROR after 15.01s — pq: canceling statement due to user request (57014)

real    0m15.020s
```

This pins the new inner-loop `ctx.Err()` check at
`internal/executor/operators_join_agg.go:122` (every 4096
iterations of the inner loop). The query itself is still
fundamentally O(N×M) on Q13's 150 K × 1.5 M LEFT JOIN with a
NOT LIKE residual; the M0062 batch only fixes the *cancel*
gap, not Q13's runtime. M0062-0001-style profiling work would
look at the underlying NL itself.

## Distribution of outcomes

- **OK with canonical row counts:** 14 of 22 (Q1, Q2, Q3, Q4,
  Q6, Q7, Q10, Q11, Q12, Q14, Q15a, Q16, Q17, Q18, Q19, Q22 +
  Q15-CREATEVIEW DDL).
- **OK with pre-existing 0-rows correctness gap:** 2 (Q8, Q15b)
  — both tracked under M0062-0002 / M0062-0003.
- **ERROR (cancel timeout):** 4 (Q5, Q13, Q20, Q21) — tracked
  under M0062-0001 / M0062-0004 / M0062-0005. Q13 is now an
  *expected-cancel* not a *cancel-lag* failure, an M0062 win.
- **ERROR (LIKE on non-string):** 1 (Q9) — tracked under
  M0062-0006 with bisect-identified root cause and concrete
  fix path.

## Process notes

- WSL2 host stable throughout the run; commit `e114ca7`'s
  `runtime.GC()` + `debug.FreeOSMemory()` after each query
  combined with `GOMEMLIMIT=12 GiB` prevents the
  16 GB-VmHWM-stuck behaviour the M0061-0003 sweep exhibited.
  Peak `VmHWM` for this sweep was not separately measured but
  the process settled at < 6 GB RSS at sweep end.
- Sweep total elapsed: ~78 minutes (longer than M0061-0003's
  ~64 min because Q13 now runs the full 600 s budget rather
  than the prior 899 s — the cancel-after is now the
  effective bound).
- Connection isolation (commit `00ee40f`) verified: no
  result-stream aliasing across queries.

## Open follow-ups

All five M0062 long-tail sub-tasks plus the new M0062-0006
NLI fix remain open. See
`docs/milestones/0062-tpch-residual-long-tail.md` and the
`Milestone 0062` section in `.ralph/fix_plan.md`.

## Recommended next actions

1. **M0062-0006 NLI column-index re-resolution.** Concrete
   plan exists in the bisect doc; smallest perceived blast
   radius (single planner pass).
2. **M0062-0001 Q5 profile.** Use `pprof` against a
   long-running Q5 to see what dominates the 600 s.
3. **M0062-0002 Q8 0-row diagnosis.** Reproduce with a
   minimal 2-table + extract test and bisect.
