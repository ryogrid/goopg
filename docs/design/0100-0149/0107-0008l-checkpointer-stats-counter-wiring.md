# Design: Checkpointer aggregate counters via `stats.Counter` (M0107-0008, loop 12)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](../../perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessors**: [`0107-0008f-perp-stats-counter.md`](0107-0008f-perp-stats-counter.md), [`0107-0008g-btree-stats-counter-wiring.md`](0107-0008g-btree-stats-counter-wiring.md), [`0107-0008h-memring-stats-counter-wiring.md`](0107-0008h-memring-stats-counter-wiring.md), [`0107-0008i-aio-engine-totals-stats-counter-wiring.md`](0107-0008i-aio-engine-totals-stats-counter-wiring.md), [`0107-0008j-aio-per-direction-stats-counter-wiring.md`](0107-0008j-aio-per-direction-stats-counter-wiring.md), [`0107-0008k-wal-buffer-drain-stats-counter-wiring.md`](0107-0008k-wal-buffer-drain-stats-counter-wiring.md)
**Filed**: 2026-05-21

## Scope of this loop

Migrate the sixth concrete consumer of the [`stats.Counter`](0107-0008f-perp-stats-counter.md)
primitive: the `Checkpointer`'s three aggregate counters in
`internal/wal/checkpointer.go` —

| Field | Semantics |
| --- | --- |
| `numTimed` | Timer-driven checkpoint cycles. Bumped once per successful checkpoint when `spread == true`. |
| `numRequested` | SQL-`CHECKPOINT` / CLI-`pg_ctl` / `max_wal_size` volume-driven cycles. Bumped once per successful checkpoint when `spread == false`. |
| `writeTimeMs` | Cumulative wall time spent inside `flushDirty`'s `FlushAllPaced`/`FlushAll` call (milliseconds). Bumped once per flush. |

The Checkpointer was the last subsystem in the WAL package family
(after MemRing per [[0107-0008h]] and the WAL writer drain bytes per
[[0107-0008k]]) still holding additive observability counters as
`atomic.Uint64`. This loop completes the package's
"one storage shape for all additive observability counters" invariant.

The other two Checkpointer atomics —

- `lastCheckpointLSN` / `lastCheckpointRedoLSN` (LSN values, last-write-wins via `Store`)
- `statsResetAt` (timestamp set once at construction)

