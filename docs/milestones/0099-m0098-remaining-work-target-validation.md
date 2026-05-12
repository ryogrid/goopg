# Milestone 0099 — M0098 Remaining Work Closure & Target Validation

**Status:** planned
**Depends on:** M0098 (final measurement and bottleneck identification)
**Source of remaining work:** `bench/pgbench-compare/results/m0098_final_summary.md` (Remaining Work section)

## Context

M0098 delivered major gains (WAL group commit, buffer-pool partitioning, EPQ-related fixes),
but did not reach the target TPS thresholds under canonical conditions (`-c 100 -j 100 -T 180 -s 100`).
The M0098 final summary explicitly lists three unresolved bottlenecks:

1. `evictMu` serialization in buffer-pool pin path
2. insufficient WAL group-commit batching
3. high 40001 abort rate under write conflicts

This milestone closes those items and validates performance both at the canonical target condition
and across multiple client/thread configurations.

## In Scope

### M0099-0001 — Design + benchmark plan lock-in

- Produce design docs and implementation plan for:
  - `evictMu` fast-path contention removal
  - WAL group-commit batching policy (`commit_delay` / commit siblings behavior)
  - deadlock-safe conflict waiting + retry semantics
- Define benchmark execution matrix, pass/fail gates, and artifact format.

### M0099-0002 — Buffer-pool pin fast-path de-serialization

- Remove global `evictMu` from common `Pin`/`TryPin` fast path.
- Use atomic pin counters and lock ordering that preserves correctness.
- Keep deadlock safety guarantees (including prior M0098 deadlock fixes).

### M0099-0003 — WAL group-commit batching effectiveness

- Implement/adjust commit-delay based batching controls.
- Validate that write-heavy workloads produce larger flush batches and better TPS.
- Document defaults, tuning bounds, and expected trade-offs.

### M0099-0004 — Conflict handling with lower abort rate

- Introduce deadlock-safe waiting/retry strategy for conflicting updates.
- Avoid circular waits while reducing 40001 abort rate seen in M0098.
- Add targeted regression/concurrency tests for lock-wait and deadlock-avoidance behavior.

### M0099-0005 — Client/thread variation measurement matrix

Run full pgbench workloads (`standard`, `-N`, `-S`) at scale 100 for:

- `(c,j) = (10,10)`
- `(c,j) = (25,25)`
- `(c,j) = (50,50)`
- `(c,j) = (100,100)`
- `(c,j) = (150,150)`
- `(c,j) = (200,200)`
- `(c,j) = (100,50)`
- `(c,j) = (50,100)`

For each point, capture:

- TPS (including average and variance)
- latency
- failed transaction rate
- notes on cold/warm pool state

And explicitly confirm whether targets are reached:

- Standard: `>= 1500 TPS`
- Simple Update (`-N`): `>= 1500 TPS`
- Select Only (`-S`): `>= 10000 TPS`

### M0099-0006 — Canonical target-condition final validation

- Re-run canonical condition: `-c 100 -j 100 -T 180 -s 100`
- Include cold and warm interpretations where relevant
- Capture CPU/alloc/mutex/block pprof for any residual gap
- Publish final summary under `bench/pgbench-compare/results/`

## Out of Scope

- New unrelated optimizer or SQL-feature milestones
- Non-performance feature expansions not tied to M0098 Remaining Work
- Hardware scaling assumptions outside the current benchmark environment

## Required Design Docs

- `docs/design/0099-0001-evictmu-pin-fastpath-deserialization.md`
- `docs/design/0099-0002-wal-group-commit-batching-policy.md`
- `docs/design/0099-0003-deadlock-safe-conflict-waiting.md`
- `docs/design/0099-0004-pgbench-client-thread-matrix-validation.md`

## Definition of Done

1. All three M0098 Remaining Work bottlenecks have concrete implementation and verification evidence.
2. Deadlock regressions are prevented by dedicated concurrency tests.
3. Full `(clients, threads)` matrix results are published with TPS/latency/failure metrics.
4. Report explicitly states whether each target is achieved at each matrix point.
5. Canonical run (`-c 100 -j 100`) is re-validated with updated binary and archived artifacts.
6. Final summary and result artifacts are committed under `bench/pgbench-compare/results/`.
