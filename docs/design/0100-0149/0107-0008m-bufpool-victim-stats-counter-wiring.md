# Design: Bufpool dirty-victim counters via `stats.Counter` (M0107-0008, loop 13)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](../../perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessors**: [`0107-0008f-perp-stats-counter.md`](0107-0008f-perp-stats-counter.md), [`0107-0008g-btree-stats-counter-wiring.md`](0107-0008g-btree-stats-counter-wiring.md), [`0107-0008h-memring-stats-counter-wiring.md`](0107-0008h-memring-stats-counter-wiring.md), [`0107-0008i-aio-engine-totals-stats-counter-wiring.md`](0107-0008i-aio-engine-totals-stats-counter-wiring.md), [`0107-0008j-aio-per-direction-stats-counter-wiring.md`](0107-0008j-aio-per-direction-stats-counter-wiring.md), [`0107-0008k-wal-buffer-drain-stats-counter-wiring.md`](0107-0008k-wal-buffer-drain-stats-counter-wiring.md), [`0107-0008l-checkpointer-stats-counter-wiring.md`](0107-0008l-checkpointer-stats-counter-wiring.md)
**Filed**: 2026-05-21

## Scope of this loop

Migrate the seventh concrete consumer of the
[`stats.Counter`](0107-0008f-perp-stats-counter.md) primitive: the
buffer pool's dirty-victim instrumentation pair in
`internal/storage/bufpool.go` —

| Field | Semantics |
| --- | --- |
| `totalVictimCount` | Foreground evictions that displaced a valid page. Bumped once per `chooseVictimSlot` success when `wasValid == true`. |
| `dirtyVictimCount` | Subset of the above where the displaced page was dirty (an actual write-out was needed before reuse). |

The pair feeds the bgwriter DoD metric `DirtyVictimRate() float64 =
dirtyVictimCount / totalVictimCount`. The bgwriter goroutine
(M0048-0003) targets this ratio at ≤ 5 %: as long as bgwriter is
flushing fast enough, foreground pin paths almost never see a dirty
victim.

The two counters move from `atomic.Int64` to `stats.Counter`. No
other bufpool field changes: per-slot `state`, `clockHand`,
`tombstones`, and `bufmap` atomics are not additive counters and
stay on `atomic.*`.

## What landed

| File | Change |
| --- | --- |
| `internal/storage/bufpool.go` | Add `internal/stats` import; change `Pool.dirtyVictimCount` / `Pool.totalVictimCount` field types from `atomic.Int64` to `stats.Counter`. `DirtyVictimRate()` reads switch from `.Load()` to `.Sum()`. `ResetVictimStats()` switches from `.Store(0)` to `.Reset()`. The two `.Add(1)` call sites in `chooseVictimSlot`'s `wasValid` branch stay byte-identical (untyped constant `1` accepts both `atomic.Int64.Add(int64)` and `stats.Counter.Add(int64)`). |

`sync/atomic` import is retained — every other bufpool atomic
(`Slot.state`, `Pool.clockHand`, `Pool.tombstones`, the `bufmap`
key/val triplet, the stress-test counters) still uses `atomic.*`.
Only the two additive victim counters move.

## Why this is a good consumer

`chooseVictimSlot` runs on the foreground `Pin` path whenever the
pool needs a free slot — i.e. on every cache miss against a cold
data page. Multiple client backends evict concurrently (different
goroutines / different Ps), so the previously-shared
`totalVictimCount` and `dirtyVictimCount` cache lines bounced on
every cross-backend miss. Under a c=100 pgbench standard load with
shared_buffers smaller than the active set, evictions reach hundreds
of thousands per second; the `totalVictimCount` line was the hot one
because every valid-page eviction bumps it.

Per-P sharding via `stats.Counter` keeps each backend's bump on its
current P's shard line — no cross-core invalidation.

The cold-path reader is the bgwriter goroutine via `DirtyVictimRate()`
(called every bgwriter tick, ~once per `bgwriter_delay = 200 ms`), and
test code via `pool.ResetVictimStats()` / `DirtyVictimRate()`. The
cross-shard `Sum` (256 atomic loads per counter × 2 counters = 512
atomic loads per tick) is paid every ~200 ms — negligible.

## Public observation semantics preserved

- `DirtyVictimRate() float64` returns the same numeric ratio. The
  divisor and dividend are both 256-shard sums of strictly
  non-negative deltas; `int64` overflow would require a sustained
  ~10⁹ evictions per second for ~290 years to wrap, so the cast to
  `float64` in the ratio computation observes identical values.