— are **not** counters and stay on `atomic.*`. `stats.Counter` is
purpose-built for additive integer accumulation; LSNs and timestamps
neither shard cleanly nor benefit from per-P fan-out (an LSN's
intended semantics are "the most recent value", not "the sum of all
writes").

## What landed

| File | Change |
| --- | --- |
| `internal/wal/checkpointer.go` | Add `internal/stats` import; change `Checkpointer.numTimed`, `.numRequested`, `.writeTimeMs` field types from `atomic.Uint64` to `stats.Counter`. `Stats()` reads switch from `.Load()` to `uint64(.Sum())`. The two `.Add(1)` call sites in `runOnce`'s `spread`-branch stay byte-identical (untyped constant `1` accepts both `atomic.Uint64.Add(uint64)` and `stats.Counter.Add(int64)`). The single `.Add(uint64(time.Since(flushStart).Milliseconds()))` site in `flushDirty` simplifies to `.Add(time.Since(flushStart).Milliseconds())` — `time.Duration.Milliseconds()` already returns `int64`; the `uint64` cast was only there for `atomic.Uint64.Add`'s unsigned argument. |

`sync/atomic` import is retained — `lastCheckpointLSN`,
`lastCheckpointRedoLSN`, and `statsResetAt` still use `atomic.*`.
Only the three additive counters move.

## Why this is a good consumer

The Checkpointer is single-goroutine on the write side: the
`Run`/`runOnce` loop owns all three counters. Cross-P contention on
the counter cache lines is therefore not the dominant motivation
here (unlike [[0107-0008h]] MemRing where ≥2 walsender goroutines
concurrently bump `hits`/`misses`, or [[0107-0008i]] AIO where the
`method=worker` goroutine pool bumps the Engine totals from many
worker Gs).

The migration still pays for itself for two reasons:

1. **Uniformity-of-shape across the WAL package's observability surface.**
   `internal/initdb/wal_io_views.go` already binds `pg_stat_wal_io`
   columns against `MemRing` (stats.Counter) and the WAL writer drain
   bytes (stats.Counter). `pg_stat_checkpointer` is rendered by
   `internal/initdb/open.go` from the Checkpointer's `Stats()` struct.
   Both views run at the same stats-collector cadence; both feed the
   same operator dashboards; both should observe the same
   consistency-and-storage shape. Mixing `atomic.Uint64` ("strictly
   sequentially consistent across all readers") with `stats.Counter`
   ("eventually consistent across 256 shards") inside the WAL
   package's view layer would force every reader and every future
   maintainer to remember which counter has which guarantees.
2. **Future-proofing against multi-writer evolution.** The
   single-writer-today invariant is not architectural — it is
   incidental to the current Run loop. A future change that off-loads
   `flushDirty`'s cost or shards the spread-checkpoint pacer across
   helper goroutines would immediately make the `writeTimeMs` writes
   multi-writer. Migrating now means that change lands without
   re-touching the counter shape.

The cold-path reader is `Checkpointer.Stats()`, called only from
`registerStatCheckpointerView` at the user's `SELECT * FROM
pg_stat_checkpointer` cadence. The cross-shard `Sum` (256 atomic
loads per counter × 3 counters = 768 atomic loads) is paid once per
view query — negligible against the rest of the row-build path.

## Public observation semantics preserved

- `Stats` struct fields (`NumTimed`, `NumRequested`, `WriteTimeMs`)
  keep their `uint64` types and observed values. The `uint64(.Sum())`
  cast at the boundary preserves the exact numeric value of the
  total (`stats.Counter.Sum() int64` is always non-negative in
  practice because all three counter sources are non-negative deltas;
  conversion to `uint64` is bit-identical for values in
  `[0, math.MaxInt64]`, which covers any plausible cumulative
  checkpoint count or millisecond total — even 10 ms per cycle at
  1 cycle/s for 100 years is 3.15 × 10¹⁰ ms, well within `int64`).
- `pg_stat_checkpointer.{num_timed, num_requested, write_time_ms}`
  columns in `internal/initdb/open.go::registerStatCheckpointerView`
  format via `fmt.Sprintf("%d", uint64Value)`; the column-render path
  observes identical decimal strings.
- No external consumer reads the counters directly — every observer
  goes through `Stats()` (or the view it backs). The migration is
  invisible past the `Checkpointer` boundary.

## Caller-facing behaviour preserved

- No tests assert these counter values directly via the unexported
  fields (verified by `grep -rln 'NumTimed\|NumRequested\|WriteTimeMs'
  internal/ --include='*_test.go'` returning empty); the public
  `Stats()` accessor is the only assertion path, and that path
  preserves bit-identical output.
- `pg_stat_checkpointer` virtual-table registration in
  `internal/initdb/open_test.go::TestOpenRegistersStatCheckpointerView`
  continues to find the row provider intact; counter values surface
  as plain decimal strings exactly as before.

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Three per Checkpointer =
**48 KiB / server** (the Checkpointer is a singleton — one per WAL
writer per cluster). Flat against any imaginable working set.

Cumulative `stats.Counter` cost across the M0107-0008 migrations on
a single server (singleton consumers only):

| Consumer | Counters | Bytes |
| --- | --- | --- |
| MemRing ([[0107-0008h]]) | 2 | 32 KiB |
| AIO Engine totals ([[0107-0008i]]) | 3 | 48 KiB |
| AIO per-direction ([[0107-0008j]]) | 8 | 128 KiB |
| WAL drain bytes ([[0107-0008k]]) | 2 | 32 KiB |
| **Checkpointer (this loop)** | **3** | **48 KiB** |

Total for the five singleton consumers = **288 KiB per server**,
flat. Trivial against the GiB-scale buffer pool / WAL buffer
working set.

Per-BTree consumer ([[0107-0008g]]) is excluded from the singleton
total because its cost scales with index count (32 KiB / index, not
per server).

## Why not migrate `lastCheckpointLSN` / `statsResetAt` too

The other Checkpointer atomics are not counters:

- **`lastCheckpointLSN` / `lastCheckpointRedoLSN`** — LSN values
  written via `.Store(lsn)` after each successful checkpoint. Reader
  semantics are "the most recent value", not "the sum of all stores".
  Sharding would be meaningless (across-shard sum of a stream of
  monotonic LSNs is not the latest LSN). Keeping them on
  `atomic.Uint64` is the right shape; the writer is single-goroutine
  (the Checkpointer Run loop) so there's no contention to relieve
  anyway.
- **`statsResetAt`** — set exactly once at `NewCheckpointer` time and
  read on every `Stats()` call. Single-writer-then-read-only; the
  cache line lands in the snapshot of any reader's working set and
  stays there. No contention possible.

## Follow-ups (separate loops)

- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the
  remaining `runtimeshim` consumer in M0107-0008 scope; lands after
  the bufpool lock-free rewrite (M0107-0006) per the M0107
  sub-milestone ordering. Unchanged from [[0107-0008k]].
- **Per-target AIO `*targetStats` migration** — formally closed as
  *do not migrate* by [[0107-0008j]] on memory-amplification grounds.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` so a runtime-symbol
  break in either surfaces in CI rather than in the field. Depends
  on the project's CI scaffolding being in place (no
  `.github/workflows/` in the repo today).

## What we measured

```
$ go test -race -count=1 -timeout 180s ./internal/wal/ ./internal/stats/
ok   github.com/goopg/goopg/internal/wal    3.095s
ok   github.com/goopg/goopg/internal/stats  1.020s

$ go test -race -count=1 -timeout 180s ./internal/storage/ ./internal/aio/ ./internal/runtimeshim/
ok   github.com/goopg/goopg/internal/storage     5.367s
ok   github.com/goopg/goopg/internal/aio         1.041s
ok   github.com/goopg/goopg/internal/runtimeshim 1.224s
```

`internal/initdb/...` shows pre-existing failures unrelated to this
change (the same set documented in loops 5/9/10/11).

## PG-compat impact

None. The Checkpointer aggregate counters are observability
instrumentation backing the goopg `pg_stat_checkpointer` virtual
table. The view's column shape (`num_timed BIGINT`, `num_requested
BIGINT`, `write_time_ms BIGINT`, `last_checkpoint_lsn pg_lsn`,
`stats_reset TIMESTAMPTZ`) is unchanged; the column-render path
formats identical decimal strings; the public `Stats` struct field
types are byte-identical. The parent design's PG-compat gate
(invariants §6 Phase D5) is satisfied trivially: no on-disk format,
WAL record format, catalog heap-tuple layout, or wire-protocol
bytes change.
