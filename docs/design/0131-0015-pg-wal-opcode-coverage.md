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
| `CLOG_ZEROPAGE` | 0x00 | H (part 5) | `ZeroCLOGPage` + `SimpleLruWritePage` `clog.c:1114-1130` | every 32768 XIDs (8192 B × 4 XIDs/byte) |
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
| `XLOG_SMGR_TRUNCATE` | 0x20 | H (part 6) | per-fork `smgrtruncate` to `xlrec->blkno` `storage.c:997-1091` | `TRUNCATE`, `VACUUM` tail truncation |

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

### S21a-1 — implementation notes (landed 2026-08-12)

S21a is split in two. **S21a-1 is the recognition layer**: every opcode whose
correct redo is *nothing*, plus the two decisions that had to be made before any
page-mutating arm can be written. **S21a-2** is the page work — `MULTI_INSERT`,
`VISIBLE`, `LOCK`, `CONFIRM`, `LOCK_UPDATED`, the zero-extend, `CLOG_ZEROPAGE`,
`SMGR_TRUNCATE`, `HEAP2_REWRITE`'s refusal — and is unchanged from the plan
above.

What landed, all in `replayDecodedXLogRecord` (`internal/wal/recovery.go`)
unless noted:

- **The RM_HEAP2 mask** is now `xlogHeapOpMask` (0x70), matching `heap2_redo`'s
  own `info & XLOG_HEAP_OPMASK` (`heapam_xlog.c:1229`). Landing this *before*
  the multi-insert arm means the two changes never have to be right at the same
  time. Its guard (`TestReplayHeap2UsesHeapOpMask`) rides a prune opcode with
  the init bit set, because the opcode the mask actually protects —
  `MULTI_INSERT|INIT_PAGE` = 0xD0 — has no arm to reach until S21a-2.
- **Recognised no-ops** with their upstream citation in the comment:
  `HEAP_TRUNCATE` (heap_redo's arm is an explicit comment-only break,
  `heapam_xlog.c:1201-1208` — the physical effect arrives as SMGR records),
  `HEAP2_NEW_CID` (`heapam_xlog.c:1246-1252`), `XACT_ASSIGNMENT` /
  `XACT_INVALIDATIONS` (`xact.c:6429-6443`; the latter is ignored outright
  upstream), `STANDBY_LOCK` / `STANDBY_INVALIDATIONS` (`standby_redo` returns
  before its first arm when `standbyState == STANDBY_DISABLED`,
  `standby.c:1170-1172`, which a crash-recovery start always is).
  `STANDBY_LOCK` alone is why any PG tail containing DDL refused the start.
- **Loud refusal** for `XACT_PREPARE` / `COMMIT_PREPARED` / `ABORT_PREPARED`,
  wrapping `ErrUnsupportedRecord` and naming two-phase commit in the message.
  Silently no-opping a `COMMIT_PREPARED` would stamp an XID committed whose
  `PREPARE` — and therefore whose heap changes — were never applied.

**`XLOG_NEXTOID` is applied in two passes, not one.** S16.4 refused it because
`xlog_redo` sets `nextOid` exactly (`xlog.c:8292-8308`) and dropping one lets
goopg re-issue OIDs a crashed PG already handed to catalog rows. It is now
recognised as a *page-level* no-op in `replayDecodedXLogRecord` and applied by
`replayNextOIDFromWAL` (`internal/initdb/xact_recovery.go`), which
`initdb.Open` calls immediately after seeding the counter from
`pg_control.checkPointCopy.nextOid`. The reason is structural, not incidental:
the physical pass is handed a `*storage.Manager` and nothing else, while the
OID counter lives in `catalog.InMemory`. `CLOG_TRUNCATE` and the xact
commit/abort stamps already use exactly this split, and keeping physical replay
a pure page function is worth more than routing a catalog handle into it.

Two deliberate deviations from upstream, both recorded in the code:

