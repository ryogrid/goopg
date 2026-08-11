# PG WAL opcode coverage — replay a crashed PostgreSQL's heap, btree and xact tail

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (Theme F, S21a + S21b + S22)

## Problem

goopg's PG-format replay switch (`replayDecodedXLogRecord`,
`internal/wal/recovery.go:2207-2528`) has an arm for every rmgr goopg *emits*, and
inside each arm a `case` for every opcode goopg *emits* — nothing else. That
suffices for goopg-authored WAL and for a **cleanly** shut down PG directory,
whose post-checkpoint tail is empty (`0130-0002` §"WAL replay constraint"), but
not for a **crashed** one, whose tail is whatever the workload was doing when the
postmaster died.

Across the eight rmgrs goopg claims, upstream defines **59 opcodes**; goopg
handles **23**. Of the 36 it does not: **17 fail loudly**
(`unsupportedDecodedXLogRecord`, `recovery.go:2605-2615`) and **19 fail silently**
— 12 RM_XLOG opcodes via `return false, nil` (`recovery.go:2245-2248`) and 7 btree
opcodes via the FPI-only arm (`recovery.go:2516-2524`), which `continue`s past any
block with no image and still reports `applied=true`. The silent 19 are the
dangerous half; S16.3 turns the btree seven into refusals, which is why **S21b is
gated on S16**.

None of these is exotic: `XLOG_HEAP2_MULTI_INSERT` is every `COPY`,
`XLOG_HEAP2_VISIBLE` every `VACUUM`, `XLOG_HEAP_LOCK` every `SELECT … FOR UPDATE`,
`CLOG_ZEROPAGE` fires once per 32768 XIDs, and the btree set is ordinary index
maintenance. Separately, a **second** WAL pass has its own bug: `replayCLogFromWAL`
(`internal/initdb/xact_recovery.go:43-105`) runs after physical replay
(`internal/initdb/open.go:1252`) and stamps CLOG, but never dispatches on the xact
opcode and never parses `subxacts[]`. That is S22.

## Design

### Dispatch today — the three shapes of "not handled"

`ApplyRecord` (`recovery.go:2011-2029`) routes to the **native** payload switch
only when `payload[0]` is a known goopg RecordKind *and* the PG header matches
what `recordKindToRmgrInfo` would assign; every PG-authored record goes to
`replayDecodedXLogRecord`. So the native replay functions are live for goopg's own
WAL and usable as models, but a PG record never reaches them.

| rmgr | arm | opcode mask | `default:` |
|---|---|---|---|
| RM_XLOG (0) | `:2221` | `XLRRmgrInfoMask` 0xF0 | **silent** `return false, nil` (`:2245`) |
| RM_XACT (1) | `:2250` | `xlogXactOpMask` 0x70 | hard error (`:2265`) |
| RM_SMGR (2) | `:2303` | 0xF0 | hard error (`:2317`) |
| RM_CLOG (3) | `:2375` | 0xF0 | hard error (`:2383`) |
| RM_STANDBY (8) | `:2292` | 0xF0 | hard error (`:2300`) |
| RM_HEAP2 (9) | `:2438` | **0xF0** | hard error (`:2449`) |
| RM_HEAP (10) | `:2386` | `xlogHeapOpMask` 0x70 | hard error (`:2435`) |
| RM_BTREE (11) | `:2452` | 0xF0 | **silent FPI-only** (`:2516`) |

**Correction found while verifying: the RM_HEAP2 arm masks with 0xF0, but
`XLOG_HEAP_OPMASK` (0x70) applies to HEAP2 too** — stated at
`postgres/src/include/access/heapam_xlog.h:48-53` and implemented as
`switch (info & XLOG_HEAP_OPMASK)` in `heap2_redo`
(`postgres/src/backend/access/heap/heapam_xlog.c:1230`). `XLOG_HEAP_INIT_PAGE`
(0x80) is OR'd into MULTI_INSERT whenever the page is initialised
(`postgres/src/backend/access/heap/heapam.c:2607-2611`), so a PG bulk insert onto
a fresh page arrives as `info = 0xD0`. S21a must **fix the mask**, not merely add
cases. (RM_BTREE genuinely uses all four bits — `btree_redo` switches on the bare
`info`, `nbtxlog.c:1020-1024` — so 0xF0 is right there.)

### The opcode matrix

Legend: **H** handled · **X** hard error · **S** silent.

