# 10 — Phased Roadmap

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-21 |

Eight phases. The ordering principle is that **nothing user-visible changes
until the substrate is proven**, and the phase that first produces a parallel
plan is late enough that every hazard in
[01](01-current-state-and-gap-analysis.md) has already been closed under test.

Every phase carries the standard gates: units, `make race-gate`,
`scripts/tpch-spotcheck.sh` (Q12 = 2 / Q13 = 33), `make plan-gate`, and the
pre-commit pgbench smoke. Only the *additional* gates are listed per phase.

## P0 — GUC fidelity fixes

Independent of everything else and worth landing first because it is
observable today.

- Fix `min_parallel_table_scan_size` (`8GB` → `8MB`) and
  `min_parallel_index_scan_size` (`512MB` → `512kB`)
  ([08](08-planner-integration.md) §4.3). Prefer adding `UnitBlocks` to
  `internal/config/guc.go` over converting the boot values, so `SHOW` matches
  PG for any value rather than just the default.
- Register `max_parallel_workers`.

Gates: a `SHOW` assertion per GUC. **Plan-gate: zero diffs** — no planner code
is touched.

## P1 — Per-session GUC plumbing

Wire the parallel GUCs from the session to the executor context following the
`sessionStatsTarget` precedent (`internal/server/dispatch.go:1087,376`,
`dispatch_extended.go:155`) — [08](08-planner-integration.md) §4.2.

Nothing reads the values yet. This phase exists so that the phase which does
read them is not also inventing the channel.

Gates: a test that a session `SET` reaches the executor context.
**Plan-gate: zero diffs.**

## P2 — `HashAggregate` label correction

Rename the `GroupAggregate (%d keys)` label to `HashAggregate`
([06](06-parallel-aggregation.md) §4.1), because the runtime is hash-based in
every case and the later `Partial `/`Finalize ` prefixes would otherwise cement
a misnomer.

Gates: **plan-gate diff is expected and large** (every grouped query), reviewed
line by line, then recaptured. No runtime behaviour changes — that is exactly
what makes this a safe phase to take the recapture on, isolated from anything
semantic.

## P3 — Concurrency substrate

The whole of [03](03-concurrency-substrate.md), with **no parallel execution
yet**. Build the pieces and prove them in isolation:

- Worker context derivation: shared vs per-worker field split (§2), connection
  callbacks nil'd in worker contexts (§2.4).
- Per-worker `mctx` children, leader-allocated (§5).
- Materialisation helper for the worker output boundary (§3), plus the
  debug-build `ArenaID == 0` assertion.
- First-error-wins error channel, per-worker `recover()`, child-context
  cancellation (§4).
- Per-worker instrumenters with merge-at-join, replacing reliance on the
  `instrumentScope` global (§6.4).
- Per-worker `kvcache.Budget` (§2.2).
- **Make `mctx.Perm()` safe for concurrent allocation** (§3.2). This is a
  pre-existing defect — the permanent arena is process-global with an
  unsynchronised bump allocator, so two concurrent *sessions* doing
  big-mantissa numeric arithmetic already race on it — but parallel query turns
  it from rare into routine. A mutex on the permanent context alone suffices;
  it is off the per-row hot path.

Gates: **race-gate is the point of this phase.** Unit tests that spin N
goroutines through the substrate with no operators attached.
**Plan-gate: zero diffs.**

## P4 — Parallel Seq Scan + Gather

The first phase that executes anything in parallel, but **not** the first that
plans it: insertion stays off, and parallel execution is reachable only through
a test-only forcing path.

- Shared atomic block allocator ([04](04-parallel-scan.md) §2); everything else
  in the scan stays per-worker (§3).
- Ring buffer disabled for parallel scans, prefetch disabled (§4.1, §4.2) —
  both re-tuned later with measurements.
- `Gather` plan node + operator ([05](05-gather-and-gather-merge.md)), batching,
  buffered channel, leader participation, drain-before-join `Close`.
- `Gather` in `planChildren` or it is invisible in EXPLAIN.
- Implement as a legacy `Operator` reached through the existing `OpAdapter`
  fallback (`internal/executor/executor.go:563-569`), leaving the slab path
  untouched ([05](05-gather-and-gather-merge.md) §1).

Gates: race-gate; the identity gate over a forced-parallel subset; the
cancellation, deadlock, panic and goroutine-leak tests
([09](09-verification-and-measurement.md) §6); the pin-balance and `ArenaID`
assertions. **Plan-gate: zero diffs** — nothing inserts a Gather yet.

## P5 — Partial / Finalize aggregation

- `AggMode` on `planner.Aggregate` with `AggModeSimple` as the zero value
  ([06](06-parallel-aggregation.md) §1.1).
- Combine rules per lane, including the Chan-Golub-LeVeque variance formula and
  the float-special precedence (§2.1, §2.2).
- Partial-state transport via the batch side-channel (§3).
- The refusal set (§5).