- Upstream *sets* `nextOid` from the record ("better to just believe the record
  exactly") because it must survive OID wraparound. goopg takes the **maximum**
  of the pg_control seed and the WAL records, because
  `catalog.InMemory.advanceNextOIDLocked` is monotone and has no wraparound
  path; both sources are lower bounds on what PG had already allocated, so the
  counter must clear both.
- `replayNextOIDFromWAL` scans from record **zero**, not from
  `ExportedReplayStart`. A pre-checkpoint `NEXTOID` is only redundant with
  pg_control if that checkpoint refreshed `nextOid`, and one extra idempotent
  maximum costs nothing on a shared, already-decoded `ReadAll`.

`EncodeXLogNextOidPG` exists as the encode sibling of `DecodeXLogNextOid` so the
guard exercises goopg's real framing rather than a hand-built buffer; goopg
never emits the record in normal operation (it allocates OIDs one at a time and
republishes the counter at each checkpoint, where PG pre-allocates blocks of
`VAR_OID_PREFETCH` and must log each block's ceiling).

**S28 stays a self-arming skip**, as expected: its refusal simply moves from
`rmid=0 info=0x30` to the first page-mutating opcode in the tail. The skip
predicate matches any "unsupported xlog record" refusal, so nothing needed
changing there.

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

### S21a-2 — implementation notes, part 1: MULTI_INSERT + the replay gap (landed 2026-08-12)

S21a-2 is the page-**mutating** half. It lands opcode by opcode, each landing
shrinking S28's self-arming skip. This section covers the first two, which share
one new helper.

**`XLOG_HEAP2_MULTI_INSERT` (0x50) — `replayDecodedXLogHeapMultiInsert`.** Every
COPY: `heap_multi_insert` packs one page's worth of tuples into a single record,
so a crash tail taken during a bulk load is made almost entirely of these. Redo
mirrors `heap_xlog_multi_insert` (`heapam_xlog.c:600-731`):

- Main data is `xl_heap_multi_insert{uint8 flags; uint16 ntuples;
  OffsetNumber offsets[]}` — C alignment puts `ntuples` at byte 2 and the array
  at byte 4 (`SizeOfHeapMultiInsert = 4`, `heapam_xlog.h:188`).
- **The offsets array is present only when `XLOG_HEAP_INIT_PAGE` is CLEAR.** With
  the bit set the page is reinitialised and tuple *i* lands at
  `FirstOffsetNumber + i`, so upstream saves the array's bytes. Reading the
  init-page layout for a non-init record (or vice versa) is silent corruption,
  not a decode error — hence a dedicated guard for each.
- Block 0's data run is the SHORTALIGNed sequence of
  `xl_multi_insert_tuple{datalen, t_infomask2, t_infomask, t_hoff}` + `datalen`
  tuple bytes. Upstream PANICs with "total tuple length mismatch" if the walk
  does not consume the run exactly; goopg refuses the record, because a short
  walk means an unconsumed tuple that would be dropped while replay reports
  success.
- The per-tuple rebuild is byte-identical to the single-tuple `xl_heap_insert`
  path (xmin from `xl_xid`, invalid xmax, self-pointing `t_ctid`), so the two now
  share `buildTupleFromXLogHeapHeader` — `xl_multi_insert_tuple[2:7]` is the same
  `{t_infomask2, t_infomask, t_hoff}` triple as `xl_heap_header`.
- `XLH_INSERT_ALL_VISIBLE_CLEARED` / `ALL_FROZEN_SET` are mirrored onto the page
  header's `PD_ALL_VISIBLE` bit, as upstream does inline. The *fork* half of the
  visibility map is `XLOG_HEAP2_VISIBLE`'s job (still open).

**The replay gap — `redoHeapPageForBlock`.** A record referencing a block past
the fork's flushed length is not an error: the primary extended the relation in
shared buffers and logged the insert, but the extension itself is not WAL-logged
and the dirty page never reached disk. Upstream zero-extends up to the block
(`XLogReadBufferExtended` → `ExtendBufferedRelTo` with `EB_PERFORMING_RECOVERY`,
`xlogutils.c:479-539`); goopg's heap-insert arm answered
`"xlog heap-insert replay gap block=N nblocks=M"` and refused the whole start.
The new helper does the acquire-or-extend-or-skip decision for both heap redo
paths — `pd_lsn` idempotency for a page that exists, `PageInit` when
`XLOG_HEAP_INIT_PAGE`/`BKPBLOCK_WILL_INIT`/all-zero, zero-fill of the
intervening blocks otherwise. Fixing it inside the shared helper is what makes
the two paths siblings rather than two copies that drift.

**One deviation, recorded in the ledger.** Upstream's
`PageAddItem(overwrite=true)` can refill an already-allocated (dead) line
pointer in place; goopg's `PageInsertItemRawAt` would *shift* the line-pointer
array and displace the tuple already at that slot. Redo refuses that case with
`ErrUnsupportedRecord` rather than corrupting the page silently — reachable only
when PG reused a dead line pointer on the target page, which a plain COPY into a
freshly extended or append-only page never does.

Guards (`internal/wal/heap_multi_insert_pg_test.go`, 6 tests / 9 subtests, all
proven fail-when-broken by scripted reverts): init-page apply, explicit-offsets
apply, idempotent re-apply, zero-extend across a 2-block gap **on both heap
paths**, the two malformed-block-data refusals, and the line-pointer-reuse
refusal.

### S21a-2 — implementation notes, part 2: HEAP_LOCK + HEAP_CONFIRM (landed 2026-08-12)

The two remaining **RM_HEAP** page mutations. Both stamp a single tuple header on
an already-existing page, which is what makes them one slice rather than two:
they share the buffer-acquisition rule that distinguishes them from every
insert-like record.

**`XLOG_HEAP_LOCK` (0x60) — `replayDecodedXLogHeapLock`.** After MULTI_INSERT
this is the most common record in an OLTP tail: every `SELECT … FOR UPDATE/SHARE`,
every foreign-key row check, and the tuple lock an UPDATE takes on a row it is
about to rewrite. goopg emits its own row locks as the *native*
`RecordKindHeapLock` record (`replayHeapLock`), so this arm is reached only by a
real-PG record — which, unlike goopg's, can carry a multixact xmax or an
updater's key-share lock. Redo mirrors `heap_xlog_lock` (`heapam_xlog.c`):

- Main data is `xl_heap_lock{TransactionId xmax; OffsetNumber offnum;
  uint8 infobits_set; uint8 flags}` (`SizeOfHeapLock = 8`, `heapam_xlog.h:396`).
- `infobits_set` is the wire encoding of the infomask bits, decoded by
  `xlogHeapLockInfomaskBits` — goopg's port of `fix_infomask_from_infobits`.
  `HEAP_XMAX_SHR_LOCK` is deliberately absent there, as upstream notes: a share
  lock arrives as its two component bits.
- The page mutation itself is `storage.PageApplyHeapLockRedo`, the **redo
  sibling** of the runtime `PageSetHeapTupleLockOnly`. They are separate on
  purpose: the runtime helper stamps a lock goopg is taking *now* (single xid,
  always lock-only, strength bit mandatory), while redo must reproduce whatever
  PG decided, and additionally resets `t_ctid` and cmax. It clears
  `HEAP_XMAX_BITS | HEAP_MOVED` (the "turn these all off when Xmax is to change"
  clause, `htup_details.h:284`) plus `HEAP_KEYS_UPDATED` before OR-ing the
  record's bits in; **if the result is locked-only it clears `HEAP_HOT_UPDATED`
  and re-points `t_ctid` at the tuple itself**, because a locker must not leave a
  forward chain link behind; then stamps xmax and sets `cmax = FirstCommandId`
  with `HEAP_COMBOCID` cleared.
- "Locked-only" is decided by `isHeapXmaxLockedOnlyPG`, upstream's
  `HEAP_XMAX_IS_LOCKED_ONLY` **in full** — including the pre-9.3 bare-EXCL_LOCK
  clause that the exported `IsHeapTupleLockOnly` deliberately omits. The narrowed
  predicate is right for tuples goopg wrote; redo classifies tuples PG wrote.
  The two are cross-referenced in their doc comments so they cannot drift.
- `HEAP_MOVED_OFF/IN` are named in `internal/storage` for the first time here.
  goopg never sets them, but redo must still *clear* them: `t_field3` is the
  `t_xvac`/`t_cid` union, and a pg_upgrade'd tuple's field3 must not survive as a
  cmax.

**`XLOG_HEAP_CONFIRM` (0x50) — `replayDecodedXLogHeapConfirm`.** The second
record of every `INSERT … ON CONFLICT`. The speculative insert writes the tuple
with a speculative *token* in `t_ctid`; confirming it overwrites the token with
the tuple's own `(block, offset)` — which is exactly goopg's fresh-insert
convention, so the apply is one `PageSetHeapTupleCtid`. Replaying only the insert
would leave a tuple whose `t_ctid` points at a garbage location for a chain
follower to chase.

**`redoExistingHeapPageForBlock` — the RBM_NORMAL sibling.** Both opcodes
register their buffer with `XLogReadBufferForRedo`, i.e. `RBM_NORMAL`, where a
block past the fork's length or an all-zero page yields `InvalidBuffer` =
`BLK_NOTFOUND` and the redo routine does nothing (`xlogutils.c:500-540`). That is
the opposite of `redoHeapPageForBlock`'s zero-extend, and the asymmetry is
upstream's, not an approximation: an insert-like record legitimately references a
block whose extension was never logged, whereas a pure tuple stamp can only find
its page missing if the relation is dropped or truncated later in the same
stream — in which case the mutation is moot. Extending instead would materialise
an empty page and then fail "invalid lp" on it.

**Two deviations, both ledger rows.** (1) Upstream also records a BLK_NOTFOUND
reference in its invalid-page hash and PANICs at end of recovery if nothing later
drops or truncates that relation; goopg keeps no such table, so a genuinely
missing page is skipped silently. (2) `XLH_LOCK_ALL_FROZEN_CLEARED` asks for the
block's visibility-map `ALL_FROZEN` bit to be cleared; the replay path holds no
VM handle, so the flag is decoded and deliberately not acted on — same resume
point as the VM fork work in the `XLOG_HEAP2_VISIBLE` slice.

Guards (`internal/wal/heap_lock_confirm_pg_test.go`, 8 tests / 9 subtests, all
proven fail-when-broken by six scripted reverts): the lock-only shape (self
`t_ctid`, HOT bit cleared, cmax reset), the multixact + keys-updated shape (chain
link and HOT bit **preserved** — the branch the first shape must not take), the
stale-xmax-bit clear, pd_lsn idempotency, the absent-page skip *with* an
assertion that the fork was not extended, the invalid-offset refusal, and a
truncated-main-data refusal per opcode.

Also fixed here: `TestReplayPGHeapMultiInsertRefusesLinePointerReuse` (part 1)
seeded its two rows from a **map literal**, so Go's randomised iteration order
inserted slot 2 first on roughly half of all runs and the test failed with "slot
2 out of range". It is an ordered slice now.

### S21a-2 — implementation notes, part 3: HEAP2_VISIBLE + the VM fork (landed 2026-08-12)

`XLOG_HEAP2_VISIBLE` (0x40) is emitted by every VACUUM for each page it marks
all-visible, and by an INSERT that freezes a page it filled itself. It is the
first opcode in this milestone with **zero reuse** — goopg's own VM updates are
the native `RecordKindHeapVisible`, whose `ApplyRecord` arm is an explicit no-op
— and the first record whose redo writes a fork other than `main`.

**The record is two independent halves.** Block 1 is the heap page and block 0
is the *visibility-map* page (fork 2, vm block number, not the heap block
number). Upstream reads the heap page with `XLogReadBufferForRedo`
(`RBM_NORMAL`) and says why the halves must not be coupled: "If the heap file
has dropped or truncated later in recovery, we don't need to update the page,
but we'd better still update the visibility map." goopg therefore runs the
`redoExistingHeapPageForBlock` skip on the heap half and continues to the vm
half regardless — a coupling bug here leaves the map permanently un-set for
exactly the pages a later truncate touched.

**The vm half is `RBM_ZERO_ON_ERROR`, and that is the new shape.** A vm fork
shorter than the referenced block is not a replay gap: like a heap extension,
a vm extension is not WAL-logged, and upstream simply `PageInit`s the page it
read as zeros. `redoVMPageForBlock` is that rule — the third member of the
`redo*PageForBlock` family, whose members differ *only* in what they do with an
absent page (zero-extend / skip / zero-extend-and-init), which is exactly the
distinction upstream draws between `RBM_EXTEND`, `RBM_NORMAL` and
`RBM_ZERO_ON_ERROR`.

**Why redo can write the fork without a `VisibilityMap` handle.** `ApplyRecord`
is given only a `*storage.Manager`, and part 2 deferred both VM items for that
reason. The resolution is that the handle was never the right target: crash
recovery replays at `internal/initdb/open.go:380`, while `Runtime.VM` is
populated by `VMLoadForks` at `:2472` — *after*. A redo that mutated the
in-memory map would be overwritten by the load; a redo that mutates the on-disk
`_vm` fork is picked up by it. The on-disk layout was already PG's
(`vm_fork.go`: 2 bits per heap block, 4 per byte, data at
`MAXALIGN(SizeOfPageHeaderData)` = `PageGetContents`), so only the addressing
arithmetic was new: `internal/storage/vm_redo.go` ports `HEAPBLK_TO_MAPBLOCK` /
`HEAPBLK_TO_MAPBYTE` / `HEAPBLK_TO_OFFSET` plus the two bit mutations inside
`visibilitymap_set` (OR, with upstream's `flags != status` short-circuit that
lets redo skip the page write entirely) and `visibilitymap_clear` (AND-NOT).

**`VISIBILITYMAP_XLOG_CATALOG_REL` is wire-only.** It rides in the record's
flags byte to tell a hot standby the relation is catalog-ish while resolving
snapshot conflicts, and upstream forbids passing it to `visibilitymap_set`.
goopg masks with `VMValidBits` before the bits reach the page and *refuses* a
flags byte carrying anything outside `VISIBILITYMAP_XLOG_VALID_BITS`, where
upstream only asserts — an unknown bit means a PG whose map semantics goopg does
not know.

**Part 2's `XLH_LOCK_ALL_FROZEN_CLEARED` deferral is discharged here**, in the
place upstream puts it: `heap_xlog_lock` clears the block's `ALL_FROZEN` bit
*before* the heap page redo and independently of it, under the comment "the
visibility map may need to be fixed even if the heap page is already
up-to-date". Skipping the clear when the pd_lsn interlock skips the tuple stamp
would leave an index-only scan trusting an all-frozen bit for a page that now
carries a live locker, so the ordering is a guard, not a detail. Only
`ALL_FROZEN` is cleared, never `ALL_VISIBLE` alone — upstream asserts against
that combination and `VMPageClearBits` refuses it.

**One deliberate deviation from `visibilitymap_pin`:** a *clear* against a
relation whose vm fork does not reach the covering block leaves the fork alone
instead of extending it. Upstream extends only because it must hand
`visibilitymap_clear` a valid buffer; the bits it then clears are already zero,
so the only difference is whether an all-zero vm page materialises on disk —
and materialising one for a clear would invent map content the primary may never
have had.

Three ledger rows: upstream's hot-standby snapshot-conflict resolution
(`ResolveRecoveryConflictWithSnapshot` against `snapshotConflictHorizon`) is
decoded and not acted on; upstream's `XLogRecordPageWithFreeSpace` FSM update is
not performed, so a promoted goopg inherits a stale free-space map; and
`VMSaveForks` rewrites every fork from the in-memory map, which tracks
`ALL_VISIBLE` only — so an `ALL_FROZEN` bit replayed here survives until the
first VM save, then silently drops to 0 (conservative: a lost frozen bit costs
work, never correctness).

Guards (`internal/wal/heap_visible_pg_test.go` 6 tests / 8 subtests +
`internal/storage/vm_redo_test.go` 4 tests, all proven fail-when-broken by six
scripted reverts): both halves landing; ALL_VISIBLE+ALL_FROZEN with the
catalog-rel bit masked off; a heap block mapping to vm block 1 that creates the
vm fork *and* leaves the absent heap page unextended; idempotency under a stale
re-apply; three refusals (unknown flag bit, truncated main data, missing heap
block ref); the ALL_FROZEN clear, including that it runs when the heap half is
LSN-skipped and that it does not create an absent fork; and the addressing
math's three boundaries (within a byte, across a byte, across a vm page) round-
tripped through `parseVMPage` so redo and the fork writer cannot drift.

### S21a-2 — implementation notes, part 4: HEAP2_LOCK_UPDATED (landed 2026-08-12)

`XLOG_HEAP2_LOCK_UPDATED` (0x60) is `XLOG_HEAP_LOCK`'s near-sibling, emitted by
`heap_lock_updated_tuple_rec` when a tuple-lock request — `SELECT ... FOR
UPDATE/SHARE`, a foreign-key RI check, an `UPDATE` about to rewrite its target
row — discovers the row it locked was concurrently updated by a still-live
transaction. Rather than lock the (now-stale) version it started with, PG walks
the update chain and locks the newest visible version instead; that record is
this opcode. It lives on RM_HEAP2 rather than RM_HEAP because the chain walk
can cross multiple row versions, unlike a plain lock which always targets the
tuple the statement is looking at.

**The wire struct is byte-identical to `xl_heap_lock`'s** — `xmax`(4) +
`offnum`(2) + `infobits_set`(1) + `flags`(1), same layout, same field meaning
— so `replayDecodedXLogHeap2LockUpdated` reuses part 2's
`decodeXLogHeapLockMainData`, `xlogHeapLockInfomaskBits`,
`redoExistingHeapPageForBlock`, and the `XLH_LOCK_ALL_FROZEN_CLEARED` →
`redoClearVMBitsForHeapBlock` call verbatim, in the same before-and-independent
position relative to the tuple redo.

**The tuple mutation is deliberately smaller than `PageApplyHeapLockRedo`'s.**
Upstream's `heap_xlog_lock_updated` clears `HEAP_XMAX_BITS|HEAP_MOVED`, ORs in
the infobits, and stamps `xmax` — full stop. It has **no** "locked-only"
branch (no `t_ctid` self-pointer, no `HEAP_HOT_UPDATED` clear) and **no**
`cmax` stamp, both present in `heap_xlog_lock`. The reason is what the opcode
means: the tuple being restamped is *not* the chain head the original locker
was targeting — it is an older, already-updated version being re-locked in
passing — so it can never legitimately claim to own the forward chain link
(clearing it would corrupt a live successor pointer) or claim `FirstCommandId`
as its own command's cmax (the locking backend's *actual* command target is a
different tuple version). New `storage.PageApplyHeapLockUpdatedRedo` is this
reduced set of operations, not a parameterised call into
`PageApplyHeapLockRedo` — the two functions must diverge on exactly these two
points, so keeping them textually separate makes an accidental reconvergence
a diff, not a silent behavior change.

Guards (`internal/wal/heap_lock_updated_pg_test.go`, 7 tests, all proven
fail-when-broken by three scripted reverts): a pre-existing forward `t_ctid`
link and `HEAP_HOT_UPDATED` survive untouched; a pre-existing `cmax` sentinel
survives untouched; stale `HEAP_XMAX_*`/`HEAP_MOVED` bits are still cleared;
the pd_lsn idempotency interlock; the RBM_NORMAL absent-page skip; the
"invalid lp" refusal; the main-data length refusal; and
`XLH_LOCK_ALL_FROZEN_CLEARED` clearing the VM's `ALL_FROZEN` bit through the
shared `redoClearVMBitsForHeapBlock` call (proving the new dispatch case wires
it correctly, not just that the shared helper works).

### S21a-2 — implementation notes, part 5: CLOG_ZEROPAGE (landed 2026-08-12)

`CLOG_ZEROPAGE` (0x00, RM_CLOG) is the opcode `WriteZeroPageXlogRec`
(`clog.c:1073-1078`) writes once per 32768 XIDs, immediately before
`ExtendCLOG` hands out the first XID of a fresh CLOG page — every long-running
cluster hits this regularly, not just at checkpoint boundaries. Its main data
is a bare `int64 pageno`; `clog_redo` (`clog.c:1114-1130`) zeroes the page and
writes it to the `pg_xact/` segment via `SimpleLruWritePage`.

**Why goopg needs an arm at all, given its own CLOG reads already default
missing pages to zero.** `clogBufferPool.readPageFromDisk`
(`clog_bufferpool.go:181-202`) already zero-fills a buffer when the backing
segment is short or absent — so for goopg's *own* reads, silently dropping
this record was accidentally harmless. It stops being harmless the moment
anything outside goopg's lenient fault-in path touches `pg_xact/` directly:
upstream `SimpleLruReadPage` treats a missing segment as a hard `ERROR`, not a
zero-fill (`slru.c`), so a real PG standby cold-starting on a goopg-written
cluster, or any tool that reads the SLRU segments directly (`pg_resetwal`,
`amcheck`), expects a segment file to physically exist for every XID range
ever assigned. A crashed real PG's WAL tail that allocated a fresh page during
the crashed run left that segment simply absent.

**No catalog/txn-manager handle needed, so — unlike `CLOG_TRUNCATE` — this is
replayed directly in the physical pass, not deferred to `replayCLogFromWAL`'s
initdb second pass.** `CLOG_TRUNCATE` needs `AdvanceOldestClogXid` and a live
`*mvcc.CLog`, which does not exist yet at the point physical replay runs
(`internal/initdb/open.go:380`, well before `mvcc.OpenCLog` at `:1006`).
`CLOG_ZEROPAGE` needs neither: it is pure page-zeroing at a fixed segment
offset, computable from `mgr.DataDir()` alone.

**Segment path math is a deliberate small duplication, not a shared call.**
`wal` cannot import `internal/mvcc` (`mvcc` already imports `wal` for the
async-commit WAL-flush hook — importing back would cycle), so
`replayDecodedXLogClogZeroPage` reimplements
`clogBufferPool.segPathForPage`'s arithmetic (`clogSLRUPagesPerSegment = 32`,
segment name `"%04X"` of `pageno/32`, byte offset `(pageno%32)*BLCKSZ`) rather
than reaching into `mvcc`. Same trade discussed for `CLOG_TRUNCATE`
(`wal_pg_faithful_rmgr_dispatch_preference`).