#### RM_HEAP (10) — `heap_redo`, `heapam_xlog.c:1181-1223`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `XLOG_HEAP_INSERT` | 0x00 | H `:2387` | `heap_xlog_insert` `heapam_xlog.c:417` | every `INSERT` |
| `XLOG_HEAP_DELETE` | 0x10 | H `:2392` | `heap_xlog_delete` `:341` | every `DELETE` |
| `XLOG_HEAP_UPDATE` | 0x20 | H `:2409` | `heap_xlog_update(false)` `:683` | non-HOT `UPDATE` |
| `XLOG_HEAP_TRUNCATE` | 0x30 | **X** | **no-op** `:1201-1208` | `TRUNCATE` |
| `XLOG_HEAP_HOT_UPDATE` | 0x40 | H `:2400` | `heap_xlog_update(true)` `:683` | HOT `UPDATE` |
| `XLOG_HEAP_CONFIRM` | 0x50 | **X** | `heap_xlog_confirm` `:958` | `INSERT … ON CONFLICT` speculative insert (`heapam.c:6149`) |
| `XLOG_HEAP_LOCK` | 0x60 | **X** | `heap_xlog_lock` `:997` | `SELECT … FOR UPDATE/SHARE`, FK RI checks |
| `XLOG_HEAP_INPLACE` | 0x70 | H `:2422` | `heap_xlog_inplace` `:1134` | `VACUUM`'s `pg_class`/`pg_database` in-place updates |

**Correction to the Theme F list: `XLOG_HEAP_TRUNCATE` needs no redo work at
all** — *"TRUNCATE is a no-op because the actions are already logged as SMGR WAL
records. TRUNCATE WAL record only exists for logical decoding"*
(`heapam_xlog.c:1201-1208`). It needs a recognised case; the real truncation
arrives as `XLOG_SMGR_TRUNCATE`.

#### RM_HEAP2 (9) — `heap2_redo`, `heapam_xlog.c:1226-1259`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `XLOG_HEAP2_REWRITE` | 0x00 | **X** | `heap_xlog_logical_rewrite` `rewriteheap.c:1073` | `VACUUM FULL`/`CLUSTER` **with a logical slot** (`rewriteheap.c:894`) |
| `..._PRUNE_ON_ACCESS` | 0x10 | H `:2439` | `heap_xlog_prune_freeze` `:30` | opportunistic prune on any read |
| `..._PRUNE_VACUUM_SCAN` | 0x20 | H `:2439` | ditto | `VACUUM` first pass |
| `..._PRUNE_VACUUM_CLEANUP` | 0x30 | H `:2439` | ditto | `VACUUM` freeze |
| `XLOG_HEAP2_VISIBLE` | 0x40 | **X** | `heap_xlog_visible` `:182-340` | every `VACUUM` (VM bit + `PD_ALL_VISIBLE`) |
| `XLOG_HEAP2_MULTI_INSERT` | 0x50 | **X** | `heap_xlog_multi_insert` `:536-682` | every `COPY`, multi-row `INSERT`, `CREATE TABLE AS` |
| `XLOG_HEAP2_LOCK_UPDATED` | 0x60 | **X** | `heap_xlog_lock_updated` `:1071` | locking a row further down an update chain |
| `XLOG_HEAP2_NEW_CID` | 0x70 | **X** | **no-op** `:1247-1253` | `wal_level=logical` only |

#### RM_XACT (1) — `xact_redo`, `postgres/src/backend/access/transam/xact.c:6363-6446`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `XLOG_XACT_COMMIT` | 0x00 | H `:2251` | `xact_redo_commit` | every commit that wrote |
| `XLOG_XACT_PREPARE` | 0x10 | **X** | `PrepareRedoAdd` `xact.c:6417-6427` | `PREPARE TRANSACTION` |
| `XLOG_XACT_ABORT` | 0x20 | H `:2263` | `xact_redo_abort` | every abort that wrote |
| `XLOG_XACT_COMMIT_PREPARED` | 0x30 | **X** | `xact_redo_commit` + `PrepareRedoRemove` | `COMMIT PREPARED` |
| `XLOG_XACT_ABORT_PREPARED` | 0x40 | **X** | `xact_redo_abort` + `PrepareRedoRemove` | `ROLLBACK PREPARED` |
| `XLOG_XACT_ASSIGNMENT` | 0x50 | **X** | **no-op outside hot standby** `xact.c:6429-6436` | the 65th+ subxid of one top-level xact |
| `XLOG_XACT_INVALIDATIONS` | 0x60 | **X** | **no-op** `xact.c:6437-6441` | catalog invals outside a commit |

