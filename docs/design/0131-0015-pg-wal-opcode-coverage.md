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
| `XLOG_HEAP2_REWRITE` | 0x00 | **REFUSED** (S21a-2 pt 7) | `heap_xlog_logical_rewrite` `rewriteheap.c:1073` | `VACUUM FULL`/`CLUSTER` **with a logical slot** (`rewriteheap.c:894`) |
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
| `INSERT_UPPER` | 0x10 | H (S21b part 1) | `btree_xlog_insert(f,f,f)` | downlink insert after a child split (`nbtinsert.c:1342`) |
| `INSERT_META` | 0x20 | H (S21b part 1) | `btree_xlog_insert(f,t,f)` | ditto, when the fast-root also moves (`nbtinsert.c:1348`) |
| `SPLIT_L` / `SPLIT_R` | 0x30/0x40 | H `:2470` | `btree_xlog_split` `:251` | page split |
| `INSERT_POST` | 0x50 | H (S21b part 2a) | `btree_xlog_insert(t,f,t)` | insert splitting a **posting list** on a deduplicated leaf (`nbtinsert.c:1337`) |
| `DEDUP` | 0x60 | H (S21b part 2b) | `btree_xlog_dedup` `:464-556` | leaf deduplication before a split (`nbtdedup.c:265`) |
| `DELETE` | 0x70 | H (S21b part 3) | `btree_xlog_delete` `:652-716` | LP_DEAD / bottom-up index deletion (`nbtpage.c:1369`) |
| `UNLINK_PAGE` / `_META` | 0x80/0x90 | H `:2503` | `btree_xlog_unlink_page` `:802` | page deletion |
| `NEWROOT` | 0xA0 | H `:2461` | `btree_xlog_newroot` `:941` | root split |
| `MARK_PAGE_HALFDEAD` | 0xB0 | H `:2494` | `btree_xlog_mark_page_halfdead` `:717` | page deletion, first phase |
| `VACUUM` | 0xC0 | H `:2486` | `btree_xlog_vacuum` `:598` | `VACUUM` index pass |
| `REUSE_PAGE` | 0xD0 | S (S21b part 3, named) | **no-op outside hot standby** `:1006-1015` | recycling a deleted page (`nbtpage.c:933-953`, itself gated on `XLogStandbyInfoActive()`) |
| `META_CLEANUP` | 0xE0 | H (S21b part 1) | `_bt_restore_meta(record, 0)` `:82` | `VACUUM`'s metapage cleanup-XID update (`nbtpage.c:304`) |

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

### S21a-2 part 7 — `XLOG_HEAP2_REWRITE`'s loud refusal (landed 2026-08-12)

The last opcode of S21a-2, and the only one whose landing is a *refusal* rather
than a redo. `XLOG_HEAP2_REWRITE` (0x00 RM_HEAP2) is emitted while a
`VACUUM FULL` / `CLUSTER` rewrites a table whose pre-rewrite row versions a
logical replication slot may still have to decode — `rewriteheap.c:894`, reached
only through `logical_rewrite_log_mapping`, i.e. `wal_level=logical` plus a slot
that can reach the relation. Its redo touches no relation page: it truncates a
`pg_logical/mappings/` file to the record's `offset`, rewrites the mapping tail
(old-ctid → new-ctid), and fsyncs it (`heap_xlog_logical_rewrite`,
`rewriteheap.c:1073-1160`).

**Why refusing is the honest answer.** goopg has no `pg_logical/mappings`
consumer, so neither alternative is defensible. Implementing the redo maintains
a file nothing reads — a half implementation whose only effect is to make the
gap invisible. Recognising it as a no-op (the treatment `HEAP2_NEW_CID` gets one
arm below, and for good reason: that record has no physical effect at all) would
leave a slot on the resulting cluster decoding the rewritten table against
mappings that stop mid-rewrite, with nothing reporting an error. So the arm
returns `(false, err)` with `ErrUnsupportedRecord` and a message naming the
feature — the operator learns *this cluster ran VACUUM FULL/CLUSTER on a table
with a logical replication slot*, not "recovery failed". Same shape as the
2PC refusal (`xlogXactPrepare`/`CommitPrepared`/`AbortPrepared`) already in
`replayDecodedXLogRecord`.

The `ErrUnsupportedRecord` wrapping is not cosmetic: it is the class the reader
uses to distinguish a durable record it cannot handle from a torn crash tail
(`format.go`, M0131-S16.2). Before this arm the record fell to RM_HEAP2's
`default:`, which returns a bare error carrying no class at all.

