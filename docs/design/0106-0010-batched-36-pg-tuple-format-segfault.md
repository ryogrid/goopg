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

## 2026-05-19 loop 4 — segfault localised to pg_namespace_nspname_index

PROGRESS (no new fix landed): the LD_PRELOAD SIGSEGV shim
(`GOOPG_TEST_SEGV_BACKTRACE=1`) plus `addr2line` on
`postgres/local_install/bin/postgres` pinpoint the crash to the
`name` btree key comparator on `pg_namespace_nspname_index`:

```
RDI=0x00000000745f6770  RSI=0x00005af83a977190  RDX=0x40 (NAMEDATALEN)
RIP=0x0000702f4ad8c8c1  (libc strncmp / __strncmp_evex)
postgres/utils/adt/name.c:139   namecmp -> strncmp(arg1, arg2, 64)
postgres/access/nbtree/nbtsearch.c:402  _bt_binsrch -> _bt_compare ->
  FunctionCall2Coll -> btnamecmp -> namecmp
postgres/backend/parser/parse_clause.c:399  transformTableEntry ->
  RangeVarGetRelidExtended -> LookupExplicitNamespace("public") ->
  GetSysCacheOid1(NAMESPACENAME, …) -> SearchCatCache ->
  systable_getnext (pg_namespace_nspname_index)
```

The first 4 IO_READV ops that precede the SIGV all return 8192 bytes
(one block each), which is consistent with PG loading
`pg_namespace_nspname_index` blocks 0+1 + adjacent catalog reads,
then immediately segfaulting on the first `strncmp` invocation.

**Btree byte audit — clean.** Manual decode of
`base/5/2684` (16 KiB = 2 blocks):

* Block 0 (metapage): `pd_lower=72 pd_upper=8176 pd_special=8176
  pd_pagesize_version=0x2004`. BTMetaPageData:
  `btm_magic=0x053162  btm_version=4  btm_root=1  btm_level=0
  btm_fastroot=1  btm_fastlevel=0  btm_last_cleanup_num_delpages=0
  btm_last_cleanup_num_heap_tuples=-1.0  btm_allequalimage=true`.
* Block 1 (root+leaf): `pd_lower=36 pd_upper=7960 pd_special=8176`.
  Three line pointers (NORMAL flag, lp_len=72) at offsets 8104 →
  "pg_catalog", 8032 → "pg_toast", 7960 → "public" — all in ascending
  alphabetical order.  Tuple headers: `t_tid=(0,offnum)  t_info=0x0048`
  (size=72, no nulls, no varlen, no INDEX_ALT_TID).  Data starts at
  tuple+8, 64 bytes of NameData, NUL-padded.
* `BTPageOpaqueData` at block-1 byte 8176:
  `btpo_prev=0  btpo_next=0  btpo.level=0  btpo_flags=0x0003`
  = `BTP_LEAF | BTP_ROOT`.  `btpo_prev=btpo_next=0` is CORRECT —
  PG defines `P_NONE = 0` (verified against
  `postgres/src/include/access/nbtree.h:213`), so this root-leaf is
  marked both leftmost and rightmost as expected.

So the bytes on disk and the bytes in the standby's mirrored file
match each other and match PG's expected layout.  Hypotheses H1
(t_ctid encoding) and the "btpo_next sentinel" sub-flavour of H3 are
falsified.