Guards (`internal/wal/clog_zeropage_pg_test.go`, 2 tests, proven
fail-when-broken by a scripted revert of the dispatch case): a fresh
`pageno=3` creates segment `0000`, sized to cover the page, all-zero; a
`pageno=65` (segment 2, `pageInSeg=1`) creates segment `0002` and does not
create segment `0000`.

### S21a-2 — implementation notes, part 6: SMGR_TRUNCATE (landed 2026-08-12)

`XLOG_SMGR_TRUNCATE` (0x20, RM_SMGR) is the physical half of every
`TABLE`/`INDEX` `TRUNCATE` and every `VACUUM` tail truncation —
`XLOG_HEAP_TRUNCATE` (RM_HEAP 0x30, recognised as a no-op in S21a-1) carries
only OIDs for logical decoding; the actual file shrink arrives here
(`heapam_xlog.c:1201-1208`). Its main data is `BlockNumber blkno` +
`RelFileLocator{spcOid,dbOid,relNumber}` + `int flags` (20 bytes,
`storage_xlog.h:46-51`); `smgr_redo`'s arm (`storage.c:997-1094`) forcibly
`smgrcreate`s the main fork (a later-dropped relation still gets its truncate
replayed, "prefer to recreate the rel … until the drop is seen"), then
truncates each fork the `SMGR_TRUNCATE_{HEAP,VM,FSM}` flag selects — the main
fork to `blkno` (a NON-zero surviving prefix for a VACUUM tail truncation),
the vm and fsm forks unconditionally to zero.