**Correction to the Theme F list: `XLOG_XACT_ASSIGNMENT` is not "any
subtransaction".** Emission is gated on `isSubXact && XLogStandbyInfoActive()` and
`nUnreportedXids >= PGPROC_MAX_CACHED_SUBXIDS || log_unknown_top`
(`xact.c:751-782`), with `PGPROC_MAX_CACHED_SUBXIDS = 64`
(`postgres/src/include/storage/proc.h:39`). A single `SAVEPOINT` in the S28
workload will **not** produce one; 65 savepoints in one transaction will. Both
0x50 and 0x60 are correct crash-recovery no-ops: 0x50's body is guarded by
`standbyState >= STANDBY_INITIALIZED`, and 0x60 carries *"XXX we do ignore this
for now, what matters are invalidations written into the commit record."*
`PREPARE`/`COMMIT_PREPARED`/`ABORT_PREPARED` must **refuse loudly** — 2PC recovery
is out of scope and `max_prepared_transactions` BootVal is `"0"`.

#### RM_STANDBY (8) — `standby_redo`, `postgres/src/backend/storage/ipc/standby.c:1163-1220`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `XLOG_STANDBY_LOCK` | 0x00 | **X** | no-op outside hot standby | any `AccessExclusiveLock` (DDL) — `LogAccessExclusiveLocks` `standby.c:1412-1424` |
| `XLOG_RUNNING_XACTS` | 0x10 | H `:2293` | no-op outside hot standby | every online checkpoint |
| `XLOG_INVALIDATIONS` | 0x20 | **X** | no-op outside hot standby | catalog invals under `wal_level ≥ logical` |

The **whole rmgr** is a correct crash-recovery no-op: `standby_redo` returns
before its opcode switch when `standbyState == STANDBY_DISABLED`
(`standby.c:1171-1172`), always true outside hot standby. Three recognised no-ops
plus an unknown-opcode error is byte-exact parity, not an approximation.

#### RM_CLOG (3) — `clog_redo`, `postgres/src/backend/access/transam/clog.c:1107-1143`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `CLOG_ZEROPAGE` | 0x00 | **X** | `ZeroCLOGPage` + `SimpleLruWritePage` `clog.c:1114-1130` | every 32768 XIDs (8192 B × 4 XIDs/byte) |
| `CLOG_TRUNCATE` | 0x10 | H `:2376` (page no-op; the second pass applies it) | `AdvanceOldestClogXid` + `TruncateCLOG` | `VACUUM` freeze-horizon advance |

**Native analog, measured.** `EnablePGSLRUMirror` (`internal/mvcc/clog.go:567-601`)
does write a zeroed `BLCKSZ` page — but only to segment `0000`, only if absent,
only at startup; it does **not** generalise to an arbitrary `pageno`. What
transfers is `clogBufferPool.segPathForPage`
(`internal/mvcc/clog_bufferpool.go:175-179` — `%04X` of `pageNo /
slruPagesPerSegment`, offset `pageInSeg * BlockSize`) plus `writePageToDisk`
(`:280`). ZEROPAGE replay is "pin, memset, write" over that pool: small, but not
free reuse.

#### RM_SMGR (2) — `smgr_redo`, `postgres/src/backend/catalog/storage.c:981-1095`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `XLOG_SMGR_CREATE` | 0x10 | H `:2304` | `smgrcreate` `storage.c:989-996` | `CREATE TABLE/INDEX`, new fork |
| `XLOG_SMGR_TRUNCATE` | 0x20 | **X** | per-fork `smgrtruncate` to `xlrec->blkno` `storage.c:997-1091` | `TRUNCATE`, `VACUUM` tail truncation |

**Native analog, measured.** `replaySmgrTruncate` (`internal/wal/recovery.go:4657-4670`)
exists and is idempotent, but truncates to **zero** blocks
(`mgr.TruncateRelation`), and its decoder (`DecodeSmgrTruncate`, `:1100-1115`)
carries only `{dbOid, relOid, fork}` — no `blkno`, no fork bitmask. Upstream's
`xl_smgr_truncate` carries `blkno` plus `SMGR_TRUNCATE_{HEAP,FSM,VM}` and
truncates each requested fork to that length. The idempotency shape is reusable;
the PG decoder and the partial-length truncate are new.

