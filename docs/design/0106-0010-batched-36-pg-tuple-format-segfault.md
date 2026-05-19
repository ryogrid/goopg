# M0106-0010 batched-36 — PG client backend SEGFAULTs reading user-table heap pages

## Status

PARTIAL (loop 3, 2026-05-19) — heap-tuple MAXALIGN bug identified and
fixed in `PageAddHeapTuple`. User-table heap bytes now match PG's
expected layout byte-for-byte (offset MAXALIGN'd, header fields
correct: Infomask2=natts, Infomask|=HEAP_XMAX_INVALID, Hoff=24,
varlena header 0x15 for "bootstrap"). PG still segfaults on the SELECT
— the residual cause is elsewhere; see "What's next" below.

## TL;DR

After batched-35 landed canonical XLOG_HEAP_INSERT/DELETE/UPDATE WAL emission
for user-table DML, `TestE2E_FailoverGoopgToPG/async` advances further than
before: the PG standby now (a) accepts the goopg pg_basebackup, (b) starts up
into hot-standby mode, (c) connects walreceiver to the goopg primary at
`0/1000000`, and (d) begins streaming WAL bytes (apply LSN advances from
`0/100A2B8` → `0/100E398`).

The test still fails. The PG client backend SEGFAULTs while executing the
first user query against `public.bench_log`:

```
[630416] LOG:  client backend (PID 630433) was terminated by signal 11: Segmentation fault
[630416] DETAIL:  Failed process was running: SELECT count(*) FROM public.bench_log WHERE client = -999
[630416] LOG:  terminating any other active server processes
```

The postmaster then SIGQUITs every child (walreceiver included), enters crash
recovery, restarts, replays a bit more, and re-segfaults — repeating until the
30 s `waitForPGCount` deadline elapses.

## Three partial fixes attempted in this loop (uncommitted at investigation
start — preserved in the same commit as this design doc)

1. **`internal/wal/iterator.go` — segment-boundary anchor**
   `pg_basebackup`'s `START_REPLICATION 0/1000000` rounds the start LSN down
   to the previous segment boundary (`XLogSegmentOffset` in
   `pg_basebackup.c`). When `startLSN % segSize == 0`, goopg's previous
   `pos = startLSN - 1` formula landed at the LAST byte of the preceding
   segment, so the walsender emitted a duplicate / off-by-one byte at the
   segment boundary. Fix: bump `startLSN` by one when it is exactly a
   segment-size multiple so `pos = startLSN - 1 = segN*segSize`.

2. **`internal/wal/format.go` — `xl_prev` is already 0-based**
   batched-35 added a `prevPG = prev - 1` mapping to convert goopg's 1-based
   LSN to PG's 0-based LSN. This was wrong: the caller in `writer.go` stores
   `prevRecPtr = start - 1`, so the value passed in IS already the 0-based
   `RecPtr`. The extra `-1` produced a `xl_prev` that pointed one byte too
   early, and PG would treat the chain as broken at the first record. Fix:
   pass `prev` through verbatim; `prev == 0` continues to mean
   `InvalidXLogRecPtr`.

3. **`internal/executor/operators_storage.go` — PG-native heap tuple encoding
   when `ctx.LogCanonical != nil`**
   Two complementary changes:
   - `writeHeapRowReturning` now encodes the row with `EncodeRowPG` +
     `NullBitmapPG` (and sets `Header.Natts = len(cols)` + `Infomask |=
     HeapXmaxInvalid`) whenever a canonical WAL hook is active. This is the
     hot path for user-table INSERT / UPDATE.
   - A new `writeHeapRowReturningPG` is the unconditional PG-native variant.
     `syncIndexToCatalogHeap` → `writeHeapRowCanonical` now goes through it,
     so the catalog rows that a PG standby will read after attaching are
     emitted in PG's exact heap-tuple format.

