# 0131-0022 — replay mistakes a segment's unusable tail for end-of-WAL (M0131-S30.1)

Status: **fix landed 2026-08-11** (reader side). One writer-side divergence
discovered and deferred (see §5).

Predecessors: `0131-0020` (crash-recovery row loss measured and confirmed),
`0131-0021` (S30.3 page-identity divergence, a different defect in the same
milestone).

## 1. The failure

`RUNS=3 bash analysis/crashprobe30.sh` lost 4416 of 500000 committed rows in one
run. Recovery stopped early and said so at WARN:

```
end of WAL reached during replay reason="invalid record header"
detail="padding bytes nonzero (0xff 0x07)" lsn=134217721
```

`134217721 - 1 = 134217720` is **8 bytes before a segment boundary**
(128 MiB = 8 × 16 MiB). Everything after that point — all of it committed and
acknowledged — was discarded, and the next append overwrote it.

## 2. Why those 8 bytes are not a record

goopg's LSN is a raw byte offset that includes page headers, so the writer has
to decide what to do when a record does not fit in the rest of a segment. The
stripe writer — `insertPosTracker.reserveEmittedAndPublish`
(`internal/wal/reserve_emitted.go`), the path every production append takes —
re-lands the record at the segment boundary and pads the skipped range
`[curr, boundary)` with an `XLOG_NOOP`. A pad record needs at least
`xlogMinimumRecordSize` (24) bytes, so when the leftover is smaller the gap is
simply left alone:

```go
gapLen := boundary - startCandidate
if gapLen < xlogMinimumRecordSize {
        // no pad possible; leave the bytes, keep t.prev so the record at
        // `boundary` still chains to the last record before the gap
}
```

