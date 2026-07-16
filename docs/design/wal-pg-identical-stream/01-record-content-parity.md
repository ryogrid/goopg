# 01 — Record content parity (Section A)

| Field  | Value                                                        |
| ------ | ------------------------------------------------------------ |
| Status | draft — **agent-reviewed** 2026-07-15 (design verified sound; atomic-flip hazard confirmed real) |
| Date   | 2026-07-15                                                   |
| Scope  | Make heap / heap2 / btree / xact / smgr / clog / xlog record **bodies** byte-identical to PG 18.3 |
| Target | [`../wal-native-pg-format/03-pg183-wal-record-schemas.md`](../wal-native-pg-format/03-pg183-wal-record-schemas.md) (byte layouts) |

## 1. The shape of the gap

goopg's WAL **frame** is PG-faithful and its **decoder** is a complete, faithful
PG reader — `internal/wal/pg_xlog_decode.go` handles `XLogRecordBlockHeader`
(`:242-294`), `XLogRecordBlockImageHeader` + hole reconstruction (`:296-317`),
`RelFileLocator`/`SAME_REL`, all `BKPBLOCK_*`/`BKPIMAGE_*` flags, and the
origin/toplevel-xid chunks. It rejects only compressed images (`:262`).

**The entire gap is on the encode side.** `encodeRecordXLog` (`internal/wal/format.go:247-272`)
does exactly one thing to the body: `wrapXLogMainData(payload)` (`format.go:157-170`)
wraps the whole native `payload` in a single `XLR_BLOCK_ID_DATA_SHORT/LONG`
main-data chunk. It emits **zero** `XLogRecordBlockHeader` / FPI bytes, and
`xl_xid` is hardcoded `0` (`format.go:238`). Every native `Encode*` shares the
prefix `kind(1) | DBOid(4) | RelOid(4) | Fork(1) | blk(4) | …` — none of which
exists in a PG main-data section (the rel lives in a block ref's `RelFileLocator`,
the block number in the block ref, the kind in `xl_info`).

So Part A is: **build PG bodies + real block references on the encode side, and
move replay onto the existing decoder.**

## 2. The keystone: a PG-faithful block-reference + FPI encoder

No block-reference encoder exists anywhere in the tree (repo-wide search for
`XLogRecordBlockHeader`/`BKPBLOCK` construction outside the decoder returns zero
hits). The only one ever written — `buildCanonicalSingleFPIBody` in the deleted
`internal/catalog/canonical.go` (recoverable at `git show 1f0a3eca^:internal/catalog/canonical.go`)
— was single-block, `HAS_IMAGE`-only, `holeOffset=0` (no hole removal), never
`HAS_DATA`. It is a **layout reference, not a reusable component**.

### 2.1 Design

Add a block-reference assembler in `internal/wal` that mirrors PostgreSQL's
`XLogRecordAssemble` (`postgres/src/backend/access/transam/xloginsert.c`) and the
on-wire order documented in [doc 03 §1.3](../wal-native-pg-format/03-pg183-wal-record-schemas.md):

```
type BlockRef struct {
    Block    storage.BlockRef        // rel RelFileLocator + fork + block number
    ForkFlags uint8                  // fork (low nibble) + BKPBLOCK_* flags
    Data     []byte                  // rmgr block data (HAS_DATA)
    Image    *FullPageImage          // optional FPI (HAS_IMAGE)
    WillInit bool                    // BKPBLOCK_WILL_INIT
    SameRel  bool                    // BKPBLOCK_SAME_REL back-ref
}
type FullPageImage struct {
    Page       []byte                // the raw 8192-byte page
    HoleOffset uint16                // = pd_lower  (bytes before the hole)
    HoleLength uint16                // = pd_upper - pd_lower  (BKPIMAGE_HAS_HOLE)
    Apply      bool                  // BKPIMAGE_APPLY
    // compression optional/deferred; PG default wal_compression=off, so uncompressed
    // hole-removed images are byte-identical to a default PG cluster.
}
func assembleXLogRecord(mainData []byte, blocks []BlockRef, xid uint32) (body []byte)
```

`assembleXLogRecord` emits, in order (doc 03 §1.3): per block ascending —
`XLogRecordBlockHeader{id, fork_flags, data_length}`, then (if `HAS_IMAGE`)
`XLogRecordBlockImageHeader{length, hole_offset, bimg_info}`, then (if not
`SAME_REL`) `RelFileLocator` + `BlockNumber`; then the main-data header
(`XLR_BLOCK_ID_DATA_SHORT/LONG`); then the payload region — each block's FPI bytes
(hole-split: `page[0:holeOffset] ++ page[holeOffset+holeLength:]`), then each
block's `Data`, then `mainData`.