Guards (`internal/wal/heap2_rewrite_pg_test.go`, 3 tests, proven fail-when-broken
by a scripted revert of the dispatch case — both refusal guards fail with the
generic `unsupported xlog record rmid=9 info=0x00`): the dispatch arm refuses
with `ErrUnsupportedRecord`, `applied=false`, and a message mentioning
`logical` / `VACUUM FULL` / `CLUSTER`; a real-shaped 40-byte
`xl_heap_rewrite_mapping` record (`{mapped_xid, mapped_db, mapped_rel, offset,
num_mappings, start_lsn}`, no block references) driven through
encode→decode→`ApplyRecord` reaches that same refusal rather than an earlier
decode error; and `XLOG_HEAP2_NEW_CID` (0x70) still returns `(false, nil)`, so
the new arm has not swallowed its nearest logical-decoding-only neighbour.

**S21a-2 is now CLOSED.** Next: S21b (btree, ~6 opcodes — `INSERT_UPPER` 0x10,
`INSERT_META` 0x20, `INSERT_POST` 0x50, `DEDUP` 0x60, `DELETE` 0x70,
`META_CLEANUP` 0xE0; `REUSE_PAGE` 0xD0 needs no redo), gated on S16 which is
already done.

### S21b — implementation notes, part 1: INSERT_UPPER + INSERT_META + META_CLEANUP (landed 2026-08-12)

The three btree opcodes whose whole redo is *insert a downlink and/or rewrite the
metapage* — no new page primitive, so they group naturally against the posting-list
pair (`INSERT_POST`/`DEDUP`, part 2) and the item-array rewrite (`DELETE`, part 3).

`replayDecodedXLogBtreeInsert` grew upstream's own two arguments — the function is
literally `btree_xlog_insert(isleaf, ismeta, posting)` (`nbtxlog.c:160-247`)
instantiated four ways, and goopg already had the `(true,false,false)` one — plus
two limbs in upstream's order:

- **block 1, when `!isleaf`.** An insert into an internal page IS the completion of
  the child's split, so redo clears the child's `BTP_INCOMPLETE_SPLIT`
  (`_bt_clear_incomplete_split`, `:132-155`). Upstream reads it *before* block 0 and
  **unconditionally**: `_bt_insertonpg` registers `cbuf` as block 1 on every `!isleaf`
  path (`nbtinsert.c:1342-1343`), and `XLogReadBufferForRedo` PANICs on an
  unregistered id. goopg therefore REFUSES a record missing block 1 rather than
  skipping the limb, which would leave a permanently half-split child while replay
  reported success (its own guard).
- **block 2, when `ismeta`.** The metapage, registered `REGBUF_WILL_INIT`
  (`nbtinsert.c:1359-1360`) and rebuilt from the carried `xl_btree_metadata`.

**The block-0 image branch must NOT return early** — the shape the pre-S21b insert
replay had, since with one limb there was nothing to fall through to. Upstream's
`XLogReadBufferForRedo` reports `BLK_RESTORED` for block 0 only and
`btree_xlog_insert` still reaches `if (ismeta) _bt_restore_meta(record, 2)`;
returning after the image restore leaves the metapage naming a stale root with
replay reporting success. That is the one non-obvious limb, so it has a dedicated
guard.

`_bt_restore_meta` became the shared `replayDecodedXLogBtreeRestoreMeta(…, blockID,
what)` — three call sites now need it on **two different block ids**:
`INSERT_META` and `NEWROOT` on block 2, `META_CLEANUP` on block 0. Hard-coding the
id (the obvious refactor) would silently rebuild the wrong page for `META_CLEANUP`,
so the id is a parameter and the guard seeds a second page to prove it. The
`NEWROOT` metapage limb was folded onto the helper unchanged — its error strings
were already identical.

`XLOG_BTREE_META_CLEANUP`'s entire upstream redo is `_bt_restore_meta(record, 0)`:
`_bt_set_cleanup_info` (`nbtpage.c:304`) stamping `btm_last_cleanup_num_delpages`
after a `VACUUM`, touching no other page.

**Correction to the S21a-2 closing note:** it said `REUSE_PAGE` 0xD0 "was
recognised in S21a-1". It was not — S21a-1's six recognised no-ops are
`HEAP_TRUNCATE`, `HEAP2_NEW_CID`, `XACT_ASSIGNMENT`, `XACT_INVALIDATIONS`,
`STANDBY_LOCK` and `STANDBY_INVALIDATIONS`, none of them RM_BTREE. 0xD0 still falls
to the `default:` refusal and is part 3's work.

5 guards in `internal/wal/btree_insert_upper_pg_test.go`, ALL proven fail-when-broken
by 4 scripted reverts (dropped child limb → 2 FAIL; early return on a block-0 image
→ 1 FAIL; `META_CLEANUP` reading block 2 → 1 FAIL; all three dispatch arms removed
→ 5 FAIL).

