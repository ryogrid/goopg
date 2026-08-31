# 0131-0028 — the cross-segment pad record clobbers a page header (M0131-S30.1b)

Status: **fix landed 2026-08-12** (writer side). One layout detail discovered
and deferred (see §6).

Predecessors: `0131-0020` (crash-recovery row loss measured and confirmed),
`0131-0022` (S30.1 — the *reader* side of the same segment-boundary layout
rule), `0131-0027` (S30.7/S30.8, the run that surfaced this).

## 1. The failure

`RUNS=3 bash analysis/crashprobe30.sh` at HEAD `3e621d22` lost 6762 of 500000
committed rows in run 2. Recovery stopped early and said so at WARN:

```
end of WAL reached during replay reason="invalid page header"
detail="wal: invalid page header: magic=0x0020 want 0xd118"
lsn=117432305 stream_offset=100655088
```

Two numbers name the defect outright:

- `117432304` (the reported LSN is 1-based) is **16 bytes before the page
  boundary at `117432320`**, which is itself **one page before the 112 MiB
  segment boundary** at `117440512` (7 × 16 MiB).
- `0x0020` decodes as the byte pair `0x20 0x00`. Those are bytes 16 and 17 of
  an `XLogRecord` header — `xl_info` and `xl_rmid` — and `0x20`/`0` is exactly
  `XLOG_NOOP` / `RM_XLOG_ID`: **the segment pad record's own header**, sitting
  where a page header belongs.

## 2. Root cause

When a stripe reservation would straddle a segment boundary,
`insertPosTracker.reserveEmittedAndPublish` (`internal/wal/reserve_emitted.go`)
re-lands the record at the boundary and fires `onCrossSegment` so the gap
`[curr, boundary)` is filled with an `XLOG_NOOP` pad. The composer that fills
it, `emitSegmentPad` (`internal/wal/segment_pad_emit.go`), did:

```go
padLen := int(boundary - gapStart)
pad, err := buildSegmentPadRecord(padLen, gapPrev)
walBuf.writeReserved(int64(gapStart), pad)     // …and MemRing
```

The gap is a range of **stream** bytes. In page-header mode the stream is not
all record bytes — every 8 KiB boundary inside it holds a 24-byte page header
(40-byte at a segment boundary). Writing `padLen` raw record bytes at
`gapStart` therefore does two wrong things at once when the gap spans a page
boundary:

1. it **overwrites the page-header slot** with record bytes, and
2. it makes the pad's own `xl_tot_len` overrun `boundary` by the same amount,
   because the reader reassembles a record by skipping page headers
   (`extractRecordBytes`), so a `padLen`-byte record consumes `padLen + 24`
   bytes of stream.

Every other record in goopg goes through `emitWithPageHeaders`, which
interleaves the headers and sets `XLP_FIRST_IS_CONTRECORD`/`xlp_rem_len` on the
continuation page. The pad — and only the pad — did not.

Upstream has no equivalent: PG reserves in *usable* byte space
(`ReserveXLogInsertLocation`, `postgres/src/backend/access/transam/xlog.c`), so
its `XLOG_SWITCH`/noop fill is copied through `CopyXLogRecordToWAL` like any
other record and cannot miss a page header. goopg's LSN is a raw byte offset
that includes page headers, so its pad has to do that arithmetic explicitly.

### Why the gap can span a page at all

The gap only opens when the reservation's emitted size exceeds
`boundary - curr`, so a page-spanning gap needs a record larger than ~8 KiB.
Those are routine here: goopg emits a full-page image per record
(`perf-optimize3`: ~33 KB of WAL per pgbench transaction), so a
segment-crossing FPI record opens a multi-KiB gap whenever the cursor is not
already in the segment's last page. That is why the failure reproduces roughly
every other crash run rather than never.

## 3. The fix

`emitSegmentPad` now sizes the pad **record** to the gap minus the header bytes
that will be interleaved into it, and emits it through the same
`emitWithPageHeaders` every other record uses:

```go
recLen := gapLen
if lay.pageHeaders && lay.segSize > 0 {
        recLen -= pageHeaderBytesIn(int64(gapStart), int64(boundary), lay.segSize)
        …
}
pad, _ := buildSegmentPadRecord(recLen, gapPrev)
out, leading := emitWithPageHeaders(pad, recLen, int64(gapStart), lay.segSize, lay.sysID, lay.tli)
// len(out) == gapLen, asserted
```