**Why goopg's native analog cannot be reused as-is.** goopg's own
`RecordKindSmgrTruncate` / `replaySmgrTruncate` (`recovery.go`) always
truncates to zero blocks (`mgr.TruncateRelation`) and its decoder
(`DecodeSmgrTruncate`) carries only `{dbOid, relOid, fork}` — no `blkno`, no
fork bitmask, because goopg's own `TRUNCATE` always empties the whole file. A
real PG's VACUUM tail truncation is a genuinely partial shrink, which needed
two new primitives: `storage.relFile.truncateTo(n)` / `Manager.TruncateRelationTo`
(idempotent — a no-op if the file already has `<= n` blocks, mirroring
`TruncateRelation`'s zero-block idempotency) alongside the existing
truncate-to-zero pair, and a new PG decoder
(`decodeXLogSmgrTruncate`, `internal/wal/recovery.go`) that keeps
`decodeXLogSmgrCreate`'s default/global tablespace-OID remap to goopg's
`TblOid=0` convention. `applySmgrTruncate` reproduces `smgr_redo`'s
create-then-truncate order and per-flag fork selection directly (~0% redo
reuse from the native path; the idempotency shape is what transfers).

**Deliberate deviation, no ledger row needed:** upstream calls `XLogFlush(lsn)`
before truncating "so that if the truncation fails … you cannot start up the
system … until you fix the underlying situation" — a torn-truncate durability
guard for a *live* server applying WAL during normal operation. goopg only
replays this opcode during a single-threaded startup/crash-recovery pass with
no concurrent WAL writer and no independent flush point to protect, so the
`XLogFlush` call has no counterpart here; skipping it changes no observable
outcome in a batch-replay context.

Guards (`internal/wal/smgr_truncate_pg_test.go`, 4 tests, proven
fail-when-broken by a scripted revert of the dispatch case): a partial
main-fork truncate to `blkno=3` on a 10-block file lands at exactly 3 blocks
and is idempotent on replay; `SMGR_TRUNCATE_VM` alone truncates only the vm
fork to zero, leaving the main and fsm forks untouched; a truncate record
naming a relation with no on-disk main fork recreates it (one init block)
and then truncates it per the flags, matching upstream's recreate-then-drop
order; the default/global tablespace OID remap round-trips through the new
decoder.

**STILL OPEN in S21a-2: `HEAP2_REWRITE`'s loud refusal** (0x00 RM_HEAP2,
out-of-scope `VACUUM FULL`/`CLUSTER` with a logical slot — needs an
`ErrUnsupportedRecord`-style message, not real redo). Then S21b (btree, ~6
opcodes, gated on S16 which is already done).

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
