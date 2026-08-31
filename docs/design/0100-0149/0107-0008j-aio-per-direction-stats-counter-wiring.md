---
status: accepted
milestone: M0107-0008
relates: [0107-0008f, 0107-0008g, 0107-0008h, 0107-0008i]
landed: 2026-05-21 (loop 10)
---

# 0107-0008j — AIO per-direction counters via stats.Counter

## Why this loop

[[0107-0008i]] migrated the AIO `Engine` aggregate totals
(`submitted` / `completed` / `errored`) from `atomic.Uint64` to
`stats.Counter`, but deliberately left the per-direction breakdown
(`readSubmitted` / `readCompleted` / `readErrored` /
`writeSubmitted` / `writeCompleted` / `writeErrored`) and per-direction
latency-sum (`readLatencySumMicros` / `writeLatencySumMicros`)
on `atomic.Uint64`. The result was a consistency-shape asymmetry:

- `Stats.Submitted` (eventual-consistent across 256 shards)
- `Stats.ReadSubmitted + Stats.WriteSubmitted`
  (sequentially-consistent on two atomic.Uint64 lines)

The invariant `Submitted == ReadSubmitted + WriteSubmitted` already
held only best-effort under the pre-migration code (Submit's two
`Add(1)` calls — aggregate + per-direction — are not atomically
grouped). After [[0107-0008i]] the two sides became asymmetric in
their visibility delay: a `Stats()` snapshot taken under heavy load
could observe the per-direction sum running ahead of (or behind)
the aggregate by hundreds of operations, not just one.

This loop restores symmetry by moving the per-direction submit /
complete / error counters — and the two per-direction latency
`SumMicros` counters that share the completion hot path — to
`stats.Counter`.

`readLatencyMaxMicros` and `writeLatencyMaxMicros` remain
`atomic.Uint64`: `advanceMax` needs `CompareAndSwap` and
`stats.Counter` does not expose CAS (each shard tracks a sum, not a
per-shard max; cross-shard max is meaningless for monotonic-forward
clamping).

The per-target stats (`*targetStats` in the `sync.Map`-keyed
breakdown — `submitted` / `completed` / `errored` /
`latencySumMicros` / `bytes`) stay on `atomic.Uint64` for now:

- They are naturally sharded by target identity. A relfile / WAL
  segment / control-file path typically sees writes from a single
  pinning backend or a single checkpointer/walwriter goroutine, so
  the per-target counter line does not bounce across cores the way
  a single global aggregate does.
- They live in lazily-allocated `*targetStats` records. Migrating
  them to `stats.Counter` would inflate each record from ~48 B to
  ~80 KiB (5 × 16 KiB Counter). With "thousands of distinct
  targets" (per the type's own doc comment) the worst-case memory
  cost is ~80 MiB instead of a flat 48 KiB.
- The `pg_stat_io` view-shape invariant — one row per
  `(target, direction)` — is unaffected by storage choice.

Per-target migration is therefore a separate decision (cost vs.
benefit) that can be re-evaluated independently of the per-direction
asymmetry this loop is closing.

## What changes

`internal/aio/aio.go::Engine`:

```diff
-    readSubmitted  atomic.Uint64
-    readCompleted  atomic.Uint64
-    readErrored    atomic.Uint64
-    writeSubmitted atomic.Uint64
-    writeCompleted atomic.Uint64
-    writeErrored   atomic.Uint64
+    readSubmitted  stats.Counter
+    readCompleted  stats.Counter
+    readErrored    stats.Counter
+    writeSubmitted stats.Counter
+    writeCompleted stats.Counter
+    writeErrored   stats.Counter

-    readLatencySumMicros  atomic.Uint64
+    readLatencySumMicros  stats.Counter
     readLatencyMaxMicros  atomic.Uint64
-    writeLatencySumMicros atomic.Uint64
+    writeLatencySumMicros stats.Counter
     writeLatencyMaxMicros atomic.Uint64
```

`Submit` and `finishHandle` keep their `.Add(1)` calls byte-identical
(untyped constant `1` is accepted by both
`atomic.Uint64.Add(uint64)` and `stats.Counter.Add(int64)`).

`finishHandle`'s two latency-sum bumps change from
`Add(elapsedMicros)` (uint64) to `Add(int64(elapsedMicros))` because
`stats.Counter.Add` takes `int64`. The cast is well-defined:
`elapsedMicros` is derived from `time.Duration.Microseconds()` which
returns `int64`; the intermediate `uint64` was only to give
`advanceMax` an unambiguous unsigned argument. The latency overflow
threshold (18M years at 1 B I/Os per second, per the existing
comment) is unaffected: `int64` has the same 63-bit range and the
Counter's per-shard cells are also `int64`.