#### RM_XLOG (0) — `xlog_redo`, `postgres/src/backend/access/transam/xlog.c:8279-8660`

Two opcodes handled; the other twelve **silently no-op'd** (`recovery.go:2245-2248`).

| opcode | hex | goopg | verdict |
|---|---|---|---|
| `CHECKPOINT_SHUTDOWN`/`_ONLINE` 0x00/0x10, `NOOP` 0x20, `SWITCH` 0x40, `BACKUP_END` 0x50, `RESTORE_POINT` 0x70, `FPW_CHANGE` 0x80 | | S | safe: nothing to do, or handled upstream in `xlogrecovery_redo` too |
| `NEXTOID` | 0x30 | S | **a real gap.** `xlog_redo` believes the record exactly: `TransamVariables->nextOid = nextOid; oidCount = 0` (`xlog.c:8292-8308`). Dropping it lets goopg re-issue OIDs PG allocated after the last checkpoint |
| `PARAMETER_CHANGE` | 0x60 | H `:2222` | — |
| `END_OF_RECOVERY` | 0x90 | S | carries a TLI switch; safe only while goopg ignores TLI (S18.3) |
| `FPI_FOR_HINT` | 0xA0 | **S — the real gap** | torn-page protection for hint-bit writes. `xlog_redo` restores its images exactly like `XLOG_FPI`, differing only in tolerating a missing image (`xlog.c:8520-8552`). Silently dropping it leaves a possibly-torn page on disk |
| `FPI` | 0xB0 | H `:2228` | — |
| `OVERWRITE_CONTRECORD` | 0xD0 | S | `xlog_redo` is *itself* a no-op (`xlog.c:8481-8483`, *"handled in xlogrecovery_redo()"*); the real semantics live in the **reader**, so keep the no-op — the work belongs to S16/S19 |
| `CHECKPOINT_REDO` | 0xE0 | S | PG17+; S20.2 must teach `isCheckpointRecord` about it |

#### RM_BTREE (11) — `btree_redo`, `postgres/src/backend/access/nbtree/nbtxlog.c:1018-1073`

| opcode | hex | goopg | upstream redo | produced by |
|---|---|---|---|---|
| `INSERT_LEAF` | 0x00 | H `:2453` | `btree_xlog_insert(t,f,f)` `nbtxlog.c:160-250` | ordinary index insert |
| `INSERT_UPPER` | 0x10 | **S** | `btree_xlog_insert(f,f,f)` | downlink insert after a child split (`nbtinsert.c:1342`) |
| `INSERT_META` | 0x20 | **S** | `btree_xlog_insert(f,t,f)` | ditto, when the fast-root also moves (`nbtinsert.c:1348`) |
| `SPLIT_L` / `SPLIT_R` | 0x30/0x40 | H `:2470` | `btree_xlog_split` `:251` | page split |
| `INSERT_POST` | 0x50 | **S** | `btree_xlog_insert(t,f,t)` | insert splitting a **posting list** on a deduplicated leaf (`nbtinsert.c:1337`) |
| `DEDUP` | 0x60 | **S** | `btree_xlog_dedup` `:464-556` | leaf deduplication before a split (`nbtdedup.c:265`) |
| `DELETE` | 0x70 | **S** | `btree_xlog_delete` `:652-716` | LP_DEAD / bottom-up index deletion (`nbtpage.c:1369`) |
| `UNLINK_PAGE` / `_META` | 0x80/0x90 | H `:2503` | `btree_xlog_unlink_page` `:802` | page deletion |
| `NEWROOT` | 0xA0 | H `:2461` | `btree_xlog_newroot` `:941` | root split |
| `MARK_PAGE_HALFDEAD` | 0xB0 | H `:2494` | `btree_xlog_mark_page_halfdead` `:717` | page deletion, first phase |
| `VACUUM` | 0xC0 | H `:2486` | `btree_xlog_vacuum` `:598` | `VACUUM` index pass |
| `REUSE_PAGE` | 0xD0 | **S** | **no-op outside hot standby** `:1006-1015` | recycling a deleted page (`nbtpage.c:933-953`, itself gated on `XLogStandbyInfoActive()`) |
| `META_CLEANUP` | 0xE0 | **S** | `_bt_restore_meta(record, 0)` `:82` | `VACUUM`'s metapage cleanup-XID update (`nbtpage.c:304`) |