**Hole detection** (the piece the deleted builder lacked): read the standard PG
page header `pd_lower`/`pd_upper` (already modeled — `storage.PageHeader`,
`internal/storage/page.go`); set `HoleOffset = pd_lower`,
`HoleLength = pd_upper - pd_lower`, `BKPIMAGE_HAS_HOLE` when `HoleLength > 0`. This
both matches PG byte-for-byte **and shrinks WAL** vs today's full-8192 FPIs.

### 2.2 Validation

The assembler is validated round-trip against `pg_xlog_decode.go` immediately
(`decodeXLogBlockRefHeader` → `XLogBlockRef{Rel, Block, HasImage, Image, Data}`),
and against `pg_waldump` on emitted segments.

## 3. The atomic encode↔replay flip (the central discipline)

goopg's own recovery is a **direct sibling** of the native encoders, coupled in
two compounding ways:

1. `ApplyRecord` (`recovery.go`) confirms `headerMatchesEmittedKind` then switches
   on `payload[0]` into `replayHeapInsert` → `DecodeHeapInsert` → parses the exact
   native wire layout the `Encode*` produced. Encode / Decode / replay are three
   parallel expressions of one native format.
2. `parseXLogRecordData` sets `decoded.Payload = xlogRecord.MainData` **only** when
   there are zero block refs and no origin/toplevel-xid chunks
   (`pg_xlog_decode.go:227-230`). **The moment an encoder emits a real block
   reference, `decoded.Payload` becomes nil**, so `ApplyRecord` falls through to
   `replayDecodedXLogRecord` (`recovery.go:9232`) — which today only restores
   full-page images and `unsupportedDecodedXLogRecord`s anything else.

**Consequence — the rule for every record:**

> Convert a record's **encoder and replayer in the same change**. Rewrite
> `Encode*` → PG body (with block refs), delete the native `Decode*`/`replay*`, and
> add a genuine per-rmgr handler to `replayDecodedXLogRecord` that applies the PG
> main-data + block data / FPI from `r.XLog.Blocks` / `r.XLog.MainData`. Update the
> pgoutput `classifier.go` (a third native-body consumer) for heap records.

A half-flip (PG body, native replay) breaks recovery because `r.Payload` goes nil
and the `payload[0]` switch never runs. This is the highest-risk aspect of Part A;
it is gated per record by G-crash + a round-trip test.

**End state:** `pg_xlog_decode.go` is the *single source of truth* for record
format — read and written the same way — and the native `Decode*`/`replay*`
functions and the `payload[0]` dispatch in `ApplyRecord` are retired as records
convert. `replayDecodedXLogRecord` grows from FPI-only to a full PG rmgr-redo
(mirroring `heap_redo`/`btree_redo`/`xact_redo`).

## 4. `xl_xid` threading

`classifyXLogRecord` returns `xid=0` unconditionally and `Writer.Append(payload)`
has no xid parameter, so there is no channel to carry the xid to
`encodeRecordXLog`. PG bodies do **not** carry the xid in main-data, so it must be
threaded via the header. Plan: add an `xid uint32` parameter to `Append` /
`appendPGCompat` / `encodeRecordXLog` (the header encoder already has the field —
`XLogRecord.XID`, set at `format.go:257`), and stamp the live backend xid at each
emit site. The xid is already in hand: at commit (`open.go` `SetXactMarkerLogger`
receives `xid`), and for heap records via `Context.MaterializeWriterXID()` /
`effectiveWriterXID` (`operators_storage.go`).

## 5. Unify the FPI and the logical record

Today goopg emits the FPI (`storage.Pool.maybeEmitFPI` → `EncodePageImage`) and the
logical mutation record (`markHeapInsertDirty` → `MarkDirtyLogicalChange`) as **two
separate WAL records**. PostgreSQL emits **one** record whose block 0 optionally
carries the FPI (`BKPBLOCK_HAS_IMAGE`) alongside the block data. The design unifies
them: the mutation emit path builds one PG record with the block reference, and
consults the existing first-touch/redo-gating logic (`needsImage` /
`PublishRedoRecPtr`, `internal/storage/bufpool.go`) to decide whether that block
ref also carries an FPI. The FPI trigger, page copy, and redo-pointer publication
are reused; only the *record they attach to* changes from a standalone `XLOG_FPI`
to block 0 of the mutation record. (Standalone `XLOG_FPI` remains for the genuine
"FPI for hint" / checkpoint-forced cases PG also emits standalone.)