4. **`internal/server/replication.go` — break livelock on WAL iterator error**
   `replyStartReplication` used to `<-receiveDone` after cancelling the
   stream context when the WAL iterator returned an error. The receiver
   goroutine is parked inside a blocking `r.ReadFrame()` that context
   cancellation does not interrupt, so the wait never unblocks: the receiver
   is waiting for `CopyDone` from the client; the client is waiting for the
   server to send keep-alives; and the server is parked here. Fix: skip the
   `<-receiveDone` rendezvous on the error path and let the deferred conn
   close (when `handleConn` returns) deliver EOF to the receiver.

## Why the test still fails

The segfault is on a user-table `SELECT` against `public.bench_log`, so the
remaining defect is in the bytes that land on a heap page of a user table.
Even with the EncodeRowPG switch, PG dereferences a bogus pointer when it
tries to deform a `(int, text)` tuple from page 0 of `bench_log`. Two
hypotheses, ordered by likelihood:

- **(H1) `t_ctid` block-number encoding.** goopg writes
  `t_ctid.block` as a single little-endian `uint32`; PG writes it as
  `BlockIdData { bi_hi uint16; bi_lo uint16 }`. For block 0 these coincide,
  but `t_ctid.ip_blkid` is read by `ItemPointerGetBlockNumberNoCheck` even
  on the first-page tuple if a HOT chain is walked. Easy to falsify: write
  a unit test that round-trips a single-row page through PG's
  `heap_deform_tuple` (via `pg_filedump` in dryrun mode).

- **(H2) `t_hoff` / null-bitmap alignment.** `EncodeRowPG` may not have
  produced strict MAXALIGN'd offsets for variable-length text columns,
  or `t_hoff` may not reflect the bitmap actually written.
  `writeHeapRowReturning` sets `Hoff = DefaultHeapTupleHoff` (24) when
  `Bitmap` is empty but `Header.SetNatts(len(cols))` was added AFTER the
  `MarshalBinary` call site, so `t_infomask2` may still be zero on the
  bytes that hit the page.

The combined hypothesis test is to dump the first 8 KiB of `bench_log` from
the goopg primary's data directory, run it through `pg_filedump` from
`postgres/local_install/bin/`, and compare to a known-good PG-produced page.

## What's next (batched-37 candidate)

1. Bytes-on-page audit: write a Go test that builds a heap page using the
   current `writeHeapRowReturning(..., LogCanonical: non-nil)` and compares
   to a fixture produced by `pg_filedump`. This is the smallest reproducer
   and the one that lets us iterate without restarting PG.
2. Once the page bytes match: re-run TestE2E_FailoverGoopgToPG/async. The
   segfault should disappear; the next failure (if any) is expected to be
   on the streaming side rather than the user-page read side.
3. Then handle the second wave of segfaults the moment a real concurrent
   workload starts (`waitForPGCount("...WHERE src = 'pre'", 1, 30s)`).

## 2026-05-19 loop 3 — MAXALIGN fix landed

Findings from the byte-level audit (manual via xxd, not pg_filedump):

* Before fix: the goopg primary's `base/5/16400` (bench_log heap) had
  `pd_upper = 8154` (i.e. NOT 8-byte aligned). PG18 requires every
  tuple offset to be MAXALIGN'd; `heap_deform_tuple` dereferences
  alignment-sensitive offsets at the tuple base, so a misaligned
  base segfaults on the first SELECT.
* Root cause: `storage.PageAddHeapTuple` allocated a slot of exactly
  `len(raw)` bytes (`newUpper = upper - len(raw)`) rather than PG's
  `alignedSize = MAXALIGN(size); newUpper = upper - alignedSize`.
* Fix: `PageAddHeapTuple` now subtracts `MAXALIGN(len(raw))` from
  `pd_upper` and writes the actual tuple bytes at the slot's low
  end; padding bytes (0..7) at the slot's high end stay zero from
  `InitPage`. The line-pointer `Length` still reports the real
  tuple length so `ParseHeapTuple` reads exactly the tuple bytes.

