# Design: WAL drain byte counters via `stats.Counter` (M0107-0008, loop 11)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessors**: [`0107-0008f-perp-stats-counter.md`](0107-0008f-perp-stats-counter.md), [`0107-0008g-btree-stats-counter-wiring.md`](0107-0008g-btree-stats-counter-wiring.md), [`0107-0008h-memring-stats-counter-wiring.md`](0107-0008h-memring-stats-counter-wiring.md), [`0107-0008i-aio-engine-totals-stats-counter-wiring.md`](0107-0008i-aio-engine-totals-stats-counter-wiring.md), [`0107-0008j-aio-per-direction-stats-counter-wiring.md`](0107-0008j-aio-per-direction-stats-counter-wiring.md)
**Filed**: 2026-05-21

## Scope of this loop

Migrate the fifth concrete consumer of the [`stats.Counter`](0107-0008f-perp-stats-counter.md)
primitive: the `walBufferCounters` struct in `internal/wal/writer.go`. The
struct's two fields — `overflowDrainBytes` and `flushDrainBytes` — were the
last `atomic.Uint64` additive counters in the WAL writer's observability
surface. Migrating them aligns the WAL package on a single counter shape
(`MemRing.hits`/`.misses` already use `stats.Counter` per
[[0107-0008h]]) and removes the cross-backend cache-line bounce on the
drain hot path.

The public API — `Writer.WALBuffersOverflowDrainBytes() uint64` and
`.WALBuffersFlushDrainBytes() uint64` — is unchanged. Only the *internal*
storage moves from a pair of `atomic.Uint64` fields touched via
`.Add(uint64)` / `.Load()` to a pair of `stats.Counter` values touched
via `.Add(int64)` / `.Sum()` with a single `uint64(...)` cast at the
public boundary.

## What landed