`predictXLogRecordLen` (used for pre-reservation, `appendPGCompat`) currently
assumes body length `== len(payload)`; once bodies are assembled (block headers,
FPI, hole), the predictor must compute the assembled length, or record assembly
moves ahead of the space reservation. The emit plumbing (`writer.go`,
`insert_pos.go`, `append_xlog_payload.go`) is otherwise payload-agnostic and needs
no per-record change.

## 6. Per-record body targets

Target byte layouts are [doc 03](../wal-native-pg-format/03-pg183-wal-record-schemas.md);
current native encoders are in `internal/wal/recovery.go`. The common transform for
all of them: **move `RelFileNode`+`blk` out of main-data into a constructed block
reference; shrink main-data to PG's struct; carry tuple/item bytes as block-0 data;
attach an FPI to block 0 only on first-touch.**

| Record | native encoder | PG target (doc 03 §) | key delta |
| --- | --- | --- | --- |
| HeapInsert (4) | `recovery.go:7564` | §2.1 `xl_heap_insert{offnum,flags}` + blk0 `xl_heap_header`+tuple | tuple bytes → blk0 `HAS_DATA`; `CONTAINS_NEW_TUPLE`; FPI first-touch only |
| HeapDelete (6) | `recovery.go:7603` | §2.2 `xl_heap_delete{xmax,offnum,infobits_set,flags}` | derive `infobits_set` from tuple infomask (incl. `HEAP_KEYS_UPDATED`) + `XLH_DELETE_*` |
| HeapHotUpdate (13) | `recovery.go:7691` | §2.3 `xl_heap_update`(14) + 2 block refs | 2-block layout + prefix/suffix flags; route real non-HOT updates here (not Delete+Insert) |
| HeapLock (10) | `recovery.go:7651` | §2.5 `xl_heap_lock`(8) | map lock strength → `infobits_set` `XLHL_*` |
| HeapPruneOpt (14)/HeapVacuum (7)/HeapFreeze (26) | `recovery.go:7740/7866/8185` | §2.7 composite `xl_heap_prune`(2) + `XLHP_*` sub-records | fold 3 goopg records into one `xl_heap_prune`; opcode by reason; freeze plans for cleanup |
| SmgrCreate (11) | `recovery.go:7805` | §6.2 `xl_smgr_create{rlocator,forkNum}`(16) | map goopg rel → `RelFileLocator`+`ForkNumber` |
| BtreeInsert (5) | `recovery.go:8504` | §3.1 `xl_btree_insert{offnum}` + blk0 tuple | PG `IndexTupleData` (t_tid+t_info) as blk0 data |
| BtreeSplit (3) | `recovery.go:8553` | §3.2 `xl_btree_split`(10) + 4 block refs | drop full-page embed; right-half items as blk1 data |
| BtreeVacuum (22) | `recovery.go:7926` | §3.4 `xl_btree_vacuum`(4) | switch "kept items" model → "deleted/updated offsets" |
| BtreeNewRoot (24) | `recovery.go:8080` | §3.6 `xl_btree_newroot`(8) + 3 block refs | items→blk0; left-child + metapage refs |
| BtreeUnlinkPage (23) | `recovery.go:8012` | §3.7 `xl_btree_unlink_page`(36) | full struct incl. pad@12 + 8-aligned `FullTransactionId safexid`@16 |
| BtreeMarkPageHalfDead (25) | `recovery.go:8152` | §3.8 `xl_btree_mark_page_halfdead`(20) | 5-field struct + internal pad |
| XactCommit (8)/CommitInval (32) | `recovery.go:7359/7378` | §4.1 `xl_xact_commit{xact_time}` + xinfo chunks | `xact_time` + `HAS_INFO`; `HAS_INVALS` chunk for the inval variant; subxacts |
| XactAbort (9) | `recovery.go:7367` | §4.2 `xl_xact_abort{xact_time}` | as commit minus invals |
| ClogTruncate (33) | `recovery.go:7389` | §6.1 `xl_clog_truncate`(16) | `pageno` int64 (compute from xid) + db oid |
| Checkpoint (2) | `recovery.go:7469` | §5.1 — already 88-byte `CheckPoint` | tag opcode explicitly (not by length); fill `oldestActiveXid` for online |
| PageImage (1) | `recovery.go:7546` | §5.4 `XLOG_FPI` w/ `XLogRecordBlockImageHeader` + hole | rewrite as block ref with image header; remove hole |
| ParameterChange | `internal/wal/parameter_change.go` | §5.5 — already 28-byte struct | confirm field order |
| segment pad | `internal/wal/segment_pad.go` | §5.3 — already genuine `XLOG_NOOP` | verify `xl_info=0x20`, `xl_rmid=0` |

