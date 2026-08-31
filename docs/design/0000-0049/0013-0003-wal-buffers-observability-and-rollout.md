# 0013-0003 — WAL Buffers Observability and Rollout

**Status:** accepted
**Milestone:** [0013 — WAL Buffers Optimization](../../milestones/0013-wal-buffers-optimization-with-eviction-safe-wal-before-data-durability.md)
**Spans seam:** counters surface, `pg_stat_wal_io` extension, startup log line.
**Cross-links:**
[0013-0001](0013-0001-wal-buffers-architecture.md) (the buffer +
two-stage FlushUpTo this slice exposes),
[0010-0003](0010-0003-wal-direct-io-observability-and-operations.md)
(the existing `pg_stat_wal_io` view we extend).

## Context

M0013-0001 introduced the bounded in-memory WAL buffer; M0013-0002
pinned the WAL-before-data invariant under that buffer with
regression tests. This slice closes M0013 by giving an operator the
SQL-queryable surface needed to verify the buffer is doing what it
should and to triage anomalies (e.g. constant overflow drains
indicating undersized `wal_buffers`).

## Counters

Four lifetime / instantaneous counters live alongside the existing
direct-I/O counters in the writer:

| Counter                     | Meaning                                                        |
|-----------------------------|----------------------------------------------------------------|
| `wal_buffers_capacity_bytes`| The `wal_buffers` GUC value at construction. Static.           |
| `wal_buffers_bytes_resident`| Current `walBuf.resident()` — bytes in RAM not yet drained.     |
| `wal_buffers_overflow_drain_bytes` | Lifetime total bytes drained because an Append would have overflowed. |
| `wal_buffers_flush_drain_bytes`    | Lifetime total bytes drained by `flushUpTo`'s Stage 1.          |

**Why two drain counters and not one?** They differentiate two very
different operator concerns. High `overflow_drain_bytes` means the
buffer is sized too small for the workload's append rate — bumping
`wal_buffers` would help. High `flush_drain_bytes` means commits /
evictions / checkpoints are forcing many small drains — typical and
healthy; the operator sees this as the buffer's "natural" cost of
durability rather than as a sizing problem.

The two counters are independent: a Stage-1 drain that *also*
crossed the overflow threshold would be very rare under correct
operation (FlushUpTo is supposed to win the race), so attributing
each drain call to exactly one bucket keeps the semantics clean
without a third "both" bucket.

## Implementation

A new struct mirrors the directIOCounters pattern — a single
allocation owned by the Writer and shared with state via pointer:

```go
type walBufferCounters struct {
    overflowDrainBytes atomic.Uint64
    flushDrainBytes    atomic.Uint64
}
```

`state.append`'s overflow-drain branch and `state.flushUpTo`'s
Stage 1 branch each pass a distinct entry-point that increments
the right counter inside `drainBufferBytes`. The helper grows a
`reason` parameter (an enum or two top-level wrappers) so the
counter classification stays at the call site rather than guessing
from context.

Resident bytes is a live read: `walBuf.resident()` under the
writer goroutine's serialisation. The view does the read while
the writer is idle on the channel — same pattern as `MemRing.BytesResident()`.

## SQL surface

Extending `pg_catalog.pg_stat_wal_io` rather than adding a sibling
view. The operator already SELECTs from this view to triage WAL
I/O; adding four columns keeps the "one place to look" experience.
The four new columns appear at the end:

```
direct_io_active, direct_io_fallback_reason,
direct_writes, tail_rmw_writes,
send_buffer_capacity_bytes, send_buffer_bytes_resident,
send_buffer_hits, send_buffer_misses,
wal_buffers_capacity_bytes, wal_buffers_bytes_resident,
wal_buffers_overflow_drain_bytes, wal_buffers_flush_drain_bytes
```

When the writer is configured with `wal_buffers=0` the buffer is
disabled; the four columns render `0`/`0`/`0`/`0` (the static-zero
case the existing view already handles for `direct_writes` etc.).

## Startup log line

`cmd/goopg start` adds one new event:

```
event=wal_buffers_attached capacity_bytes=16777216
```

Logged when `wal_buffers > 0` resolves through the GUC at startup.
Suppressed when `wal_buffers=0` (no buffer to surface). Mirrors the
existing `event=wal_sender_memory_buffer_attached` shape so an
operator can grep one vocabulary across both.

## Tests

Tests live in two places to avoid an import cycle:

- `internal/wal/wal_buffer_test.go` (existing): add
  `TestWALBufferCountersTrackDrains` — Append past cap to bump
  `OverflowDrainBytes`; FlushUpTo to bump `FlushDrainBytes`;
  assert each counter moves only when its trigger fires.
- `internal/initdb/wal_io_views_test.go`: extend the existing
  `pg_stat_wal_io` row-shape test with the four new columns
  (capacity matches GUC, resident moves with appends, drain
  counters expose nonzero after a forced drain).

## Out of scope

- Histograms / percentiles for drain sizes — over-engineered for
  v0; the four lifetime counters answer 99% of triage questions.
- Background-drain (`wal_writer_delay`-like scheduler) — milestone
  declares it out of scope.
- Operator playbook beyond what fits in the existing
  `0013-0001-wal-buffers-architecture.md` Out-of-scope section.

## Closes M0013

After this slice the milestone DoD is fully covered:

| DoD item                                              | Where it lives                        |
|-------------------------------------------------------|---------------------------------------|
| #1 wal_buffers exists with default 16 MB             | M0013-0001 GUC                       |
| #2 Append keeps WAL in memory while within capacity   | M0013-0001 state.append branching     |
| #3 Overflow drains in strict LSN order, no gaps       | M0013-0001 drainBufferBytes           |
| #4 Drained bytes tracked as dirty WAL sync debt       | state.dirty bookkeeping (existing)    |
| #5 FlushUpTo two-stage                                | M0013-0001 flushUpTo                  |
| #6 Eviction enforces WAL durability through pageLSN   | M0013-0002 (test pinned)              |
| #7 Checkpoint enforces same                           | M0013-0002 (test pinned)              |
| #8 Regression tests for normal/overflow/forced/failure| M0013-0001/0002 (this slice adds counter coverage) |
| #9 Required design docs accepted                      | 0013-0001/0002/0003                   |
