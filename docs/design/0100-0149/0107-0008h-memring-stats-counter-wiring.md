# Design: WAL `MemRing` hit/miss counters via `stats.Counter` (M0107-0008, loop 8)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](../../perf-optimize/08-runtime-internals.md) §4 "Use case 2: per-P statistics counters"
**Predecessors**: [`0107-0008f-perp-stats-counter.md`](0107-0008f-perp-stats-counter.md), [`0107-0008g-btree-stats-counter-wiring.md`](0107-0008g-btree-stats-counter-wiring.md)
**Filed**: 2026-05-21

## Scope of this loop

Migrate the second concrete consumer of the [`stats.Counter`](0107-0008f-perp-stats-counter.md)
primitive (after the loop-7 btree counters): the in-memory WAL send-buffer's
`hits` and `misses` counters in `internal/wal/mem_ring.go`. These are the
two counters bumped on every `(*MemRing).ReadAt` — once unconditionally,
along one of two branches (hit when `[pos, pos+len) ⊆ [head, tail)`,
miss otherwise).

The public API — `MemRing.Hits() uint64` and `MemRing.Misses() uint64` —
is unchanged. Only the *internal* storage moves from a pair of
`atomic.Uint64` fields touched via `.Add(1)` / `.Load()` to a pair of
`stats.Counter` values touched via `.Add(1)` / `.Sum()` with a single
`uint64(...)` cast at the public boundary.

## What landed

| File | Change |
| --- | --- |
| `internal/wal/mem_ring.go` | Drop `sync/atomic` import; add `internal/stats` import; change `hits`, `misses` field types from `atomic.Uint64` to `stats.Counter`; update `Hits()` / `Misses()` to return `uint64(r.hits.Sum())` / `uint64(r.misses.Sum())`. The `.Add(1)` call sites in `ReadAt` are byte-identical (both old and new types expose the same `Add(delta) → void` method shape — `int64` for `stats.Counter`, `uint64` for `atomic.Uint64`, both accept the untyped constant `1`). |

`MemRing`'s public methods (`Append`, `ReadAt`, `Range`, `BytesResident`,
`Cap`, `Hits`, `Misses`) keep their signatures and return types. Callers
in `internal/initdb/wal_io_views.go` (`pg_stat_wal_io`) and
`internal/initdb/replication_views.go` (`pg_stat_replication`) read
`ring.Hits()` and `ring.Misses()` as `uint64` and continue to compile
and observe the same values.

## Why this is a good consumer

The parent chapter §4 names "rows scanned, buffers hit, tuples returned"
as the prototypical per-P-counter consumers. `MemRing.hits` /
`MemRing.misses` are exactly that shape:

- **Per-call frequency**: `ReadAt` is invoked once per record (often per
  WAL block) that a walsender needs to stream. Under a multi-subscriber
  workload — one walsender goroutine per active replication connection
  — N concurrent ReadAt callers all bump the same global atomic.
- **Multi-P contention**: each walsender runs on whichever P the
  scheduler picks. With ≥2 active subscribers (the M0094-0005 hot-read
  E2E test, the M0102 heterogeneous-replication failover suite, any
  cascading-replica deployment), the shared cache line for `hits`
  hops between cores on every increment.
- **Cold-path read pattern**: `Hits()` / `Misses()` are read only by the
  `pg_stat_wal_io` and `pg_stat_replication` view builders — that is
  PG's stats-collector cadence (`pg_stat_reset` boundary), not per-frame
  on the hot path. Cross-shard sum is paid once per view query.
- **Read-only-after-construction semantics preserved**: neither field is
  ever `.Store(0)`'d in production code — there is no `Reset()` method
  on `MemRing`, and `pg_stat_wal_io` never zeros the counter. The
  `stats.Counter.Sum()` cross-shard read is therefore the only
  observable operation post-migration; `Reset()` is unused here.

## Why not a smaller change

A drop-in replacement of `atomic.Uint64` → `stats.Counter` could
theoretically be done with a wider one-liner type aliasing — but the
parent design (and the loop-7 migration that preceded this) deliberately
keeps each consumer migration in its own file/loop so each can be
reviewed and reverted independently. The lock-free-bufpool follow-up
(`bufpool.dirtyVictimCount` / `totalVictimCount`) deliberately lands
after [[06-bufpool-lockfree]] for the same reason — it changes the
clock-sweep contention shape, which interacts with the rewrite.

The WAL byte counters in `internal/wal/writer.go`
(`overflowDrainBytes`, `flushDrainBytes`) and the
checkpointer counters (`numTimed`, `numRequested`, `writeTimeMs`) were
considered and rejected:

| Candidate | Storage today | Why not this loop |
| --- | --- | --- |
| `wal.writer.overflowDrainBytes` / `flushDrainBytes` | `atomic.Uint64` | Bumped from a single writer goroutine; no cross-core contention to remove. |
| `wal.checkpointer.numTimed` / `numRequested` / `writeTimeMs` | `atomic.Uint64` | Bumped at checkpoint cadence (~30 s default); call rate ~0.03/s — well below any contention threshold. |
| `aio.Engine.submitted` / `completed` (and per-direction split) | `atomic.Uint64` | High call rate, but the per-direction breakdown is a separate consumer family. Migrating it cleanly couples to the broader `pg_stat_io` view shape, which is a wider unification than one loop should swallow. Worth a follow-up loop on its own. |
| `bufpool.dirtyVictimCount` / `totalVictimCount` | `atomic.Int64` | Clock sweep runs from one P at a time (hand serialised); contention is on the page being evicted, not the counter line. |
| `aio.targetStats.submitted` / `completed` / `errored` (per-target Sync.Map values) | `atomic.Uint64` (inside `*targetStats`) | Same as Engine totals — coupled to the wider `pg_stat_io` per-target shape. Defer to the AIO migration loop. |
| `wal.subscriber_mon.receivedLSN` / `latestEndLSN` / `lastMsgSendUnixNano` | `atomic.Uint64` / `atomic.Int64` | Not a counter — these are "last observed value" fields written via `.Store(...)` overwrites, not additive `.Add(1)` increments. `stats.Counter` is the wrong primitive. |
| `mvcc.procArray.xid` / `xmin` / `inTxn` | `atomic.Uint64` / `atomic.Uint32` | Same — these are slot state, not counters. |

