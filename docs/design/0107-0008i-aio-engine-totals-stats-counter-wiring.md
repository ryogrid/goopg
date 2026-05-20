# Design: AIO Engine totals (`submitted`/`completed`/`errored`) via `stats.Counter` (M0107-0008, loop 9)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessors**: [`0107-0008f-perp-stats-counter.md`](0107-0008f-perp-stats-counter.md), [`0107-0008g-btree-stats-counter-wiring.md`](0107-0008g-btree-stats-counter-wiring.md), [`0107-0008h-memring-stats-counter-wiring.md`](0107-0008h-memring-stats-counter-wiring.md)
**Filed**: 2026-05-21

## Scope of this loop

Migrate the third concrete consumer of the [`stats.Counter`](0107-0008f-perp-stats-counter.md)
primitive: the AIO `Engine`'s **aggregate totals** —
`submitted`, `completed`, `errored` — in `internal/aio/aio.go`.
These three counters are bumped on every `Submit` (one) and every
`finishHandle` (one + conditional error bump) and read on the cold path
by the `pg_stat_io`-shape consumer that calls `Engine.Stats()`.

This loop is deliberately narrow:

- **In scope**: the three Engine-level aggregate counters only.
- **Out of scope** (deferred to a later, wider-shape loop per
  [[0107-0008h]] §"Why not a smaller change"):
  - Per-direction breakdown (`readSubmitted`, `readCompleted`,
    `readErrored`, `writeSubmitted`, `writeCompleted`, `writeErrored`).
  - Per-target stats (`targetStats.submitted`, `.completed`, `.errored`,
    `.latencySumMicros`, `.latencyMaxMicros`, `.bytes`).
  - Latency Sum/Max fields (`readLatencySumMicros`,
    `readLatencyMaxMicros`, `writeLatencySumMicros`,
    `writeLatencyMaxMicros`) — the Max half needs CAS, not additive.
  - `inFlight` (signed delta-counter that goes negative on a Submit
    cancellation path; `stats.Counter` is signed-Add-capable but
    `inFlight` is currently read as a coherent in-flight gauge against
    the inflight-map and inverting it across shards complicates the
    invariant).
  - `nextID` (monotonic identifier allocator, not a counter).

The per-direction, per-target, and latency fields all couple to the
`pg_stat_io`/`pg_stat_aio_targets` view shape; per [[0107-0008h]] that
unification is "worth a follow-up loop on its own." This loop migrates
only the unambiguously-additive Engine-level totals.

## What landed

| File | Change |
| --- | --- |
| `internal/aio/aio.go` | Add `github.com/goopg/goopg/internal/stats` import. Change `Engine.submitted`, `.completed`, `.errored` field types from `atomic.Uint64` to `stats.Counter`. Update the three reader sites in `Engine.Stats()` from `e.<field>.Load()` to `uint64(e.<field>.Sum())`. The three `.Add(1)` call sites in `Submit` and `finishHandle` are byte-identical (untyped constant `1` is accepted by both `atomic.Uint64.Add(uint64)` and `stats.Counter.Add(int64)`). |

`sync/atomic` is still imported by the same file — `inFlight`,
`nextID`, the per-direction counters, the latency Sum/Max counters, the
per-target counters inside `*targetStats`, and the `advanceMax` CAS
helper all continue to use `atomic.Int64` / `atomic.Uint64`. Only the
three Engine-level totals move.

## Why these three counters

Selection criterion (the same one [[0107-0008g]] and [[0107-0008h]]
used):

- **Per-call frequency, hot path.** `Submit` runs on every I/O the
  buffer pool, WAL writer, walsenders, and checkpointer push through.
  `finishHandle` runs at the same cadence. At a heavy write workload
  this is thousands of bumps per second; under the `method=worker`
  pool the bumps come from multiple worker goroutines concurrently.
- **Multi-P contention.** With `method=worker` (the default for
  `io_uring` fallback hosts and the configured method for pgbench
  workloads), `finishHandle` runs on whichever P the worker
  goroutine landed on. The shared `atomic.Uint64` cache line for
  `completed` hops between cores on every completion.
- **Cold-path read pattern.** `Engine.Stats()` is the sole reader; it
  feeds the `pg_stat_io`-shape view in `internal/initdb/open.go`
  ("Backed by aio.Engine.Stats()"). View queries run at PG's
  `pg_stat_*` cadence (operator-driven `SELECT`s), not per-frame.
  Cross-shard `Sum()` is paid once per snapshot.
- **No reset path in production.** `Engine` exposes no `Reset()` for
  these counters; `pg_stat_io` never zeros them. `stats.Counter`'s
  `Reset()` method is unused at this caller — consistent with
  pre-migration behaviour.

## Why a narrow scope is the right shape

[[0107-0008h]] explicitly weighed `aio.Engine.submitted/completed/errored`
and decided the *full* AIO migration (per-direction + per-target +
latency Sum) was too coupled to a `pg_stat_io` view-shape unification
to land cleanly in one loop. This loop honours that decision by
splitting the migration along the shape boundary:

- **Aggregate totals** (this loop): no view-shape change. The three
  values keep returning `uint64` via `Stats.Submitted` / `.Completed` /
  `.Errored` exactly as before. Operators reading the view see
  identical numbers — only the in-memory storage changed.