`pageHeaderBytesIn(from, to, segSize)` counts one header per page boundary in
`[from, to)` — long-form at a segment boundary, short elsewhere. A boundary at
`from` counts (the emitter writes a leading header when it starts on one); a
boundary at `to` does not (the emitter stops after the record's last byte).
That is the exact mirror of `emitWithPageHeaders`' own loop, so `len(out)`
always equals `gapLen`; the composer asserts it rather than trusting it.

The layout parameters (`pageHeaders`, `segSize`, `sysID`, `tli`) reach the
composer through a new `padLayout` struct, pinned into the `onCrossSegment`
closure by `newStripeWriterCore` from the writer state. Its zero value keeps
the old raw-bytes shape, which is correct for the header-less fixtures a few
unit tests still build.

The resulting `recLen` can never fall below the 24-byte minimum: if the gap
contains `k ≥ 1` headers it is at least `k × 8192` bytes wide and loses only
`24k`. The composer still errors on a short pad as defence in depth (the
`onCrossSegment` contract turns that into a panic — a failed pad breaks the
`xl_prev` chain and must never be silent).

### Tests — `internal/wal/reader_segment_pad_page_gap_test.go`

The fixture parks the write cursor `lead` bytes before the **last page
boundary** of a 4-page segment (sizing every filler payload from the writer's
own `predictXLogRecordLen`/`predictEmittedSize`, as `0131-0022`'s fixture does),
then appends a record too large for the remaining page — so the pad spans
exactly one interior page boundary.

| test | asserts |
|---|---|
| `TestSegmentPadSpanningPageBoundaryReplays` (lead 8/16/64) | every committed payload replays |
| `TestSegmentPadKeepsPageHeaderIntact` | the header inside the gap decodes, claims its own `xlp_pageaddr`, and flags `XLP_FIRST_IS_CONTRECORD` |
| `TestPageHeaderBytesIn` | the header-counting arithmetic, incl. both boundary conventions |

Negative control — with the fix disabled the fixture reproduces the production
signature **byte for byte**:

```
WARN wal: end of WAL reached during replay reason="invalid page header"
     detail="wal: invalid page header: magic=0x0020 want 0xd118" lsn=24561
--- FAIL: …/lead16: payload 49/52 (8213 bytes) missing from replay (48 records read)
```

`24560` is 16 bytes before the fixture's page boundary at `24576`, the same
geometry as `117432304`/`117432320` in production.

## 4. Reader vs writer — which side owns this

`0131-0022` fixed the *reader* for the sub-header segment tail because two
goopg writer paths disagree about that layout and both streams already exist on
disk. This one is the opposite call: no writer has ever produced a *valid*
stream with a record clobbering a page header, so there is nothing for the
reader to tolerate. Stopping at a page header that is not one is the correct
reader behaviour (upstream's `XLogReaderValidatePageHeader` does the same); the
bug is entirely that goopg wrote such a page.

## 5. End-to-end measurement

`RUNS=3 bash analysis/crashprobe30.sh` (SF=5, 500000 rows, 16-client load,
SIGKILL at 30 s), pre-fix binary vs post-fix binary — see the run log referenced
in `.ralph/fix_plan.md` M0131-S30.1b. `analysis/crashprobe30.sh` now snapshots
`pg_wal` (and `global/`) into `<run>/pg_wal_crash` **before** the restart, since
recovery appends over the very bytes that prove an early end-of-WAL.

## 6. Discovered and deferred — `xl_prev` of a page-aligned pad

`xl_prev` names a record's *content* start: the common path in
`reserveEmittedAndPublish` sets `t.prev = start + leading`, skipping the leading
page header. The cross-segment path sets `t.prev = startCandidate` unadjusted,
so when the gap itself begins exactly on a page boundary the pad's own 24-byte
leading header is not skipped and the following record's `xl_prev` is 24 too
low. goopg's reader does not verify `xl_prev`, so this costs nothing today; it
is a `pg_waldump` / PG-standby parity defect. Filed in
`.ralph/deferral_ledger.md` (2026-08-12, M0131-S30.1b); the fix is to thread
`emitSegmentPad`'s already-returned `leading` back through the
`onCrossSegment` hook signature.