- `ResetVictimStats()` semantics — "every observer sees zero after
  the call returns" — are preserved by `stats.Counter.Reset()`
  (which performs 256 atomic stores, one per shard, in a single
  function call). The bgwriter test (`TestBgwriterDoDDirtyVictimRate`)
  calls `ResetVictimStats()` between phases and asserts the next
  `DirtyVictimRate()` reads observe only post-reset evictions; this
  ordering is preserved because the per-shard `atomic.Int64.Store`
  inside `Reset` and the per-shard `atomic.Int64.Add` inside the
  next eviction are serialised via the underlying cache-coherence
  protocol.

## Caller-facing behaviour preserved

- The only callers of `DirtyVictimRate()` are the bgwriter loop
  (`internal/storage/bgwriter.go`) and the DoD test
  (`internal/storage/bgwriter_test.go::TestBgwriterDoDDirtyVictimRate`).
  Both observe the same ratio.
- The only callers of `ResetVictimStats()` are the DoD test
  (`internal/storage/bgwriter_test.go:193`). The test continues to
  PASS unchanged.
- No external consumer reads the unexported counter fields directly.

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Two per Pool =
**32 KiB / server** (one Pool per running server). Flat against any
imaginable working set, and small compared to the buffer pool's own
~GiB-scale slot array.

Cumulative `stats.Counter` cost across the M0107-0008 migrations on
a single server (singleton consumers only):

| Consumer | Counters | Bytes |
| --- | --- | --- |
| MemRing ([[0107-0008h]]) | 2 | 32 KiB |
| AIO Engine totals ([[0107-0008i]]) | 3 | 48 KiB |
| AIO per-direction ([[0107-0008j]]) | 8 | 128 KiB |
| WAL drain bytes ([[0107-0008k]]) | 2 | 32 KiB |
| Checkpointer ([[0107-0008l]]) | 3 | 48 KiB |
| **Bufpool victim stats (this loop)** | **2** | **32 KiB** |

Total for the six singleton consumers = **320 KiB per server**, flat.
Trivial against the GiB-scale buffer pool / WAL buffer working set.

Per-BTree consumer ([[0107-0008g]]) is excluded from the singleton
total because its cost scales with index count (32 KiB / index).

## Why not migrate `Slot.state` / `clockHand` / `tombstones` / `bufmap` keys too

The other bufpool atomics are not additive counters:

- **`Slot.state`** — packed 64-bit state word with pin / usage /
  dirty / valid / IO / gen bit-fields. Mutations are CAS, not
  `Add`; the field shape is intrinsically last-write-wins on each
  bit-field (e.g. pin count's CAS-with-retry loop).
- **`Pool.clockHand`** — clock-sweep cursor mutated by
  `atomic.AddInt64` and observed via `Load`. Looks counter-shaped
  but is semantically a *cursor*: readers need the latest value to
  pick the next victim slot, not the cross-shard sum (which would
  be meaningless — "sum of cursor positions" has no interpretation).
- **`Pool.tombstones`** — gauge tracking the *current* tombstone
  count in `bufmap`; bumped on tombstone insertion and `Store(0)`
  on compaction. Readers compare against a threshold to trigger
  compaction; the cross-shard sum would give a stale upper bound
  but compaction is rare enough that contention isn't a concern.
- **`bufmap` `key0` / `key1` / `val` triplet per cell** — the
  hash-table cell's packed payload; reads use CAS-with-retry to
  detect concurrent mutation. Not a counter.

`stats.Counter` is purpose-built for additive integer accumulation;
none of the above match that shape.

## Follow-ups (separate loops)

- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the
  remaining `runtimeshim` consumer in M0107-0008 scope; lands after
  the bufpool lock-free rewrite (M0107-0006) per the M0107
  sub-milestone ordering. Unchanged from [[0107-0008l]].
- **Per-target AIO `*targetStats` migration** — formally closed as
  *do not migrate* by [[0107-0008j]] on memory-amplification grounds.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` so a runtime-symbol
  break in either surfaces in CI rather than in the field.

## What we measured

```
$ go test -race -count=1 -timeout 180s ./internal/storage/ ./internal/stats/
ok   github.com/goopg/goopg/internal/storage     5.368s
ok   github.com/goopg/goopg/internal/stats       1.020s
```

The `internal/storage/` suite includes
`TestBgwriterDoDDirtyVictimRate`, which is the end-to-end assertion
that the bgwriter holds the dirty-victim ratio ≤ 5 % under load —
i.e. the exact callers that touch both counters in the hot path.

## PG-compat impact

None. The bufpool dirty-victim counters are internal observability
instrumentation backing only the bgwriter's own pacing decisions
and the DoD test. They are not surfaced to any
`pg_stat_*` view, do not appear in any wire-protocol message, and
do not affect on-disk format, WAL record framing, CRC, page header,
or any catalog heap-tuple bytes. The parent design's PG-compat gate
(invariants §6 Phase D5) is satisfied trivially.
