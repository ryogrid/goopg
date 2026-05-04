# 0045-0001 — `detectWritePos` from a Non-Zero Starting Segment

**Status:** draft
**Parent milestone:** M0045
**Date:** 2026-05-04

## 1. Objective

Patch `internal/wal/writer.go::detectWritePos` so that it accepts a
data directory whose smallest WAL segment is any non-negative
number — not just 0 — while preserving the existing gap-detection
guarantee for the segments that *are* on disk.

## 2. The bug

Run-007's hard-kill restart triggered:

```text
goopg start: goopg: wal: wal: first segment is 00000000000000000000023F,
expected 000000000000000000000000
```

The triggering code is `internal/wal/writer.go:873-875`:

```go
if segNos[0] != 0 {
    return 0, 0, fmt.Errorf("wal: first segment is %s, expected %s",
        formatSegmentName(segNos[0]), formatSegmentName(0))
}
```

This is incompatible with `internal/wal/retention.go::SlotAwareRetainer.Retain`,
which deletes WAL segments strictly before the last checkpoint
LSN as part of normal operation. After even one full retention
cycle, no real cluster has segment 0 on disk.

## 3. The fix

Replace the segment-loop body in `detectWritePos` with a version
that:

1. Treats the smallest segment number on disk (`segNos[0]`) as
   the lower bound of the contiguous range, not segment 0.
2. Computes `expected` for each `i` as
   `segNos[0] + uint64(i)`, so gap detection still flags real
   corruption (e.g., segments 575 and 577 with no 576).
3. Computes `writePos` as
   `firstSegNo*segSize + bytesUsedInLastSeg`, where
   `bytesUsedInLastSeg` accumulates over the segments ON DISK
   plus the inner-segment offset returned by
   `scanLastSegmentEnd`.

Sketch:

```go
firstSegNo := segNos[0]
for i, segNo := range segNos {
    expected := firstSegNo + uint64(i)
    if segNo != expected {
        return 0, 0, fmt.Errorf("wal: gap at segment %s",
            formatSegmentName(expected))
    }
    sz := segSizes[expected]
    if i < len(segNos)-1 && sz != segSize {
        return 0, 0, fmt.Errorf(
            "wal: non-final segment %s has size %d, expected %d",
            formatSegmentName(expected), sz, segSize)
    }
    if i < len(segNos)-1 {
        // intra-on-disk segments are full-size; tracked
        // implicitly via firstSegNo*segSize below.
        continue
    }
    usedBytes, lastRecPtr, scanErr :=
        scanLastSegmentEnd(walDir, expected, sz, segSize, pageHeaders)
    if scanErr != nil {
        return 0, 0, scanErr
    }
    // Absolute LSN-byte-offset convention: segment K starts at
    // byte K * segSize. Earlier-than-firstSegNo segments are
    // gone (retention deleted them) — but the positions they
    // occupied still count toward the absolute writePos.
    writePos := int64(firstSegNo)*segSize +
        int64(len(segNos)-1)*segSize + usedBytes
    prevRecPtr := lastRecPtr
    return writePos, prevRecPtr, nil
}
```

Net diff: the unconditional `if segNos[0] != 0 { return error }`
goes away; `expected := uint64(i)` becomes `expected :=
firstSegNo + uint64(i)`; the `writePos` accumulator is replaced
by the closed-form expression above.

## 4. Worked example (run-007 reproducer)

Inputs (segSize = 16 MiB = 16 777 216 B):

```
firstSegNo  = 0x23F = 575
last seg    = 0x4D2 = 1234   (hypothetical end-of-WAL)
on-disk segs = [575, 576, …, 1234]   (660 segments)
last seg size = 16 777 216  (the active segment)
scanLastSegmentEnd returns: usedBytes = 12 345 678, lastRecPtr = …
```

Expected `writePos`:

```
writePos = 575 * 16 777 216 + 659 * 16 777 216 + 12 345 678
        = (1234 * 16_777_216) + 12_345_678 - (1 * 16_777_216)
        ; the -1 cancels: last seg's 16 777 216 B aren't all used
        = 1234 * 16_777_216 + 12_345_678
        = 20 711 980 654
```

I.e., the absolute LSN at which the next write resumes. Same as
the pre-bug formula would have produced if segments 0..574 had
been preserved.

## 5. Alternative considered

**Pre-allocate dummy zero-byte placeholder files for retired
segments.** Rejected: doubles the file count over the retention
horizon, papers over rather than fixes the recovery model, and
breaks every other "list segment files" loop that assumes a
non-zero file is meaningful.

## 6. Verification

- New unit test `internal/wal/writer_test.go::TestDetectWritePos_NonZeroFirstSeg`
  fakes a temp dir with a segment numbered `firstSegNo > 0`,
  populated with crafted record bytes terminated by an EOS
  sentinel, and asserts `detectWritePos` returns the expected
  absolute LSN.
- Existing tests for the segment-0 case (post-`initdb`) must
  continue to pass — `firstSegNo = 0` is a special case of the
  generalised loop.
- Gap-detection regression: a directory containing segments
  `[575, 577]` (skipping 576) still errors with
  `wal: gap at segment 0000000000000000000000240`.

## 7. Out of scope for 0045-0001

- The replay phase (covered by 0045-0002).
- Discovering the last-checkpoint LSN (covered by 0045-0003).
- The integration test that exercises kill-and-restart end-to-end
  (covered by 0045-0004).
