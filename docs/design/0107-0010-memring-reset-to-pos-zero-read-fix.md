# 0107-0010 MemRing ResetToPos: walsender zero-read fix

**Status**: accepted  
**Accepted**: 2026-05-21

## Problem

`TestE2E_FailoverGoopgToPG/async` failed with:

```
invalid magic number 0000 in WAL segment 000000010000000000000001, LSN 0/1000000, offset 0
```

The PG standby's walreceiver connected to the goopg primary and streamed WAL starting at
`0/1000000` (start of segment 1). The first 8 KiB page it received had all-zero bytes instead
of a valid WAL page header (`xlp_magic = 0xD119`).

## Root cause

`MemRing` is an in-memory ring buffer that walsenders use to serve WAL without disk reads.
It is created in `loadState` via `NewMemRing(cfg.SenderMemoryBuffer)` with `head=tail=0`.

In Path B (async-drain stripe writer, M0107-0007), WAL bytes are written to the MemRing via
`AdvanceWindow + WriteReserved + PublishUpTo`, NOT via the old `Append` method. The `Append`
method had a reset branch (`if pos != r.tail { r.head = pos; r.tail = pos }`) that would set
`head = writePos` on the first call; the new Path B write sequence has no equivalent reset.

After the first Path B append at `writePos ≈ 0x105CEA0`:

```
AdvanceWindow(0x105CF00)          → head = max(0, 0x105CF00 - cap) ≈ 0xF6E00
WriteReserved(0x105CEA0, bytes)   → writes at ring offset 0x105CEA0 % cap
PublishUpTo(0x105CF00)            → tail = 0x105CF00
```

Now `ReadAt(0x1000000, 8)` checks:

```
pos >= head    → 0x1000000 >= 0xF6E00  ✓
end <= tail    → 0x1008000 <= 0x105CF00 ✓
```

`ReadAt` returns **true** and copies bytes from `buf[0x1000000 % cap]` — but that slot was
never written; it contains zeros from `make([]byte, cap)`. The walsender serves those zeros
to the PG standby, which sees `xlp_magic = 0x0000`.

The on-disk segment file at `pg_wal/000000010000000000000001` offset 0 has a valid page
header (written by initdb / the prior writer), so the bug only manifests via the MemRing
path, not the disk-fallback path.

## Fix

Add `MemRing.ResetToPos(pos int64)` which sets `head = tail = pos` under the write lock,
making the ring appear empty at the given LSN anchor. `ReadAt` returns false for any
position before `pos`.

In `loadState`, call `st.memRing.ResetToPos(writePos)` after creating the ring. This
anchors the ring at `writePos` (the last WAL position from a previous session, discovered
by `detectWritePos`). When the first new record is appended at `writePos`, `AdvanceWindow`
sets `head = max(writePos, end - cap) ≈ writePos`, keeping all prior positions inaccessible.
The walsender's `ReadAt` misses for any LSN before `writePos` and falls back to disk, which
has the correct data.

For a fresh cluster (no previous WAL, `writePos = 0`), `ResetToPos(0)` is a no-op (same as
the default state). The bug only manifests on restarts where `writePos > 0` (e.g., after
initdb wrote WAL to segment 1).

## Changes

| File | Change |
|------|--------|
| `internal/wal/mem_ring.go` | Add `ResetToPos(pos int64)` method |
| `internal/wal/writer.go` | Call `st.memRing.ResetToPos(writePos)` in `loadState` |
| `internal/wal/mem_ring_test.go` | 4 new regression tests |

## Tests

- `TestMemRingResetToPos` — unit test: after `ResetToPos(8192)`, `ReadAt` misses at
  positions < 8192 and hits after `Append(8192, ...)`.
- `TestMemRingResetToPosNilSafe` — nil receiver is a no-op.
- `TestMemRingZeroReadAfterTailAdvance` — Path B write sequence
  (`AdvanceWindow + WriteReserved + PublishUpTo`): the "buggy" ring (no reset) can serve
  zeros; the "fixed" ring (`ResetToPos(highLSN)` first) correctly rejects `ReadAt(lowLSN)`.
- `TestMemRingLoadStateAnchorPreventsZeroRead` — integration: writer1 populates WAL (closes);
  writer2 re-opens same walDir (simulating server restart); after one more append, `ReadAt(0)`
  on writer2's ring returns false.

Gate: `TestE2E_FailoverGoopgToPG/async` PASS (was: "invalid magic number 0000 at 0/1000000").

## PG-compat

No WAL byte format change. The ring is a read cache for the walsender; on-disk WAL is
unchanged. Standbys see correctly-formed WAL pages from the disk fallback path for positions
before `writePos`, and from the MemRing for newer positions.
