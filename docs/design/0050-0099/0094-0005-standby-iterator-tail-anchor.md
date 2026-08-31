---
id: 0094-0005-standby-iterator-tail-anchor
status: accepted
milestone: M0094-0005
date: 2026-05-14
related: [0005-0001-streaming-replication-architecture, 0005-0002-standby-recovery-and-replay]
---

# M0094-0005 — Standby replayer tail-anchor off-by-one fix

## Problem

`TestE2E_PhysicalReplication` and `TestReplicationEndToEnd` were
failing because the standby's continuous-replay goroutine died
immediately on boot with:

```
standby replay: stopped on error event=standby_replay_error
  apply_lsn=16664 err="wal: stream replay read: wal: corrupt record:
  bad xlog total length 0"
```

The error reproduced on every standby start, before the walreceiver
had a chance to deliver a single record.

## Root cause

`RecordIterator` uses a 1-based byte-LSN convention documented at
`internal/wal/iterator.go` line 63:

```
// startLSN==0 maps to byte offset 0; startLSN==N maps to offset N-1.
```

`Writer.WrittenLSN()` returns the LSN of the **last byte already
appended** — i.e. one less than the LSN of the next record's first
byte (`Append` returns `start = writePos + leading + 1`).

The "tail anchor" idiom — "begin reading new records as they
arrive" — therefore requires `startLSN = WrittenLSN() + 1`. With
that, `pos = startLSN - 1 = WrittenLSN()` lands exactly past the
last written byte; `written > pos` is false; `Next()` blocks until
the writer advances. `TestRecordIteratorStartLSNSkipsExisting` and
the (now-added) `TestRecordIteratorAnchorAtTailBlocks` exercise
both arms of this convention.

`startStandbyReplayer` and `startWalreceiver` in `cmd/goopg/main.go`
passed `rt.WAL.WrittenLSN()` directly, which made the iterator
anchor *inside* the last record on disk (offset `WrittenLSN()-1`).
`readOneAt` would read 24 bytes starting at the last data byte of
that record; the resulting `XLogRecord` header field
`xl_tot_len = 0` (cross-segment padding zeros) tripped the
"bad xlog total length 0" guard. The replayer exited; no new WAL
ever applied.

The same off-by-one in `startWalreceiver.StartLSN` made the primary's
walsender anchor inside the same record on the *primary* side; it
streamed garbage records (or blocked on a bogus oversized `tot_len`)
instead of the post-restart tail.

## Fix

Two single-call-site fixes in `cmd/goopg/main.go`:

1. `startStandbyReplayer`: anchor the iterator at `WrittenLSN()+1`
   (block at tail). The replayer's `applyLSN` baseline stays at
   `WrittenLSN()` so observers see a monotone progression even
   before the first record arrives.
2. `startWalreceiver`: send `StartLSN: WrittenLSN()+1` so the
   walsender's iterator on the primary anchors at the start of the
   next record after the cloned WAL tail.

## Regression test

`TestRecordIteratorAnchorAtTailBlocks` (in `internal/wal/iterator_test.go`)
plants a record, anchors at `WrittenLSN()+1`, asserts `Next()`
blocks until cancel (rather than returning the historical
`bad xlog total length 0` error), then appends a new record and
asserts `Next()` returns it cleanly.

## Verified

- `TestRecordIteratorAnchorAtTailBlocks` — PASS (new)
- `TestPort_Recovery001StreamRep` — PASS
- `TestPort_Recovery013CrashRestart` — PASS
- `TestPort_Recovery019ReplslotLimit` — PASS
- `TestPort_Recovery047CheckpointPhysicalSlot` — PASS
- `TestKillKillRecovery` (cluster) — PASS
- All `./internal/wal/...` — PASS
- All `./cmd/goopg/...` — PASS

## Scope boundary

This fix closes the tail-anchor crash that prevented *any* WAL
replay on a freshly-booted standby. `TestE2E_PhysicalReplication`
and `TestReplicationEndToEnd` continue to fail (pre-existing on
master) for an *independent* reason: primary-side `WrittenLSN()`
does not advance after an INSERT-via-lib/pq or after a `CHECKPOINT`
RPC in the replcluster setup, so the standby's correctly-anchored
iterator finds nothing to stream. That is a separate WAL-emit /
WAL-visibility bug on the primary and is filed as the follow-up
work item for M0094-0005.