**Correction to the Theme F list: `XLOG_BTREE_REUSE_PAGE` needs no redo.** Its
whole body is `if (InHotStandby) ResolveRecoveryConflictWithSnapshotFullXid(…)`,
and the header comment says the record exists *only* as a hot-standby conflict
point. It becomes a recognised no-op — leaving **six** btree opcodes needing real
redo, not seven.

### S21a — non-btree (est ~2 loops)

1. **Fix the RM_HEAP2 mask** to 0x70 with 0x80 read as `XLOG_HEAP_INIT_PAGE`.
   Without this every `COPY`-onto-a-fresh-page record misses its case even after
   the case exists.
2. **`XLOG_HEAP2_MULTI_INSERT` 0x50** — highest-value single opcode.
   `replayHeapMultiInsert` (`recovery.go:3842-3882`) already does "read block →
   `InitPage` if new → `pd_lsn` gate → `PageAddHeapTuple` per entry → `SetLSN` →
   `WriteBlock`". New: the `xl_heap_multi_insert` / `xl_multi_insert_tuple`
   block-0 decoder, `XLOG_HEAP_INIT_PAGE`, and the `offsets[]` array. ~70% reuse.
3. **`XLOG_HEAP_LOCK` 0x60** — `replayHeapLock` (`recovery.go:4075-4105`) has the
   same read / `IsNew` / `pd_lsn` / `PageSetHeapTupleLockOnly` / `SetLSN` shape.
   New: decoding `xl_heap_lock` (`heapam_xlog.c:997-1069`) and mapping
   `infobits_set` onto goopg's lock strength. ~60% reuse.
4. **`XLOG_HEAP2_VISIBLE` 0x40 — the Theme F note overstates the analog.**
   goopg's `RecordKindHeapVisible` replay is an explicit **no-op**
   (`recovery.go:2109-2118`: *"VM is recomputed by VACUUM after a crash"*): 0%
   reuse. Upstream sets `PD_ALL_VISIBLE` on the heap page (block 1) and the VM
   bits (block 0). Cheapest correct behaviour is to apply `PD_ALL_VISIBLE` and
   **clear** rather than set the VM bits — always safe — and say so in the code.
5. **Recognised no-ops, each with its upstream citation in the comment:**
   `HEAP_TRUNCATE` 0x30, `HEAP2_NEW_CID` 0x70, `XACT_ASSIGNMENT` 0x50,
   `XACT_INVALIDATIONS` 0x60, `STANDBY_LOCK` 0x00, `STANDBY_INVALIDATIONS` 0x20.
6. **Loud refusals:** `XACT_PREPARE`/`COMMIT_PREPARED`/`ABORT_PREPARED`, and
   `HEAP2_REWRITE` 0x00 (goopg has no `pg_logical/mappings` consumer; upstream's
   redo writes a mapping file, `rewriteheap.c:1073-1100` — a half implementation
   is worse than a refusal).
7. **Small remainder:** `HEAP_CONFIRM` 0x50 (set the speculative tuple's `t_ctid`
   to itself); `HEAP2_LOCK_UPDATED` 0x60 (HEAP_LOCK's shape one step down an
   update chain — **it can carry a MultiXactId xmax**, S24 territory,
   `0131-0016`); `CLOG_ZEROPAGE` 0x00 on the `clogBufferPool` seam above;
   `SMGR_TRUNCATE` 0x20 with a new PG decoder (`blkno` + fork flags) and a
   per-fork partial truncate, keeping `replaySmgrTruncate`'s idempotency;
   `XLOG_FPI_FOR_HINT` 0xA0 through the existing
   `replayDecodedXLogHeapFPIBlocks` (`recovery.go:2535-2547`), tolerating a record
   with no image (`xlog.c:8542-8546`); and `XLOG_NEXTOID` 0x30 split out of the
   silent default so goopg's OID counter advances.

**Also in S21a: zero-extend instead of erroring at `recovery.go:2665`.**
`replayDecodedXLogHeapInsert` handles `block.Block < nblocks` and `== nblocks`;
its `default:` returns `"wal: xlog heap-insert replay gap block=%d nblocks=%d"`.
Upstream's `XLogReadBufferExtended` instead `smgrcreate`s and, for any
`blkno >= lastblock` in a non-`RBM_NORMAL` mode, calls
`ExtendBufferedRelTo(…, blkno+1, …)`
(`postgres/src/backend/access/transam/xlogutils.c:482-518`) — it zero-extends the
file. A PG crash tail routinely references a block past the flushed length, so
this arm is reachable on the very first reverse crash start.