Verification:

* `internal/executor/canonical_tuple_bytes_test.go` — new unit test
  that builds the bench_log canonical tuple bytes and asserts
  byte-exact PG layout + page-level MAXALIGN. PASS.
* On-disk verification: `base/5/16400` now has `pd_upper = 8152`
  (MAXALIGN'd) and the inserted tuple matches PG's expected
  HeapTupleHeaderData layout byte-for-byte:
  Xmin=4, Xmax=0, t_field3=0, t_ctid=(0xFFFFFFFF,0),
  t_infomask2=2, t_infomask=0x0800, t_hoff=24,
  body=`19 fc ff ff 15 'bootstrap'`.
* Catalog files (pg_class 1259, pg_attribute 1247, pg_namespace 2615)
  inspected at block 0 — all already have MAXALIGN'd `pd_upper`
  values (288, 280, 7856 respectively).

PG STILL SEGFAULTS on the SELECT. The residual defect is NOT in the
bench_log user-table tuple bytes. Hypotheses for batched-37:

* **(H3) Index pages.** No index is created on `bench_log` in the test,
  but the planner reads pg_class indexes (e.g.
  `pg_class_relname_nsp_index`) which goopg builds via
  `internal/access/btree`. The btree's `PageAddItemRaw` /
  `PageInsertItemRawAt` were initially MAXALIGN'd in this loop but
  reverted to keep btree space-fit consistent (page panics on
  boundary cases otherwise); those paths still emit non-MAXALIGN'd
  item offsets, which a PG18 backend reading the index will
  dereference.
* **(H4) Wrong relation lookup.** The "at character 22" log marker
  places the segfault near the FROM clause in
  `SELECT count(*) FROM public.bench_log WHERE client = -999`. PG
  is in the parser/analyzer reading `pg_class` / `pg_namespace`
  catalogs. Even though those catalog pages are MAXALIGN'd, a
  per-row corruption (e.g. wrong `relfilenode` OID, wrong
  `attlen`/`atttypid` for bench_log's columns) could still drive
  the planner into a bad memory dereference. A targeted
  `pg_filedump` on `base/5/1259` (pg_class) restricted to bench_log
  would localise this.
* **(H5) `pg_internal.init`.** The test copies goopg-generated
  relcache init files to the standby. If those still describe
  bench_log's columns with attrlen/attbyval/attalign values that
  disagree with what `pg_attribute` says, PG's `formrdesc` or
  `RelationCacheInitializePhase3` would build a wrong TupleDesc,
  and `heap_deform_tuple` would dereference at a wrong offset.

Files touched this loop:

- `internal/storage/heap.go` — `PageAddHeapTuple` MAXALIGNs.
- `internal/executor/canonical_tuple_bytes_test.go` — new test
  pinning the byte layout.

## Files touched

- `internal/wal/iterator.go`
- `internal/wal/format.go`
- `internal/executor/operators_storage.go`
- `internal/executor/operators_ddl.go`
- `internal/server/replication.go`

## Tests

- `TestE2E_PhysicalReplication` — PASS (goopg → goopg, unchanged).
- `TestE2E_FailoverGoopgToPG/async` — FAIL (PG client backend segfault on
  `SELECT count(*) FROM public.bench_log WHERE client = -999`).
- `internal/executor`, `internal/server`, `internal/mvcc`, `internal/storage`,
  `internal/catalog` unit suites — PASS.
- `internal/wal` — two pre-existing failures (`TestCheckpointerWritesCheckpointMarkers`,
  `TestEncodeRecordXLogClassifiesXactCommitXID`) inherited from batched-35; not
  introduced by this loop.
- `internal/initdb` — `TestSynchronousCommitFlushesByDefault` continues to
  fail (M0106-0012, pre-existing).