Those bytes belong to no record. `readAllPageAware` walked straight into them
anyway: the offset is mid-page, so the walk read a 24-byte header there — and
`extractRecordBytes` cheerfully completed it from the *next* segment's first
page (skipping that page's 40-byte long header on the way). The result is the
gap bytes glued to the head of a real record: `xl_info`/`xl_rmid`/padding land
on payload bytes, header validation fails, and the walk calls it end-of-WAL.
`0x07ff` in the measured detail line is the high half of the *following*
record's `xl_prev`.

A freshly preallocated segment has zeros there, which fails just as loudly one
branch later (`xl_tot_len = 0` → "bad xlog total length"); a **recycled**
segment has whatever the previous write cycle left, which is how the measured
run got `0xff`.

Upstream has no equivalent gap: `ReserveXLogInsertLocation` reserves in *usable*
byte space and lets a header span the boundary
(`postgres/src/backend/access/transam/xlog.c`), so the skip is goopg-specific
and belongs to the reader that mirrors goopg's own layout rule.

## 3. Why the obvious fix is wrong

The first shape of this fix — "if the segment tail is shorter than a record
header, skip it" — is unconditional, and it *breaks* replay of streams goopg
has already written.

`state.appendPGCompat` has a second writer path. **Path A** (taken when
`Config.WALBuffers == 0`, when the record cannot fit the ring, or when the ring
needs draining, `internal/wal/writer.go`) encodes at the tracker cursor and
emits with `emitWithPageHeaders` — with **no boundary re-land at all**. It
happily starts a record in a segment's last 8 bytes and continues it after the
next segment's long page header. Measured directly: a fixture built through
Path A puts a CRC-valid record at offset 32760 of a 32768-byte segment, and the
pre-existing reader decodes it correctly (`extractRecordBytes` already skips the
intervening page header). An unconditional skip drops that record and every
record behind it — the same data loss the fix is meant to remove, in the other
writer's streams.

## 4. The fix

`readAllPageAware` skips a sub-header segment tail **only when the bytes there
are not a record**, decided by the record's own CRC:

```go
if segRemain := segSize - pos%segSize; segSize >= XLOGBlockSize &&
        segRemain < int64(xlogRecordHeaderSize) &&
        !recordStartsAt(stream, off, pos, segSize) {
        off += int(segRemain)
        continue
}
```

`recordStartsAt` (`internal/wal/reader.go`) decodes the header, bounds-checks
`xl_tot_len`, re-assembles the whole record through `extractRecordBytes` and
verifies `xl_crc`. It is the same evidence `durableUnknownRecord` already uses
for the neighbouring question ("is this unparseable header nevertheless a
durable record?"): a false positive costs a 2^-32 CRC collision on garbage, a
false negative would discard durable data.

The `segSize >= XLOGBlockSize` guard keeps sub-page "segments" used by a few
unit fixtures out of the branch.

### Tests

`internal/wal/reader_segment_tail_gap_test.go` builds a two-segment WAL whose
first segment ends exactly `gapLen` bytes short of the boundary, by sizing each
payload from the writer's own `predictXLogRecordLen` / `predictEmittedSize`, so
the cursor's landing position is known before each `Append`. The writer path is
selected by `Config.WALBuffers`.

| test | path | asserts |
|---|---|---|
| `TestReadAllSkipsShortSegmentTailGap` (gap 8, 16) | B | all 68 payloads replay across the gap |
| `TestReadAllSkipsShortSegmentTailGapWithStaleBytes` | B | same, with the gap overwritten with `0xff` (recycled-segment shape, the measured failure) |
| `TestReadAllKeepsRecordStraddlingSegmentBoundary` | A | the straddling record and its successors still replay |

Verified both ways: with the skip disabled the three Path-B tests fail
("64 records read", `crosses-the-boundary` missing — exactly the production
signature); with the skip made unconditional the Path-A test fails the same way.

## 5. Discovered and deferred — the two writer paths disagree

The two append paths lay out a segment boundary differently: Path B re-lands and
leaves a gap, Path A straddles. That is a sibling-path divergence in the WAL
*format*, so any consumer (goopg's reader, `pg_waldump`, a PG standby reading
goopg WAL) has to tolerate both. The reader now does; the writer should not need
it to. Unifying them — most plausibly by giving Path A the same re-land through
`reserveEmittedAndPublish`'s prediction, since PG-side tools tolerate straddling
but goopg's own pad/`xl_prev` chaining is written for the re-land — is filed in
`.ralph/deferral_ledger.md` (2026-08-11, M0131-S30.1) and left unchecked in
`fix_plan.md`.

## 6. End-to-end measurement

`RUNS=2 bash analysis/crashprobe30.sh`, same host, minutes apart:

| | pre-fix binary | post-fix binary |
|---|---|---|
| rows | run 1 **490984 / 500000** (9016 lost), run 2 500000 | 500000 / 500000 both runs |
| index parity | run 1 `idx_count=490977`, `heap_anti_missing=9016` | `idx_count=500000`, `heap_anti_missing=0` |
| early end-of-WAL | run 1 `padding bytes nonzero (0xff 0x06)` at lsn=117440505 (8 bytes short of 112 MiB) | **none logged** |
| atomicity | fails both runs | fails both runs (see below) |

The row-loss half of S30 is gone; `OVERALL: FAIL` persists only on the
atomicity invariant.

## 7. What this does *not* fix

S30's remaining slices are untouched: **S30.2** (an early end-of-WAL is still a
`WARN` that starts the cluster anyway — it must be loud and must refuse),
**S30.4** (no checkpoint fires during ~180 MiB of WAL, which is why replay
reaches a boundary at all and why a truncated replay costs so much), and
**S30.7** — filed from the measurement above: a crash still tears transactions
(`sum(abalance) != sum(history.delta)`, in both directions across runs) even
when no row is lost. Until S30.2 lands, a stream that ends early for any
*other* reason will keep failing quietly, so
`RUNS=3 bash analysis/crashprobe30.sh` remains the gate for the milestone
rather than for this slice.