`MemRing.hits` / `.misses` are the next best fit by the same selection
criterion the loop-7 doc named: paired counters in a single struct,
bumped per call on a hot read path, read on the cold path through a
public snapshot API that already returns `uint64`.

## Caller-facing behaviour preserved

- **Type-level**: `MemRing.Hits()` and `.Misses()` keep their `uint64`
  return type. Callers — `internal/wal/mem_ring_test.go`,
  `internal/initdb/wal_io_views.go`, `internal/initdb/replication_views.go`
  — observe the same values they did before the migration.
- **Semantic**: the counters are still "best-effort" — a concurrent
  `ReadAt` may or may not be reflected in a same-instant `Hits()`
  observation. That was also true of `atomic.LoadUint64` (Go's
  memory model: the atomic load is sequentially consistent against
  other atomic accesses, but two non-atomic happens-before chains
  could still see different totals at the same wall-clock instant).
- **Nil-safe**: the `r == nil → return 0` guards in `Hits()` /
  `Misses()` are preserved verbatim. `MemRing == nil` is the
  documented "no ring; every read goes to disk" mode and must keep
  returning zero without panicking.
- **No reset**: `MemRing` exposes no `Reset()`. The `stats.Counter`'s
  `Reset()` method is therefore unused by this caller — consistent
  with the pre-migration code, which also had no reset path.

## What we measured

```
$ go test -race -count=1 -timeout 120s ./internal/wal/
ok   github.com/goopg/goopg/internal/wal   3.098s

$ go test -race -count=1 -timeout 60s ./internal/stats/
ok   github.com/goopg/goopg/internal/stats 1.022s
```

The existing `internal/wal/mem_ring_test.go` already covers the
counter contract end-to-end through the public API:

- `TestMemRingNewZeroCapNil` asserts `r.Hits() == 0 && r.Misses() == 0`
  on a nil ring (the nil-safe guard, verbatim preserved).
- `TestMemRingHitSimple` (line 46) asserts `Hits == 1, Misses == 0`
  after one successful ReadAt.
- `TestMemRingMissAfterEviction` (line 65) asserts `Hits == 1,
  Misses == 1` after one hit and one miss.
- `TestMemRingPartialOverlapMisses` (line 73) asserts the
  half-resident partial-overlap path increments `misses`, not `hits`.
- The walsender-integration test at `mem_ring_test.go:202`
  (`if hits := w.MemRing().Hits(); hits == 0`) asserts the counter
  bumps through the full `wal.Writer → MemRing.ReadAt → walsender`
  path under realistic streaming load.

A dropped increment, torn read, or shard-misallocation regression in
`stats.Counter` would surface in any of these as a count mismatch and
fail the suite. The `stats.Counter` primitive's own race-clean
regression suite (`internal/stats/counter_test.go`) covers the
cross-shard sum correctness invariant directly with stricter
assertions (exact total after 524 288 adds across 32 producers) than
this caller could.

## Memory cost

`stats.Counter` is `256 × 64 B = 16 KiB`. Two per `MemRing` =
**32 KiB / ring**. A goopg server has exactly one `MemRing`
(constructed once at WAL writer init via the `wal_sender_memory_buffer`
GUC; `nil` if disabled), so the total cost is **32 KiB per server**,
flat. Trivial against any realistic working set (the default ring
itself is 16 MiB; the buffer pool is typically GiB-scale).

The cost is bounded by ring count, not by record count or WAL byte
rate.

## PG-compat impact

None. The MemRing send-buffer counters are observability instrumentation
goopg-specific to the in-memory WAL handoff path (see
`docs/design/0010-0002-walsender-in-memory-wal-handoff.md`). PostgreSQL
does not expose `send_buffer_hits` / `send_buffer_misses` natively —
they appear only on goopg's `pg_stat_wal_io` and the
`send_buffer_*` columns of goopg's extended `pg_stat_replication`
view (both built in `internal/initdb/{wal_io_views,replication_views}.go`).
The parent design's PG-compat gate (invariants §6 Phase D5) is
satisfied trivially: no on-disk format, WAL record format, catalog
heap-tuple layout, or wire-protocol bytes change.

## Follow-ups (separate loops)

- **Bufpool per-slot `SemaAcquire`/`SemaRelease` caller** — the
  remaining `runtimeshim` consumer in M0107-0008 scope; lands after
  the bufpool lock-free rewrite ([[06-bufpool-lockfree]]) per the
  M0107 sub-milestone ordering.
- **AIO Engine submitted/completed/errored counters** (and the
  per-direction read/write split) — coupled to a wider `pg_stat_io`
  view-shape unification; worth its own loop.
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` and `internal/stats/` so a runtime-symbol
  break in either surfaces in CI rather than in the field. Depends on
  the project's CI scaffolding being in place (no `.github/workflows/`
  in the repo today).
