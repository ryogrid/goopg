# 09 — Correctness Gates and Measurement Plan

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |
| depends on | all preceding chapters |

Parallel execution is the highest-risk change this engine can make, for a
reason stated in the repository itself: `Makefile:393-396` justifies the
`race-gate` target as catching "data races specific to goopg's thread-parallel
architecture (as opposed to PG's process-parallel design where data races
aren't possible between backends)". PG's process model makes an entire class of
bug structurally impossible. goopg is giving that up deliberately
([03](03-concurrency-substrate.md)), so the gates have to earn it back.

## 1. The primary gate: serial ≡ parallel identity

**Every parallel plan must return exactly what its serial counterpart returns.**
Not "equivalent", not "same multiset after sorting" unless the query is itself
unordered — identical.

The lever already exists. `debug_parallel_query` is registered and
enum-validated (`internal/config/defaults.go:723-729`, tested in
`internal/config/debug_parallel_query_test.go`) and is PG's own mechanism for
forcing parallel plans in testing. Turning it on where a Gather is legal, and
running the **existing executor test corpus unchanged**, is the highest-value
test in this bundle: it reuses hundreds of assertions written without any
knowledge of parallelism.

Harness: the in-process pattern (`newDDLFixture` /
`internal/executor/storage_ddl_test.go:535`, `runQueryWithErr` /
`enum_seqscan_test.go:153`) needs no server and runs inside the mandatory units
gate.

**Two carve-outs, both stated rather than discovered later.**

*Float-lane aggregates are not bit-identical.* `var_*`, `stddev_*`, `covar_*`,
`corr` and `regr_*` accumulate with `float64 +=`
(`internal/executor/operators_join_agg.go:2107,2135-2139`), so a
partial+combine+finalize result differs from serial in the last ULPs. The gate
must name these aggregates explicitly and compare within a stated tolerance.
Conversely — and worth asserting, not merely allowing — `sum` and `avg` *are*
bit-identical even for float input, because they accumulate exactly through
`numericAdd` (`:1694-1704`).

*Ordering.* Results are identical
**only** where the query defines an order. `Gather` interleaves arbitrarily and
`Gather Merge` leaves ties between workers undefined
([05](05-gather-and-gather-merge.md) §4). Tests over unordered queries compare
as multisets — which the existing TPC-H parity harness already does.

## 2. race-gate is an acceptance criterion, not a periodic check

`make race-gate` (`Makefile:410-422`) runs `go test -race` over everything
except an exclusion list that **does not** exclude `internal/executor` or
`internal/planner`. It is therefore already pointed at exactly the code this
bundle changes.

Every phase in [10](10-roadmap.md) is gated on it. Four known pieces of shared
mutable state will fail it the moment workers exist, and must be fixed as part
of the phase that introduces workers, not deferred:

- `instrumentScope` (`internal/executor/instrument.go:215`) over an
  unsynchronised `nodeStatsTable` (`:202`) — fixed by per-worker instrumenters
  merged at the Gather boundary ([03](03-concurrency-substrate.md) §6.4).
- `kvcache.Budget` (`internal/executor/kvcache/kvcache.go:20-52`) — per-worker
  budgets ([03](03-concurrency-substrate.md) §2.2).
- Any `Context` field from [03](03-concurrency-substrate.md) §2.2 that is
  accidentally shared.
- **`mctx.Perm()`'s unsynchronised bump allocator**
  ([03](03-concurrency-substrate.md) §3.2). Big-mantissa numerics allocate from
  a process-global arena with no lock, so N workers doing numeric arithmetic
  race on it. Note race-gate may *not* catch this without a test that
  deliberately provokes concurrent big-numeric creation — the values must
  exceed an int64 mantissa, which ordinary test data does not. Such a test is
  required, not optional.

A race-gate pass is necessary but not sufficient: the detector only reports
races it *observes*, and a two-worker test may not schedule the interleaving
that matters. Hence §3.

## 3. Targeted invariant assertions (debug builds)

Three cheap assertions that convert a class of subtle bug into a loud failure.
Each exists because the failure mode is otherwise silent and late.

| Assertion | Guards | Why it must be an assertion |
| --- | --- | --- |
| Every datum leaving a worker has `ArenaID == 0` **or `ArenaID == PermContextID`** — on *all* kinds, not just string/bytes | The tuple ownership contract ([03](03-concurrency-substrate.md) §3, §3.1) | Using `cloneRow` instead of `Materialize()` passes every single-threaded test and corrupts results only under timing. Restricting the check to `KindString`/`KindBytes` would miss big-mantissa `KindNumeric`, which `cloneRowOwned` does **not** promote (§3.1) |
| Per-worker pin counter is zero at worker exit | Pin balance ([03](03-concurrency-substrate.md) §6.3) | `Unpin` **panics** on underflow (`internal/storage/bufpool.go:1918-1930`), so an imbalance crashes the process rather than leaking — a crash whose stack points at the victim, not the culprit |
| No worker goroutine outlives its Gather's `Close` | Lifecycle ([03](03-concurrency-substrate.md) §6.1) | The statement `mctx` is released by `defer stmtCtx.Release()` (`internal/server/dispatch.go:290-306`) and cascades to worker arenas; an escaped worker reads freed memory |

## 4. The aggregate decomposition matrix

One case per aggregate from the transition/final case lists
([06](06-parallel-aggregation.md) §2), each asserting **partial+combine+final
== serial**, run with a forced multi-worker split so the combine path is
actually exercised (a single-partial split would pass trivially).

Positive cases: `count`, `sum` (int / numeric / float lanes),
`avg` (all three lanes), `min`, `max`, `bool_and`, `bool_or`, `every`,
`bit_and`, `bit_or`, `bit_xor`, `any_value`, the variance family in **all three
lanes** (float Youngs-Cramer, exact `big.Int`, exact `big.Rat`), the
`regr_*`/`covar_*`/`corr` family, and a user aggregate with `COMBINEFUNC`.

Two cases deserve special mention because ordinary data will not catch them:

- **Float special values** — NaN, +Inf, −Inf, and the +Inf/−Inf combination
  that must yield NaN ([06](06-parallel-aggregation.md) §2.1). A wrong
  precedence rule differs from serial output *only* on data containing
  infinities.
- **Lane divergence** — one worker seeing only integers (exact `big.Int` lane)
  while another sees numerics (`big.Rat` lane), forcing the combine to demote
  to the wider lane ([06](06-parallel-aggregation.md) §2.2). Requires data
  crafted so the split lands that way.

Negative cases, asserting the plan **stays serial**: `DISTINCT` aggregates,
`WITHIN GROUP`, ordered `array_agg`/`string_agg`, and a user aggregate with no
`COMBINEFUNC`. These matter as much as the positive ones — a refusal that
silently stops refusing is how wrong results ship.

## 5. GUC honouring

The parallel GUCs are currently accepted and ignored
([01](01-current-state-and-gap-analysis.md) §5), so "the GUC does nothing" is
the *pre-existing* state and a test asserting it works is a genuine new
guarantee:

- `max_parallel_workers_per_gather = 0` suppresses the Gather entirely.
- `Workers Planned:` tracks the setting and the size ladder
  ([08](08-planner-integration.md) §2).
- `parallel_leader_participation = off` is honoured.
- `min_parallel_table_scan_size` above the relation size suppresses the Gather.
- `SHOW min_parallel_table_scan_size` returns **`8MB`**, not `8GB` — the
  fidelity fix ([08](08-planner-integration.md) §4.3). This one is worth
  landing and testing independently of everything else, since it is observable
  today.

## 6. Cancellation, shutdown, and leaks

The client-EOF watcher's header (`internal/server/eof_watch.go:12-20`) records
the incident that motivated it: orphaned work at over 100 % CPU and 11.7 GB RSS
after a client died. N workers multiply that failure mode, so these are
regression tests for a bug this project has already paid for once.

- Cancel mid-scan (statement timeout, client EOF, explicit cancel): all workers
  exit; **no goroutine leak** (compare `runtime.NumGoroutine()` before and
  after, or use a leak detector).
- `LIMIT` above a Gather satisfied early: `Close` cancels, drains, and joins
  ([05](05-gather-and-gather-merge.md) §3.1).
- **Deadlock probe**: `Close` must drain the channel before joining, or a
  worker blocked on send never observes cancellation. A test that fills the
  channel and then cancels is the direct check.
- Worker panic: converted to an `ExecError` (`XX000`), siblings cancelled, the
  query fails, **the process survives** ([03](03-concurrency-substrate.md) §4.3).
- Multi-worker simultaneous failure: asserts *an* error of the right class, not
  a specific one — which error wins is genuinely non-deterministic
  ([03](03-concurrency-substrate.md) §4.2).

## 7. Plan-gate

`make plan-gate` (`Makefile:376-392`) diffs EXPLAIN against a captured baseline
and SKIPs cleanly when no server or baseline is available.

Expectations per [08](08-planner-integration.md) §7: **zero diffs** for every
phase before Gather insertion is enabled; a deliberate, reviewed, recaptured
diff for the `HashAggregate` label correction; and a large intended diff for the
insertion phase itself.

The discipline that matters is the one this repository already uses: a
plan-gate diff is either predicted in advance or it is a bug. "Recapture and
move on" without an explanation is how a silent regression becomes a baseline.

## 8. Measurement

### 8.1 What to measure

TPC-H SF1 under the existing capped harness. Q1 (grouped aggregate over
`lineitem`) and Q6 (filtered scan) are the canonical demonstrators — Q1
exercises partial/finalize aggregation, Q6 exercises the scan alone.

Secondary: Q9, Q14, Q17, Q19 — all queries PG parallelises and all currently
far from PG ([README](README.md)).

### 8.2 What to measure that is *not* time

- **Buffer pool hit rate** before and after, because [04](04-parallel-scan.md)
  §4.1 changes ring-buffer behaviour and the safe default disables the ring.
- **Peak memory.** N workers × `work_mem`-derived budgets
  ([03](03-concurrency-substrate.md) §2.2), plus N hash tables in a parallel
  aggregate with no spill ([06](06-parallel-aggregation.md) §4.2), plus rows in
  flight in the Gather channel ([05](05-gather-and-gather-merge.md) §2.2). None
  of this is accounted for by `work_mem`. PG has the same gap; that does not
  make it safe to leave unmeasured.
- **`mctx.Lookup` contention.** The global `ctxMu`
  (`internal/mctx/mctx.go:110-121`) is taken on every arena-string dereference
  and is the single most likely reason an early measurement disappoints
  ([03](03-concurrency-substrate.md) §5.1). Profile it explicitly rather than
  concluding "parallelism did not help".

### 8.3 How to report it honestly

This project has already been burned by a measurement artifact read as a code
result — the round-1 TPC-H sweep's tail was degraded by something systematic
and was reported as variance, corrected only later by comparing against a clean
control run
(`analysis/tpch-csq-evolution-baseline-to-round3-20260721.md` §4). Two rules
follow:

1. **Run serial and parallel sweeps back to back on the same server**, so
   environmental drift affects both. A parallel number compared against a
   sweep from a different session is not evidence.
2. **State the worker count with every number.** A 2.1× speedup with 3-way
   parallelism (leader + 2 workers) is a *61 % efficiency*, and reporting only
   the ratio hides that.

Every sweep runs under the capped wrapper (`scripts/goopg-test-run.sh` /
`scripts/csq-bench-server.sh`), which since R2-0 refuses a configuration with
`memory.high < GOMEMLIMIT`. Sweeps and the pre-commit pgbench smoke must run
**sequentially**, not concurrently — a concurrent bench server has already been
observed degrading the smoke gate from 700 to 390 TPS.

### 8.4 What success looks like

Honest targets, given leader + 2 workers is a 3× ceiling and no real workload
reaches its ceiling:

- Q6 (pure scan): the closest to ideal; anything below ~2× suggests the scan is
  not the bottleneck or the allocator/pool is contending.
- Q1 (grouped aggregate): ~1.8–2.5×, with the gap to Q6 attributable to the
  finalize step and group-table memory.
- **No regression anywhere.** A query that does not parallelise must be
  unchanged, and a query that does must not be *slower* — which is a real
  risk for small relations, and precisely what the size rule
  ([08](08-planner-integration.md) §2) exists to prevent.

A result below these is informative, not a failure — provided §8.2 is measured
so the reason is known rather than guessed.

## 9. Regression surfaces outside the executor

- **`pg-regress-runner.sh -v subselect`** and the isolation specs: parity must
  not decrease. Several isolation specs set `debug_parallel_query` during
  session setup (noted at `internal/server/dispatch.go:895`), which currently
  does nothing — once it does, those specs exercise real parallelism.
- **`internal/testutil/tpch/upstreampg_test.go`** diffs against real
  PostgreSQL; it is the strongest available oracle for the identity gate.
- **pgbench smoke** (the pre-commit hook): must be unaffected. Nothing in this
  bundle touches the write path, so a change there is a signal, not noise.