`Stats()` reads change from `.Load()` to `uint64(.Sum())` for the
eight migrated fields. Public `Stats` struct field types are
unchanged (`uint64`); `internal/initdb/aio_views.go` view bindings
observe identical column types and values.

## Caller-facing behaviour preserved

- **Type-level**: All eight migrated fields keep their `uint64`
  surface (`Stats.ReadSubmitted` etc.) and their positions in the
  `Stats` struct.
- **Semantic**: Counters remain monotonic-non-decreasing per
  process lifetime. Per-shard atomic loads in `Sum()` give the
  same memory-model guarantee the original `atomic.Uint64.Load()`
  did — each individual shard's value is well-defined; cross-shard
  ordering is unspecified but the running total is monotonic per
  shard.
- **Invariant tightening**: After this loop, the
  `Submitted == ReadSubmitted + WriteSubmitted` invariant is
  symmetric in its consistency shape — both sides are eventual-
  consistent across 256 shards. A `Stats()` snapshot may observe
  short transient gaps on either side, but neither side is held
  artificially "ahead" or "behind" the other by storage choice.
- **MaxMicros unchanged**: `advanceMax` continues to operate on
  `atomic.Uint64` with `CompareAndSwap`. Stats consumers see the
  same worst-case observed sample.

## What we measured

```
$ go build ./internal/aio/...
$ go test -race -count=1 -timeout 120s ./internal/aio/ ./internal/stats/
ok  	github.com/goopg/goopg/internal/aio	1.040s
ok  	github.com/goopg/goopg/internal/stats	1.021s

$ go test -race -count=1 -timeout 240s \
    ./internal/storage/ ./internal/wal/ ./internal/aio/ \
    ./internal/stats/ ./internal/runtimeshim/
ok  	github.com/goopg/goopg/internal/storage	5.347s
ok  	github.com/goopg/goopg/internal/wal	3.212s
ok  	github.com/goopg/goopg/internal/aio	1.041s
ok  	github.com/goopg/goopg/internal/stats	1.022s
ok  	github.com/goopg/goopg/internal/runtimeshim	1.223s
```

No new tests added. The existing `internal/aio/aio_test.go` covers
all eight migrated counters end-to-end through the `Stats()` API:
the `Submit → Wait → completed.Add` paths exercise per-direction
submit/complete; the latency-sum + max assertions exercise both
SumMicros (now Counter) and MaxMicros (still atomic.Uint64). A
dropped increment or cross-shard sum bug in `stats.Counter` would
surface as a mismatch in any of them. `stats.Counter`'s own
race-clean suite covers the primitive directly.

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Eight new Counters per
`Engine` = **128 KiB / engine** (on top of [[0107-0008i]]'s 48 KiB,
totalling **176 KiB / engine**). A goopg server constructs exactly
one `*aio.Engine`, so the total is **176 KiB per server**, flat.
Trivial against any realistic working set.

The cost is bounded by engine count (one), not by I/O rate.

## PG-compat impact

None. The migrated counters feed goopg's `pg_stat_io` and
`pg_stat_aio_targets` views, whose column shapes and observed
values are unchanged. PostgreSQL's own `pg_stat_io` (PG 16+)
reports the same logical counters; goopg's view continues to
return identical column names and types. No on-disk format, WAL
record format, catalog heap-tuple layout, or wire-protocol bytes
change. Parent design's PG-compat gate (invariants §6 Phase D5)
is satisfied trivially.

## Follow-ups (separate loops)

- **Per-target AIO migration** — the `*targetStats` records'
  `submitted` / `completed` / `errored` / `latencySumMicros` /
  `bytes` fields. Decision deferred to a dedicated cost-vs-benefit
  loop given the per-target memory amplification (~80 KiB per
  target × thousands of targets vs. naturally-sharded access
  pattern that already avoids cross-core line bouncing). The
  `latencyMaxMicros` field would stay `atomic.Uint64` regardless
  (CAS requirement).
- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the
  last remaining `runtimeshim` consumer in M0107-0008 scope;
  lands after the bufpool lock-free rewrite ([[06-bufpool-lockfree]],
  M0107-0006) per the M0107 sub-milestone ordering.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` — unchanged from
  [[0107-0008i]]'s follow-up list.