### S21b — btree (est ~2 loops, gated on S16.3)

Land only after S16.3 has turned the FPI-only `default:` into a refusal;
otherwise a silent regression is indistinguishable from success. Six opcodes need
real redo — `INSERT_UPPER`, `INSERT_META`, `INSERT_POST`, `DEDUP`, `DELETE`,
`META_CLEANUP` — plus `REUSE_PAGE` as a recognised no-op.

The three `INSERT_*` variants are one upstream function under different flags
(`btree_xlog_insert(isleaf, ismeta, posting)`, `nbtxlog.c:160-250`), and goopg
already implements the `(true,false,false)` instantiation as
`replayDecodedXLogBtreeInsert` (`recovery.go:2453-2460`); generalising it is the
cheapest group — `INSERT_UPPER` is the same on a non-leaf page, `INSERT_META`
adds `_bt_restore_meta(record, 2)`, `INSERT_POST` adds replacing the posting-list
tuple at `postingoff`. `META_CLEANUP` is `_bt_restore_meta(record, 0)` alone, and
goopg already writes `xl_btree_metadata` on the newroot path (`0130-0012` §S11.5a,
block 2), so that encoder/decoder exists.

`DEDUP` (`nbtxlog.c:464-556`) and `DELETE` (`:652-716`, sharing
`btree_xlog_updates`, `:557-597`) are the genuinely new work: both rewrite a
leaf's item area from an array of `xl_btree_update` descriptors. M0130-S11's
PG-identical nbtree page and tuple layout (`0130-0011`, `0130-0012`) is what makes
them expressible at all.

### S22 — CLOG opcode dispatch + `subxacts[]` (est ~1 loop)

The second pass, `replayCLogFromWAL` (`internal/initdb/xact_recovery.go:43-105`),
called from `internal/initdb/open.go:1252` after physical replay, contains:

```go
if r.XLog != nil && r.XLog.Header.Rmid == wal.RmgrXact && r.XLog.Header.XID != 0 {
    xid := storage.TransactionID(r.XLog.Header.XID)
    isCommit := (r.XLog.Header.Info & wal.XlogXactOpMask) == wal.XlogXactCommit
    xactStampAndAdvance(clog, txnMgr, xid, isCommit)
    continue
}
```
(`xact_recovery.go:87-92`)

**Bug (a) — every non-commit xact opcode is stamped ABORTED.** `isCommit` is
`false` for anything that is not 0x00, and the code then stamps unconditionally.
A PG `XLOG_XACT_ASSIGNMENT` (0x50) carries the *top-level* XID in `xl_xid`, so a
committed transaction that emitted one gets stamped aborted by whichever record
the loop sees last; `PREPARE` (0x10) and `COMMIT_PREPARED` (0x30) are stamped
aborted outright. Fix: dispatch — 0x00 commit, 0x20 abort, 0x50/0x60 skip,
0x10/0x30/0x40 error. The mask itself is **correct**: `xlogXactOpMask = 0x70`
(`internal/wal/pg_xlog_decode.go:24`, exported at `:31`) matches `XLOG_XACT_OPMASK`
(`postgres/src/include/access/xact.h:179`), and bit 0x80 is `XLOG_XACT_HAS_INFO`
(`xact.h:182`), already modelled as `xlogXactHasInfo`
(`internal/wal/pg_assembled_emit.go:312`) — verified, no change needed there.

**Bug (b) — `subxacts[]` is never parsed.** A PG commit record lists its
subtransaction XIDs and `xact_redo_commit` stamps them all committed; goopg
stamps only `xl_xid`, so after a reverse crash start every row written inside a
`SAVEPOINT` of a committed transaction stays invisible. The parse follows
`ParseCommitRecord` (`postgres/src/backend/access/rmgrdesc/xactdesc.c:35-125`),
walking a cursor from `MinSizeOfXactCommit`:

