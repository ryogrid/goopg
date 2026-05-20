# Design: btree write-path counters via `stats.Counter` (M0107-0008, loop 7)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessor**: [`0107-0008f-perp-stats-counter.md`](0107-0008f-perp-stats-counter.md)
**Filed**: 2026-05-21

## Scope of this loop

Migrate the first concrete consumer of the [`stats.Counter`](0107-0008f-perp-stats-counter.md)
primitive: the btree's per-tree `Inserts` and `Splits` write-path
counters in `internal/access/btree/btree.go`. These are the two
counters bumped on every `(*BTree).Insert` call (once unconditionally,
plus once on the split-path retry) and read by the M0055 baseline /
Phase-B regression harnesses via `(*BTree).Stats`.

The public `BTreeStats` struct — the snapshot returned by `Stats()` —
is unchanged. Only the *internal* storage backing the counters moves
from a pair of plain `uint64` fields touched via `atomic.AddUint64` /
`atomic.LoadUint64` / `atomic.StoreUint64` to a private
`btreeStatsCounters` holding two `stats.Counter` values.

## What landed

| File | Change |
| --- | --- |
| `internal/access/btree/btree.go` | Add `internal/stats` import; introduce `btreeStatsCounters{ inserts, splits stats.Counter }`; change `BTree.stats` field type from `BTreeStats` to `btreeStatsCounters`; rewrite `Stats()` to call `.Sum()` on each counter; rewrite `ResetStats()` to call `.Reset()`; replace the two hot-path `atomic.AddUint64(&bt.stats.*, 1)` calls with `bt.stats.*.Add(1)`. |

`BTreeStats` (the public snapshot type) is unchanged. Tests and
benchmarks that read `bt.Stats().Inserts` / `bt.Stats().Splits`
continue to compile and observe the same values.

## Why this is the right first consumer

The parent chapter §4 names "rows scanned, buffers hit, tuples
returned" as the kind of consumer the per-P primitive is built for —
counters that fire per-row or per-record on a write/read hot path.
The btree's `Inserts` and `Splits` are exactly that shape:

- `Insert` runs once per index tuple. At the M0055 baseline bench's
  steady-state 22.7 K inserts/sec on a single tree, that's already
  past the threshold where atomic-on-a-shared-cache-line shows up as
  cross-core traffic; under multi-writer workloads (which the M0055
  multi-writer stress test exercises with up to 32 concurrent
  writers) the same global atomic is the hottest non-page line in
  the working set.
- `Splits` increments only on the slow path, but the slow path is
  *already* serialised under `bt.splitMu` — the gain there is small.
  The reason both counters move in the same loop is to keep
  `btreeStatsCounters` a single struct (one field on `BTree`, one
  reset, one snapshot) rather than mixing per-P and global storage
  for two semantically-identical counters.

The `BTreeStats` snapshot is `read by tests and benchmarks at the end
of a run` — the canonical cold-path consumer pattern the
`stats.Counter` design anticipates. No production code path reads
`bt.Stats()` per row.

## Why not the other candidates first

The parent chapter §4 enumerates "rows scanned, buffers hit, tuples
returned" as illustrative; the actual candidates in the current
codebase are:

| Candidate | Storage today | Why deferred to a later loop |
| --- | --- | --- |
| btree `Inserts`/`Splits` (this loop) | `BTreeStats` field, atomic on `uint64` | **selected** |
| executor `Operator.RowsReturned` / `RowsScanned` style | not yet a uniform counter — each operator carries its own ad-hoc field, sometimes a plain int, sometimes per-batch | a stats.Counter migration here would couple with a wider unification of the per-operator metric shape; doing both in one loop would obscure the migration delta |
| bufpool `Hits`/`Misses` | not currently exposed as global counters; `internal/storage/bufpool.go`'s instrumentation lives inside individual partitions | the bufpool partitioning rewrite ([[06-bufpool-lockfree]]) is the right place to introduce per-partition stats — see the M0107-0008 "bufpool per-slot Sema wait caller" follow-up |
| WAL `BytesWritten` | per-segment counter inside `internal/wal/writer.go` already laid out for per-writer locality | already low-contention; no per-P split would visibly help |