**Remaining in S21b:** part 2 `INSERT_POST` 0x50 + `DEDUP` 0x60 (both need
`_bt_swap_posting`/`_bt_form_posting`, the first posting-list *writers* in goopg —
`internal/access/btree/posting.go` only parses), part 3 `DELETE` 0x70 +
`META_CLEANUP`'s neighbour `REUSE_PAGE` 0xD0 as a recognised no-op.

### S21b — implementation notes, part 2a: INSERT_POST (landed 2026-08-12)

`XLOG_BTREE_INSERT_POST` (0x50) is the leaf insert whose heap TID fell *inside* an
existing posting list, so `_bt_insertonpg` had to split that posting to make room
(`nbtinsert.c:1337`, the `postingoff > 0` path). goopg never emits it — its own
duplicate-key inserts APPEND to a posting (`appendTIDToPosting`) — but
**deduplication is on by default in every real PG index whose opclass allows it**,
so this is ordinary traffic in a crash tail, and PG logs an FPI only on a page's
first post-checkpoint touch. Before this slice it fell to RM_BTREE's `default:` and,
since S16.3, refused the start.

**The record does not carry the item that ends up on the page.** Block 0's data run
is `{uint16 postingoff, orignewitem}` (`nbtinsert.c:1316-1330`) — the new item as it
looked *before* the split — so redo must re-run `_bt_swap_posting` rather than
trust the record. That function is the non-obvious part, and nothing about its name
says so: **nothing grows.** `nposting` keeps `oposting`'s exact byte length and TID
count; TIDs at `[postingoff, nhtids-1)` shift one slot right, the posting's
rightmost/max TID **falls off the end**, `newitem`'s original TID fills the gap at
`postingoff`, and the evicted max TID becomes the *final* new item's `t_tid`. Fixed
length is what lets the caller overwrite the posting in place (upstream's
`memcpy(oposting, nposting, …)`; `storage.PageReplaceItemRaw`'s in-place branch
here), so the page only has to find room for the one new item. Inserting
`orignewitem` verbatim would leave BOTH the posting and the new item holding the
wrong TID — an index that *scans wrong* rather than one that fails to start, which
is why the guard asserts the swapped TIDs and not merely "the page has one more
item".

Two writers land in `internal/access/btree/posting.go`, goopg's first:
`SwapPosting` (`_bt_swap_posting`) and the exported `PGBTPostingRaw`
(`_bt_form_posting`), the latter now the ONE tuple-format posting encoder —
`marshalPosting` delegates to it for `tupleKeys()`, which is byte-identical to what
it computed inline (`postingOffsetFor` is `MAXALIGN(len(key))` and `postingSizeFor`
MAXALIGNs the total for that format), so no on-disk bytes move. Blob format keeps
its own unaligned encoder unchanged. `ApplyInsertPostingRecordAt`
(`internal/access/btree/replay.go`) is the page-level redo; the WAL side is one new
`posting` argument on `replayDecodedXLogBtreeInsert`, matching upstream's
`btree_xlog_insert(isleaf, ismeta, posting)` — now instantiated all four ways.

Refusals rather than corruption, both before the page is touched: `postingoff` out
of `(0, nhtids)` (upstream's own `elog(ERROR)` — a 0 would shift the whole array and
write TIDs that are no longer ascending), a left neighbour that is not a posting
list (upstream reads it as one unconditionally, its `Assert` compiled out), and an
`offnum < 2`, which has no predecessor at all and whose unchecked `offnum-1` would
underflow to 65535.

5 guards in `internal/wal/btree_insert_post_pg_test.go`, including the
FPI shape (restore the image, do NOT re-run the split on top of it). Proven
fail-when-broken by 2 scripted reverts: not evicting the max TID → the replay guard
FAILs on the new item's `t_tid`; dropping the `offnum < 2` check → the refusal guard
FAILs on a raw slot error from the storage layer.

**Remaining in part 2:** `DEDUP` 0x60 (`btree_xlog_dedup`, `nbtxlog.c:464-556`) — it
rebuilds the whole leaf from an interval array via `_bt_form_posting`, which
`PGBTPostingRaw` now supplies. (Landed as part 2b, below.)

### S21b — implementation notes, part 2b: DEDUP (landed 2026-08-12)

`XLOG_BTREE_DEDUP` (0x60) is the leaf deduplication pass: before letting a full leaf
split, `_bt_dedup_pass` (`nbtdedup.c:265`) merges runs of equal-key tuples into
posting lists and reclaims the space. goopg never runs one — its inserts append to
an existing posting a TID at a time — but deduplication is the default for every
opclass that allows it, so this is the *most common* PG-only btree opcode in a crash
tail, and it is the record S16.3 named when it turned the old silent FPI-only
`default:` arm into a refusal.