Also retire the **legacy native frame** (`encodeRecord`/`decodeRecord`,
`format.go:109-134`, IEEE-CRC 8-byte header) once `PageHeaders` is unconditional —
it is used only by non-`PageHeaders` test clusters and is not PG-shaped.

## 7. Reuse inventory (Section A)

| Need | Reuse (file:line) |
| --- | --- |
| PG tuple bytes (`xl_heap_header` + tuple for blk0) | `storage.HeapTuple.MarshalBinary` (`internal/storage/heap.go:317`), `EncodeRowPG` (`internal/executor/codec.go:119`), `NullBitmapPG` (`:133`) — already produced at the emit site (`writeHeapRowReturningPG` `operators_storage.go:8027`, dirtied at `markHeapInsertDirty` `:8222`) |
| Mutation hooks with rel/block/offset/xmax/tuple | `bufpool.go` `LogHeap*Func` (`:661-710`); wired closures `internal/initdb/open.go:408-628` — the single choke point to swap to PG builders |
| FPI first-touch trigger + redo gating + page copy | `storage.Pool.maybeEmitFPI` (`bufpool.go:2147`), `needsImage` (`:957`), `PublishRedoRecPtr`/`RedoRecPtr` (`:874/940`) |
| Page header pd_lower/pd_upper (hole) | `storage.PageHeader` (`internal/storage/page.go`), `SetLSN/LSN` (`:124/134`) |
| btree page mutation + leaf offset | `internal/access/btree/btree.go` `tryInsertNoSplit` (`:2163`, offset `:2194`); split (`:2271`) |
| xid / sub-xid / commit context | `Context.MaterializeWriterXID` (`context.go:1369`), `effectiveWriterXID` (`operators_storage.go:3056`), `mvcc.SubxactMap` (`subxact_visibility.go`), commit hook `SetXactMarkerLogger` (`open.go:910`) |
| Faithful decoder (the replay source) | `internal/wal/pg_xlog_decode.go` (`XLogBlockRef` `:60`, `XLogDecodedRecord` `:74`) |

**Net-new:** the block-ref/FPI encoder (§2); the PG `IndexTupleData` encoder for
btree (`t_tid`+`t_info`, replacing goopg-native `item.marshal`, `btree.go:279`); the
`xl_xact_commit` chunk assembler + subxact/inval collection at commit; the
per-record `replayDecodedXLogRecord` handlers.

## 8. Migration order

1. **Block-ref/FPI encoder** (§2) + round-trip test vs the decoder.
2. **Hot path**: HeapInsert → HeapDelete → HeapHotUpdate → BtreeInsert → XactCommit
   (each: encode+replay+classifier flip together, re-enable its pg_waldump assertion).
3. **heap2 composite**: fold HeapPruneOpt/HeapVacuum/HeapFreeze into `xl_heap_prune`.
4. **btree structural**: Split, NewRoot, Vacuum, UnlinkPage, MarkPageHalfDead.
5. **smgr / clog / FPI / checkpoint-opcode / xact chunks**; retire the legacy frame.

## 9. Verification & risks (Section A)

- Per record: G-crash (`internal/wal` + `internal/initdb` recovery), a round-trip
  test (encode → `pg_xlog_decode` → replay applies the same page state), and the
  record's `pg_waldump` assertion re-enabled.
- goopg↔goopg standby stays green throughout (decoder is the replay source).
- End: a real PG 18 standby replays goopg WAL (`TestE2E_FailoverGoopgToPG`); byte-diff
  a segment vs PG.
- **R1 (critical):** the sibling atomic flip — never land a PG body without its
  replay. **R2:** `predictXLogRecordLen` vs assembled length. **R3:** the
  FPI/logical unification changes the *number* of WAL records — audit every consumer
  that counts records or LSN deltas: the standby apply cursor / stream replayer, the
  startup **WAL-decode memoization** across recovery passes (perf-optimize2 fix-05),
  segment retention, and any FPI-count assertions in tests. **R4:** data-dir format
  break → fresh clusters.