Picking btree first keeps this loop scoped to one file and one type;
the other migrations have prerequisites in adjacent rewrites and
land cleanest after those.

## Caller-facing behaviour preserved

- **Type-level**: `BTreeStats` (the public snapshot struct) has the
  same field set, same field types (`Inserts uint64`,
  `Splits uint64`), and same zero value. Callers that compare
  `bt.Stats() == BTreeStats{}` or marshal the struct see no change.
- **Semantic**: `Stats()` is still a "best-effort" snapshot — a
  concurrent insert may or may not be observed; the same was true of
  the old `atomic.LoadUint64` pair. The doc comment on `Stats()` is
  unchanged.
- **Reset**: `ResetStats()` may interleave with a concurrent insert
  the same way the old `atomic.StoreUint64(0)` could. The
  `Counter.Reset()` doc explicitly tolerates this; the prior
  `atomic.Store(0)` was equally tolerant (a concurrent Add could
  land in the very moment after the Store, surviving the reset).

## What we measured

```
$ go test -race -count=1 -timeout 120s ./internal/access/btree/...
ok   github.com/goopg/goopg/internal/access/btree   13.365s

$ go test -race -count=1 -timeout 60s ./internal/stats/...
ok   github.com/goopg/goopg/internal/stats          1.021s
```

The M0055 baseline regression bench ran the new code path
end-to-end:

```
M0055-baseline-summary {
  inserts=100000 total_ms=4409.94 inserts_per_sec=22676
  splits=352 (0.35 %) p50_us=38 p95_us=72 p99_us=92 max_us=2958
  rss_delta_mb=5.5
}
M0055-Phase-B-summary { inserts=100000 distinct_keys=100 splits=406 }
```

Both runs report the correct `inserts` / `splits` totals through
`Stats().Sum()`, matching the issued workload exactly — i.e. the
per-P-sharded accumulation followed by the cross-shard sum gives the
same observable count the old global atomic did. Throughput is in
the same band as the pre-migration baseline run on the same hardware
(this loop did not gate on a throughput-improvement number; the
follow-up multi-writer stress sweep in the M0107 milestone-close
gate is where the per-P win shows up as a measured TPS lift).

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Two per BTree =
**32 KiB / tree**. A database with ~100 indexes carries ~3 MiB of
counter storage — trivial against any realistic working set (the
buffer pool alone is typically GiB-scale). The cost is bounded by
index count, not by row count or transaction rate.

## Test coverage rationale

No new tests landed in this loop. The existing btree test suite
already covers the counter contract end-to-end through `bt.Stats()`:

- `TestBenchBaseline_M0055` and `TestBenchDedupRetention_M0055_Phase_B`
  in `bench_baseline_test.go` assert exact counter totals after
  N=100 000 inserts. A torn read, dropped increment, or
  shard-misallocation regression in `stats.Counter` would surface as
  a count mismatch and fail these tests.
- The `stats.Counter` primitive's own race-clean regression suite
  (`internal/stats/counter_test.go`) covers the cross-shard sum
  correctness invariant directly — adding a duplicate caller-side
  test would not catch a class of bug those tests don't already.

Adding a btree-specific "concurrent insert counter total" test would
be low-signal: it would exercise the same Counter contract the stats
package already exercises with stricter assertions (exact total
after 524 288 adds across 32 producers), against a higher-overhead
test harness (full btree page lifecycle vs. a bare counter loop).

## PG-compat impact

None. The btree write-path counters are debug/regression
instrumentation; PostgreSQL does not expose them on any wire path or
catalog. The parent design's PG-compat gate (invariants §6 Phase D5)
is satisfied trivially.

## Follow-ups (separate loops)

- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the
  remaining `runtimeshim` consumer in M0107-0008 scope. The shim
  itself landed in [[0107-0008c]]; the bufpool consumer lands in its
  own loop per [`06-bufpool-lockfree`](perf-optimize/06-bufpool-lockfree.md) §4.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` so a runtime-symbol
  break in either surfaces in CI rather than in the field.
- **Further `stats.Counter` consumer migrations** (executor row
  counters, bufpool hit/miss after the lockfree rewrite,
  WAL byte counters) — each as its own loop, each pre-dated by the
  prerequisite adjacent refactor where applicable.