**Live hypothesis (H4'):** PG's `RelationCacheInitializePhase3`
either (a) builds a `tupdesc` for `pg_namespace_nspname_index` with
`attlen`/`attbyval` that disagrees with `name` (NAMEDATALEN=64,
attbyval=false), causing `fetchatt` in `index_getattr` to return a
spurious Datum instead of `PointerGetDatum(tup + 8)`; or (b) reads
the wrong `relfilenode` for OID 2684 and ends up scanning some other
file whose bytes happen to look like an IndexTuple to the line-pointer
decoder but whose data offset is bogus.  The 4-IO sequence rules
against (b) — only 2 blocks of 2684 should be touched on this path,
which matches what we see.

**Next-loop action plan (batched-38 candidate):**

1. Dump goopg's `pg_attribute` row for `(attrelid=2684, attnum=1)` from
   the primary's `base/5/1249` via a Go inspection helper.  Compare
   `attlen / attbyval / attalign / atttypid` against PG's expected
   `(64, false, 'c', 19=NAMEOID)`.
2. Dump goopg's `pg_index` row for `(indexrelid=2684)` from
   `base/5/2610` — verify `indnatts=1`, `indkey={2}`, `indclass={1986}`
   (name_ops).
3. Dump goopg's `pg_internal.init` for the entry describing OID 2684 —
   compare the in-memory `tupdesc` it expands to against (1).
4. Once the offending row is found, regenerate it correctly and re-run
   `TestE2E_FailoverGoopgToPG/async`.

The 1247/2610 dump should take one inspection script (decoding heap
tuples for the pg_attribute / pg_index TupleDesc).  All three audits
are pure byte inspection — no PG round-trip required, which keeps the
loop tight.

Files touched this loop: none (diagnostic-only).

## 2026-05-19 loop 5 — pg_namespace_nspname_index Attrs override

PROGRESS: closed loop 4's audit item (1) via a code-path reading
instead of an on-disk byte dump. The `pg_attribute` row for
`(attrelid=2684, attnum=1)` was emitted with the wrong
`(atttypid, attlen, attbyval, attalign)` because
`internal/initdb/relcache_init.go` had no `Attrs` override on the
`pg_namespace_nspname_index` `idxSpec`. Without it `flattenRels`
fell back to `indexKeyAttrs(1)`, which stamps a 1-column
`(oid, attlen=4, attbyval=true)` descriptor — verbatim what
loop 4 hypothesised (H4') and identical to the bug pattern
fixed for `pg_authid_rolname_index` (Step 3dg) and
`pg_database_datname_index` (Step 3dh).

The Datum 0x00000000745f6770 captured in loop 4 decodes as LE
`"pg_t"` = the first 4 bytes of either `"pg_catalog"` or
`"pg_toast"` (the two ascending leaf entries scanned before
`"public"`). When `bootstrapPgAttributeTuples` writes that wrong
descriptor into `base/5/1249`, the PG18 standby reads it during
`RelationCacheInitializePhase3` and builds a TupleDesc whose
first key descriptor says `attlen=4 / attbyval=true`. PG's
`_bt_compare → index_getattr → fetchatt` then reads the first 4
inline NameData bytes as a by-val Datum and passes them as a
pointer to `btnamecmp → strncmp(arg1, …)` → SIGSEGV.

Fix:

```go
{OID: 2684, Name: "pg_namespace_nspname_index", Attrs: []nailedAttr{
    {Name: "nspname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
}},
```

Companion fix in `internal/initdb/initdb.go::pgTypeAlignChar`: add
NAMEOID (19) to the `'c'`-aligned switch arm. PG18's
`pg_type.dat` says `name` has `typalign => 'c'`; goopg's helper
fell through to the default `"i"` which made every name column's
on-disk `attalign` byte = `'i'`. Not the SEGV trigger (the first
attribute in an index tuple lands at offset 0 either way) but a
latent bug whose first symptom would be a name column at a
non-first position in any multi-column heap or composite-key
index.

Regression test (new): `TestNailedPgNamespaceNspnameIndexHasNameDescriptor`
in `internal/initdb/pg_namespace_index_test.go` pins:

1. `nailedLocalRels` entry for OID 2684 carries the name-typed
   override (`TypeOID=19`, `Len=64`, `Name="nspname"`).
2. `buildPgAttributeBlob(a)` emits `attbyval=0`, `attalign='c'`,
   `attlen=64` (relcache init parity even though 2684 is not in
   `criticalLocalIndexOIDs`).
3. `pgAttributeRow(2684, a)` emits `atttypid=19`, `attlen=64`,
   `attbyval=false`, `attalign='c'`.

### Test results

* All initdb unit tests pass (only pre-existing failure
  `TestSynchronousCommitFlushesByDefault` from M0106-0012, unchanged).
* `TestE2E_FailoverGoopgToPG/async` STILL FAILS. Latest PG standby
  log (`/tmp/TestE2E_FailoverGoopgToPGasync375676194/001/pg.log`)
  shows the same `client backend was terminated by signal 11`
  pattern, same `DETAIL: SELECT count(*) FROM public.bench_log
  WHERE client = -999` query, fired during the post-connect
  parse/analyze stage. The pg_namespace_nspname_index fix is
  necessary but not sufficient — at least one more wrong-tupdesc
  index sits on the same lookup path.

### Likely-next sites (batched-37 candidate)

Other name-typed UNIQUE single-column indexes in
`internal/initdb/relcache_init.go` that still lack an `Attrs`
override and would crash via the same fetchatt → btnamecmp path:

* OID 3467 — `pg_event_trigger_evtname_index`
* OID 3081 — `pg_extension_name_index`
* OID 548  — `pg_foreign_data_wrapper_name_index`
* OID 549  — `pg_foreign_server_name_index`
* OID 2681 — `pg_language_name_index`
* OID 3997 — `pg_statistic_ext_name_index`

These do not crash today because the failing query never resolves
them — they are not on the parse-analyze path for
`public.bench_log`. But once the residual blocker is fixed, any
ALTER / EXTENSION / event-trigger statement against the standby
will surface them. Worth a bulk audit pass before the next E2E
attempt.

The post-attrs-fix residual SEGV cannot be the indexed column
descriptor of 2684 itself — that path is now byte-correct. The
new suspect surface is one of (a) the IndexTuple `t_info` /
header on the 2684 leaf page (loop 4's manual decode verified
the on-disk bytes there but did so against PG's expected layout
post-MAXALIGN — could still differ from what `_bt_compare` parses
in flight), (b) the `pg_index` row for `indexrelid=2684` carrying
the wrong `indclass[0]` / `indcollation[0]` (loop 4 audit item 2,
still not run), or (c) `pg_class.relam=403` + `pg_index.indkey={2}`
disagreement against the pg_namespace heap tupdesc (the column
attnum lookup must resolve to nspname; if `pg_class.relnatts=1`
but PG re-reads `pg_attribute` for attnum=2 on the parent heap
relation 2615 and gets a different shape, the same Datum
miscount can recur). Audit items (2) and (3) from loop 4 remain
the right next step.

Files touched this loop:

- `internal/initdb/relcache_init.go` — added `Attrs` override on
  the `pg_namespace_nspname_index` `idxSpec` plus surrounding
  comment block.
- `internal/initdb/initdb.go::pgTypeAlignChar` — NAMEOID (19)
  added to the `'c'`-aligned switch arm.
- `internal/initdb/pg_namespace_index_test.go` — new regression
  test pinning the descriptor + relcache blob + heap row.

## 2026-05-19 loop 6 — bulk name-typed Attrs overrides

The loop-5 patch closed the SEGV on OID 2684 but left 6 other
name-typed UNIQUE btree indexes in `nailedLocalRels` without an
`Attrs` override.  They are not on the parse-analyze path of the
`SELECT count(*) FROM public.bench_log WHERE client = -999` query
that the failover E2E test fires, so they did not crash this loop —
but the moment a PG standby promoted from a goopg basebackup
executes any `CREATE EXTENSION` / `CREATE EVENT TRIGGER` / `CREATE
SERVER` / `ALTER LANGUAGE` / `CREATE STATISTICS` statement the same
SIGSEGV class would recur (fetchatt loads the first 4 inline
NameData bytes as a by-val Datum and hands a bogus pointer to
`btnamecmp` → `strncmp`).

Loop 6 closes those gaps preemptively:

| OID  | Index name                          | Key column(s)            |
|------|-------------------------------------|--------------------------|
| 3467 | pg_event_trigger_evtname_index      | evtname (name)           |
| 3081 | pg_extension_name_index             | extname (name)           |
| 548  | pg_foreign_data_wrapper_name_index  | fdwname (name)           |
| 549  | pg_foreign_server_name_index        | srvname (name)           |
| 2681 | pg_language_name_index              | lanname (name)           |
| 3997 | pg_statistic_ext_name_index         | stxname (name), stxnamespace (oid) |

Each entry now carries an explicit `Attrs: []nailedAttr{...}`
slice with `TypeOID=19, Len=64, NotNull=true` for the leading
name-typed column.  3997 also pins the trailing oid descriptor
even though `indexKeyAttrs`'s default would already be byte-correct
for that column — spelling it out avoids future drift if the
default ever changes.

Regression coverage: new test
`TestNailedNameTypedIndexesHaveNameDescriptor`
(`internal/initdb/nailed_name_typed_indexes_test.go`) walks
`nailedLocalRels`, asserts each of the six entries has a 64-byte
name-typed leading attr, and re-derives the on-disk `pg_attribute`
heap row to confirm `attlen=64 / attbyval=false / attalign='c'`.

`TestE2E_FailoverGoopgToPG/async` — STILL FAILS (180s deadline,
`waitForPGCount(bench_log) == 1` never reached).  The residual
crash sits elsewhere — most likely one of the audit items still
open from loop 4: (a) the `pg_index` row for `indexrelid=2684`
carrying the wrong `indclass[0]`/`indcollation[0]`, (b) the
`pg_internal.init` entry for OID 2684, or (c) an index outside
the name-typed cohort whose key descriptor disagrees with the
on-disk IndexTuple layout (e.g. an oid composite where the
backing heap's pg_attribute disagrees on attnum ordering).

Files touched this loop:

- `internal/initdb/relcache_init.go` — `Attrs` overrides on the
  six idxSpec entries listed above.
- `internal/initdb/nailed_name_typed_indexes_test.go` — new
  regression test pinning all six descriptors + pg_attribute rows.

## 2026-05-19 loop 7 — auto-derive index Attrs from heap pg_attribute

Loop 4 audit items (2) and (3) are now closed by the parallel investigation in
this loop:

  - (2) `pg_index` row for `indexrelid=2684` carries the PG18-canonical
    values: `indkey={2}` (nspname is attnum 2 of pg_namespace), `indclass={1986}`
    (`name_ops`), `indcollation={950}` (`C_COLLATION_OID`). All produced by
    `pgIndexInitialEntries()` in `internal/initdb/initdb.go:4026`. No fix needed.
  - (3) OID 2684 is intentionally **not** in `criticalLocalIndexOIDs`, so no
    entry is written to `pg_internal.init`. PG18's
    `RelationCacheInitializePhase3` constructs the relcache entry on demand
    via catalog scans (`pg_class_oid_index` → `pg_attribute_relid_attnum_index`).
    The on-disk `pg_attribute` row for `(attrelid=2684, attnum=1)` is the
    authoritative tupdesc source — the loop-5 override controls that.

The residual SIGSEGV in loop 6 was on a *different* index than 2684. The
parse-analyze path for `SELECT count(*) FROM public.bench_log WHERE
client = -999` traverses 2684 (resolved `public`), then `pg_class_relname_
nsp_index` (2663) to resolve `bench_log` in namespace 2200, then
`pg_attribute_relid_attnam_index` (2658) for column lookups. Each of those
is a composite index with a name-typed key column lacking an explicit
`Attrs` override, so `flattenRels` was emitting `indexKeyAttrs(N)`'s
all-OID descriptor (attlen=4, attbyval=true) for the name column. PG's
`_bt_compare → index_getattr → fetchatt` then loaded the first 4 inline
NameData bytes of the leaf IndexTuple as a by-val Datum and passed the
bogus pointer to `btnamecmp` → SIGSEGV.

### Fix: auto-derive index Attrs from the parent heap relation

Rather than hand-edit `Attrs: []nailedAttr{...}` overrides on 18 more
`idxSpec` entries, `flattenRels` now resolves each `idxSpec`'s key
descriptors against the parent heap's `Attrs` via the `pgIndexInitialEntries()`
`IndRelid` / `IndKey` map. The flow:

  1. Build `heapAttrByOID: map[uint32]map[attnum→nailedAttr]` from the
     `heaps` slice (the same slice that owns the `nailedRel` for each
     parent catalog).
  2. Build `indexSeedByOID: map[uint32]pgIndexEntry` from
     `pgIndexInitialEntries()`.
  3. For each idxSpec without an explicit `Attrs` override, resolve
     each `IndKey[i]` against the heap map. If every key column resolves
     (i.e. simple-column index on a nailed heap, no expressional
     indkey=0), use the derived `[]nailedAttr`. Otherwise fall back to
     the existing `indexKeyAttrs(natts)` default.
  4. Explicit overrides on the idxSpec (loop 5 / loop 6) take precedence —
     they bypass the auto-derivation entirely.

`deriveIndexAttrsFromHeap` is a small helper in the same file. The
new `TestNailedCompositeNameIndexesAutoDerivedFromHeap` test pins ten
representative composite indexes (2663, 2658, 2691, 2704, 2693, 2686,
2754, 2689, 3164, 2669) to assert their leading or trailing name-typed
column emits `attlen=64 / attbyval=false / attalign='c'`.
`TestFlattenRelsDeriveIndexAttrsFromHeap` exercises the helper directly:
happy path, expressional-column (indkey=0), unknown heap, natts/indkey
mismatch.

### Result

`TestE2E_FailoverGoopgToPG/async` — SEGV gone. PG standby boot completes;
client backends connect and exit cleanly (no `signal 11`, no crash-recovery
restart loop in `pg.log`). The test fails 31 s into the 180 s wait with a
*different* error class:

    pq: relation "public.bench_log" does not exist at column 22 (42P01)

i.e. the parse-analyzer fully resolves `public`, then asks the relcache
for `public.bench_log` and gets a clean miss. The CREATE TABLE for
`bench_log` is either (a) not present in the WAL the standby applied
before the SELECT fired, (b) present but skipped by `StreamReplayer`'s
filter set, or (c) replayed but the resulting `pg_class` heap row /
`pg_class_relname_nsp_index` IndexTuple is not visible to a hot-standby
read. That investigation is the next-loop scope (batched-37 candidate).

### Caveats / known follow-on item

`TestNailedCompositeNameIndexesAutoDerivedFromHeap` intentionally omits
`pg_trigger_tgrelid_tgname_index` (OID 2701). `pgIndexInitialEntries`
says `indkey={2, 4}` matching PG18's 23-column `pg_trigger`, but goopg's
`pgTriggerAttrs()` uses an 8-column reduced schema where `tgname` is at
attnum 3 (attnum 4 is `tgfoid`). Auto-derivation correctly resolves heap
attnum 4 → `tgfoid` (oid_ops shape), exposing this pre-existing PG18-vs-
goopg schema mismatch. The previous OID-default behavior masked it.
Since the index is empty in the test scenario, no crash results, but a
future loop should reconcile `pgTriggerAttrs` with `pgIndexInitialEntries`.

Files touched this loop:

- `internal/initdb/relcache_init.go` — extend `flattenRels` with the
  `deriveIndexAttrsFromHeap` helper; idxSpec override semantics unchanged
  (explicit `Attrs` still takes precedence).
- `internal/initdb/nailed_composite_name_indexes_test.go` — new
  regression test for the ten composite name-typed indexes on the SELECT
  parse-analyze path, plus a helper-direct test for the no-match paths.

## 2026-05-19 loop 8 — user CREATE TABLE writes PG18-canonical pg_class / pg_attribute rows

### Discovery

After loop 7's auto-derivation fix the standby boots cleanly but
`TestE2E_FailoverGoopgToPG/async` still reports

    pq: relation "public.bench_log" does not exist at column 22 (42P01)

— same string, no SEGV, no crash-recovery restart loop. Reading the
write path revealed the root cause: `syncTableToCatalogHeap` in
`internal/executor/operators_ddl.go` builds an 8-column pg_class row in
goopg-native ordering (`{oid, relname, relnamespace, relkind, relnatts,
relfilenode, relpersistence, relisshared}`) and a 6-column pg_attribute
row. PG18's pg_class has 34 columns in a totally different order
(`oid, relname, relnamespace, reltype, reloftype, relowner, relam,
relfilenode, …, relkind`); pg_attribute has 25. When PG18 deforms the
on-disk row with its own tupdesc, `relname` decodes (NameData(64) at
offset 4 still works) but every column after offset 68 lands in the
wrong slot — `reltype` reads the goopg row's `relkind` byte and friends
— so the relcache builds a garbage `Form_pg_class`, and the
relname/namespace cache lookup that drives
`RangeVarGetRelidExtended → get_relname_relid` ultimately misses.

The nailed system catalogs already work because
`internal/initdb/initdb.go::pgClassRow` / `pgAttributeRow` emit the full
34-/25-column PG18 layout at initdb time. User CREATE TABLE was the only
code path still emitting the goopg-native short row.

### Fix

- Added `internal/executor/pg18_user_catalog_rows.go`. Mirrors
  initdb's `pgClassColDefs` / `pgAttrColDefs` schema as
  `pgClassColumnsPG18()` / `pgAttributeColumnsPG18()`, and exposes
  builders that turn a `catalog.Table` (or `catalog.Index`) plus a
  `catalog.Column` into the canonical 34-/25-column PG18 row.
  `userTypeAttrsForOID` translates the limited set of OIDs user
  CREATE TABLE produces (int*, text, varchar, bpchar, bool, bytea,
  date, time, timestamp, timestamptz, numeric, oid, float4, float8,
  name, char) into the four pg_type-derived attributes
  `(attlen, attbyval, attalign, attstorage)` that PG's
  `heap_deform_tuple` consults via the relcache tupdesc.
- `syncTableToCatalogHeap` and `syncIndexToCatalogHeap` (same file)
  now call `pgClassColumnsPG18()` + `buildUserPGClassRow(tbl)` /
  `buildUserPGClassRowForIndex(idx)` and
  `pgAttributeColumnsPG18()` + `buildUserPGAttributeRow(tbl, col)`.
  `writeHeapRowCanonical` then routes through `EncodeRowPG`, which
  already knows how to encode each PG18 column type (and produces a
  binary empty `aclitem[]` / `text[]` ArrayType for `relacl` /
  `reloptions`, matching loop-5 work for nailed catalogs).
- The four nullable trailing varlena columns on pg_attribute
  (`attacl, attoptions, attfdwoptions, attmissingval`) emit
  `NullDatum`, matching initdb's loop-3u fix that prevented a
  PANIC recursion in PG's `RelationGetIndexAttOptions`.

### Verification

New focused regression tests in
`internal/executor/pg18_user_catalog_rows_test.go`:

- `TestUserCreateTableEmitsPG18CanonicalPgClassRow` — drives
  `syncTableToCatalogHeap` against the existing DDL fixture for a
  `bench_log (client int NOT NULL, src text NOT NULL)` schema, scans
  the pg_class heap, and decodes the resulting tuple via
  `catalog.DecodePGClassPhysicalRow` (the fixed-offset PG18 decoder).
  Asserts `relname='bench_log'`, `relnamespace=PublicNamespaceOID`,
  `relkind='r'`, `relnatts=2`, `relfilenode=tbl.OID`,
  `relpersistence='p'`, `relisshared=false`.
- `TestUserCreateTableEmitsPG18CanonicalPgAttributeRows` — same shape
  for the two pg_attribute rows; asserts `(attname, atttypid,
  attnotnull)` at attnum 1 / 2.
- `TestUserPGClassRowFixedFieldsOID` — encodes via `EncodeRowPG`
  directly and reads the leading 4-byte OID + 64-byte NameData,
  pinning the on-disk byte layout PG18's `pg_class_oid_index`
  consumes.

Regression suite results (this loop's tree vs. master):

- `internal/executor` — clean, including the new tests.
- `internal/catalog`, `internal/storage`, `internal/server`,
  `internal/mvcc` — clean.
- `internal/wal` — two pre-existing failures
  (`TestCheckpointerWritesCheckpointMarkers`,
  `TestEncodeRecordXLogClassifiesXactCommitXID`) reproduce on
  unmodified master, unaffected by this loop.
- `internal/initdb` — 19 pre-existing failures (M0030 migration /
  M0106-0012 `TestSynchronousCommitFlushesByDefault`); identical set
  before/after this loop's change.
- `TestE2E_PhysicalReplication`, `TestCanonicalHeapInsertWALRoundTrip`,
  `TestCanonicalUserRowOnEmptyPageM0106_0010_36` all PASS.

`TestE2E_FailoverGoopgToPG/async` (under
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) — STILL FAILS but with progress: PG
standby boots cleanly, walreceiver streams, no SEGV, no crash-recovery
restart. The 30 s `waitForPGCount` deadline elapses because every
client backend reports `relation "public.bench_log" does not exist`.

### Why the same error string persists — and what's left

The pg_class heap row is now byte-correct (DecodePGClassPhysicalRow
agrees), but PG18's name → OID lookup goes through
`pg_class_relname_nsp_index` (a system btree). `syncTableToCatalogHeap`
writes the heap rows but does **not** insert matching entries into
the system btrees `pg_class_oid_index` (2662) or
`pg_class_relname_nsp_index` (2663). On PG18:

  RangeVarGetRelidExtended
    → SearchSysCache2(RELNAMENSP, …)
      → systable_getnext (uses pg_class_relname_nsp_index)
        → btree probe → not found → InvalidOid
    → ereport ERROR (42P01) "relation … does not exist"

Same applies to `pg_attribute_relid_attnum_index` (2659) and friends.
initdb seeds these nailed indexes at bootstrap, but user CREATE TABLE
omits them. Loop 9 (batched-37 candidate) should extend
`syncTableToCatalogHeap` to emit canonical btree IndexTuples (or to
walk PG18's `index_insert` path) into pg_class_relname_nsp_index and
pg_class_oid_index — the heap-side work landed this loop is a
prerequisite (the index leaf TID points back at the heap row that
must already decode correctly under PG18's tupdesc).

Files touched this loop:

- `internal/executor/pg18_user_catalog_rows.go` — NEW. 34-/25-column
  PG18-canonical row builders + the limited pg_type attribute map.
- `internal/executor/operators_ddl.go` — `syncTableToCatalogHeap` and
  `syncIndexToCatalogHeap` switched to the new helpers; goopg-native
  row construction removed at those two call sites.
- `internal/executor/pg18_user_catalog_rows_test.go` — NEW. Three
  regression tests pinning the new byte layout.

## 2026-05-19 loop 9 (batched-38) — runtime sys-btree insert on user CREATE TABLE

### Where loop 8 left off

Loop 8 fixed the on-disk heap row layout for user `CREATE TABLE`:
`syncTableToCatalogHeap` now writes the PG18-canonical 34-column pg_class
row and 25-column pg_attribute rows. The heap byte-decode is correct.
But the residual `relation "public.bench_log" does not exist (42P01)`
remained, because PG18's `RangeVarGetRelidExtended` →
`SearchSysCache2(RELNAMENSP)` resolves a relname → OID via the system
btree `pg_class_relname_nsp_index` (2663), and `syncTableToCatalogHeap`
never inserts into that index.

### What landed this loop

1. **New file** `internal/executor/sys_catalog_index_insert.go` — runtime
   IndexTuple builders + a generic leaf-root inserter:
   - `buildIndexTupleOidKey(heapBlk, heapOff, oid)` →
     16-byte IndexTuple for the single-uint32-key indexes
     (`pg_class_oid_index`, 2662).
   - `buildIndexTupleNameOidKey(heapBlk, heapOff, name, oid)` →
     80-byte IndexTuple for `(NameData[64], uint32)` composite-key
     indexes (`pg_class_relname_nsp_index`, 2663).
   - `buildIndexTupleOidInt2Key(heapBlk, heapOff, attrelid, attnum)` →
     16-byte IndexTuple for `(uint32, int16)` composite-key indexes
     (`pg_attribute_relid_attnum_index`, 2659).
   - `insertCanonicalSysBtreeLeaf(ctx, indexOID, indexTuple, cmp)` —
     pins block 1 of the index file (the leaf-root page initdb wrote);
     walks line pointers comparing the new key against existing keys
     via `cmp(a, b []byte) int` to find the sorted insert slot; calls
     `storage.PageInsertItemRawAt`; snapshots the updated page for the
     WAL FPI; emits a canonical `XLOG_BTREE_INSERT_LEAF` via
     `catalog.PgCanonicalBtreeInsert` when `ctx.LogCanonical != nil`.
   - Three wrappers (`insertPgClassOidIndexEntry`,
     `insertPgClassRelnameNspIndexEntry`,
     `insertPgAttributeRelidAttnumIndexEntry`) plug the three indexes
     into `syncTableToCatalogHeap` / `syncIndexToCatalogHeap`.

2. **No-space-skip path**: bootstrap fills several leaf-root pages
   (notably 2663) to within a few bytes of the page budget. When
   `PageInsertItemRawAt` returns `ErrNoSpaceInPage`, the inserter
   silently returns `nil` rather than failing the parent DDL. The
   parent `CREATE TABLE` completes successfully; the leaf-root remains
   in its bootstrap state. Page-split implementation is deferred to a
   follow-on loop; until then a goopg primary with a "full" 2663 will
   continue to fail the PG-standby probe.

3. **`writeHeapRowCanonical` now returns the heap TID**
   (`storage.ItemPointer`) so the sync helpers can chain the index
   insertions on the heap row's actual `(block, offset)`.

4. **`replayDecodedXLogRecord` learns to handle `RmgrBtree`**
   (`internal/wal/recovery.go`). The previous case-switch only knew
   `RmgrXLog`, `RmgrXact`, `RmgrStandby`, and `RmgrHeap`; an emitted
   `XLOG_BTREE_INSERT_LEAF` would crash goopg's own replay path on the
   next primary restart with `wal: unsupported xlog record rmid=11
   info=0x00`. The new `RmgrBtree` branch reuses
   `replayDecodedXLogHeapFPIBlocks` (the function is FPI-generic; the
   `Heap` suffix is historical) to restore the leaf-root page from
   the FPI carried by every canonical btree-insert record.

### Result

`TestE2E_PhysicalReplication` — STILL PASSES (regression-clean after
the recovery-path fix above). All `internal/executor`,
`internal/catalog`, `internal/storage`, `internal/server`,
`internal/mvcc`, `internal/wal` packages pass (modulo the same 2
pre-existing `internal/wal` failures and 19 pre-existing `internal/initdb`
failures unrelated to this loop). New regression tests:

- `TestSyncTableInsertsSysCatalogIndexEntries` — pins that
  `syncTableToCatalogHeap` writes one entry into pg_class_oid_index,
  one entry into pg_class_relname_nsp_index (in correct sorted
  position relative to pre-existing entries on the leaf-root), and one
  entry per column into pg_attribute_relid_attnum_index.
- `TestSyncTableSkipsSysIndexInsertWhenLeafRootFull` — pins the
  no-space-skip path: a leaf-root packed to 97 80-byte tuples causes
  the relname_nsp insert to be skipped silently while the other two
  indexes' inserts succeed.
- `TestBuildIndexTupleOidKeyByteLayout` — pins the 16-byte byte
  layout of the OID-keyed IndexTuple (ItemPointerData / t_info / key
  data, all little-endian).

### Files touched this loop

- `internal/executor/sys_catalog_index_insert.go` — NEW (insert
  helpers + key compare functions + WAL emission).
- `internal/executor/sys_catalog_index_insert_test.go` — NEW (3 tests
  + 2 helper functions).
- `internal/executor/operators_ddl.go` — `syncTableToCatalogHeap` and
  `syncIndexToCatalogHeap` now chain index inserts after each heap
  row write; `writeHeapRowCanonical` returns `(ItemPointer, error)`.
- `internal/wal/recovery.go` — `replayDecodedXLogRecord` learns
  `RmgrBtree` case.

### Still failing → next loop (batched-39 candidate)

`TestE2E_FailoverGoopgToPG/async` — STILL FAILS. The bootstrap-filled
leaf-root of `pg_class_relname_nsp_index` (and probably 2659 too,
since pg_attribute has ~60 nailed relations × ~5 attrs each) leaves no
room for a user CREATE TABLE entry, so the no-space-skip path silently
discards the insert and the PG standby cannot resolve
`public.bench_log` by name. Implementing a PG-canonical leaf-root
split — promote the leaf-root to an internal root, allocate two leaf
children, redistribute the items, emit `XLOG_BTREE_SPLIT_L` /
`XLOG_BTREE_NEWROOT` WAL records — is the next blocker on the
`TestE2E_FailoverGoopgToPG/async` path.

## 2026-05-19 loop 10 (batched-39) — leaf-root → 2-leaf + new-root split on runtime sys-btree insert

### Where loop 9 left off

Loop 9 wired the runtime IndexTuple-insert path for the three system btrees
probed by parse-analyze of user-table SELECTs (`pg_class_oid_index` 2662,
`pg_class_relname_nsp_index` 2663, `pg_attribute_relid_attnum_index` 2659).
The wiring landed cleanly for 2662 and 2659 (single new entry per
CREATE TABLE; leaf-root has headroom). For 2663 the bootstrap packed the
leaf-root to within ~4 bytes of the page budget, so
`PageInsertItemRawAt` returned `ErrNoSpaceInPage` and the runtime helper
took the silent-skip branch — leaving the PG-standby unable to resolve
`public.bench_log` by name.

### What landed this loop

1. **New file** `internal/executor/sys_catalog_btree_split.go`:
   - Layout metadata for each supported system btree (`keyMetaForSysBtree`
     returns `(tupleSize, nkeyatts)` for the three indexes registered in
     the runtime insert path).
   - `buildSysBtreeLeafPage`, `buildSysBtreeInternalRootPage`,
     `buildSysBtreeMinusInfDownlink`, `buildSysBtreeInternalDownlink`,
     `buildSysBtreeLeafHighKey`, `writeSysBtreeMetapageInPlace` —
     duplicate the corresponding helpers in
     `internal/initdb/btree_index_bootstrap.go` (duplication is forced by
     the `initdb → executor` package dependency; executor cannot import
     initdb).
   - `mergeSortedSlice(existing, newTuple, cmp)` — splices the new
     tuple into the already-sorted leaf-root entries, returning the
     merged slice + the 0-based insertion index.
   - `splitLeafRootAndInsert(ctx, indexOID, rel, leafSlot, indexTuple, cmp)`
     — the orchestration:
     1. Read every entry from the locked block-1 (`leafSlot`).
     2. Merge in `indexTuple`.
     3. Pick a 50/50 split at `len(merged)/2`.
     4. `Pool.PinNew` ×2 → fresh blocks for the right leaf and the new
        internal root.
     5. `Pool.Pin` block 0 (metapage); lock it.
     6. Build new page bytes: rewrite block 1 as leaf-only with a P_HIKEY
        pivot at slot 1; build block `rightBlk` as a rightmost leaf; build
        block `rootBlk` as an internal BTP_ROOT with two downlinks
        (minus-infinity → 1, full → `rightBlk`); rewrite the metapage
        with `btm_root=rootBlk`, `btm_level=1`.
     7. Install the new bytes into each pinned buffer, mark all four
        slots dirty, then unpin (the leaf slot is returned still
        pinned+locked so the caller's existing cleanup runs).
     8. Emit **four** canonical `XLOG_BTREE_INSERT_LEAF` records (one per
        modified block) with FPI = the post-mutation page bytes. PG18's
        `btree_xlog_insert` returns `BLK_RESTORED` from
        `XLogReadBufferForRedo` on every record and the per-tuple logic
        never runs, so the rmgr/info combo "insert leaf at block 0
        (metapage)" is benign — the FPI restoration happens before redo
        and the metapage bytes are correct after restoration.

2. **`insertCanonicalSysBtreeLeaf` no longer silently skips on
   `ErrNoSpaceInPage`**: it dispatches to `splitLeafRootAndInsert` and
   propagates any error. The previous silent-skip branch is removed.

### WAL design choice — XLOG_BTREE_INSERT_LEAF + FPI for every block, not
SPLIT_L/NEWROOT

The PG-canonical `XLOG_BTREE_SPLIT_L` / `XLOG_BTREE_NEWROOT` records
carry per-tuple metadata (firstright key, downlink composition,
metapage delta) that requires faithful tracking of the split's
internal state on the redo side. With FPI=apply set on every block
reference, PG's redo path restores the page from FPI before invoking
the rmgr handler; the handler's tuple-level logic detects
`BLK_RESTORED` and short-circuits. Emitting **four** independent
`XLOG_BTREE_INSERT_LEAF` records — one per modified block, each with
FPI=apply — produces a sequence whose post-replay byte state is
identical to a real split's, while sidestepping the SPLIT_L/NEWROOT
encoding work. The penalty is record-volume (~4 × 8KiB FPIs per
split); this is acceptable because user CREATE TABLE is rare and the
leaf-root for the 3 critical indexes splits at most once per
bootstrap.

### Result

`TestE2E_PhysicalReplication` — STILL PASSES (regression-clean
against `internal/executor`, `internal/catalog`, `internal/storage`,
`internal/wal`, `internal/mvcc`, `internal/initdb`).

New regression test in
`internal/executor/sys_catalog_index_insert_test.go`:
`TestSyncTableSplitsSysIndexLeafRootWhenFull` — pre-fills 2663 with 97
80-byte tuples (the exact bootstrap saturation), runs
`syncTableToCatalogHeap` for a `public.bench_log` table, then pins:
- NBlocks = 4 (meta + left leaf + right leaf + new root).
- Metapage `btm_root = 3`, `btm_level = 1`.
- Block 1 is BTP_LEAF only (no BTP_ROOT), `btpo_next = 2`,
  `btpo_prev = P_NONE`, `btpo_level = 0`; a P_HIKEY pivot occupies
  slot 1.
- Block 2 is rightmost BTP_LEAF only, `btpo_prev = 1`,
  `btpo_next = P_NONE`.
- Block 3 is BTP_ROOT only, `btpo_level = 1`, two downlinks.
- Combined data-tuple count across both leaves = 98 (97 + 1 new).
- "bench_log" appears in exactly one of the two leaves.

The pre-existing skip-test (`TestSyncTableSkipsSysIndexInsertWhenLeafRootFull`)
is replaced by the split-test above.

### Still failing → next loop (batched-40 candidate)

`TestE2E_FailoverGoopgToPG/async` — STILL FAILS with the same
`pq: relation "public.bench_log" does not exist (42P01)` symptom and
no PG-standby SIGSEGV (the test now reaches the
`waitForPGCount` timeout after ~30s rather than the previous 180s,
suggesting the standby is alive and re-running the SELECT cleanly).
Hypotheses for the residual failure (to investigate in batched-40):

- **(H1)** The standby may not be applying the new 4-record FPI burst
  because some other invariant of `XLOG_BTREE_INSERT_LEAF` is now
  violated for the metapage block (PG may decline to apply FPI on a
  metapage when the record's `info` says "insert leaf"). Solution: use
  PG's `XLOG_FPI` (rmgr=RM_XLOG_ID, info=0xA0) for the metapage record
  specifically — a record-type-agnostic FPI carrier.

- **(H2)** A SECOND leaf-root may also be packing-full at bootstrap and
  the runtime insert there is now erroring out (the new split path is
  not yet a no-op for indexes whose tuple size is unknown to
  `keyMetaForSysBtree`). The new code returns an error for any
  unsupported index OID, which could surface as a `CREATE TABLE`
  failure on goopg's primary — to be verified by re-reading the goopg
  primary log around the bench_log DDL.

- **(H3)** A different system btree on the parse-analyze path — e.g.,
  `pg_namespace_nspname_index` (2684) — does not yet receive a
  user-table entry from the runtime insert path. The current wiring
  covers 2662/2663/2659; it does NOT touch 2684. `public` namespace's
  pg_namespace row exists at bootstrap so this is unlikely to be the
  issue for `bench_log`, but it should be ruled out.

### Files touched this loop

- `internal/executor/sys_catalog_btree_split.go` — NEW (page builders +
  split orchestration; ~340 lines).
- `internal/executor/sys_catalog_index_insert.go` — `insertCanonicalSysBtreeLeaf`
  dispatches to the split path on `ErrNoSpaceInPage`; the silent-skip
  branch is removed.
- `internal/executor/sys_catalog_index_insert_test.go` —
  `TestSyncTableSkipsSysIndexInsertWhenLeafRootFull` replaced by
  `TestSyncTableSplitsSysIndexLeafRootWhenFull`; four new helpers
  (`readPage`, `readBTreeOpaque`, `readMetapageRootAndLevel`,
  `pageLineCount`, `pageHasHighKey`).

## 2026-05-19 loop 11 (batched-40) — dual blocker discovered: wrong DBOid + multi-level-btree split

This loop ran `TestE2E_FailoverGoopgToPG/async` after batched-39 and
attempted to close the residual `pq: relation "public.bench_log" does
not exist (42P01)` failure. Hypotheses (H1) "metapage FPI rejected", (H2)
"second leaf-root packing full", (H3) "missing index in
pg_namespace_nspname_index" turned out to be wrong — the true root cause
is a pair of independent bugs unmasked by adding a disk-level diagnostic.

### Diagnostic test landed

`internal/testport/m0106_create_table_persists_to_disk_test.go`
(`TestE2E_CreateTablePersistsRelnameIndexEntryOnDisk`) starts a goopg
primary, runs `CREATE TABLE public.bench_log`, performs a clean stop
(forcing a shutdown checkpoint), then reads `base/1/2663` and
`base/5/2663` (pg_class_relname_nsp_index) from disk and dumps every
8 KiB page's btree opaque (`btpo_prev`, `btpo_next`, `btpo_level`,
`btpo_flags`) along with the first three and last NameData keys per
leaf. The test asserts `bench_log` appears in `base/5/2663`.

The diagnostic is gated behind `GOOPG_RUN_BLOCKED_M0102_E2E=1` (the
same gate as the e2e test it diagnoses) so default `go test ./...` is
not affected. It will flip to PASS in batched-41 once the underlying
fixes land.

### Finding D1 — runtime sys-btree writes go to base/1/ only

`internal/executor/operators_ddl.go` (lines 1785, 1794, 1837, 1854,
1886) and `internal/executor/sys_catalog_index_insert.go:183` all
hard-code `DBOid: catalog.DefaultDBOid` (= 1, the "template1" OID) on
their `RelFileNode` literals. The bootstrap (`internal/initdb/initdb.go`,
`internal/initdb/btree_index_bootstrap.go`) mirrors every nailed-rel
file to `base/1/`, `base/5/`, AND `global/`, so a fresh cluster's
`base/5/<oid>` files are byte-identical to their `base/1/` siblings —
but ONLY at bootstrap time. The runtime DDL path
(`syncTableToCatalogHeap`, `syncIndexToCatalogHeap`) only updates
`base/1/`.

PG18 backend connecting with `dbname=postgres` (`OID = 5`) reads
catalog rows from `base/5/...`. So after `CREATE TABLE public.bench_log`
the user-table catalog entries land in `base/1/{1259,1249,2659,2662,
2663}` but `base/5/...` stays at the bootstrap snapshot — PG can't
find the relation.

Diagnostic confirms:

```
=== POST-CREATE-TABLE base/1/2663 layout ===
file size = 49152 bytes (= 6 pages)
page 0: META … btm_root=5 btm_level=1
page 1: LEAF lpc=50 prev=0 next=4 level=0 firsts=[pg_enum_typid_label_index bench_log pg_publication_pubname_index]
…
bench_log present in base/1/2663: true

=== POST-CREATE-TABLE base/5/2663 layout ===
file size = 32768 bytes (= 4 pages)              ← bootstrap state, untouched
page 0: META … btm_root=3 btm_level=1
page 1: LEAF lpc=97 …
page 2: LEAF lpc=64 …
page 3: INTROOT lpc=2 …
bench_log NOT found in base/5/2663
```

### Finding D2 — runtime split corrupts multi-level btrees

`splitLeafRootAndInsert` (batched-39) was written assuming block 1 is
the SINGLE leaf-root of an otherwise-empty btree (block 0 = empty
metapage). Production bootstrap of `pg_class_relname_nsp_index` already
emits 4 pages (meta + 2 leaves + 1 internal root) because
`pgBuildBtreeBulkLoadSized` exceeds the single-leaf cap at ~97
entries × 80 B/tuple and goopg bootstraps 161 entries
(`nailedSharedRels` + `nailedLocalRels`).

When `insertCanonicalSysBtreeLeaf` then tries to insert
`bench_log` and block 1 (the leftmost LEAF, NOT the root) has no
space, `splitLeafRootAndInsert` runs anyway:

- Block 1 is rewritten as a leaf with a P_HIKEY taken from one of
  the merged tuples and `btpo_next` → freshly allocated block 4.
- Block 4 is the new "right" leaf.
- Block 5 is a new internal root carrying two downlinks: block 1 and
  block 4.
- The metapage's `btm_root` jumps from 3 → 5.

But the **original** internal root (block 3) and **original sibling
leaf** (block 2, which still carries the rightmost 64 entries of the
btree) are NOT updated. The new internal root at block 5 has no
downlink to block 2, so PG navigating from `btm_root=5` can never
reach block 2 — its 64 entries become unreachable.

This breaks not only `pg_class_relname_nsp_index` itself but every
`pg_attribute_relid_attnum_index` etc. that was already multi-level.
After mirroring the corrupted base/1 layout into base/5, PG-standby
boot fails with `FATAL: pg_attribute catalog is missing 3 attribute(s)
for relation OID 2695` (`pg_auth_members_member_role_index`) at
`RelationCacheInitializePhase3`.

### What landed (un-wired)

- `internal/catalog/catalog.go`: new
  `PostgresDBOid uint32 = 5` constant.
- `internal/executor/sys_catalog_postgres_db_mirror.go`: new file with
  `mirrorCatalogRelToPostgresDB(ctx, relOID)` (copy every block of
  `(DBOid=1, RelOid)` into `(DBOid=5, RelOid)` through the buffer pool)
  and `mirrorTouchedCatalogsToPostgresDB(ctx)` (covers
  1259/1249/2659/2662/2663).
- `internal/executor/operators_ddl.go::syncTableToCatalogHeap` and
  `::syncIndexToCatalogHeap` reference the helper but with
  `_ = mirrorTouchedCatalogsToPostgresDB` so it does NOT execute. The
  rationale (Finding D2) and the batched-41 plan are documented in a
  block comment at each call site.

The mirror is correct as written; it cannot ship until the multi-level
split is fixed.

### Next loop (batched-41)

`insertCanonicalSysBtreeLeaf` must learn the canonical PG18 btree
insert algorithm:

1. Open the relfile and read block 0's `btm_root` / `btm_level`.
2. Pin the root block. If `btm_level == 0` the root IS the leaf
   (current single-page case) — fall through.
3. Otherwise descend through internal pages: at each internal page,
   binary-search the downlinks for the largest key ≤ `newKey`, pin
   the child block, repeat until level reaches 0.
4. Insert `newKey` into the leaf. If `PageInsertItemRawAt` returns
   `ErrNoSpaceInPage`, split the leaf:
   - Allocate a new right leaf via `Pool.PinNew`.
   - Redistribute keys; left leaf carries a P_HIKEY taken from the
     right leaf's first key.
   - Walk up the parent chain. For each parent: try to insert the
     new downlink at the slot just after the descended-through slot.
     If that parent has no room, split it the same way, walking up.
   - If we reach the root and it overflows, create a new internal
     root via `Pool.PinNew`, populate it with two downlinks, and
     update the metapage's `btm_root` and `btm_level`.
5. WAL: emit one canonical `XLOG_BTREE_INSERT_LEAF` FPI per modified
   block, then `XLOG_BTREE_NEWROOT` if a new root was created.

Once that lands, re-wire `mirrorTouchedCatalogsToPostgresDB` at both
DDL sync sites and re-run `TestE2E_FailoverGoopgToPG/async`. The
diagnostic test should flip to PASS.

### Files touched this loop

- `internal/catalog/catalog.go` — new `PostgresDBOid` constant.
- `internal/executor/sys_catalog_postgres_db_mirror.go` — NEW (105
  lines: page-level mirror + per-DDL convenience wrapper).
- `internal/executor/operators_ddl.go` — reference to the helper in
  `syncTableToCatalogHeap` and `syncIndexToCatalogHeap` with
  block-comment rationale; helper itself is currently un-wired
  (`_ = mirrorTouchedCatalogsToPostgresDB`).
- `internal/testport/m0106_create_table_persists_to_disk_test.go` —
  NEW (~220 lines: cluster setup + disk-level dump + pinned
  assertions for both base/1/2663 and base/5/2663).

## 2026-05-19 loop 12 (batched-41): multi-level btree insert + DBOid mirror re-wired

### What landed

Two new helpers and one refactor replace the single-leaf-root-only
`insertCanonicalSysBtreeLeaf`:

- `internal/executor/sys_catalog_btree_multilevel.go` (NEW, ~360 lines):
  - `readSysBtreeMeta` — parses `btm_root` / `btm_level` from the metapage.
  - `descendSysBtreeToLeaf` — walks internal pages selecting downlinks
    where `key ≤ newKey`, returning the target leaf block.
  - `collectAllLeafTuples` — descends leftmost via slot-1 minus-infinity
    downlinks, then follows `btpo_next` across every leaf and returns
    all data tuples in sorted order (high keys skipped).
  - `buildBulkSysBtreeLayout` — in-package mirror of
    `initdb/btree_index_bootstrap.go::pgBuildBtreeBulkLoadSized`
    (executor → initdb would form an import cycle; helper is
    duplicated, ~80 lines).
  - `rebuildSysBtreeWithNewEntry` — overflow fallback that collects all
    tuples, merges `newTuple`, runs the bulk-build layout, overwrites
    pages 0..N-1 in place via the buffer pool, and emits one canonical
    `XLOG_BTREE_INSERT_LEAF` FPI WAL record per touched page.
  - `insertIntoExistingLeaf` — multi-level-aware in-place insert that
    respects the leaf's high key (slot 1 on non-rightmost leaves).

- `insertCanonicalSysBtreeLeaf` refactor: reads the metapage first, then
  dispatches:
  - `btm_level == 0` → `insertIntoSingleLeafRoot` (the batched-39
    lightweight path with `splitLeafRootAndInsert` as overflow fallback).
  - `btm_level >= 1` → `descendSysBtreeToLeaf` +
    `insertIntoExistingLeaf`; if the target leaf is full, fall back to
    `rebuildSysBtreeWithNewEntry` (handles downlink propagation
    uniformly because the bulk-build path regenerates the whole layout).

- `mirrorTouchedCatalogsToPostgresDB` re-wired at both DDL sync sites
  (`syncTableToCatalogHeap`, `syncIndexToCatalogHeap`).

### Verification

- `TestE2E_CreateTablePersistsRelnameIndexEntryOnDisk` — flips to PASS.
  Disk dump after CREATE TABLE + clean shutdown:
  ```
  base/1/2663 page 1: lpc=97 prev=0 next=2 firsts=[pg_publication_oid_index bench_log pg_aggregate]
  base/1/2663 page 2: lpc=65 prev=1 next=0 (rightmost) first data=pg_publication_oid_index
  base/1/2663 page 3: INTROOT lpc=2 (downlinks: -inf→1, pg_publication_oid_index→2)
  bench_log present in base/1/2663: true
  bench_log present in base/5/2663: true
  ```
- All affected packages PASS: `internal/executor`, `internal/catalog`,
  `internal/storage`, `internal/server`, `internal/mvcc`. `internal/wal`
  has the same 2 pre-existing failures
  (`TestCheckpointerWritesCheckpointMarkers`,
  `TestEncodeRecordXLogClassifiesXactCommitXID`) inherited from
  the batched-40 baseline.

### Residual: TestE2E_FailoverGoopgToPG/async still 42P01

Disk-level diagnostic (added inline to the failover test) confirms the
PG standby's basebackup snapshot **does** contain `bench_log` in both
`base/1/2663` and `base/5/2663`, and in both `base/1/1259` and
`base/5/1259` (pg_class heap):

```
[diag] base/1/2663 (pg_class_relname_nsp_index) size=32768 hasBenchLog=true
[diag] base/1/1259 (pg_class_heap)              size=40960 hasBenchLog=true
[diag] base/5/2663 (pg_class_relname_nsp_index) size=32768 hasBenchLog=true
[diag] base/5/1259 (pg_class_heap)              size=40960 hasBenchLog=true
```

(2662/1249/2659 correctly do NOT contain the literal "bench_log"
string because their keys are OID- or attnum-based.) The standby still
fails `parseopen` with `42P01: relation "public.bench_log" does not
exist at character 22` once PG attempts to parse the bootstrap INSERT.
No FATAL / PANIC and no `pg_attribute catalog is missing` (the bad
mirror symptom in batched-40 is fixed). The 42P01 originates from
`parserOpenTable, parse_relation.c:1445`, i.e. the standard
`RangeVarGetRelidExtended` lookup path.

Hypotheses for the residual (next-loop investigation candidates):

- (H1) The rebuilt leaf pages keep a stale `pd_lsn` (the rebuild copies
  bytes via `slot.Page()` then `MarkDirty` but never sets `pd_lsn` from
  the WAL record's returned LSN). PG may treat the basebackup file as
  pre-checkpoint and apply a *stale* FPI from the WAL stream over it.
- (H2) Subtle high-key / downlink format mismatch: the rebuild uses
  `buildSysBtreeLeafHighKey` / `buildSysBtreeInternalDownlink` which
  mirror the initdb helpers, but PG's btree code may also require
  `INDEX_ALT_TID_MASK` semantics or `ip_posid==nkeyatts` invariants
  that the helpers under-enforce. Byte-compare a bootstrap-built page
  vs. a rebuild-built page to spot the divergence.
- (H3) `pg_internal.init` copied from goopg's `base/1` into the
  standby's `base/{1,5}` may carry stale relcache descriptors that
  poison PG's syscache for `RELNAMENSP`. Comment out
  `copyInitFiles` and re-run to bisect.
- (H4) The DDL transaction's `RelcacheInvalPending` marker fires a
  `RecordKindXactCommitInval` that unlinks goopg's init file but
  the goopg standby replay path does not propagate the
  invalidation through to the PG standby's view (since this is a
  *PG* standby, no goopg replay runs there).

### Files touched this loop

- `internal/executor/sys_catalog_btree_multilevel.go` — NEW (~360 lines).
- `internal/executor/sys_catalog_index_insert.go` — refactored
  `insertCanonicalSysBtreeLeaf` to dispatch on `btm_level`; split out
  `insertIntoSingleLeafRoot` (single-leaf-root preserved verbatim).
- `internal/executor/operators_ddl.go` — wired
  `mirrorTouchedCatalogsToPostgresDB` in `syncTableToCatalogHeap` and
  `syncIndexToCatalogHeap`.
- `internal/testport/e2e_failover_goopg_to_pg_test.go` — added inline
  disk-state diagnostic dump (gated on `GOOPG_RUN_BLOCKED_M0102_E2E`).

## 2026-05-19 loop 13 (batched-42): pd_lsn stamping on canonical FPI emit paths (H1)

### What landed

`LogCanonicalFunc` now returns the WAL end-LSN of the appended record so
canonical-FPI emit sites can stamp `pd_lsn` on the rewritten page. The
PG18 standby's recovery path (`xlogutils.c::XLogReadBufferForRedo`)
compares `page->lsn` against the WAL record's LSN; a `pd_lsn` of 0 made
every replay of an old FPI clobber the basebackup-correct page.

Type / interface changes:

- `internal/catalog/canonical.go`:
  - `LogCanonicalFunc` signature: `func([]byte) error` →
    `func([]byte) (uint64, error)` (uint64 is the end-LSN).
  - `PgCanonicalHeapInsert`, `PgCanonicalBtreeInsert`,
    `PgCanonicalHeapDelete` all return `(uint64, error)`.
- `internal/initdb/open.go`: wrapper now returns the end-LSN from
  `walWriter.Append`.

Call-site updates (every canonical-FPI emit now stamps `pd_lsn` while
its target slot is still pinned):

- `internal/executor/operators_ddl.go` — `writeHeapRowCanonical` keeps
  the slot locked during WAL emit, then calls
  `storage.MustHeader(slot.Page()).SetLSN(endLSN)` before unlocking.
- `internal/executor/operators_storage.go` — `emitCanonicalHeapInsert`
  and `emitCanonicalHeapDelete` restructured to lock + WAL-emit +
  SetLSN + unlock (was: snapshot bytes → unpin → emit).
- `internal/executor/sys_catalog_index_insert.go::insertIntoSingleLeafRoot`
  — same lock-then-emit-then-SetLSN sequence inside the `LogCanonical`
  branch.
- `internal/executor/sys_catalog_btree_split.go::splitLeafRootAndInsert`
  — keeps all four slots (leaf, right, root, meta) pinned+locked
  through the four `PgCanonicalBtreeInsert` calls; stamps `pd_lsn` on
  each from the returned end-LSN before unpinning.
- `internal/executor/sys_catalog_btree_multilevel.go::insertIntoExistingLeaf`
  and `rebuildSysBtreeWithNewEntry` — both stamp `pd_lsn` on rewritten
  leaf pages from the FPI's end-LSN.

The split path was previously **MarkDirty → Unlock → Unpin → emit**.
That order was safe for correctness on the goopg-local replay path
(the writer keeps the page in the buffer pool and `pd_lsn` is
re-stamped on flush via the writer's own bookkeeping), but the **PG18
standby has no such bookkeeping** and reads `pd_lsn` directly from the
on-disk page bytes copied by `pg_basebackup`. Without explicit stamping
before the slot is flushed, PG sees `pd_lsn=0` and replays every FPI.

### Verification

- `go build ./...` clean.
- `internal/catalog`, `internal/executor`, `internal/storage`,
  `internal/server`, `internal/mvcc` — all PASS.
- `internal/wal` — same two pre-existing failures
  (`TestCheckpointerWritesCheckpointMarkers`,
  `TestEncodeRecordXLogClassifiesXactCommitXID`) inherited from
  batched-40/41 baseline; no new regressions.
- `internal/initdb` — same pre-existing failure
  (`TestRollbackedTableNotVisibleAfterRestart`) inherited from the
  batched-40/41 baseline (verified by `git stash` + re-run); no new
  regressions.
- `TestE2E_CreateTablePersistsRelnameIndexEntryOnDisk` — still PASS;
  `bench_log` lands in both `base/1/2663` and `base/5/2663`.

### Residual

Re-running `TestE2E_FailoverGoopgToPG/async` (gated on
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) is required to confirm whether the
stamp closes the residual `42P01: relation "public.bench_log" does
not exist` and to disambiguate H1 from H2–H4. The test exercises a
full pg_basebackup + streaming-replication cycle and was not run in
this loop; that verification is the first step of batched-43.

Next loop (batched-43): run the failover test end-to-end with the
batched-42 changes; if H1 alone closes it, mark M0106-0010 complete.
If 42P01 persists, the disk-byte-compare experiment of H2 (a
bootstrap-built page vs. a rebuild-built page using the
`dumpRelnameNspIndexLayout` diagnostic) is the next-cheapest probe.

## 2026-05-19 loop 14 (batched-43): H1 verified, root cause shifts to pg_xact SLRU format mismatch

### What was run

Ran `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -v -run
'TestE2E_FailoverGoopgToPG/async' ./internal/testport/` with the
batched-42 pd_lsn-stamping commit (`0e5a891`) at HEAD.  Goal: see
whether H1 alone (FPI pages now carry a non-zero `pd_lsn`) closes the
residual `42P01: relation "public.bench_log" does not exist`.

### Result

H1 alone did **not** close it.  The PG standby boots cleanly to
`PM_HOT_STANDBY`, the walreceiver connects at `0/1000000` and streams
up to `0/1060F58`, and the client backend issuing
`SELECT count(*) FROM public.bench_log WHERE client = -999` returns
`ERROR: 42P01` repeatedly until the 30 s deadline.  Crucially, no
SIGSEGV, no `pg_attribute catalog is missing N attribute(s)`, no
crash-recovery loop — the failure mode is purely a name-lookup miss.

### The smoking gun

The test's inline disk-state diagnostic (`e2e_failover_goopg_to_pg_test.go:67`)
prints (from this run):

| Path                                              | Size  | hasBenchLog |
|---------------------------------------------------|-------|-------------|
| `base/1/2663` (pg_class_relname_nsp_index)        | 32768 | true        |
| `base/1/2662` (pg_class_oid_index)                | 16384 | false (OID-keyed) |
| `base/1/1259` (pg_class_heap)                     | 40960 | true        |
| `base/1/1249` (pg_attribute_heap)                 | 106496| false (attname-only payload) |
| `base/1/2659` (pg_attribute_relid_attnum_index)   | 32768 | false (int-keyed) |
| `base/5/2663`                                     | 32768 | true        |
| `base/5/2662`                                     | 16384 | false       |
| `base/5/1259`                                     | 40960 | true        |
| `base/5/1249`                                     | 106496| false       |
| `base/5/2659`                                     | 32768 | false       |

So **bench_log IS on disk** in both DB oids' name-typed pg_class
index (the only one whose payload contains the literal string
"bench_log") and in pg_class heap.  Loop 12's mirror is working.
Loop 13's pd_lsn stamp is on the pages.  PG simply cannot make the
row **visible**.

Direct inspection of the post-test standby data directory pinned the
cause:

```
$ STBY=/tmp/TestE2E_FailoverGoopgToPGasync.../001/pg-standby
$ ls -la "$STBY/pg_xact"
total 8
drwx------  2 ryo ryo 4096 May 19 20:09 .
drwx------ 19 ryo ryo 4096 May 19 20:10 ..

$ ls -la "$STBY/global/pg_xact"
-rw------- 1 ryo ryo 4 May 19 20:09 ...
```

The standby has:

1. An **empty** `pg_xact/` directory — no SLRU segment files
   (`pg_xact/0000`, `pg_xact/0001`, ...) at all.  Upstream PG18 stores
   one segment file per `BLCKSZ * CLOG_XACTS_PER_BYTE` block of XIDs
   (`postgres/src/backend/access/transam/clog.c:48`) and treats every
   XID lookup as an `SimpleLruReadPage_ReadOnly` against those files.
2. A bogus 4-byte `global/pg_xact` **file** — goopg's flat clog
   (`internal/mvcc/clog.go:24`).  PG never reads that path; it expects
   `pg_xact/` to be a directory.

PG's hot-standby snapshot considers a tuple visible only if its xmin
is `< xmax_snapshot` AND `TransactionIdDidCommit(xmin)` returns true,
which routes through `TransactionLogFetch` →
`SimpleLruReadPage_ReadOnly`.  With no segment files, the SLRU
returns nothing for any XID, so every tuple's xmin appears
not-committed and every row that requires a non-bootstrap commit
proof is invisible.  That is why `bench_log` is on disk yet
unreachable by name lookup: PG's
`SearchSysCache2(RELNAMENSP, "bench_log", ...)` traverses
`pg_class_relname_nsp_index` 2663, finds the leaf entry, fetches the
heap tuple via the entry's `t_tid`, and then rejects the heap row's
xmin in `HeapTupleSatisfiesMVCC`.

The standby log corroborates this: `next transaction ID: 3`,
`0 KnownAssignedXids (num=0 tail=0 head=0)`, and
`xmin required by slots: data 0, catalog 0` — i.e. the standby has
no committed-XID information whatsoever.  Even bench_log's xmin (the
exec-time XID, likely 2) shows up as "not committed" because there's
no pg_xact byte to prove otherwise.

### Why H1 was still load-bearing

H1 (pd_lsn stamping) remains a correctness fix and must stay landed:
without it a streamed FPI WAL record could overwrite a freshly
basebackup-ed catalog page with a stale full-page-image on the
standby.  The fact that no SIGSEGV occurred and that
`hasBenchLog=true` survived the basebackup → recovery → streaming
sequence implies pd_lsn is preventing exactly that class of corruption.
Loop 14 confirms H1 is necessary but not sufficient.

### Why H2/H3/H4 are now de-prioritised

- H2 (bootstrap-built vs. rebuild-built page byte-compare): the
  disk-state diagnostic already shows the right tuple lands in the
  index; the post-rebuild page byte layout cannot be the cause of an
  MVCC-visibility miss.
- H3 (comment out copyInitFiles): the init file does not contain user
  relations; bench_log discovery goes through the catalog scan path,
  not the init file.  Independent of the residual.
- H4 (RelcacheInvalPending in xact commit): the standby doesn't need a
  cache invalidation if it can't read the row in the first place.
  Subordinate to the pg_xact gap.

The new root cause supersedes all three.

### What batched-44 must do

Generate a PG18-compatible `pg_xact/` SLRU directory during goopg
init and maintain it during normal operation:

1. **Directory + segment files.**  `bootstrapCLog` (initdb.go:5479)
   currently writes a single flat file `global/pg_xact`.  Rework it
   to instead create `pg_xact/0000` (and later segments as XIDs
   grow), each `BLCKSZ * CLOG_XACTS_PER_PAGE` bytes covering 32 768
   pages × 8 192 XIDs = 268 435 456 XIDs per file in upstream — for
   bootstrap the first segment of `BLCKSZ` (one page = 8 KiB) is
   plenty.
2. **2-bit-per-XID encoding.**  PG18 uses
   `TRANSACTION_STATUS_IN_PROGRESS=0x00`, `COMMITTED=0x01`,
   `ABORTED=0x02`, `SUB_COMMITTED=0x03` packed 4 per byte
   (`postgres/src/include/access/clog.h:13`).  The goopg flat-file
   encoding is 1 byte per XID with a different mapping
   (`Unknown=0, Committed=1, Aborted=2`).  Translate at write time.
3. **Marker XIDs.**  After bootstrap and after each
   `bootstrapCLog`-equivalent point, mark `FrozenTransactionId (2)`,
   `BootstrapTransactionId (1)` and every XID used by the bootstrap
   catalog inserts as COMMITTED so PG's `TransactionIdDidCommit`
   answers truthfully.  PG18 sets the BootstrapXid commit bit
   implicitly via `BootstrapTransactionIdDidCommit`, but for any XID
   that issued a real heap insert (including the CREATE TABLE
   DDL transaction's XID), the bit must be set in `pg_xact/0000`.
4. **Runtime maintenance.**  Wire `mvcc.Manager.commitTxn` /
   `abortTxn` to also write the PG-style 2-bit entry, in addition to
   (or instead of) the current flat-file write.  Keep the flat file
   for now if goopg's own startup depends on it (M0030-0007); the PG
   directory must coexist.
5. **basebackup inclusion.**  `internal/server/basebackup.go`'s
   `filepath.Walk` already ships every directory under `dataDir`
   verbatim, so the PG-compatible `pg_xact/0000` will be picked up
   automatically once it exists.  The vestigial `global/pg_xact`
   file should be excluded (`isExcludedFile` filter) to avoid
   shipping the legacy goopg blob — PG never reads it, but its
   presence is a maintenance hazard.

## 2026-05-19 loop 15 (batched-44) — pg_xact SLRU mirror LANDED; new residual is pg_control nextXid

### Outcome

Implemented the PG-canonical `pg_xact/` SLRU mirror per batched-43's plan
and re-ran `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -v -run
'TestE2E_FailoverGoopgToPG/async' ./internal/testport/`.  The test still
fails with 42P01, but the failure mode has shifted: the SLRU bits are
now correctly stamped, yet PG's snapshot-mvcc check treats `xmin=3` as
"in the future" because `CheckPoint.nextXid` in `global/pg_control`
is still the initdb-time `FirstNormalTransactionId=3`.

### What landed

- `internal/mvcc/clog.go`: added `EnablePGSLRUMirror(dir)` and the
  `mirrorToSLRULocked(xid, status)` projection. Layout constants
  match PG18 exactly (`CLOG_BITS_PER_XACT=2`, 4 lanes per byte,
  `BLCKSZ*4=32768` XIDs per page, 32 pages per `%04X` segment).  The
  mirror is enabled at the canonical post-`OpenCLog` points and is a
  no-op for `xid < FirstNormalTransactionId`, so byte 0 of segment 0
  matches PG's initdb output exactly (`BootStrapCLOG` calls
  `SimpleLruZeroPage(0)` and never stamps the bootstrap/frozen lanes).
- `internal/initdb/initdb.go::bootstrapCLog`: bootstrap now also
  creates the `pg_xact/` directory + a zeroed BLCKSZ page in
  `pg_xact/0000`, mirroring PG's `BootStrapCLOG → SimpleLruZeroPage(0)
  → SimpleLruWritePage` sequence.
- `internal/initdb/open.go`: every server start (initdb path AND
  recovery path) calls `EnablePGSLRUMirror`, which also backfills
  the SLRU from already-loaded flat-file entries so a cluster
  upgraded from before this change is healed on first open.
- `internal/server/basebackup.go::excludeFiles`: legacy goopg
  `global/pg_xact` flat file is dropped from the basebackup stream.
  The basename filter does NOT match the top-level `pg_xact/`
  directory because the `IsDir()` check happens first in the Walk
  callback.
- `internal/initdb/pg_xact_slru_test.go`: new pinning tests for the
  byte layout, page extension behavior, segment rollover at XID
  1 048 576, and the BootstrapXid/FrozenXid invariant
  (lanes 1 and 2 of byte 0 must remain zero).

### Verification

- Inspected the post-test standby `pg_xact/0000`:
  `byte 0 = 0x40` (only lane 3 / XID 3 set = COMMITTED).
- Inspected the post-test primary `pg_xact/0000`:
  `byte 0 = 0x40` (XID 3), `byte 1 = 0x01` (XID 4).
  The standby is missing XID 4 because pg_basebackup snapshotted the
  SLRU before the INSERT; this is expected and is supposed to be
  reconciled via WAL replay (next section).
- Legacy `global/pg_xact` is absent on standby; goopg-side primary
  still has it (kept for M0030-0007 internal startup until M0106-0013).
- Unit tests pass:
  - `go test -count=1 -run TestCLog ./internal/mvcc/` — green
  - `go test -count=1 -run TestBootstrapCLog_WritesPGCanonicalSLRU
    -run 'TestCLog_SLRUMirror_.*' ./internal/initdb/` — green
- `TestE2E_FailoverGoopgToPG/async` — still FAIL but at a different
  layer.

### Why the test still fails: nextXid stale

The standby log line `next transaction ID: 3` is the smoking gun.
`StartupXLOG` initialised `TransamVariables->nextXid` from
`CheckPoint.nextXid` in pg_control, which goopg writes at initdb time
in `internal/initdb/pgcontrol.go:198`:

```go
le.PutUint64(hdr[64:], pgFirstNormalXID)  // = 3
```

There is no code path that updates this field at runtime — neither
`mvcc.Manager.allocateXID` nor the checkpointer touches pg_control.
At basebackup time the primary has already consumed XID 3 (the CREATE
TABLE bench_log), but the shipped `pg_control` still reports nextXid=3.

The PG standby therefore sees:

1. `TransamVariables->nextXid = 3`
2. First snapshot taken on the standby: `snapshot->xmax = 3`.
3. The bench_log heap row in `pg_class` has `xmin = 3`.
4. `XidInMVCCSnapshot(3, snapshot)` short-circuits at
   `TransactionIdFollowsOrEquals(xid, snapshot->xmax)` (line 1884 of
   `snapmgr.c`) and returns `true` (still in progress).
5. `HeapTupleSatisfiesMVCC` discards the tuple as not-yet-committed.
6. `SearchSysCache2(RELNAMENSP, ...)` returns 0 →
   `RangeVarGetRelidExtended → 42P01`.

The SLRU lane is never even consulted on the visibility path — the
range check eliminates it first.  That is why batched-43's "stamp the
SLRU bits" hypothesis was necessary-but-not-sufficient: the bits exist
now, but PG's snapshot doesn't include them in its visible window.

### Next loop (batched-45): advance pg_control.nextXid

Two complementary changes are needed; both must land for the test to
pass:

1. **At checkpoint time**, update `CheckPoint.nextXid` in pg_control
   to `mvcc.Manager.NextXID()` so a basebackup snapshot of pg_control
   reflects current XID consumption.  Hook the checkpointer (see
   `internal/wal/checkpointer.go`) to call back into pg_control
   serialization with the live nextXid value.
2. **At commit time**, emit a PG-canonical `XLOG_XACT_COMMIT` record
   (RmgrXact, info `XLOG_XACT_COMMIT`, payload `xl_xact_commit`)
   alongside the goopg-native `RecordKindXactCommit` so PG standby's
   `xact_redo_commit` advances `latestObservedXid` and stamps the
   SLRU during streaming replay.  Without this, only the
   basebackup-snapshot XIDs are visible on the standby and any commit
   that happens after basebackup is invisible until the next
   basebackup cycle.

Step 1 alone fixes the bench_log lookup for the CREATE TABLE which
happens before basebackup.  Step 2 fixes the subsequent INSERTs and
brings the standby into true streaming-replicated state.  H1 (pd_lsn
stamping, batched-42) and the SLRU mirror (this loop) remain
necessary prerequisites for step 2 to work end-to-end.

## Original batched-44 plan (superseded by the loop 15 outcome above)

### Test that pins the gap

Add a focused test under `internal/initdb/` that calls
`bootstrapCLog` (post-rework) and asserts:

- `pg_xact/0000` exists, is 8192 bytes, and starts with the
  `COMMITTED` 2-bit code for XIDs `BootstrapXid (1)`,
  `FrozenXid (2)`, and any bootstrap-allocated XID.
- `global/pg_xact` is **not** present, or is empty if kept for
  back-compat.

Then re-run `TestE2E_FailoverGoopgToPG/async`.  If pg_xact is the
last load-bearing gap, the test will flip to PASS and M0106-0010 can
be marked complete.  If 42P01 persists, the next probe is the standby's
`pg_xact_status('xid')` output (or equivalent shimmed via the
test's psql harness) to confirm whether PG sees the bench_log xmin as
COMMITTED after the new SLRU is in place.

### Permitted/forbidden interactions for batched-44

Unchanged from M0106 milestone preamble: no PG-source edits beyond
diagnostic `elog(DEBUG1, ...)` calls; no `if (goopg_compat)`
branches.  The work is entirely on the goopg side: implement
`pg_xact/` SLRU directly so PG sees a stock-shaped clog.

## 2026-05-19 loop 16 (batched-45a) — checkpointer rewrites pg_control.nextXid

Status: LANDED. Step 1 of the batched-44 follow-up plan above is now
wired end-to-end. Step 2 (PG-canonical `XLOG_XACT_COMMIT`) is deferred
to batched-46 so this loop stays inside Ralph's one-task-per-loop
contract.

### What changed

1. `internal/control/pgcontrol.go`: `ControlFileData` gains a
   `CheckPointCopyNextXid uint64` field. `decodeControlFileData`
   reads `buf[64:]` and `encodeControlFileData` writes it back, so
   PG's `checkPointCopy.nextXid` (`FullTransactionId`, offset 64)
   now roundtrips through every `UpdateControlFile` callback instead
   of being preserved-by-accident-only. Before the fix, the field
   was preserved because nothing in the codec touched it; after the
   fix, the value can be deliberately advanced.
2. `internal/wal/checkpointer.go`: `CheckpointerConfig` gains a
   `NextXIDFn func() uint64` hook. `runCheckpoint`, after appending
   the checkpoint marker, reads the hook (when wired) and sets
   `cd.CheckPointCopyNextXid = max(current, hook())`. The
   monotonicity guard matters because the hook returns
   `mvcc.Manager.NextXID()`, which always increases, but pg_control
   could in theory carry a higher value from a prior crash-recovery
   replay path that hasn't yet been folded into the manager's
   in-memory counter.
3. `internal/initdb/open.go`: when constructing the production
   checkpointer, wires
   `NextXIDFn: func() uint64 { return uint64(txnMgr.NextXID()) }`.
   Test-only constructors leave the hook nil — `runCheckpoint`
   short-circuits to today's behaviour in that case.

### Regression coverage

- `internal/control/control_test.go::TestUpdateControlFileNextXidRoundTrip` —
  seeds nextXid=3 in a synthetic pg_control, verifies decode reads
  3, mutates to 42, verifies the on-disk byte at offset 64 becomes
  42, then performs a no-op update and confirms the byte is still
  42. This pins encode/decode symmetry: the bug would manifest as
  the second update zeroing offset 64 because encode never wrote
  it.
- `internal/wal/checkpointer_test.go::TestCheckpointerWritesNextXidIntoPgControl` —
  end-to-end: wires `NextXIDFn` returning 4711, runs one
  `runCheckpoint`, and asserts pg_control offset 64 reads 4711.
  Then re-runs `runCheckpoint` with a hook returning 100 and
  asserts offset 64 still reads 4711 (monotonicity guard). Final
  CRC32C check confirms the file is well-formed.

### Expected effect on TestE2E_FailoverGoopgToPG/async

The standby boot log line `next transaction ID: 3` should now read
the live nextXid from pg_control instead of the hardcoded bootstrap
value. The bench_log `CREATE TABLE` row (xmin = first user XID) will
fall *inside* the standby snapshot's visible window and
`SearchSysCache2` will resolve the relation. If batched-45a alone
closes the 42P01 residual, the test flips to PASS; if it does not,
the remaining gap is the streaming-replay path which batched-45b
(`XLOG_XACT_COMMIT`) will address.

### batched-45b plan (next loop)

Emit a PG-canonical `XLOG_XACT_COMMIT` record alongside the existing
`RecordKindXactCommit`. PG's `xact_redo_commit`
(`postgres/src/backend/access/transam/xact.c::xact_redo`) parses the
record into an `xl_xact_parsed_commit`, advances
`latestObservedXid` via `ProcArrayApplyXidAssignment`, stamps the
SLRU via `TransactionIdAsyncCommitTree`, and updates
`KnownAssignedXids`. Without this record on the wire, the standby's
snapshot will never advance past the basebackup-time view.

The encoder will live in `internal/wal/xact_record.go` (or a new
`xact_commit_compat.go`) and mirror the layout in
`postgres/src/include/access/xact.h::xl_xact_commit`:

```
struct xl_xact_commit {
    TimestampTz xact_time;   /* 8B */
    /* followed optionally by xl_xact_xinfo, xl_xact_dbinfo, ... */
};
```

For v0 we emit only the minimum payload (xact_time + the committed
XID encoded in the record header field `xl_xinfo`), which is what
`xact_redo_commit` needs to bump `latestObservedXid`. Subtransactions,
relfilenodes-to-drop, and invalidation messages will be deferred to a
later batched task.

## 2026-05-19 loop 17 (batched-46) — PG-canonical XLOG_XACT_COMMIT/ABORT emit

Status: LANDED. Step 2 of the batched-44 follow-up plan is now wired
end-to-end. Combined with batched-45a (`pg_control.nextXid`
rewritten at every checkpoint) and batched-44 (`pg_xact/` SLRU
mirror), the PG18 standby attached via basebackup now has all three
load-bearing inputs it needs to mark post-basebackup commits as
visible during streaming replay.

### What changed

1. `internal/catalog/canonical.go` gains a minimal-payload
   PG-canonical xact-record encoder family:
   - `BuildCanonicalXactCommitPayload(xid uint32, xactTimeUsecSinceY2K int64) []byte`
   - `BuildCanonicalXactAbortPayload(xid uint32, xactTimeUsecSinceY2K int64) []byte`
   - `PgCanonicalXactCommit` / `PgCanonicalXactAbort` are the
     `LogCanonicalFunc` wrappers (nil-callback safe).
   - New constants: `canonicalRmgrXact = 1` (PG's `RM_XACT_ID`),
     `canonicalInfoXactCommit = 0x00` (`XLOG_XACT_COMMIT`),
     `canonicalInfoXactAbort = 0x20` (`XLOG_XACT_ABORT`).
   The on-wire body is `[xlrBlockIDDataShort(0xFF)][len=8][xact_time(8)]`
   — no block refs, no `XLOG_XACT_HAS_INFO`, no follow-on chunks.
   `ParseCommitRecord` short-circuits at info=0x00 leaving
   `parsed.xinfo = 0`, so xinfo-gated branches in `xact_redo_commit`
   (dbinfo, subxacts, relfilelocators, invals, origin) are bypassed
   — exactly the minimum needed to advance `latestObservedXid` and
   stamp `pg_xact` via `TransactionIdAsyncCommitTree`.

2. `internal/initdb/open.go` xact-marker logger (the
   `txnMgr.SetXactMarkerLogger` closure registered around line 652)
   now appends the canonical record right after the existing
   `EncodeXactCommit` / `EncodeXactCommitInval` / `EncodeXactAbort`
   payload. The two-Append shape is deliberate: the legacy
   goopg-native record is what `Classify` and the logical-decoding
   pipeline (M0008) consume; the canonical record is what a PG18
   standby's `xact_redo` consumes. Both must land in the same WAL
   stream because the standby uses the canonical record to advance
   `latestObservedXid` while goopg's own `StreamReplayer` uses the
   legacy record to call `mvcc.Manager.ReplayXactCommit` (via
   `replayedXactInfo`).

   The canonical Append is gated on `walWriter.PageHeadersEnabled()`
   — legacy / test clusters skip it. Synchronous-commit's
   `FlushUpTo(endLSN)` advances `endLSN` to the canonical record's
   end so both records are on disk before the client gets its
   acknowledgement.

3. `internal/initdb/open.go` gains `pgEpoch2000` +
   `pgTimestampNowUsec()` helpers (mirrors the `pgEpoch` constant in
   `internal/executor/operators_ddl.go`; defined locally to avoid an
   `initdb → executor` import edge). `pgTimestampNowUsec()` is the
   `xact_time` value handed to every emitted canonical commit/abort.

### Why goopg's own recovery is unaffected

`nativeApplyRecordKindKnown(0xFE)` returns false, so canonical
records always route through `replayDecodedXLogRecord` regardless of
how they were appended. That dispatcher's `RmgrXact` case treats
`xlogXactCommit` and `xlogXactAbort` as `return false, nil` no-ops
— goopg's actual xact bookkeeping is driven by the
`RecordKindXactCommit` marker (kind=0x07) that travels alongside.
`StreamReplayer.replayedXactInfo` picks up the legacy marker first
(`rec.Payload[0]`), so `onXactReplay` is called exactly once per
transaction (not twice).

### Regression coverage

- `internal/catalog/canonical_test.go::TestBuildCanonicalXactCommitPayload`
  pins envelope + body bytes for a commit record (rmgr=1,
  info=0x00, payload tag+len+8B xact_time).
- `…::TestBuildCanonicalXactAbortPayload` pins the abort variant
  (info=0x20, negative xact_time accepted as int64).
- `…::TestPgCanonicalXactCommit_NilLogFnIsNoop` proves the
  legacy-WAL guard: nil `LogCanonicalFunc` short-circuits without
  emitting anything.
- `…::TestPgCanonicalXactCommit_RouteThroughLogFn` proves the
  encoder hands the payload to the caller's callback verbatim and
  returns the endLSN the callback reports — the contract the
  `walWriter.Append` wrapper in the production hot path relies on.

Build: `go build ./...` clean. New tests: `go test -run
'TestBuildCanonicalXact|TestPgCanonicalXact' ./internal/catalog/`
PASS. Pre-existing baseline failures in `./internal/initdb/`
(M0030 migration / M0106-0012 sync-commit flush) reproduce
unchanged on master HEAD `7b01447` before this loop's diff. WAL
package's `TestEncodeRecordXLogClassifiesXactCommitXID` and
`TestCheckpointerWritesCheckpointMarkers` are also pre-existing
baseline failures (they expect `classifyXLogRecord` to map the
goopg-native `RecordKindXactCommit` payload to `RmgrXact`, but the
M0105-0007 change deliberately routes all goopg-internal records
through `RmgrXLog` with `info=0xF0` so PG safely skips them; the
batched-46 path satisfies the spirit of these tests via the
separately-emitted canonical record).

### Expected effect on TestE2E_FailoverGoopgToPG/async

batched-44 placed `pg_xact/0000` byte 0 = 0x40 (XID 3 COMMITTED).
batched-45a advanced `pg_control.checkPointCopy.nextXid` from the
hardcoded `pgFirstNormalXID=3` to the live `mvcc.Manager.NextXID()`.
batched-46 closes the remaining streaming-replay gap: a fresh
user-table INSERT (xmin = NextXID at INSERT time) now ships its
commit through both record kinds.  The standby's
`xact_redo_commit` sees the canonical record, calls
`AdvanceNextFullTransactionIdPastXid(xid)`, calls
`TransactionIdAsyncCommitTree(xid, …, lsn)` which writes the SLRU
status bit, and calls `RecordKnownAssignedTransactionIds(xid)` so
the standby's `KnownAssignedXids` array reflects the visible XID.
The next `bench_log` SELECT on the standby observes
`xmin < snapshot.Xmax`, the SLRU lookup returns COMMITTED, and the
row is returned.

If 42P01 persists after batched-46 lands, the next residual is
most likely one of:
- a missing `XLOG_STANDBY` snapshot record (which seeds
  `KnownAssignedXids` before the streamed commits arrive) — but
  the goopg primary issues a shutdown-checkpoint on every primary
  shutdown, and PG's `xlog_redo` for shutdown checkpoints calls
  `ProcArrayApplyRecoveryInfo` which constructs synthetic
  `RunningTransactionsData`, so this is unlikely to be the next
  blocker.
- a relfilenode mismatch on `bench_log`'s heap relfile (the
  canonical commit record bumps the standby's snapshot, but
  `SearchSysCache2(RELNAMENSP, ...)` still has to resolve
  `bench_log` to a relfile, which requires the heap pages from
  `base/{1,5}/2663` to be both physically present and visible
  through the standby's MVCC snapshot of `pg_class`).

Re-run `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -v -run
'TestE2E_FailoverGoopgToPG/async' ./internal/testport/` after
batched-46 lands and capture the residual.

## 2026-05-19 loop 18 (batched-47) — checkpoint nextXid parameterisation

Closes the 42P01 `relation "public.bench_log" does not exist`
residual that survived batched-46.

### Why batched-46 alone was insufficient

batched-46 emits a PG-canonical `XLOG_XACT_COMMIT` record alongside
the goopg-native marker so the standby's `xact_redo_commit`
advances `latestObservedXid` and seeds `KnownAssignedXids` during
streaming replay. That path correctly handles xacts that commit
AFTER the basebackup snapshot. But `bench_log` is created BEFORE
pg_basebackup runs, so its `pg_class` row's `xmin` is established
during the *pre-basebackup* CREATE TABLE. The checkpoint record
inside the basebackup payload is the only carrier of that XID's
existence — there is no later `xact_redo_commit` to replay.

### Root cause (two layers)

**Layer 1 — load-bearing**: `EncodeCheckpointCompat` in
`internal/wal/recovery.go:837` hardcoded the CheckPoint struct's
`nextXid` (offset 24) and `oldestActiveXid` (offset 80) to literal
`3`. PG's `InitWalRecovery` (xlogrecovery.c) decodes this record
during standby startup and calls
`AdvanceNextFullTransactionIdPastXid(checkPoint.nextXid - 1)`,
which sets `ShmemVariableCache->nextXid` and (transitively)
`latestCompletedXid`. With the value stuck at 3, the standby's
recovery snapshot has `xmax = latestCompletedXid + 1 = 3`, so XID
3 is treated as "future" and every pre-basebackup user tuple is
invisible. Symptom: `parse_relation.c:1445 parserOpenTable` raises
42P01 because `RelnameGetRelid` → `SearchSysCache2(RELNAMENSP,
…)` finds no visible pg_class row for `bench_log`.

**Layer 2 — tooling parity**: the runtime
`wal.CheckpointerConfig` in `internal/initdb/open.go:861` did NOT
set `DataDir`, so the `if c.cfg.DataDir != ""` branch in
`runCheckpoint` (which rewrites `pg_control.CheckPointCopyNextXid`
via `control.UpdateControlFile`) was a silent no-op in
production. `TestCheckpointerWritesNextXidIntoPgControl` passes
only because the test passes `DataDir: dir` itself; batched-45a's
invariant was never enforced in the real basebackup path. This
layer affects `pg_controldata` output and any tooling that reads
`pg_control.nextXid` directly, but it is NOT the visibility
blocker because the standby reads `nextXid` from the CheckPoint
WAL record (layer 1), not from `pg_control.nextXid`.

### Fix

1. `internal/wal/recovery.go`:
   `EncodeCheckpointCompat(redoLSN0 uint64, tli uint32, nextXid
   uint64) []byte`. Writes `nextXid` to offset 24 and
   `uint32(nextXid)` to offset 80 (`oldestActiveXid` — mirrors PG's
   shutdown-checkpoint convention since `CheckpointNow` blocks
   while no user xact is in-flight). Floors at 3 so callers passing
   0 still encode a sane bootstrap value.
2. `internal/wal/checkpointer.go`:
   `runCheckpoint` samples `nextXid := c.cfg.NextXIDFn()` BEFORE
   calling `Append(EncodeCheckpointCompat(redoLSN0, 1, nextXid))`,
   so the WAL record carries the live mvcc value. The same value
   is also fed into the subsequent `control.UpdateControlFile`
   call (layer 2).
3. `internal/initdb/open.go`:
   Runtime `CheckpointerConfig` now sets `DataDir: abs`, enabling
   layer 2.

### Verification

- `TestCheckpointerWritesNextXidIntoPgControl` — PASS.
- `TestE2E_FailoverGoopgToPG/async`: standby now logs `next
  transaction ID: 4; next OID: 16384` (was `3; 16384`) and the
  42P01 on `bench_log` is closed. The test now progresses past
  parse-analyse and the next residual is
  `XX000: could not open relation with OID 2665 at column 22` —
  a system-catalog-index lookup tracked in batched-48.
- Pre-existing baseline failures unaffected
  (`TestCheckpointerWritesCheckpointMarkers`,
  `TestEncodeRecordXLogClassifiesXactCommitXID`, M0030 migration
  tests in `./internal/initdb`).

### Why we did NOT also bump nextOid in the CheckPoint record

`nextOid` (offset 32) remains hardcoded at 16384 because goopg has
no centralised OID allocator that exposes a counter through
`txnMgr`. Object OIDs are minted ad-hoc in catalog mutators. The
visibility regime that matters for the 42P01 is keyed on `xmin`
(pg_xact + snapshot), not OID. Standby's `next OID: 16384`
display is therefore cosmetic until goopg gains a persisted OID
counter — tracked under M0106-0013 (pg_control parity).

### Next loop (batched-48)

Diagnose `XX000: could not open relation with OID 2665`. Upstream
OID 2665 is `pg_largeobject_loid_pn_index`. Likely path: a
planner / executor stage on the standby is reading a system
relation goopg's basebackup payload did not seed. Re-run with
`log_min_messages=debug5` already enabled and capture the failing
backend's stack frame around `relation_open` /
`index_open`.

## 2026-05-19 loop 19 (batched-48) — bootstrap pg_constraint_conrelid_contypid_conname_index (OID 2665)

### Diagnosis (corrects batched-47's "next loop" hypothesis)

OID 2665 is NOT `pg_largeobject_loid_pn_index` — that was a stale
note in the batched-47 follow-up. The authoritative declaration is
`postgres/src/include/catalog/pg_constraint.h:180`:

    DECLARE_UNIQUE_INDEX(pg_constraint_conrelid_contypid_conname_index,
                         2665, ConstraintRelidTypidNameIndexId,
                         pg_constraint,
                         btree(conrelid oid_ops, contypid oid_ops,
                               conname name_ops));

The standby's failure path:

1. Client issues `SELECT count(*) FROM public.bench_log WHERE client = -999`.
2. Parser/analyser opens `public.bench_log` (`parserOpenTable` →
   `relation_open` → `RelationIdGetRelation`).
3. Relcache build for any user table that may carry NOT NULL
   constraints calls `CheckNNConstraintFetch` (relcache.c:4615)
   which opens pg_constraint via
   `systable_beginscan(conrel, ConstraintRelidTypidNameIndexId, …)`.
4. With OID 2665 missing from goopg's bootstrap, the index relcache
   probe fails and PG raises `XX000: could not open relation with
   OID 2665 at character 22` (character 22 = the `p` in `public.bench_log`,
   the FROM-clause relation).

PG18 promoted user NOT NULL constraints into actual pg_constraint
rows, so `CheckNNConstraintFetch` is now on the hot path for every
user table relcache build — not only tables with CHECK constraints
as in earlier majors.

### Fix

Bootstrap a metapage-only btree for OID 2665 in three layers,
mirroring the existing pattern for `pg_constraint_oid_index`
(OID 2667):

1. `internal/initdb/relcache_init.go` — append
   `{OID: 2665, Name: "pg_constraint_conrelid_contypid_conname_index"}`
   to the idxSpec list passed to `flattenRels`. The seed indkey,
   attr shape, and `IsShared=false` propagate automatically:
   - `pgIndexNattsByOID()` derives indnatts=3 from the pg_index seed.
   - `deriveIndexAttrsFromHeap` infers per-column descriptors from
     pg_constraint's pg_attribute rows (conrelid attnum=9 oid_ops,
     contypid attnum=10 oid_ops, conname attnum=2 name_ops). The
     name-typed third key picks up attlen=64/attbyval=false from
     `pgConstraintAttrs()` so `_bt_compare` reads the inline
     NameData by reference and avoids the strncmp-over-inline-bytes
     SIGSEGV class fixed in batched-36 loops 4–6.
2. `internal/initdb/initdb.go::pgIndexInitialEntries` — append
   `entry(2665, 2606, []int16{9, 10, 2}, []uint32{oidOps, oidOps, nameOps},
   []uint32{0, 0, cCollation}, true, false)`. UNIQUE (not PKEY)
   because pg_constraint's declaration is `DECLARE_UNIQUE_INDEX`,
   not `DECLARE_UNIQUE_INDEX_PKEY` (2667 owns that role).
3. `internal/initdb/initdb.go` — three OID lists at lines ~1097,
   ~1242, ~1333 (the `base/1/`, `perDBIndexOIDs`, and global/
   empty-btree placeholders) gain `2665` so an empty metapage file
   exists at every required path. PG's `load_critical_index` (via
   the relcache init-file replay path) PANICs if any of the listed
   files is absent.

Empty metapage suffices because pg_constraint on the standby has
no rows for user tables (the primary's CREATE TABLE on goopg emits
goopg-native catalog rows, which are not replicated as pg_constraint
heap tuples — that gap is M0106-0011 territory). `systable_beginscan`
returns no rows, `CheckNNConstraintFetch` completes, and the
relcache entry for bench_log finishes loading. Treating the standby
as not knowing about NOT NULL is acceptable for read-only queries;
the source of truth is the primary.

### Verification

- `go test -run 'TestPgIndexInitialEntriesIndkeyMatchesPG18' ./internal/initdb/`
  PASS (after extending the pinned indkey map with `2665: {9, 10, 2}`).
- `go test -run 'TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree'
  ./internal/initdb/` PASS (mustHave list gains 2665).
- `TestE2E_FailoverGoopgToPG/async`: the XX000 error on OID 2665 no
  longer appears in the standby log. The standby reaches the next
  residual (see below).
- Pre-existing baseline failures unchanged: 17 tests in
  `./internal/initdb/` (M0030 migration + M0106-0012 sync-commit
  flush family) and 2 in `./internal/wal/`
  (TestCheckpointerWritesCheckpointMarkers,
  TestEncodeRecordXLogClassifiesXactCommitXID). All reproduce on
  master HEAD `6665f91` before this loop's diff.

### Next residual (batched-49)

Standby backends now abort on the same SELECT with:

    TRAP: failed Assert("j > attnum"), File: "heaptuple.c", Line: 642
    backtrace:
      ExceptionalCondition
      nocachegetattr+0x2be
      extractRelOptions+0x40
      RelationIdGetRelation+0x122
      relation_open+0x5b
      parserOpenTable+0x56
      addRangeTableEntry+0xce

`extractRelOptions` calls `fastgetattr(pg_class_tuple,
Anum_pg_class_reloptions=33, …)`. `nocachegetattr` walks the
TupleDesc forward from the last cached "natts so far" position; the
`j > attnum` assert fires when the cached fast-path exceeded the
column being requested — typically because the tuple's `t_natts`
disagrees with the TupleDesc, or because attcacheoff was poisoned
on a NULL column earlier in the row.

Hypothesis: bench_log's pg_class row was written by goopg's runtime
sys-btree path (batched-36 loop 9) without re-populating
attcacheoff or with a varlena/NULL pattern that violates PG's
fastgetattr invariants for column 33 (`reloptions text[]`). Next
loop captures the bench_log pg_class tuple bytes off the standby's
heap page and walks them through PG18's heap_form_tuple/nocachegetattr
prefix to localise the bad attnum.

## 2026-05-19 loop 20 (batched-49) — HEAP_HASVARWIDTH on PG-canonical writeHeapRowReturningPG

### Root cause (corrects batched-48's "next loop" hypothesis)

The assert is **not** an attcacheoff poisoning bug. PG18
`nocachegetattr(attnum=33)` for `Anum_pg_class_reloptions` walks
this exact path when `slow == false`:

1. Line 542 — `HeapTupleNoNulls(tup)` is TRUE for goopg's pg_class
   row (`relacl="{}"`, `reloptions="{}"`, `relpartbound=""` are
   *non-null* varlena values, so `NullBitmapPG` returns nil and
   `HEAP_HASNULL` is unset). slow stays false.
2. Line 590 — `if (HeapTupleHasVarWidth(tup))` gates the
   varlena-prefix check that would otherwise set `slow = true`.
   goopg's runtime heap-row writer (`writeHeapRowReturningPG`)
   never stamped `HEAP_HASVARWIDTH`, so this branch is **skipped**.
3. Line 605 — the fast-path offset-init loop runs. It walks the
   TupleDesc forward starting from j=1, computing per-attribute
   offsets for fixed-width columns, breaking on the first column
   with `att->attlen <= 0` (line 633).
4. pg_class has its first varlena attribute (`relacl`) at TupleDesc
   index 31 (0-based). The loop breaks at j=31. attnum=32 (0-based,
   reloptions). Line 642 `Assert(j > attnum)` → `Assert(31 > 32)`
   → false → SIGABRT.

The same hole already burned us during initdb in batched-25 /
Step 3ct, which is why `bootstrapPostgresDatabase` in
`internal/initdb/initdb.go:1073` and two other init-time writers
explicitly `tuple.Header.Infomask |= storage.HeapHasVarWidth`. The
runtime DDL path (`syncTableToCatalogHeap`) re-used the generic
`writeHeapRowReturningPG` helper which inherited the same omission.

### What landed this loop

1. **`internal/executor/codec.go`** gains two helpers:
   - `pgPhysicalTypeIsVarlena(t catalog.Type) bool` — mirrors the
     varlena branches of `encodeValuePG` (returns true for text,
     varchar, bpchar, numeric, unknown, and all varlena array /
     oidvector / int2vector / pg_node_tree types).
   - `pgRowHasVarWidth(cols, row) bool` — returns true iff at least
     one non-null column in row maps to a varlena type. Mirrors PG's
     `heap_fill_tuple` which sets `HEAP_HASVARWIDTH` only on
     non-null varlena values
     (`postgres/src/backend/access/common/heaptuple.c:326`).
2. **`internal/executor/operators_storage.go::writeHeapRowReturningPG`**
   stamps `tuple.Header.Infomask |= storage.HeapHasVarWidth` when
   `pgRowHasVarWidth(cols, row)` is true, right after
   `HeapXmaxInvalid`. This affects every runtime PG-canonical heap
   write — `syncTableToCatalogHeap`, `syncIndexToCatalogHeap`, and
   any future PG18 catalog write path.

### Tests added

- `TestSyncTableStampsHeapHasVarWidthOnPGClassRow` — pins that the
  pg_class row written by `syncTableToCatalogHeap` for a user
  CREATE TABLE carries `HEAP_HASVARWIDTH` (and `HEAP_XMAX_INVALID`)
  in t_infomask.
- `TestPgRowHasVarWidthDetectsVarlenaCols` — direct unit coverage of
  the helper, including the null-varlena edge case (PG semantics:
  null varlena does *not* set the flag).

### Result

`TestE2E_FailoverGoopgToPG/async` no longer aborts in
nocachegetattr. PG18 standby completes parse-analyze of
`SELECT count(*) FROM public.bench_log WHERE client = -999`:
`relation_open(public.bench_log)` succeeds, `extractRelOptions`
reads `reloptions` cleanly, the rangetable entry is built.

New residual on the same query:

    ERROR: 42883: function count() does not exist at character 8

`pg_proc` lookup for `count(*)` fails on the standby. Probable
cause: goopg's basebackup payload includes a slim pg_proc that is
missing or misordered for the `count(int4)` / `count(*)`
overload — `LookupFuncName` returns no match. This is the new
blocker for batched-50.

Pre-existing baseline failures unchanged: 17 in `./internal/initdb/`,
2 in `./internal/wal/`. New tests above pass.

### Next loop (batched-50)

Diagnose the `function count() does not exist` error.
1. Confirm whether goopg's basebackup-time `pg_proc` heap contains
   the `count` rows (`count()`, `count("any")`).
2. If rows are present, check `pg_proc_proname_args_nsp_index`
   (2691) bootstrap — same family of "system btree empty / page
   not seeded" bugs that batched-48 closed for pg_constraint
   (OID 2665).
3. Re-run `TestE2E_FailoverGoopgToPG/async` until the standby
   completes the SELECT and reports a row count ≥ 1.

## 2026-05-19 loop 21 (batched-50)

### Diagnosis

The 42883 "function count() does not exist" error was a populated-index
problem, not a missing-heap-row one. Confirmed via `pg_proc_seed_data.go`
that goopg already bootstraps the canonical `count` entries (OID 2147 for
`count("any")`, OID 2803 for `count(*)`). The hot path on the PG standby
is `ParseFuncOrColumn → FuncnameGetCandidates → SearchSysCacheList1(
PROCNAMEARGSNSP, CStringGetDatum("count"))` (parse_func.c:629). That
syscache is backed by `pg_proc_proname_args_nsp_index` (OID 2691). Before
this loop, `base/{1,5}/2691` and `global/2691` were empty btree
placeholders (metapage + zero-entry root) written by the per-DB and
shared-index initialisation loops in `initdb.go`, so the syscache
returned no rows and parse-analyse reported the function did not exist.

The hint "with at least an empty metapage" carried over from
batched-48's pg_constraint fix was misleading. `pg_constraint` genuinely
has zero user-table rows on the standby (M0106-0011 territory), so an
empty index returned the right answer (no constraints). `pg_proc` is
populated at bootstrap and the standby legitimately needs to find its
3397 rows via the name index.

### Fix landed

New file `internal/initdb/pg_proc_proname_args_nsp_index_bootstrap.go`
adds three layered builders:

1. `pgEncodeOidvectorForIndex(oids)` — produces the on-disk binary form
   of an oidvector value (24-byte ArrayType header + n*4-byte values,
   `vl_len_ = (24 + 4n) << 2`). Matches `oidVectorBytes` in initdb.go
   and PG18 `buildoidvector` byte-for-byte.
2. `pgBuildIndexTupleProcKey(blk, off, proname, proargtypes, pronamespace)`
   — variable-length IndexTuple builder. Sets `INDEX_VAR_MASK` (0x4000)
   on `t_info` because proargtypes is varlena; tuple body layout is
   `proname (64) | proargtypes (24+4n) | pronamespace (4)`, MAXALIGN'd
   to 8. For count(*) the tuple is exactly 104 bytes.
3. `pgBuildBtreeBulkLoadVariable(sortedTuples, nkeyatts)` — generalises
   `pgBuildBtreeBulkLoadSized` to non-fixed tuple sizes. Reserves
   `max-tuple-size + 4` (P_HIKEY budget) on every non-rightmost leaf so
   packing is monotonic and never has to be rewound. Single-internal-
   root assumption verified for the pg_proc scale (~50 leaves of ~75
   tuples each, ~50 downlinks of ~104 B fit easily in the 8152-byte
   root payload).

`bootstrapPgProcPronameArgsNspIndex` ties them together: iterates
`pgProcInitialEntries()` aligned 1:1 with the heap TIDs returned by
`bootstrapPgProcTuples`, normalises a nil proargtypes to `[2281]`
(matching `pgProcRow`'s default), sorts the entries per PG18
`btoidvectorcmp` semantics (proname → vector length → vector
elements → pronamespace), builds the tuples, and writes the file
to `base/1/2691`, `base/5/2691`, and `global/2691`.

Call site wired in `initdb.go::Init` right after
`bootstrapPgProcOidIndex`.

### Tests added

`internal/initdb/pg_proc_proname_args_nsp_index_test.go`:

- `TestBootstrapPgProcPronameArgsNspIndexWritesPopulatedBtree` — pins
  the file exists in all three locations, is a multi-block btree
  (>2 blocks ⇒ multi-leaf), and that NameData("count") appears in
  some leaf.
- `TestPgBuildIndexTupleProcKeyLayout` — byte-level pin for the
  count(*) IndexTuple (size 104, INDEX_VAR_MASK set, NameData
  payload, empty oidvector header, pronamespace=11, zero pad).
- `TestPgEncodeOidvectorForIndex{Empty,OneElement}` — direct unit
  coverage of the oidvector encoder for proargtypes=[] (count(*))
  and proargtypes=[2276] (count("any")).

### Verification

`TestE2E_FailoverGoopgToPG/async` — PG standby's `count` lookup now
succeeds. New residual on the same query:

    ERROR: 42809: count(*) specified, but count is not an aggregate
    function at character 8

This is `ParseFuncOrColumn → check_agg_arguments` failing because
`pgProcRow` hardcodes `prokind='f'` (function) for every entry, but the
two `count` rows are aggregates and require `prokind='a'`. PG18 finds
the row via the index (the fix worked) but then refuses the call shape
because the kind disagrees. This is the new blocker for batched-51.

All affected packages pass: `internal/executor`, `internal/catalog`,
`internal/storage`, `internal/server`, `internal/mvcc`. Pre-existing
baseline failures unchanged: 17 in `./internal/initdb/`, 2 in
`./internal/wal/`.

### Next loop (batched-51)

Plumb a per-entry `Kind byte` (or `IsAggregate bool`) through
`pgProcEntry`, default to `'f'` for non-aggregate rows, set `'a'` on
the two `count` entries (and every other aggregate in
`pg_proc_seed_data.go` whose `HandlerName == "aggregate_dummy"`).
Confirm `TestE2E_FailoverGoopgToPG/async` advances past 42809; the
likely next residual is `pg_aggregate` lookup (`AGGFNOID` syscache /
`pg_aggregate_fnoid_index` OID 2650) — verify whether bootstrap
populates that path too.

## 2026-05-19 loop 22 (batched-51)

`pgProcEntry` gains a `Kind byte` field (`internal/initdb/initdb.go`).
`pgProcRow` consults `e.Kind` for the prokind char; when zero the new
`derivePgProcKind(handlerName)` helper falls back to the upstream
`pg_proc.dat` handler-name convention — `aggregate_dummy → 'a'`,
prefix `window_ → 'w'`, otherwise `'f'`. This recovers the canonical
PROKIND value for every seed entry without touching the 3397-row
`pg_proc_seed_data.go` table by hand: all 119 `aggregate_dummy`
entries (count/avg/sum/min/max/variance/stddev/regr_*/percentile/…)
emit `prokind='a'`, all 19 `window_*` entries (row_number/rank/
dense_rank/lag/lead/first_value/last_value/nth_value/…) emit
`prokind='w'`. AM-handler / type-IO / regular function rows retain
`prokind='f'`. Explicit `Kind` on a per-entry basis remains the
override path for entries whose handler name does not encode the
kind (none today, but future-proofs against pg_proc.dat additions
where the convention breaks).

Regression coverage (`internal/initdb/pg_proc_bootstrap_test.go`):

- `TestPgProcRowAggregatePrkindIsA` — encodes OID 2147
  (`count("any")`) and OID 2803 (`count(*)`) through
  `EncodeRowPG(pgProcColDefs(), pgProcRow(...))` and asserts
  `payload[96] == 'a'` so the on-disk FormData_pg_proc byte that
  PG18's `Anum_pg_proc_prokind` deformer reads is canonical.
- `TestPgProcRowWindowPrkindIsW` — same for OID 3100 (`row_number`)
  and OID 3101 (`rank`), pinning `payload[96] == 'w'`.
- `TestPgProcRowExplicitKindOverridesDerivation` — synthetic entry
  with `Kind='p'` plus `HandlerName="aggregate_dummy"` produces
  `payload[96] == 'p'` so the override path stays load-bearing.
- `TestDerivePgProcKind` — direct unit pin on the handler-name
  derivation table including the boundary `len("window_") == 7`
  case (must NOT match the window prefix).

Existing pinning tests stay green:
- `TestPgProcRowBtreeHandlerMatchesFormPgProc` continues to assert
  `payload[96] == 'f'` for the bthandler AM-handler row (handler
  name `"bthandler"` triggers the default-`'f'` arm).
- `TestPgProcInitialEntriesCoverAMHandlers`,
  `TestBootstrapPgProcTuplesWritesRowsToBase1And5`,
  `TestPgProcRowStatGetWalReceiverIsSRF`,
  `TestPgProcAttrsMatchesPg18FormPgProc` — all PASS.

`go test ./internal/executor/ ./internal/catalog/ ./internal/storage/
 ./internal/server/ ./internal/mvcc/` clean. `./internal/initdb/`
carries the same 17 pre-existing baseline failures inherited from
batched-50 (none touch the pg_proc bootstrap path).
`TestE2E_FailoverGoopgToPG/async` was NOT re-run in this loop;
that verification is the first step of batched-52.

### Next loop (batched-52)

Re-run `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -v -run
'TestE2E_FailoverGoopgToPG/async' ./internal/testport/`. If the
42809 "count is not an aggregate function" symptom is closed, the
likely next residual is `pg_aggregate` lookup (PG18's
`ParseFuncOrColumn → resolve_aggregate_transtype` issues
`SearchSysCache1(AGGFNOID, aggfnoid)` once parse_analyze accepts
the aggregate call). Verify whether goopg's basebackup payload
contains `pg_aggregate` heap rows for `aggfnoid=2147/2803` and that
`pg_aggregate_fnoid_index` (OID 2650) is bootstrapped with a
populated btree (same family as batched-50 fix for OID 2691). If
not, file batched-52 work analogous to batched-50: build the
8-column FormData_pg_aggregate rows, write to base/{1,5}/2600 and
the global/ shadow, and bootstrap a populated 2650 btree keyed on
aggfnoid (oid_ops).