**The record carries no tuples at all** — only `xl_btree_dedup{uint16 nintervals}`
in the main data and an array of `BTDedupInterval{OffsetNumber baseoff; uint16
nitems}` (4 bytes each) as block 0's data run. Everything the merged postings are
made of is already on the page being replayed, so this is a page-**rebuild** slice
rather than an edit: `btree.ReplayDedupPage` snapshots the pre-image's items,
collapses each interval's run of `nitems` consecutive items into one posting formed
over the run's heap TIDs in page order (`PGBTPostingRaw` = `_bt_form_posting`, the
primitive part 2a exported), copies every non-interval item verbatim, and re-adds
the result. Upstream reaches the same page by re-running its dedup state machine
(`_bt_dedup_start_pending` / `_bt_dedup_save_htid` / `_bt_dedup_finish_pending`)
under the record's interval bounds and then asserts the reconstruction matched
(`memcmp(state->intervals, intervals, …) == 0`); the bounds are what make the two
formulations identical, which is why redo needs no key comparisons — and therefore
no catalog — to do it.

Two details decide correctness, and both produce a page with the *right item count*
when got wrong:

- **`nitems` counts ITEMS, not heap TIDs.** An item inside a run may already be a
  posting list from an earlier dedup pass, contributing several TIDs. When such an
  item is the run's *base*, only its key material is the base tuple —
  upstream's `basetupsize = BTreeTupleGetPostingOffset(base)` — and its TID array
  joins the merge. Treating it as opaque key bytes yields a posting short by
  however many TIDs it already held.
- **The high key is not re-added here.** Upstream copies `P_HIKEY` onto its temp
  page first (`PageGetTempPageCopySpecial` + the `!P_RIGHTMOST` branch), but
  `resetPageItems` already re-installs the separator for every rewrite path in
  this package (split, VACUUM, replay), so `ReplayDedupPage` rebuilds DATA items
  only. Re-adding it leaves the page carrying two separators — caught by the
  non-rightmost guard, which is the reason that guard exists.

`BTP_HAS_GARBAGE` is cleared as upstream does: the rebuild rewrites every item, so
no LP_DEAD line pointer survives it. The FPI branch is the plain one — an image is
the finished page, so redo restores it and stops rather than merging already-merged
postings a second time.