| File | Change |
| --- | --- |
| `internal/wal/writer.go` | Add `internal/stats` import; change `walBufferCounters.overflowDrainBytes` and `.flushDrainBytes` field types from `atomic.Uint64` to `stats.Counter`; update `WALBuffersOverflowDrainBytes()` and `WALBuffersFlushDrainBytes()` to return `uint64(c.overflowDrainBytes.Sum())` / `uint64(c.flushDrainBytes.Sum())`. The two `.Add(uint64(n))` call sites in `drainBufferBytes` switch to `.Add(n)` directly (`n` is already `int64`; the `uint64` cast was only there for `atomic.Uint64.Add`'s unsigned argument). |

`sync/atomic` import is retained — `writeLSNAtomic`, `writeLSNMirror`, and the
flush-group state still use `atomic.*`. Only the additive byte counters
move.

## Why this is a good consumer

The drain-byte counters are bumped from `drainBufferBytes`, which is
called along three paths:

| Path | Call site |
| --- | --- |
| Overflow inside `Append` (no commit) | `state.append → drainBufferBytes(walBuf.resident(), drainReasonOverflow)` / `drainBufferBytes(need, drainReasonOverflow)` |
| Overflow inside `AppendRaw` (no commit) | `state.appendRaw → drainBufferBytes(...)` (same two sites) |
| Explicit flush | `state.flushUpTo → drainBufferUpTo → drainBufferBytes(need, drainReasonFlush)` |

All three paths execute under `state.appendMu` — i.e. the drain itself
is single-writer-at-a-time. The previous design note (in
[[0107-0008h]]'s comparison table) cited this as a reason **not** to
migrate, but that note was incomplete. `appendMu` serialises drains
without binding them to any particular P; whichever client-backend
goroutine acquires `appendMu` next runs the drain on whatever P it
happens to be scheduled on. With the previous shared `atomic.Uint64`
field, every cross-backend handoff invalidated the line on the
previous backend's P and refetched it on the next; with `stats.Counter`,
each backend writes to its current P's shard line and no cross-P
invalidation occurs on the counter at all (the `appendMu` cache line
itself still bounces — but that line is owned by the mutex
machinery, not the counter).

The cold-path reader is the `pg_stat_wal_io` view builder
(`internal/initdb/wal_io_views.go`), which reads through the public
accessors at stats-collector cadence. Cross-shard `Sum` is paid once
per view query.

## Public accessor semantics preserved

- **Type**: `Writer.WALBuffersOverflowDrainBytes()` and
  `.WALBuffersFlushDrainBytes()` keep their `uint64` return type.
- **Nil-safe**: the `w.walBufferCounters == nil → return 0` guard in
  each accessor is preserved verbatim.
- **Eventual-consistency**: a concurrent drain may or may not be
  reflected in a same-instant accessor read. That was also true of
  `atomic.LoadUint64` (Go's memory model gives the atomic load
  sequential consistency against other atomic accesses, but the two
  fields were always read independently — a snapshot read of both
  could observe values from different drain instants).

## Caller-facing behaviour preserved

- `internal/initdb/wal_io_views.go::registerStatWALIOView` formats both
  values via `fmt.Sprintf("%d", ...)`; the `uint64` field still
  formats to the same decimal string.
- `internal/wal/wal_buffer_test.go::TestWALBufferCountersTrackDrains`
  asserts the counters start at 0, advance on overflow-driven drains,
  start at 0 for `FlushDrainBytes` until any `FlushUpTo`, and advance
  on flush. All three assertions still pass through the migrated
  storage because the public accessors return the same `uint64`
  totals.

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Two per `walBufferCounters`
= **32 KiB / Writer**. A goopg server has exactly one `Writer` (one
WAL writer goroutine per cluster), so the total cost is **32 KiB per
server**, flat.

Cumulative `stats.Counter` cost across the M0107-0008 migrations on a
single server:

| Consumer | Counters | Bytes |
| --- | --- | --- |
| btree ([[0107-0008g]]) | 2 / BTree × (~N indexes) | 32 KiB / index |
| MemRing ([[0107-0008h]]) | 2 | 32 KiB |
| AIO Engine totals ([[0107-0008i]]) | 3 | 48 KiB |
| AIO per-direction ([[0107-0008j]]) | 8 | 128 KiB |
| **WAL drain bytes (this loop)** | **2** | **32 KiB** |

Total for the four singleton consumers (MemRing + AIO + WAL drain) =
**240 KiB per server**, flat. Trivial against the GiB-scale buffer
pool / WAL buffer working set.

## Why not a smaller change

A drop-in atomic.Uint64 → stats.Counter for *only* one of the two
fields would split the consistency shape inside the same struct. The
pair lives together in `walBufferCounters`, is written together in
`drainBufferBytes`, and is surfaced together by `pg_stat_wal_io` —
keeping them on the same primitive (per the loop-7/8 discipline)
preserves the “one struct, one storage shape” invariant.

## Follow-ups (separate loops)

- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the
  remaining `runtimeshim` consumer in M0107-0008 scope; lands after
  the bufpool lock-free rewrite (M0107-0006) per the M0107
  sub-milestone ordering.
- **Per-target AIO `*targetStats` migration** — formally closed as
  *do not migrate* by [[0107-0008j]] on memory-amplification grounds
  (per-record inflation from ~48 B to ~80 KiB at thousands of
  targets ≈ ~80 MiB worst case, no contention benefit because targets
  are naturally identity-sharded). The decision can be revisited if
  per-target hot path ever shows up in mutex/CPU profiles.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` so a runtime-symbol
  break in either surfaces in CI rather than in the field. Depends
  on the project's CI scaffolding being in place (no
  `.github/workflows/` in the repo today).

## What we measured

```
$ go test -race -count=1 -timeout 120s ./internal/wal/ ./internal/stats/
ok   github.com/goopg/goopg/internal/wal    3.094s
ok   github.com/goopg/goopg/internal/stats  1.022s

$ go test -race -count=1 -timeout 60s ./internal/storage/ ./internal/aio/ ./internal/runtimeshim/
ok   github.com/goopg/goopg/internal/storage     5.346s
ok   github.com/goopg/goopg/internal/aio         1.043s
ok   github.com/goopg/goopg/internal/runtimeshim 1.225s
```

`internal/initdb/...` shows pre-existing failures unrelated to this
change (verified by stashing the diff and reproducing them on the
loop-10 tip).

## PG-compat impact

None. The WAL drain byte counters are observability instrumentation
goopg-specific to the in-memory WAL buffer (see
[`docs/design/0013-0003-pg-stat-wal-io-buffer-columns.md`](0013-0003-pg-stat-wal-io-buffer-columns.md)).
PostgreSQL does not expose `wal_buffers_overflow_drain_bytes` /
`wal_buffers_flush_drain_bytes` natively — they appear only on
goopg's extended `pg_stat_wal_io` view (built in
`internal/initdb/wal_io_views.go`). The parent design's PG-compat
gate (invariants §6 Phase D5) is satisfied trivially: no on-disk
format, WAL record format, catalog heap-tuple layout, or wire-protocol
bytes change.