- **Per-direction / per-target / latency** (future loop, deferred):
  these touch the `pg_stat_io` row shape (one row per (target,
  direction) tuple, with `Sum/Avg/Max` aggregations). Migrating them
  as a single, shape-coherent unit is the right granularity; doing
  half now and half later would create a transient state where
  `Submitted == ReadSubmitted + WriteSubmitted` becomes asymmetric in
  its consistency guarantees (one side eventual-consistent across 256
  shards, the other side `atomic.Uint64`-sequentially-consistent).
  Defer until the whole family can move together.

The aggregate-totals invariant —
`Stats.Submitted == Stats.ReadSubmitted + Stats.WriteSubmitted` — is
**already best-effort** under the pre-migration code: the four
`atomic.Uint64.Add(1)` calls in `Submit` are not atomically grouped, so
a `Stats()` interleaved between `e.submitted.Add(1)` (line 434) and
`e.readSubmitted.Add(1)` (line 437) already observes a 1-unit gap. The
migration does not weaken this invariant materially — `stats.Counter`'s
`Sum()` is still well-defined per shard load, and the only new
weakness is that a single `Sum()` is not a snapshot across the 256
shards. In practice the same eventual-consistency property holds.

## Caller-facing behaviour preserved

- **Type-level**: `Stats.Submitted`, `.Completed`, `.Errored` keep
  their `uint64` field types and their position in the struct.
  Callers — `internal/initdb/aio_views.go::registerStatAIOView` and
  the `pg_stat_io` view binding in `internal/initdb/open.go` —
  observe identical column types and values.
- **Semantic**: counters are still monotonic-non-decreasing per
  process lifetime. The per-shard atomic loads inside `Sum()` give
  the same memory-model property `atomic.Uint64.Load` did (each
  individual shard's value is well-defined; cross-shard ordering is
  unspecified but the running total is monotonic per shard).
- **InFlight unchanged**: `Stats.InFlight` continues to be read from
  `e.inFlight.Load()` (still `atomic.Int64`). The inflight-map
  invariant — `inFlight` reflects `len(inflight)` ± in-flight
  Submit/finishHandle delta — is preserved.

## What we measured

```
$ go build ./internal/aio/...
$ go test -race -count=1 -timeout 120s ./internal/aio/
ok   github.com/goopg/goopg/internal/aio   1.027s

$ go test -race -count=1 -timeout 60s ./internal/stats/
ok   github.com/goopg/goopg/internal/stats 1.021s

$ go test -race -count=1 -timeout 240s \
    ./internal/storage/ ./internal/wal/ ./internal/aio/ \
    ./internal/stats/ ./internal/runtimeshim/
ok   github.com/goopg/goopg/internal/storage     5.350s
ok   github.com/goopg/goopg/internal/wal         3.204s
ok   github.com/goopg/goopg/internal/aio         1.032s
ok   github.com/goopg/goopg/internal/stats       1.024s
ok   github.com/goopg/goopg/internal/runtimeshim 1.224s
```

No tests were added — the existing `internal/aio/aio_test.go` (the
`Engine.Stats()` round-trip tests, the `Submit → Wait → completed.Add`
end-to-end paths, and the latency-Sum/Max assertion tests) all exercise
the three migrated counters through the same public `Stats()` API a
view consumer would. A dropped increment or cross-shard sum bug in
`stats.Counter` would surface in any of them as a mismatch and fail
the suite.

`internal/initdb/...` shows pre-existing failures unrelated to this
change (the same set called out in
[[0107-0008e]]); verified by stashing the diff and re-running the
same `-run` selector on `master` — failures reproduce identically.

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Three per `Engine` =
**48 KiB / engine**. A goopg server constructs exactly one `*aio.Engine`
(via `aio.NewEngine` in `internal/initdb/open.go`), so the total cost
is **48 KiB per server**, flat. Trivial against any realistic working
set (the buffer pool is GiB-scale; the AIO inflight map alone exceeds
this at modest queue depths).

The cost is bounded by engine count (one), not by I/O rate.

## PG-compat impact

None. The Engine totals feed goopg's `pg_stat_io` view, whose column
shape and observed values are unchanged. PostgreSQL's own
`pg_stat_io` (PG 16+) reports the same logical counters; goopg's view
continues to return identical column names and types. No on-disk
format, WAL record format, catalog heap-tuple layout, or wire-protocol
bytes change. The parent design's PG-compat gate (invariants §6
Phase D5) is satisfied trivially.

## Follow-ups (separate loops)

- **Per-direction + per-target + latency AIO migration** — the wider
  `pg_stat_io` view-shape unification deferred above. Migrates
  `readSubmitted`/`readCompleted`/`readErrored`/`writeSubmitted`/
  `writeCompleted`/`writeErrored`, the per-direction latency
  `SumMicros` fields (Max stays atomic — it needs CAS), and the
  per-target `*targetStats` fields together so the cross-counter
  arithmetic stays shape-coherent.
- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the last
  remaining `runtimeshim` consumer in M0107-0008 scope; lands after
  the bufpool lock-free rewrite ([[06-bufpool-lockfree]]) per the
  M0107 sub-milestone ordering.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` — unchanged from
  [[0107-0008h]]'s follow-up list.
