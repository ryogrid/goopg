# Walsender In-Memory WAL Handoff (M0010)

- status: accepted
- date: 2026-04-29
- supersedes: —

## Goal

Give walsender a RAM-resident copy of recently-written WAL bytes so
streaming replication doesn't depend on OS page-cache residency.
Pairs with M0010-0001 (`wal_direct_io=on`) — without the page cache
warming WAL writes, every sender pread would hit disk; the
in-memory ring closes that gap.

Sized via the new `wal_sender_memory_buffer` GUC (default 16 MiB,
0 disables). The wal.Writer feeds every successful
`state.writeAt` into the ring; `RecordIterator` (the byte-source
walsender uses) consults the ring before falling back to per-segment
pread syscalls. Lagging senders that fall outside the retention
window cleanly miss and stream from disk — no protocol regression.

## Non-goals

- **Replacing disk reads entirely.** The ring covers the recent
  window. Logical-replication catch-up after a long disconnect
  still pages through historical segments. The existing
  `readSegmentSlice` path is the disk fallback.
- **Per-sender backpressure.** Eviction is FIFO by LSN; a single
  slow sender doesn't pin the ring. Senders that fall out of the
  window resume from disk without dropping the connection.
- **Cross-segment stitching of partial hits.** A read that
  straddles the resident/evicted boundary returns a miss; the
  iterator fetches the whole range from disk in one go. Avoids
  the complexity of two-source byte assembly for a marginal hit-
  rate gain.
- **`fsync` semantics.** The ring is a sender hot-path
  optimisation. `flushUpTo` still drives `fdatasync` on the
  segment fd; durability is unchanged.

## File map

| File | Role |
| --- | --- |
| `internal/wal/mem_ring.go` | `MemRing` type — fixed-size byte ring keyed by 0-based LSN-byte position. `Append(pos, data)` mirrors writer bytes; `ReadAt(pos, out)` returns `(n, true)` on full hit, `(0, false)` on miss; `Hits()` / `Misses()` for observability. Single `sync.RWMutex`: writers Lock briefly during memcpy + head/tail advance; readers RLock during their own memcpy so the ring's bytes can't be overwritten under them. |
| `internal/wal/writer.go` | New `Config.SenderMemoryBuffer int64`. `state.memRing = NewMemRing(cfg.SenderMemoryBuffer)`. `state.append` calls `s.memRing.Append(writePos, record)` AFTER `state.writeAt` succeeds — a failed pwrite must not leave the ring with bytes the disk doesn't have. New `Writer.MemRing()` accessor. |
| `internal/wal/iterator.go` | `readBytesAt` consults `it.writer.MemRing().ReadAt(pos, out)` first; on hit returns from RAM, on miss falls through to the legacy per-segment pread loop. |
| `internal/config/defaults.go` | Registers `wal_sender_memory_buffer` GUC (`TypeInt`, default `16777216`, range `[0, 1 GiB]`, `ContextPostmaster`). |
| `internal/initdb/open.go` | `OpenOptions.WALSenderMemoryBuffer` plumbs to `wal.Config.SenderMemoryBuffer`. |
| `cmd/goopg/main.go` | Reads the GUC; logs `event=wal_sender_memory_buffer_attached capacity_bytes=N` at startup when the ring is configured. |
| `internal/wal/mem_ring_test.go` | Unit tests for the ring + integration tests for the iterator hit / miss paths. |

## Concurrency model

Single `sync.RWMutex` on `MemRing`:

- **Writer** (the wal.Writer's loop goroutine, post-`writeAt`):
  takes the write lock, memcpys data into the ring at
  `tail % cap`, advances `tail`, evicts head if needed. Total
  hold time ≈ memcpy of `len(record)` bytes.
- **Readers** (a walsender goroutine per active replication
  connection): take the read lock, validate `[pos, pos+n) ⊆
  [head, tail)`, memcpy out, release. Multiple readers run in
  parallel.

The memcpy happens **under the lock** (not after a snapshot), so
a concurrent eviction can't free the bytes mid-read. This costs
brief lock contention on the writer side; the alternative
(snapshot-then-copy) requires a per-byte stable mapping which
defeats the wrap-around storage. With a 16 MiB ring the writer's
memcpy is ≤ 16 MiB; in practice each Append is ≤ a few KiB so
read-side latency impact is negligible.

The `hits` / `misses` atomic counters live outside the mutex —
incremented under RLock so observability code can read them
without taking any wal lock.

## Eviction & LSN semantics

`tail` mirrors the writer's `writePos`: every successful
`state.writeAt(pos, recBytes)` is followed by
`memRing.Append(pos, recBytes)`, which advances tail by
`len(recBytes)`. When `tail - head > cap`, head advances to
`tail - cap`. `head` is the LSN-byte of the oldest resident byte;
reads at `pos < head` always miss.

The reset path (`pos != r.tail`) handles the post-recovery /
post-restart case where the writer's first Append starts at a
non-zero `writePos`. The ring drops any stale bytes (none yet,
because Append is the only writer) and rebases `head = tail =
pos`. Subsequent appends resume normally.

A single Append larger than `cap` (e.g. a 32 MiB record into a
16 MiB ring) keeps only the trailing `cap` bytes; head and tail
re-anchor at `pos + len - cap`. The bytes at offsets `[pos,
head)` are by construction never resident.

## Hit / miss accounting

Both atomic counters bump exactly once per `ReadAt`:

- **Hit**: full range `[pos, pos+n)` ⊆ `[head, tail)`. `hits++`,
  return `(n, true)`.
- **Miss**: any portion of the range outside `[head, tail)`.
  `misses++`, return `(0, false)`. The caller does NOT
  re-attempt against the ring after disk fallback (one decision
  per read).

`Cap()`, `Hits()`, and `Misses()` are stable for the writer's
lifetime once configured. M0010-0003's `pg_stat_replication`
columns (`send_buffer_hits`, `send_buffer_misses`,
`send_buffer_bytes_resident`) read these accessors directly.

## Interaction with `wal_direct_io`

The ring is independent of `wal_direct_io` — both can be on, off,
or one without the other. But the rationale is symbiotic:

- **Both on** (the M0010 production target): WAL writes bypass
  page cache; senders read from RAM ring. Best case for
  cache-pressure-bound primaries.
- **Direct-I/O on, ring off**: senders pay disk reads for every
  byte. Strictly worse than legacy. Operators are expected to
  keep `wal_sender_memory_buffer > 0` whenever
  `wal_direct_io=on`; the startup logger surfaces both flags so
  misconfigurations are catchable.
- **Direct-I/O off, ring on**: ring is redundant with the page
  cache, but harmless — every sender read hits the ring before
  the page cache, saving a syscall per record. Marginal win.
- **Both off**: legacy behaviour, no overhead.

## Tests

Unit tests on `MemRing`:

- `TestMemRingNilSafe` — `NewMemRing(0)` returns nil; every
  method on nil is no-op or zero-return.
- `TestMemRingRoundTripWithinCap` — basic faithful byte
  round-trip; counters bump.
- `TestMemRingEvictsOldBytesOnOverflow` — writes past cap evict
  head; reads of evicted bytes miss, residents hit.
- `TestMemRingPartialOverlapMisses` — read straddling
  head boundary returns `(0, false)` (no two-source stitching).
- `TestMemRingWraps` — write that crosses the buffer's wrap
  boundary still round-trips.
- `TestMemRingWriteLargerThanCap` — single Append longer than
  the ring keeps only the trailing cap bytes.
- `TestMemRingConcurrentReads` — 4 readers vs 100 writes; no
  data races (Go's race detector enforces correctness; pinned
  by the Lock-during-memcpy invariant).

Integration tests:

- `TestIteratorReadsFromMemRing` — with the ring configured AND
  the on-disk segment removed before iteration, the iterator
  still streams (proving the read came from RAM). Asserts
  `MemRing.Hits() > 0` post-Next.
- `TestIteratorFallsBackToDiskWithoutRing` — same scenario with
  `SenderMemoryBuffer=0`; the iterator's pread succeeds (the
  segment file is intact). Pins that the ring is genuinely
  optional and the legacy disk path is unchanged.

## Cross-references

- `docs/design/root-0008-wal-and-recovery.md` — overall WAL
  architecture; this doc adds the "writer mirrors recent bytes
  to RAM" axis.
- `docs/design/0010-0001-wal-direct-io-write-path.md` — the
  motivating sister slice. With direct I/O on and the ring on,
  senders never depend on page cache residency.
- `docs/design/0005-0001-streaming-replication-architecture.md` —
  walsender's iterator-driven byte source. This doc swaps the
  byte source from "always disk" to "ring then disk".
- `docs/design/0007-0001-wal-segment-preallocation.md` — the
  Append → writeAt write path the ring tees off.

## Upstream references

- `postgres/src/backend/replication/walsender.c` — upstream's
  walsender flow. PG 18 keeps the WAL-data send path on the
  page cache; our ring is the goopg-specific equivalent that
  decouples sender throughput from page-cache residency.
- `postgres/src/backend/access/transam/xlog.c` — `XLogCtl->pages`
  is upstream's in-shared-memory WAL buffer. Conceptually
  similar (RAM mirror of recent WAL), but goopg's ring is
  sender-side only — the writer's own buffering is M0010-0001's
  per-write RMW scratch, not this ring.