| step | present when | consumes |
|---|---|---|
| `xact_time` | always | 8 B (`MinSizeOfXactCommit`, `xact.h:334`) |
| `xl_xact_xinfo` | `info & XLOG_XACT_HAS_INFO` (0x80) | `uint32 xinfo` |
| `xl_xact_dbinfo` | `xinfo & HAS_DBINFO` (1<<0) | `Oid dbId, tsId` — 8 B |
| **`xl_xact_subxacts`** | `xinfo & HAS_SUBXACTS` (1<<1) | `int32 nsubxacts`, then `nsubxacts × TransactionId` |
| `xl_xact_relfilelocators` | `xinfo & HAS_RELFILELOCATORS` (1<<2) | `int32 nrels` + `nrels × RelFileLocator` |
| … | | remaining chunks per `xactdesc.c:87-122` |

The walk may stop once `subxacts[]` is read. Every chunk is `int32`-aligned by
construction (`xact.h:237-239`), so a flat little-endian cursor suffices. goopg
already walks the first two steps in `xactCommitCarriesInvals`
(`internal/wal/pg_assembled_emit.go:358-366`) — extend that into an exported
`ParseXactCommitSubxacts(info uint8, mainData []byte) ([]TransactionID, error)`
and stamp every returned XID with `xactStampAndAdvance`. Abort records carry the
same chunk (`xl_xact_abort`, `xact.h:336-347`) and want the same treatment with
`isCommit=false`.

## Guards

1. Per-opcode unit tests over fixtures captured from a real PG 18.3 via
   `internal/testutil/pgcluster`: run the workload, `pg_ctl stop -m immediate`,
   read the tail with `wal.ReadAll`, assert `replayDecodedXLogRecord` returns
   `applied=true` with the expected page state.
2. A PG `XLOG_HEAP2_MULTI_INSERT` **with** `XLOG_HEAP_INIT_PAGE` (`info == 0xD0`)
   dispatches — the mask fix, which no case-addition alone catches.
3. Each of the seven documented no-ops (HEAP_TRUNCATE, HEAP2_NEW_CID,
   XACT_ASSIGNMENT, XACT_INVALIDATIONS, STANDBY_LOCK, STANDBY_INVALIDATIONS,
   BTREE_REUSE_PAGE) returns `(false, nil)`, while an *undefined* opcode in the
   same rmgr still errors.
4. `PREPARE`/`COMMIT_PREPARED`/`ABORT_PREPARED` and `HEAP2_REWRITE` refuse with a
   message naming the feature.
5. `replayDecodedXLogHeapInsert` against block `nblocks + 3` zero-extends and
   inserts, instead of `"xlog heap-insert replay gap"`.
6. S21b: PG-captured `XLOG_BTREE_DEDUP` and `XLOG_BTREE_DELETE` replay to the same
   page bytes PG produces, and S16.3's refusal no longer fires.
7. S22: a captured PG commit record carrying `subxacts[]` stamps every subxid
   committed; an `XLOG_XACT_ASSIGNMENT` record leaves CLOG **untouched** (today it
   stamps aborted); an E2E whose PG workload opens a `SAVEPOINT`, writes, releases
   and commits before the kill sees those rows after a goopg start.
8. The S28 reverse crash E2E (`0131-0017`) with the full opcode workload: COPY,
   VACUUM, `SELECT … FOR UPDATE`, TRUNCATE, SAVEPOINT, index-heavy insert.
9. UNITS + RACE (`internal/wal`, `internal/initdb`) + SMOKE + SPOT green.

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
  §"Theme F" (S21/S22) — the decomposition this doc expands
- `docs/design/0131-0013-wal-reader-fail-closed.md` — S16.3, prerequisite for S21b
- `docs/design/0130-0012-rm-btree-wal-content-parity.md`, `0130-0011-*` —
  goopg's existing btree record/page parity work
- `docs/design/0130-0002-pg-class-heap-persistence.md` §"WAL replay constraint"
- `docs/design/0131-0016-multixact-durable-slru.md` — S24, reached via
  `XLOG_HEAP2_LOCK_UPDATED`
- `.ralph/deferral_ledger.md` row `2026-08-11 M0131-S10.5` — missing-rmgr ledger row
- `postgres/src/include/access/heapam_xlog.h:33-66`,
  `postgres/src/include/access/nbtxlog.h:27-43`,
  `postgres/src/include/access/xact.h:169-196`,
  `postgres/src/include/storage/standbydefs.h:34-36`,
  `postgres/src/include/access/clog.h:55-56`,
  `postgres/src/include/catalog/pg_control.h:68-82`,
  `postgres/src/include/catalog/storage_xlog.h:30-31`
- memory: `wal_pg_faithful_rmgr_dispatch_preference`, `pattern_sibling_paths_must_agree`