Refusals rather than corruption, all before the page is touched: an interval array
shorter than the record's own `nintervals` (trusting `len(block.Data)` instead would
replay a *partial* dedup pass and leave the page half-merged), a `baseoff` that does
not fall on a run boundary, a run of one item (upstream records an interval only in
`_bt_dedup_finish_pending`'s `nitems > 1` branch), a run overrunning the page, and a
pivot tuple inside a run.

6 guards in `internal/wal/btree_dedup_pg_test.go` (rightmost two-interval merge,
non-rightmost page whose base is an existing posting + the `BTP_HAS_GARBAGE` clear +
untouched `btpo_next`, the FPI shape, and three malformed-record refusals). Proven
fail-when-broken by 2 scripted reverts: dropping the unmatched-interval check → the
refusal guard FAILs; not contributing a base posting's existing TIDs → the merge
guard FAILs on the posting's TID count.

### S21b part 3 — `DELETE` 0x70 + `REUSE_PAGE` 0xD0 (landed 2026-08-12)

`XLOG_BTREE_DELETE` is the LP_DEAD **simple deletion** pass
(`_bt_delitems_delete`, `nbtpage.c:1369`): an index scan that lands on a dead heap
tuple marks the entry, and the next insert short of room on that page deletes the
marked entries instead of splitting. goopg has no such pass — its index cleanup
rides `RecordKindBtreeVacuum` — so it never emits the opcode, but a real PG
primary emits it constantly, and until this slice the record hit RM_BTREE's
`default:` arm, which since S16.3 refuses a record whose blocks do not all carry a
full-page image.

The record is `xl_btree_delete{snapshotConflictHorizon, ndeleted, nupdated,
isCatalogRel}` (`SizeOfBtreeDelete` = 9; the first and last fields are hot-standby
conflict resolution, which goopg does not implement) plus a block-0 data run of
**deleted offsets, updated offsets, then one variable-length `xl_btree_update` per
updated offset**. Redo is upstream's two steps in upstream's order
(`btree.ReplayDeletePage`):

- **`btree_xlog_updates` (`:556-597`) first.** This is the half that made part 3
  real work rather than a fourth `PageIndexMultiDelete` caller. A posting-list
  tuple's TIDs die *one at a time*, so cleanup cannot express the work as "delete
  these items" alone: the tuple is REWRITTEN without the dead TIDs. Each
  `xl_btree_update` carries `ndeletedtids` **0-based indexes into the posting's own
  TID array** (`nbtxlog.h:258-263` says so explicitly — they are not page offset
  numbers), and `updatePostingRaw` is `_bt_update_posting` (`nbtdedup.c`): more
  than one survivor re-forms a posting sized exactly as `_bt_form_posting` would,
  and **exactly one survivor collapses to a PLAIN non-pivot tuple** —
  `INDEX_ALT_TID_MASK` off, the survivor's heap TID back in `t_tid`, size back to
  `keysize`. A one-entry "posting" is a tuple no PG ever writes, and every posting
  reader keys off `BT_IS_POSTING`, so leaving the bit set fails far from this
  record.
- **`PageIndexMultiDelete` second** (`ReplayVacuumDelete`, already present), then
  the `BTP_HAS_GARBAGE` clear.

**The order is not cosmetic.** Both offset arrays are page offset numbers in the
**pre-deletion** coordinate space, and an update rewrites in place without moving a
line pointer, so updating first leaves the deletion offsets meaning what the
primary meant. Deleting first shifts every later offset down and rewrites the wrong
tuples. (Upstream's one difference between the two records — `VACUUM` clears
`btpo_cycleid`, `DELETE` deliberately does not — is not a difference here: goopg's
opaque has no cycle-id field.)

**Sibling path: `xl_btree_vacuum` shares all of it.** Its payload is the same
bytes and its redo the same two calls, and goopg *refused* its `nupdated > 0` form
outright (`"posting-list updates (xl_btree_update) are not implemented"`) — so a
real PG's VACUUM records were refusing the start for the same missing primitive.
Both opcodes now share `decodeXLogBtreeDeletePayload` and `ReplayDeletePage` rather
than growing a second, drifting copy.

`REUSE_PAGE` 0xD0 gets a named arm that does **nothing**: upstream's
`btree_xlog_reuse_page` (`:1006-1015`) is one `if (InHotStandby)` conflict resolve
and mutates no page, so the record registers no blocks at all — which is precisely
why it cannot stay on the `default:` arm, whose S16.3 precondition ("every block
carries an applicable image") a block-less record can never meet. With it,
**RM_BTREE's opcode space is complete**: every value `nbtxlog.h` defines now has a
named arm, so the fallback is reachable only via an info value outside that space
(where upstream `btree_redo` PANICs and goopg refuses). The S16.3 guard's probe
moved from 0xD0 to 0xF0 accordingly.

Refusals before the page is touched: an update naming a non-posting item, an update
deleting *every* TID (upstream's `Assert(nhtids > 0)`; the primary deletes the whole
item instead), a TID index out of range or not strictly ascending (upstream's redo
loop has a single `d` cursor and would silently keep a TID it claims is dead), a
block-0 run truncated relative to the record's OWN counts, and a deleted offset
outside the page's data range.

7 guards in `internal/wal/btree_delete_pg_test.go` (whole-item deletion with the
hint clear, deletions + posting updates in one record, the single-survivor collapse,
the vacuum sibling path, the FPI shape, five malformed-record refusals, and the
0xD0 no-op). Proven fail-when-broken by 3 scripted reverts: applying deletions
before updates → 2 FAIL; re-forming a posting for a single survivor → 1 FAIL
(panic); dropping the `xl_btree_update` decode → 4 FAIL.

**S21b is closed.** Still deferred, and recorded in the ledger: goopg emits neither
`DELETE` nor `DEDUP` (no LP_DEAD deletion pass, no dedup pass), and `REUSE_PAGE`'s
hot-standby conflict resolution has no counterpart because goopg has no hot standby.

### S22 — implementation notes (landed 2026-08-12)

Both bugs fixed in one slice; the plan above survived contact with the code
essentially unchanged, with three refinements worth recording.

**Bug (a), dispatch.** `internal/initdb/xact_recovery.go` now reads the opcode
first and `continue`s on anything that is not `XLOG_XACT_COMMIT` /
`XLOG_XACT_ABORT`. The plan said "0x10/0x30/0x40 error"; the implementation
**skips** them instead, because the *physical* pass already refuses two-phase
records loudly (`recovery.go`, `RmgrXact` arm — "two-phase commit recovery is not
supported") and `initdb.Open` runs that pass first. A second refusal here would
be unreachable code duplicating a message; a second *stamp* is what had to go.

**Bug (b), `subxacts[]`.** New `internal/wal/pg_xact_parse.go`:

```go
func ParseXactRecord(info uint8, mainData []byte) (XactParsed, error)
```

Named for `ParseCommitRecord`/`ParseAbortRecord` rather than the plan's
`ParseXactCommitSubxacts`, and returning a struct (`Xinfo` + `Subxacts`) rather
than a bare slice, so the later chunks (relfilelocators, dropped stats, invals)
have somewhere to land when a future slice needs them. The walk stops after the
subxact array, as planned. `xactCommitCarriesInvals` is left alone — it reads the
`xinfo` word at a fixed offset and stays correct regardless of what follows it.

Three implementation facts the plan did not carry:

1. **A truncation is an error, not a short read.** A partially decoded subxact
   list is strictly worse than none: the decoded half gets stamped committed and
   the undecoded half is swept ABORTED by `MarkUnknownAsAborted`, tearing the
   transaction exactly the way S22 exists to prevent. The caller treats a parse
   error as "keep the top-level stamp, skip the tree" — the pre-S22 behaviour —
   rather than failing the start, since the reader has already CRC-verified
   these bytes so a parse error means an unfamiliar shape, not corruption.
2. **`nextXID` must clear the highest subxact,** not just `xl_xid`; upstream
   takes `TransactionIdLatest(xid, nsubxacts, subxacts)` before
   `AdvanceNextFullTransactionIdPastXid` (`xact.c:6190`). Falls out for free
   here because `xactStampAndAdvance` advances per XID.
3. **The emit side gained the mirror encoders** —
   `EncodeXactCommitPGWithSubxacts`, `EncodeXactAbortPGWithSubxacts`,
   `EncodeXactAssignmentPG` (`internal/wal/pg_assembled_emit.go`), with the
   old two-arg entry points delegating. goopg's commit path passes no subxacts
   yet (ledger row), so today they exist to build the records replay is tested
   against — but they are the shape the emit side will need, and writing the
   chunk order once means encoder and decoder cannot drift apart.

Guards: 6 in `internal/wal/xact_parse_pg_test.go` (minimal commit, commit with
subxacts, subxacts + invals in one record, a hand-built body carrying the
`dbinfo` chunk goopg never emits, four truncation cut points, the abort twin)
and 4 in `internal/initdb/xact_recovery_test.go` (commit tree stamped committed
+ `nextXID` past the highest subxact, abort tree, a standalone
`XLOG_XACT_ASSIGNMENT` that must leave CLOG untouched, assignment-then-commit).
Proven fail-when-broken by 2 scripted reverts: dropping the opcode dispatch → 1
FAIL; dropping the subxact walk → 3 FAIL. Recorded from those reverts: the
assignment-then-commit ordering **self-heals** the dispatch bug (the later
commit overwrites the bogus abort stamp), which is why the standalone-assignment
guard is the one that catches it — the damaging case is the assignment whose
commit record has not been written yet, i.e. every transaction in flight at the
crash.

**Still deferred** (ledger row): goopg never *emits* `subxacts[]` in its own
commit records — its subxact map rides the native `RecordKindXactAssignment`
marker — so a real PG starting on a goopg `$PGDATA` still loses the
subtransaction tree of a goopg-committed transaction. That is the emit-side
mirror of this slice, and it belongs with the commit path, not with recovery.

### S21c — implementation notes: in-place line-pointer reuse (landed 2026-08-12)

Found while gating S22: with S21a's `XLOG_NEXTOID` refusal gone, the S28 crash
E2E's next stop was

```
xlog heap-multi-insert entry 0 targets already-allocated line pointer N — goopg redo has no in-place line-pointer reuse
```

**One upstream call, three goopg call sites.** `heap_xlog_insert`,
`heap_xlog_multi_insert` and `heap_xlog_update` all place their tuple with the
identical `PageAddItemExtended(page, item, size, offnum, PAI_OVERWRITE |
PAI_IS_HEAP)` (`postgres/src/backend/storage/page/bufpage.c:193-330`). That one
call hides two cases, and goopg needs two different primitives for them:

| case | upstream | goopg |
|---|---|---|
| `offnum` past the array end | append | `PageInsertItemRawAt` (its `[1,count+1]` check *is* upstream's "invalid max offset number" PANIC) |
| `offnum` inside the array, pointer LP_UNUSED | fill IN PLACE | `PageReplaceItemRaw` + `PageSetLinePointerNormal` |
| `offnum` inside the array, pointer USED | WARNING "will not overwrite a used ItemId" → caller PANICs | refuse with `ErrUnsupportedRecord` |

The in-place case is **ordinary traffic, not an exotic shape**: any page pruned
before the insert carries LP_UNUSED holes, so a `COPY` after a `VACUUM` — the
S28 workload exactly — reaches it constantly.

The three sites disagreed in three different ways, which is why this is one
slice and not three:

- **multi-insert** refused loudly (the message above) — honest, but a refusal.
- **single insert** had *no check at all*: it called `PageInsertItemRawAt`
  directly, which SHIFTS the line-pointer array right. The row at the target
  slot silently moves to the next one and every ctid pointing at it goes stale —
  silent corruption where upstream PANICs. This is the sibling-path rule
  (`pattern_sibling_paths_must_agree`) paying off: nothing in the S21a-2 work
  pointed at this path, and its own tests were green.
- **update** ignored `new_offnum` altogether — it APPENDED via
  `PageAddHeapTuple` and then merely complained when the resulting slot
  disagreed (`"xlog heap-update new-slot drift: got 48, want 5"`), which is a
  refusal on every real-PG tail that updates into a hole.

All three now route through `redoHeapPageAddItemOverwrite`
(`internal/wal/recovery.go`), which is the goopg spelling of that upstream call.
Keeping the USED-pointer case a hard refusal is deliberate: a used slot means
the page on disk disagrees with the record, and writing anyway trades a loud
start failure for a wrong ctid discovered much later.

**Effect on the S28 gate:** `TestE2E_GoopgCrashStartOnPGDataDir` advanced from
refusing at replay record 24720 to 43900, where it now stops at a *different*
defect — `"xlog heap-update: block 41 is uninitialised"`. That is the update
path missing S21a-2's `redoHeapPageForBlock` treatment (zero-extend past the
replay gap + honour `XLOG_HEAP_INIT_PAGE`), which the insert paths already have.
Filed as **M0131-S21d** with a ledger row; it is a page-acquisition defect, not
a line-pointer one, so it is not folded in here.

### S21d — implementation notes: `heap_xlog_update` acquires its pages like everyone else (landed 2026-08-12)

S21c's sibling audit was about *where on the page* a tuple lands; S21d is about
*how the page is obtained*. `replayDecodedXLogHeapUpdate` was the last heap redo
routine still doing its own `NBlocks` + `ReadBlock` + two hard refusals —
`"block %d does not exist"` and `"block %d is uninitialised"` — instead of
S21a-2's `redoHeapPageForBlock`. Upstream draws no such distinction:
`heap_xlog_update` (`heapam_xlog.c:918-931`) takes `XLogInitBufferForRedo` +
`PageInit` when `XLOG_HEAP_INIT_PAGE` is set and `XLogReadBufferForRedo`
otherwise — the same `XLogReadBufferExtended` (`xlogutils.c:479-539`) that
zero-extends the fork past its flushed end that every other heap redo routine
uses.

Both refusals fire on ordinary real-PG traffic. A crash tail routinely names a
block past the last flushed one (the extension itself was never WAL-logged), and
an UPDATE that moves a row to a freshly extended page arrives with
`XLOG_HEAP_INIT_PAGE` set and *nothing on disk to read*: the record's offsets are
relative to an empty page precisely because the primary had just initialised one.

The two block references are deliberately **not** symmetric, and that asymmetry
is upstream's:

| block | upstream call | goopg helper | page absent ⇒ |
|---|---|---|---|
| 0 (new version) | `XLogInitBufferForRedo` / `XLogReadBufferForRedo` | `redoHeapPageForBlock` | zero-extend the gap, init, apply |
| 1 (old version, cross-page only) | `XLogReadBufferForRedo`, RBM_NORMAL | `redoExistingHeapPageForBlock` | `BLK_NOTFOUND` ⇒ skip the stamp |

Only the page receiving the new tuple may be extended into existence. The page
holding the row's OLD version cannot legitimately be missing unless the relation
is dropped or truncated later in the same stream, which is exactly the case
upstream's `BLK_NOTFOUND` covers (goopg's deviation there — no invalid-page hash
table, so a genuinely missing page is skipped rather than PANICking at the end of
recovery — is the one already recorded for `redoExistingHeapPageForBlock`).

**Effect on the S28 gate:** `TestE2E_GoopgCrashStartOnPGDataDir` no longer
refuses at all — a real PG's crash tail now replays **end to end**, recovery
completes and goopg writes its own checkpoint over the PG data directory. The
test still fails, but past replay entirely and on an unrelated layer: the
PG-created table is not resolvable (`relation "s28_items" does not exist`,
42P01), i.e. cold-start catalog visibility rather than WAL. Filed as
**M0131-S21e** with a ledger row.

**Sibling audit (S21c's lesson, applied):** three PG-format redo paths still
carry the same hand-rolled "must exist and be initialised" guard where upstream
would skip — `replayDecodedXLogHeapDelete`, `replayDecodedXLogHeapPrune`, and the
shared `replayExistingXLogBlock` (all btree edit-shaped records). They were left
alone this loop on purpose: unlike the update path they fail *loudly* rather than
corrupting, none is the gate's current stop, and converting them flips several
opcodes from "refuse the start" to "silently skip" at once. Filed as
**M0131-S21f** with a ledger row rather than folded in.

### S21g — implementation notes: the new tuple is not all in the record (landed 2026-08-12)

**S28 is GREEN.** A real PostgreSQL 18.3 cluster that was SIGKILLed mid-life is
now served by goopg end to end: recovery replays PG's crash tail, every one of
the E2E's twelve row-equality checks matches PG's own pre-crash answers, and the
row PG had `SELECT … FOR UPDATE`'d is readable *and* updatable afterwards.

S21d's closing note predicted the remaining stop was a catalog-cold-start gap
(filed as S21e: "goopg's boot does not build its in-memory catalog from the
on-disk `pg_class`/`pg_attribute` it just replayed"). **That diagnosis was
wrong, and the way it was wrong is the lesson.** goopg's boot reload was fine —
`loadUserTablesFromHeapForDB` scanned the replayed `base/5/1259` and kept 424 of
429 tuples. What it could not decode were exactly three of them, with
`pg_class physical row too short: len=4`, and the three were exactly
`s28_items`, `s28_sub` and `s28_scratch`. Their *index* and *toast* pg_class
rows reloaded perfectly. A crashed PG's `CREATE TABLE` survived as an index and
a toast relation with a 4-byte husk where the table row should be.

The cause is that **an `xl_heap_update` does not necessarily carry the whole new
tuple.** Upstream's `log_heap_update`
(`postgres/src/backend/access/heap/heapam.c:8730-8800`) compares the two
versions byte-for-byte and, when they share a leading and/or trailing run inside
the data area, logs `uint16 prefixlen` / `uint16 suffixlen` in front of the
`xl_heap_header` and *omits those bytes entirely*, setting
`XLH_UPDATE_PREFIX_FROM_OLD` (0x20) / `XLH_UPDATE_SUFFIX_FROM_OLD` (0x40).
`heap_xlog_update` (`heapam_xlog.c:933-1005`) reverses it, assembling

```
SizeofHeapTupleHeader | bitmap+padding (t_hoff-23 bytes, from the record)
                      | prefixlen bytes from the OLD tuple's data area
                      | the rest of the record's tuple bytes
                      | suffixlen bytes from the OLD tuple's tail
```

goopg discarded the `flags` byte (`_` in the `decodeXLogHeapUpdateMainData`
destructuring) and treated block 0's data as an `xl_heap_insert` payload, so it
wrote *the middle bytes* as the whole tuple. `decodeXLogHeapUpdateNewTuple`
(`internal/wal/recovery.go`) now does the splice, reading the old version off the
page with `storage.PageGetItemRaw` — legitimate because upstream asserts
`newblk == oldblk` whenever either flag is set, and goopg refuses the record
rather than guessing if a cross-page reference arrives with one.

Two things worth carrying forward:

- **This failure was silent, not loud.** Every other S21 slice announced itself
  by refusing to start. This one replayed "successfully" and produced a
  structurally valid page containing a corrupt tuple; only a decoder downstream
  (the catalog reload) noticed. When a redo routine's input is *self-describing
  minus the parts the writer knew redo could reconstruct*, dropping a flag byte
  is a data-corruption bug wearing a green test.
- **A catalog UPDATE is the worst case for it.** Flipping
  `pg_class.relhasindex` after `CREATE INDEX` changes one byte in a ~150-byte
  row, so prefix+suffix compression takes the logged tuple down to ~4 bytes —
  which is why the symptom appeared on tables and not on their indexes.

The tuple is now also built *inside* the `BLK_NEEDS_REDO` branch rather than
before page acquisition, matching upstream, since the reconstruction needs the
page anyway.

S21e is therefore **resolved by root cause, not by the work it described**: no
cold-start catalog change was needed or made.

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
8. S21c: an insert into an LP_UNUSED hole replays IN PLACE without shifting the
   array — asserted on **both** the insert and multi-insert paths, since a green
   test on one proves nothing about the other; and a USED target line pointer is
   refused on both.
9. S21d: an `xl_heap_update` naming a new-tuple block past the end of the fork
   zero-extends the gap and lands the tuple (instead of `"block N does not
   exist"`), while a cross-page record whose OLD block is missing still applies
   the new version and merely skips the stamp — the extend/skip asymmetry
   asserted on both halves.
10. S21g: an `xl_heap_update` carrying `XLH_UPDATE_PREFIX_FROM_OLD` and/or
   `XLH_UPDATE_SUFFIX_FROM_OLD` reconstructs the FULL new tuple from the old
   version on the same page — asserted for prefix+suffix, prefix-only and
   suffix-only, since the three take different branches upstream; proven
   fail-when-broken by re-inserting the "treat it as uncompressed" path, which
   leaves the tuple holding only the record's middle bytes.
11. The S28 reverse crash E2E (`0131-0017`) with the full opcode workload: COPY,
   VACUUM, `SELECT … FOR UPDATE`, TRUNCATE, SAVEPOINT, index-heavy insert.
12. UNITS + RACE (`internal/wal`, `internal/initdb`) + SMOKE + SPOT green.

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