Gates: the full aggregate decomposition matrix
([09](09-verification-and-measurement.md) §4), positive **and** negative cases,
with a forced multi-worker split so combine is genuinely exercised.
**Plan-gate: zero diffs** — still no insertion.

## P6 — Enable Gather insertion

The phase where behaviour changes. Everything before it was scaffolding.

- The Gather post-pass ([08](08-planner-integration.md) §1) with the
  `compute_parallel_worker()` size ladder (§2), the no-stats refusal (§2.1),
  the `parallel_workers` reloption, and the `GOOPG_PARALLEL=off` kill switch
  (§2.2).
- `proparallel` gating (§5).
- `max_parallel_workers` semaphore, making `Workers Launched:` differ
  meaningfully from `Workers Planned:` (§4.3).
- EXPLAIN: `Workers Planned:` in `emitNodeDetailLines`, `Workers Launched:` via
  the Memoize counter pattern (§6).

Gates: **plan-gate diff is expected and large**, every diff explained before
recapture; the identity gate over the *whole* corpus with
`debug_parallel_query` forcing; the GUC-honouring tests
([09](09-verification-and-measurement.md) §5); the first real TPC-H measurement
with serial and parallel sweeps run back to back (§8.3).

This is the phase to be slowest on. Every prior phase can be verified in
isolation; this one changes what every user gets.

## P7 — Gather Merge

- `GatherMerge` node reusing `sortHeap` (`internal/executor/operators.go:953`)
  and `lessRows` (`:747`) over worker channels
  ([05](05-gather-and-gather-merge.md) §4).
- Partial sort below the Gather rather than a serial sort above it (§4.1).
- Spill-file collision and cleanup verification under N concurrent sorters
  ([07](07-parallel-hash-join.md) §5).

Gates: ordering assertions; the identity gate for ordered queries; concurrent
spill tests.

## P8 — Parallel Hash Join

- Build once in the leader, publish behind a barrier, share read-only
  ([07](07-parallel-hash-join.md) §2, §3).
- Per-worker `joinOp` instances with per-probe scratch (§4).
- Semi/Anti and the NOT-IN null-aware flags ride along unchanged (§7).

Gates: identity over the join corpus; race-gate under a probe-heavy workload;
TPC-H Q9/Q17/Q19 measurement.

## Deliberately deferred

Each with the reason, so a later reader does not mistake absence for oversight.

| Item | Why deferred | Reopen when |
| --- | --- | --- |
| Parallel Index Scan | 0 occurrences in the TPC-H reference set; goopg's `indexScanOp` materialises the whole TID list eagerly (`internal/executor/operators_index.go:189`), so the cheap version diverges from PG's design anyway | A workload appears that is index-scan-bound and large enough to parallelise |
| Cooperative parallel hash **build** | The build side is the small side by planner construction (`IsSmallDimensionSide`, `internal/planner/cardinality.go:167`); this is the one place PG's design is genuinely more advanced ([07](07-parallel-hash-join.md) §3.1) | A measured plan where build time dominates |
| Parallel `MultiHashJoin` | No PG counterpart, so no oracle plan; widens the plan-gate surface substantially ([07](07-parallel-hash-join.md) §6) | After P8 is stable and MHJ shows up as a bottleneck |
| Chunked block allocation | The per-block atomic is cheap enough that PG's motivation (spinlock amortisation) does not apply ([04](04-parallel-scan.md) §2.1) | Profiling shows the allocator atomic mattering |
| Ring buffer + prefetch re-tuning | Disabled for parallel scans in P4 as the safe default; correct sizing under N workers is an empirical question ([04](04-parallel-scan.md) §4.1, §4.2) | Buffer-pool hit-rate measurement from P6 |
| Lock-free `mctx` registry | `mctx.Lookup`'s global `ctxMu` (`internal/mctx/mctx.go:110-121`) is a process-wide serialisation point on the arena hot path — **the most likely reason an early measurement disappoints** ([03](03-concurrency-substrate.md) §5.1) | P6 measurement, where it should be profiled explicitly rather than inferred |
| A real cost model | PG's `parallel_setup_cost`/`parallel_tuple_cost` need a `(startup, total)` pair that does not exist ([01](01-current-state-and-gap-analysis.md) §4); would also make EXPLAIN's `cost=` real | Its own bundle; benefits far more than parallelism |
| SERIALIZABLE, DML, parallel maintenance | Scope decisions ([README](README.md)) | SSI predicate-lock contention is designed for; PG needed until v12 |

## A note on sequencing risk

The largest single risk in this roadmap is that P3–P5 build a substantial
amount of machinery that produces **no observable benefit** until P6. That is
deliberate — the alternative is enabling insertion early and debugging
correctness, concurrency, and planner integration simultaneously — but it means
three phases must be judged on their gates rather than on results.

The mitigation is that each of P3, P4 and P5 has a gate that genuinely fails if
the phase is wrong: race-gate for P3, the identity gate under forced
parallelism for P4, and the decomposition matrix for P5. None of them is
"landed and looks fine".
