# 04 — Remove canonical WAL + knob + skip-tag; dispatch on PG-compatible (xl_rmid, xl_info)

| Field  | Value                                                        |
| ------ | ------------------------------------------------------------ |
| Status | draft — **agent-reviewed** vs code/PG-source 2026-07-15 (2 blockers + 1 major + 5 minor found and folded in: header-decode guard for custom rmid, native-`replayX` routing, `ClogTruncate`→`RM_CLOG`) |
| Date   | 2026-07-15                                                   |
| Branch | `wal-system-pgnize`                                           |
| Oracle | PostgreSQL 18.3 — tree under [`postgres/`](../../../postgres/) |
| Follows | [01](01-emitted-wal-record-inventory.md) · [03](03-pg183-wal-record-schemas.md) (content-parity blueprint) |

## 1. Goal & non-goals

**Goal.** Make goopg's WAL look like **ordinary PostgreSQL 18.3 with no special
settings** by removing the goopg-special scaffolding bolted onto the
PG-compatible frame, and by making both classification *and* recovery dispatch
key on a **PG-compatible `(xl_rmid, xl_info)`** — a real PG-style rmgr/opcode
table — instead of the `RM_XLOG`/`info=0xF0` skip-tag plus `payload[0]` routing.

Three removals (the user's three requirements):
1. the **canonical** record family (`payload[0]==0xFE`, full-FPI PG body) emitted
   *in addition* to native records;
2. the **`GOOPG_WAL_CANONICAL` knob** and all its constants/config/env;
3. the **native skip-tag** — every record stamped `RM_XLOG`/`0xF0` so a stock PG
   *skips* it — replaced by real/PG-framework `(rmid, info)`.

**Kept (user-decided):** the PG-compatible **frame** (`PageHeaders` ON — page
headers `0xD118`, 24-byte `XLogRecord`, CRC32C), the **PG-format 88-byte
checkpoint** (`PGCompatCheckpoints` ON), and **all PG-frame decode**. Because
`PageHeaders` stays on, the on-disk **frame bytes are unchanged**; only the
per-record `(xl_rmid, xl_info)` values change (and the `0xFE` records disappear).

**Non-goals (explicitly deferred).** The record **body/content** rewrite (native
bodies → real PG struct layouts, per docs [01](01-emitted-wal-record-inventory.md)/
[03](03-pg183-wal-record-schemas.md)) is **not** in scope. Bodies stay
goopg-native, so PG tools (`pg_waldump`, PG standby) will **misparse/crash** on
the real-rmid records — those compatibility tests become **expected failures,
not regressions**, until the content rewrite lands. goopg↔goopg keeps working
(both ends share the `(rmid, info)` mapping); **fresh clusters are assumed**.

## 2. The core change

### 2.1 Today
`classifyXLogRecord` (`internal/wal/format.go:217-254`) stamps every native
record `RmgrXLog` / `xlogInfoDefault=0xF0` (`format.go:26`). Recovery has **two
parallel replay families** (verified, review):
- **Native replay** — `ApplyRecord` (`recovery.go:8756+`), reached only when
  `Rmid==RmgrXLog && Info==xlogInfoDefault` (gate `:8745`) **and** `payload[0]` is
  in the `nativeApplyRecordKindKnown` allow-list (gate `:8735`, list `:9191`;
  empty-payload gate `:8732`). It parses the **native main-data** in `r.Payload`
  via a `payload[0]` switch → `replayHeapInsert`/`replayHeapDelete`/
  `replayBtreeSplit`/… These are the functions that actually apply native
  mutations.
- **Decoded/canonical replay** — `replayDecodedXLogRecord` (`recovery.go:9235-9302`),
  reached for any `Rmid != RmgrXLog/0xF0`. Its `RmgrHeap` (`:9268-9289`) and
  `RmgrBtree` (`:9290-9299`) arms are **FPI-oriented** — they call
  `replayDecodedXLogHeapFPIBlocks`, restoring only block refs with
  `HasImage && ImageApply`. A native heap/btree body (short main-data, **no** block
  ref / FPI) routed here would iterate an **empty** `xlog.Blocks` → **no-op → the
  mutation is silently dropped**.

Canonical (`0xFE`) records are unwrapped to real `(rmid, info)` at
`format.go:226-229` / `wrapXLogMainData:162-164`. The goopg↔goopg standby path
`replayedXactInfo` (`stream_replayer.go:159`) also branches on `Rmid==RmgrXact`
(checks `payload[0]` first) and recognizes only `XactCommit(8)`/`XactAbort(9)` —
must be re-checked when rmids change (it misses `XactCommitInval(32)`).

### 2.2 After
- **classify:** a `recordKindToRmgrInfo(payload[0]) → (Rmgr, uint8)` mapping
  (table in `format.go`) replaces the `0xF0` catch-all. Records with a PG analog
  get their **real** PG rmgr/opcode; goopg-private records get a **custom rmgr id
  in PG's reserved range** (`RM_MIN_CUSTOM_ID`=128, `rmgr.h:35`). Mostly a pure
  function of `payload[0]`, with two body/length-dependent picks (harmless until
  content-parity, since replay re-keys on `payload[0]`): `BtreeSplit(3)` → L(0x30)
  vs R(0x40) — goopg has one `RecordKindBtreeSplit`, so pick one (e.g. L) or derive
  from the body; `Checkpoint(2)` → shutdown(0x00) vs online(0x10) — already decided
  by the 88-byte length branch (`format.go:234-243`), which stays.
- **recovery:** a PG-style **`switch xl_rmid { … }`** dispatch (then on `xl_info`
  opcode within each rmgr). Real-PG rmgrs (`RM_HEAP`/`RM_HEAP2`/`RM_BTREE`/
  `RM_XACT`/`RM_XLOG`/`RM_SMGR`) route analog records; the goopg custom rmgr
  routes private records. Where one PG opcode covers several goopg `RecordKind`s
  (see §3.3), the rmgr handler further discriminates on the body — exactly as
  PG's `xact_redo` inspects `xinfo`. The `RecordKind` byte remains present as the
  body's leading byte (a within-handler discriminator), but is **no longer the
  top-level dispatch key**.
- **retire** `xlogInfoDefault=0xF0` as the emission catch-all.

Net: goopg replay is structurally PG-shaped (rmgr → opcode), the header
advertises real / PG-framework rmgr ids, and goopg↔goopg recovery works because
both ends share the mapping.

## 3. The `(xl_rmid, xl_info)` mapping (emitted records)

goopg `Rmgr` is `uint8` (`internal/wal/xlog_record.go:50`): `RmgrXLog=0`,
`RmgrXact=1`, `RmgrStorage=2`, `RmgrStandby=8`, **`RmgrHeap2=9`** (already
defined, `xlog_record.go:58`), `RmgrHeap=10`, `RmgrBtree=11`. **To add:**
`RmgrCLOG=3` (for `ClogTruncate`, §3.1), `RmgrGoopgCatalog=128` (§3.2), and the
HEAP2 **opcode** constants (`xlogHeap2PruneOnAccess=0x10` / `_VacuumScan=0x20` /
`_VacuumCleanup=0x30` — `pg_xlog_decode.go` currently has heap/xact opcodes but no
HEAP2 ones). PG opcodes cited from §5 of [03](03-pg183-wal-record-schemas.md).

> **Header-decode guard (BLOCKER, review):** `DecodeXLogRecordHeader`
> (`xlog_record.go:189-191`) rejects any `Rmid > MaxKnownRmgr` (=`RmgrBtree`=11,
> `xlog_record.go:68`) with `ErrInvalidRecordHeader`. Emitting `RmgrGoopgCatalog=128`
> (or `RmgrCLOG=3`, `RmgrStandby=8`) would fail header decode → recovery aborts on
> the first such record. The implementation **must** accept the `128..255` custom
> range (and any newly-used real rmid) in `DecodeXLogRecordHeader` — e.g. allow
> `RmgrGoopgCatalog` and PG's custom range explicitly, or raise `MaxKnownRmgr`. This
> guard has never fired because canonical only ever used rmids 1/2/9/10/11.

### 3.1 Records with a PG analog → real PG (rmid, info)

| goopg `RecordKind` (#) | rmid | info (opcode) |
| --- | --- | --- |
| `PageImage` (1) | `RM_XLOG` (0) | `XLOG_FPI` (0xB0) |
| `Checkpoint` (2) | `RM_XLOG` (0) | `XLOG_CHECKPOINT_SHUTDOWN` (0x00) / `_ONLINE` (0x10) — already classified by 88-byte length (`format.go:234-243`); keep |
| `BtreeSplit` (3) | `RM_BTREE` (11) | `XLOG_BTREE_SPLIT_L` (0x30) / `_R` (0x40) |
| `HeapInsert` (4) | `RM_HEAP` (10) | `XLOG_HEAP_INSERT` (0x00) |
| `BtreeInsert` (5) | `RM_BTREE` (11) | `XLOG_BTREE_INSERT_LEAF` (0x00) |
| `HeapDelete` (6) | `RM_HEAP` (10) | `XLOG_HEAP_DELETE` (0x10) |
| `HeapVacuum` (7) | `RM_HEAP2` (9) | `XLOG_HEAP2_PRUNE_VACUUM_SCAN` (0x20) |
| `XactCommit` (8) | `RM_XACT` (1) | `XLOG_XACT_COMMIT` (0x00) |
| `XactAbort` (9) | `RM_XACT` (1) | `XLOG_XACT_ABORT` (0x20) |
| `HeapLock` (10) | `RM_HEAP` (10) | `XLOG_HEAP_LOCK` (0x60) |
| `SmgrCreate` (11) | `RM_SMGR` (2) | `XLOG_SMGR_CREATE` (0x10) |
| `HeapHotUpdate` (13) | `RM_HEAP` (10) | `XLOG_HEAP_HOT_UPDATE` (0x40) |
| `HeapPruneOpt` (14) | `RM_HEAP2` (9) | `XLOG_HEAP2_PRUNE_ON_ACCESS` (0x10) |
| `BtreeVacuum` (22) | `RM_BTREE` (11) | `XLOG_BTREE_VACUUM` (0xC0) |
| `BtreeUnlinkPage` (23) | `RM_BTREE` (11) | `XLOG_BTREE_UNLINK_PAGE` (0x80) |
| `BtreeNewRoot` (24) | `RM_BTREE` (11) | `XLOG_BTREE_NEWROOT` (0xA0) |
| `BtreeMarkPageHalfDead` (25) | `RM_BTREE` (11) | `XLOG_BTREE_MARK_PAGE_HALFDEAD` (0xB0) |
| `HeapFreeze` (26) | `RM_HEAP2` (9) | `XLOG_HEAP2_PRUNE_VACUUM_CLEANUP` (0x30) |
| `XactCommitInval` (32) | `RM_XACT` (1) | `XLOG_XACT_COMMIT` (0x00) — body-discriminated from `XactCommit`, §3.3 |
| `ClogTruncate` (33) | `RM_CLOG` (3) | `CLOG_TRUNCATE` (0x10) — emitted at `open.go:1653` (`EncodeClogTruncate`, encoder `recovery.go:7402`); needs a `RmgrCLOG=3` const |
| segment pad | `RM_XLOG` (0) | `XLOG_NOOP` (0x20) — already genuine (`segment_pad.go`); keep |

### 3.2 goopg-private records (no PG analog) → custom rmgr

All remaining emitted `RecordKind`s (beyond the §3.1 analog set — note
`ClogTruncate` moved to §3.1) are **goopg-private catalog/DDL** records
(Database 18/19/73-75, Index 20/21/94, Schema 34/35, Sequence 65/66, Role
67/68/72/76-80, Function 61-64/121-123, Publication/Subscription 50-55, Cast/
Conversion/Transform/Collation 36-45/93, Aggregate 46-49, EventTrigger 56-60,
Type/Domain/Operator/AM families 70/71/81-92/117-120, Tablespace 124/125,
ForeignServer/UserMapping 126-129, Views 102/103, TextSearch 104-116,
ColumnDefaults 69). PostgreSQL journals catalog changes as heap-tuple ops, so
these have **no PG record type**.

**Decision:** one goopg **custom resource manager**, `RmgrGoopgCatalog = 128`
(`RM_MIN_CUSTOM_ID`). All goopg-private records classify as `(128, info)`; the
per-record `RecordKind` in the body is the final discriminator inside the custom
rmgr's replay handler. This is PG-legitimate (PG's custom-rmgr framework) and
honest — a stock PG **skips** unknown custom rmgrs rather than mis-redoing a
non-PG body, and it avoids stamping a *misleading* real-PG opcode on a non-PG
record.

- `xl_info` for custom-rmgr records: the high nibble (`XLR_RMGR_INFO_MASK 0xF0`)
  is free for a coarse category; the low nibble is reserved for `XLR_*` bits. Since
  `xl_info`'s 16 opcodes can't enumerate ~110 kinds, the **body `RecordKind` byte
  remains the authoritative discriminator** within the custom handler. (Open
  option for review: subdivide into a few custom rmgrs by category, e.g.
  128=catalog-DDL, 129=role/acl, 130=textsearch — not required for correctness.)

### 3.3 Opcode collisions (one PG opcode ⇒ several goopg kinds)

Where the mapping is many-to-one, the rmgr handler discriminates on the body
(the leading `RecordKind` byte), mirroring PG (which distinguishes commit
variants by `xinfo`, not opcode):
- `XactCommit` (8) **and** `XactCommitInval` (32) → `RM_XACT`/`XLOG_XACT_COMMIT`.
- `HeapVacuum`/`HeapPruneOpt`/`HeapFreeze` map to **distinct** HEAP2 prune
  opcodes (0x20/0x10/0x30) — no collision (PG likewise uses separate opcodes
  "just for debugging", `heapam_xlog.h:52-58`).

## 4. Recovery dispatch rework

**Critical (BLOCKER 2, review):** the native mutations are applied only by the
`replayHeapInsert`/`replayHeapDelete`/`replayBtreeSplit`/… functions (the
`payload[0]` switch inside `ApplyRecord`, `recovery.go:8756+`). The FPI-only
`RmgrHeap`/`RmgrBtree` arms of `replayDecodedXLogRecord` (`:9268-9299`) **cannot**
replay a native body. So the rework must route real-rmid native records to the
**native `replayX` functions**, not to the FPI arms — otherwise every heap/btree
mutation is silently dropped on recovery.

Design: make `replayDecodedXLogRecord` the single PG-style dispatch and have every
goopg-owned rmgr arm delegate to the existing native `payload[0]` switch:

```
switch xlog.Header.Rmid {
case RmgrHeap, RmgrHeap2, RmgrBtree, RmgrXact, RmgrStorage, RmgrCLOG,   // analog
     RmgrGoopgCatalog(128):                                            // private
    // goopg-owned record: apply the native body via the existing payload[0]
    // switch (the replayHeap*/replayBtree*/replay<DDL>* functions moved out of
    // the old 0xF0-gated ApplyRecord). Where one (rmid,info) covers >1 kind
    // (§3.3: XactCommit vs XactCommitInval), the payload[0] value disambiguates.
case RmgrXLog:
    switch info { checkpoint (88-byte) / XLOG_FPI / XLOG_NOOP / … } // real handlers
}
```

- **Replace** (do not keep) the FPI-only `RmgrHeap`/`RmgrBtree` arms at
  `:9268-9299` with delegation to the native `replayX` functions.
- Retire the `Rmid==RmgrXLog && Info==xlogInfoDefault` gates (`:8745`) and the
  `nativeApplyRecordKindKnown` allow-list gate (`:8735`, `:9191`) as the *routing*
  mechanism — the `payload[0]` switch body (`:8756+`) is **reused** (it is the
  real replay logic), now reached through the rmgr arms above rather than the 0xF0
  gate. Keep the empty-payload guard (`:8732`).
- Real-PG `RmgrXLog` arms (checkpoint; FPI) stay reachable via their rmgr/info.
- Update `stream_replayer.go:159` (`replayedXactInfo`) to the new rmid scheme and
  to also recognize `XactCommitInval(32)` on the `RmgrXact` path (§2.1).
- **Old-segment / backward-compat (fresh clusters assumed):** existing pgcompat
  data dirs wrote *all* records as `RM_XLOG/0xF0`. Since fresh clusters are assumed,
  replaying those under the new dispatch is **not required**; a data-dir re-init is
  expected for this format change (as with the perf-optimize3-dash native-only
  default). Because the reused `payload[0]` switch is rmid-agnostic, a **legacy
  `RM_XLOG/0xF0` arm** that delegates to the same `payload[0]` switch is a
  near-free safety net for old dirs (recommended); new emission never produces
  `0xF0`. PG-frame decode is retained regardless.

## 5. Removal inventory (canonical + knob)

### 5.1 Fully-removed files
- `internal/catalog/canonical.go` (all `PgCanonical*`/`BuildCanonical*`,
  `LogCanonicalFunc`, `RecordKindCanonical`, envelope + `buildCanonicalSingleFPIBody`).
- `internal/wal/parameter_change.go` — **but first relocate** the non-canonical
  `GUCParameters` + `DefaultGUCParameters` (used by the checkpointer,
  `CheckpointerConfig.GUCParams` `checkpointer.go:100`; set via
  `wal.DefaultGUCParameters()` at `open.go:1698`) into a neutral home (e.g.
  `checkpointer.go`). Its emit call `ReportParameters` (`open.go:2235-2242`) and
  the `0xFE` `XLOG_PARAMETER_CHANGE` record go away (the content rewrite re-adds a
  real one later; verify nothing at startup depends on the `pg_control` GUC echo —
  R5).
- Pure-canonical tests: `internal/catalog/canonical_test.go`,
  `internal/wal/canonical_heap_roundtrip_test.go`, `parameter_change_test.go`,
  `internal/initdb/emit_canonical_switch_test.go`,
  `canonical_coverage_guard_test.go`, `native_only_audit_test.go`,
  `internal/server/copy_canonical_encoding_test.go`,
  `internal/testport/canonical_skip_test.go`.

### 5.2 Call sites to unwire (keep surrounding native writes)
- Executor emitters `emitCanonicalHeap{Insert,HotUpdate,PruneLocked,Delete}`
  (`operators_storage.go:8345-8455`) + their `if …LogCanonical != nil` sites
  (2103, 2155, 3355, 3432, 4285-4291, 5030-5034, 5633, 6174-6179, 6396, 7806).
- Direct `catalog.PgCanonical*` sites: `sys_catalog_index_insert.go:298`,
  `sys_catalog_btree_split.go:409` (416/424/432/440),
  `sys_catalog_btree_multilevel.go:366/447`,
  `operators_vacuum_datfrozenxid.go:131`, `internal/vacuum/vacuum.go:164`, and the
  canonical tail of `writeHeapRowCanonical` (`operators_ddl.go:13443+`) — keep
  `writeHeapRowReturningPG` and callers.
- Xact canonical append `open.go:1016-1034` + `pgEpoch2000`/`pgTimestampNowUsec`.
- `LogCanonical` plumbing: `executor/context.go:391/395`, `operators_vacuum.go:77`,
  `vacuum.go:53-60`, `open.go:76/2222/2250`, `server/server.go:236`, the
  `ectx.LogCanonical=…` sites (`dispatch_extended.go:233`, `database_ddl.go:1119`,
  `copy.go:189`, `dispatch.go:497`), `cmd/goopg/main.go:602`.
- `format.go` `0xFE` branches (`wrapXLogMainData:162-164`,
  `classifyXLogRecord:226-229`); `recovery.go` canonical arm (`9150`) +
  `RecordKindCanonical` (1387).

### 5.3 Knob deletion
- `emitCanonicalDefault()` (`open.go:35-56`, reads `GOOPG_WAL_CANONICAL`),
  `Config.EmitCanonical` + `emitCanonical` field + `CanonicalEnabled()`
  (`writer.go:131-141/346-350/581/634-637`), `walCfg.EmitCanonical` (`open.go:427`),
  the `!CanonicalEnabled()` warnings (`open.go:446-454`, `basebackup.go:171-186`).
  No GUC / `postgresql.conf.sample` entry exists (confirmed).

### 5.4 Additive edits (the PG-dispatch rework)
- **Landed 2026-07-15:** `internal/wal/xlog_record.go`: added `RmgrCLOG=3`,
  `RmgrGoopgCustomBase=128`, `RmgrGoopgCatalog=128`; `DecodeXLogRecordHeader`
  now accepts the `128..255` custom range (`Rmid > MaxKnownRmgr && Rmid <
  RmgrGoopgCustomBase` is the new reject condition, was `Rmid >
  MaxKnownRmgr`). Verified inert — nothing yet emits `Rmid=3` or `Rmid>=128`.
  `TestDecodeAcceptsGoopgCustomRmgrRange` pins the new boundary.
  ~~`internal/wal/xlog_record.go`: add `RmgrCLOG=3`, `RmgrGoopgCatalog=128`;
  **accept the custom range in `DecodeXLogRecordHeader` (`:189-191`)** — the
  `Rmid > MaxKnownRmgr(=11)` reject (`:68`) currently blocks 128 (and 3/8). Allow
  the new rmids / the `128..255` custom range.~~
- **Landed 2026-07-15:** `internal/wal/pg_xlog_decode.go`: added
  `xlogHeap2PruneOnAccess=0x10`, `xlogHeap2PruneVacuumScan=0x20`,
  `xlogHeap2PruneVacuumClean=0x30` (RM_HEAP2_ID opcodes, confirmed against
  `postgres/src/include/access/heapam_xlog.h:60-62`) — the 3 opcodes §3's
  mapping table cites. Verified inert (no other reference to the new names
  in the tree). No other HEAP2 opcodes needed by the mapping (REWRITE/
  VISIBLE/MULTI_INSERT/LOCK_UPDATED/NEW_CID are not used by any RecordKind
  row in §3).
  ~~`internal/wal/pg_xlog_decode.go`: add HEAP2 opcode consts
  (`0x10/0x20/0x30`) and any others used by the mapping.~~
- **Landed 2026-07-15:** `internal/wal/format.go`: `recordKindToRmgrInfo(kind
  byte) (Rmgr, uint8)` — the full §3.1 table (every PG-analog `RecordKind`)
  plus the §3.2 default (`RmgrGoopgCatalog`, info 0) for every other kind.
  Needed opcode consts added to `internal/wal/pg_xlog_decode.go`
  (`xlogHeapLock=0x60`, `xlogBtreeInsertLeaf=0x00`/`SplitL=0x30`/`SplitR=0x40`
  (R unused, goopg has one `BtreeSplit` kind — kept for completeness)/
  `UnlinkPage=0x80`/`NewRoot=0xA0`/`MarkPageHalfDead=0xB0`/`Vacuum=0xC0`,
  `xlogSmgrCreate=0x10`, `xlogXLogFPI=0xB0`, `xlogClogTruncate=0x10` —
  confirmed against `postgres/src/include/access/{heapam_xlog,nbtxlog}.h`,
  `catalog/{storage_xlog,pg_control}.h`, `access/clog.h`).
  `TestRecordKindToRmgrInfoAnalogTable`/`…CustomDefault` pin every §3.1 row +
  the §3.2 fallback. **Not yet wired into `classifyXLogRecord`** — see the
  next bullet's "found while building this" notes; `classifyXLogRecord`
  still returns the old `RmgrXLog`/`xlogInfoDefault` catch-all
  (`TestRecordKindToRmgrInfoNotYetWired` pins this and is designed to break,
  loudly, the day the wiring lands — delete it then). Verified inert
  (`recordKindToRmgrInfo` has no non-test caller); `go build ./...`/`go vet
  ./...` clean; `go test`/`go test -race ./internal/wal/...` full-package
  green; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
  `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
  failed, all 3 workloads).
  ~~`internal/wal/format.go`: `recordKindToRmgrInfo` mapping table + rewrite
  `classifyXLogRecord` to use it; retire `xlogInfoDefault` as the catch-all.~~
- **Landed 2026-07-15 (this bullet's trace + the §4 dispatch rework below,
  in one atomic commit, per R1):** `classifyXLogRecord`'s native-record
  catch-all now calls `recordKindToRmgrInfo`; all 5 coupling points traced
  below were fixed in `recovery.go` in the same change, plus a 6th one this
  trace missed — `IsGoopgNativeRecord` (used by ~20
  `internal/initdb/*_ddl_recovery.go` restart scanners to gate trusting
  `Payload[0]`) still checked only the legacy `RmgrXLog+xlogInfoDefault`
  catch-all; after wiring, catalog/DDL records classify to
  `RmgrGoopgCatalog` instead, so every scanner would have silently stopped
  re-populating the catalog registry from WAL after a restart. Fixed by
  delegating to the same `isGoopgOwnedRmgr` (plus the legacy 0xF0 catch-all
  for old data dirs). Also fixed `internal/initdb/emit_canonical_switch_test.go`'s
  `countCanonicalRecords`, whose "rmid≠RmgrXLog ⇒ canonical" detection
  heuristic went stale the moment native records started classifying into
  real per-kind rmgrs; the correct signal is `len(r.Payload)==0` (a
  canonical envelope's inner body never round-trips back through
  `classifyXLogRecord` to its own header, so `Payload` stays nil regardless
  of whether it carries an FPI block ref — ruling out a `len(Blocks)>0`
  heuristic, since the canonical xact-commit/abort markers carry none).
  Gates: full G-crash + `TestKillKillRecovery` before/after (per R1), full
  `./internal/initdb/...` + `./internal/wal/...` + `testutil/{cluster,
  pgcluster,pubsubcluster,replcluster}`, `tpch-spotcheck.sh`, and
  `RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh` all green. Two
  deferrals recorded in `.ralph/deferral_ledger.md` (2026-07-15 row): the
  optional legacy `RM_XLOG/0xF0` backward-compat arm in `ApplyRecord` (§4's
  own "recommended, not required" note) and real external block-carrying
  `XLOG_FPI` restore (out of scope per §6). The stale
  `TestRecordKindToRmgrInfoNotYetWired` test (designed to break the day
  this landed) was deleted and replaced with
  `TestClassifyXLogRecordWiredToRecordKindToRmgrInfo`.

  Original trace (kept for the record) — wiring `recordKindToRmgrInfo` into
  `classifyXLogRecord` alone, without the §4 recovery rework in the *same*
  change, would have broken goopg's own crash recovery immediately,
  confirmed by tracing three concrete paths:
  1. `ApplyRecord`'s existing gate (`ApplyRecord`, `ApplyRecord` calls this
     "M0106-0011" comment) — `if r.XLog.Header.Rmid != RmgrXLog ||
     r.XLog.Header.Info != xlogInfoDefault { return
     replayDecodedXLogRecord(mgr, r) }` — assumes every native record
     classifies `RmgrXLog`/`0xF0`. Once real per-kind rmids are live, this
     must become `if !isGoopgOwnedRmgr(r.XLog.Header.Rmid) { return
     replayDecodedXLogRecord(mgr, r) }` (`isGoopgOwnedRmgr` = new helper:
     `RmgrHeap`/`RmgrHeap2`/`RmgrBtree`/`RmgrXact`/`RmgrStorage`/`RmgrCLOG`/
     `RmgrGoopgCatalog`).
  2. `decodeRecordXLogDetailed`'s `nativeHeaderMatchesMainData`
     (`pg_xlog_decode.go:241-247`) already calls `classifyXLogRecord`
     symmetrically at decode time to decide whether to populate
     `decoded.Payload` from `xlogRecord.MainData` — so **no decode-side
     change is needed**: every no-block-refs native record (all of them,
     today) still round-trips its payload correctly once classify's output
     changes, because encode and decode call the same function. This was
     the biggest open question going in and it resolves for free.
  3. `replayDecodedXLogRecord`'s `default: return false,
     unsupportedDecodedXLogRecord(r)` arm would **error out** (not silently
     drop — worse, it aborts recovery) for any of the ~80 catalog/DDL
     `RecordKind`s not in `nativeApplyRecordKindKnown`'s allow-list (e.g.
     `RecordKindCreateTransform`), since those hit `ApplyRecord`'s
     `!nativeApplyRecordKindKnown` gate *before* the rmid gate and land
     here with `Rmid=RmgrGoopgCatalog`. Needs one new blanket case: `case
     RmgrGoopgCatalog: return false, nil` (matches every one of
     `ApplyRecord`'s own no-op cases for these kinds — the recovery driver
     in `internal/initdb/*_ddl_recovery.go` re-applies them separately).
  4. **`RecordKindPageImage` genuinely collides with real PG semantics** —
     it maps to `RmgrXLog`/`XLOG_FPI` (§3.1), so with only fix (1) above it
     would route to `replayDecodedXLogRecord`'s `RmgrXLog` arm, whose
     `default: return false, nil` (post parameter-change) would **silently
     no-op** a real page-image restore that `replayPageImage(mgr,
     r.Payload)` currently performs via the native switch — this is the R1
     "silently dropped" failure mode materializing concretely, not
     hypothetically. Needs a new `case xlogXLogFPI:` arm under `RmgrXLog`
     in `replayDecodedXLogRecord` delegating to `replayPageImage(mgr,
     r.Payload)` (payload is populated per point 2 above — no blocks).
  5. Canonical (`0xFE`) FPI records are unaffected by any of the above: they
     always carry block refs, so `nativeHeaderMatchesMainData`'s
     `len(xlogRecord.Blocks)==0` guard leaves `decoded.Payload` nil for
     them regardless of rmid, and they hit `ApplyRecord`'s *first* gate
     (`len(r.Payload)==0`) before the rmid check ever runs. The existing
     `RmgrHeap`/`RmgrBtree` FPI arms (`recovery.go:9268-9289`) **must stay
     exactly as-is** for this slice — do **not** "replace" them as the
     original §4 draft pseudocode suggested; canonical emission (§5.1-5.3)
     hasn't been removed yet, and several active call sites (§5.2) still
     depend on FPI replay for canonical btree-split/index-insert records.
     Revisit only after §5.1-5.3 lands.
- **Landed 2026-07-15:** `internal/wal/recovery.go` gained the §4 dispatch
  rework per the 5 points above (plus the `IsGoopgNativeRecord` 6th point
  noted above) — `isGoopgOwnedRmgr`, `ApplyRecord`'s rewritten rmid gate,
  the `RmgrGoopgCatalog` no-op case and the `RmgrXLog`/`XLOG_FPI` case
  (guarded on `len(r.Payload)==0` to no-op rather than error on a genuine
  external block-carrying FPI) in `replayDecodedXLogRecord`; the FPI
  `RmgrHeap`/`RmgrBtree` arms were left untouched as directed.
  ~~`internal/wal/recovery.go`: apply the §4 dispatch rework per the 5
  points above — add `isGoopgOwnedRmgr`, patch `ApplyRecord`'s rmid gate,
  add the `RmgrGoopgCatalog` no-op case and the `RmgrXLog`/`XLOG_FPI` case
  to `replayDecodedXLogRecord`, leave the FPI `RmgrHeap`/`RmgrBtree` arms
  untouched. Land together with the `classifyXLogRecord` wiring above in one
  atomic change (not split across loops) — full G-crash (`go test -run
  'Crash|Recovery|Durability' ./internal/initdb/ ./internal/wal/` +
  `TestKillKillRecovery`) before/after, per §8 R1.~~
- **Landed 2026-07-15:** `internal/wal/stream_replayer.go`'s
  `replayedXactInfo` gained a `case RecordKindXactCommitInval:` beside
  `RecordKindXactCommit` (same `DecodeXactMarker` call).
  ~~`internal/wal/stream_replayer.go:159` (`replayedXactInfo`): mostly
  unaffected — its primary path already keys on `rec.Payload[0]` (populated
  regardless of rmid per point 2 above), so `RecordKindXactCommit`/`Abort`
  need no change. Only gap: add a `case RecordKindXactCommitInval:` beside
  the existing `XactCommit`/`XactAbort` cases (same `DecodeXactMarker` call)
  — today it falls through to the `Rmid==RmgrXact` fallback, which reads
  `rec.XLog.Header.XID`, and `classifyXLogRecord`/`encodeRecordXLog` still
  hard-code `xid=0` for every non-canonical record (a separate, pre-existing
  gap — the real xid lives only in the payload body, decoded via
  `DecodeXactMarker`), so the fallback would silently report xid 0 for every
  replayed `XactCommitInval` without this fix. Update to the new
  rmid scheme; also recognize `XactCommitInval(32)` on the `RmgrXact` path.~~

## 6. Test / CI / nightly impact (expected-fail handling)

Consistent with the repo's idioms (env-gated `t.Skip`, `expected-failures.csv`,
port-status CSV `port→defer`, ledger rows):

- **Convert to unconditional `t.Skip`** (message: *"PG-tool WAL compat removed
  2026-07-15; intentional, not a regression — see .ralph/deferral_ledger.md; resumes
  after native→PG content rewrite"*): the env-gated PG-consumer tests
  `TestE2E_FailoverGoopgToPG`, `TestE2E_ChecksumStreamingGoopgToPG`,
  `TestPort_PgWaldump002SaveFullpage`, `TestPort_PgWaldumpVacuumPruneRoundtrip`.
- **Structural pg_waldump tests now failing** (real rmids over native bodies):
  `TestPort_WALPgWaldumpCompat` (**W-001**), `TestPGWaldumpParsesEmittedWAL` —
  mark expected-fail; flip `docs/test-port/postgres-oracle-port-status.csv` W-001
  `port→defer`; add rows to `ci/batch/expected-failures.csv`.
- **Nightly batch:** remove the `GOOPG_WAL_CANONICAL=on` canonical-on lane from
  `ci/batch/stages/stage-testport.sh:27-47` (the `-run` list at `:34` + the `exit`
  gate), so nightly `testport`/`units` stop raising these as `[regression]` action
  items (`ci/logs/action-items.md` → M-NIGHTLY).
- **`.ralph/deferral_ledger.md`:** add a `resolved` row (canonical/knob/skip-tag
  removed; PG-style `(rmid,info)` dispatch added; resume = after the native→PG
  content rewrite in `docs/design/wal-native-pg-format/`). Supersede the
  perf-optimize3-dash S4 rows (756/757) that promised the canonical-on lane keeps
  the resume path alive.

## 7. Verification & gates
- `go build ./... && go vet ./...` clean; grep-audit shows no `GOOPG_WAL_CANONICAL`,
  `EmitCanonical`, `CanonicalEnabled`, `LogCanonical`, `PgCanonical`,
  `RecordKindCanonical`, `xlogInfoDefault` (except any retained legacy-decode arm).
- **G-crash (make-or-break):** `go test -run 'Crash|Recovery|Durability'
  ./internal/initdb/ ./internal/wal/` + `TestKillKillRecovery`.
- **goopg↔goopg (must stay GREEN):** `TestE2E_NativeOnlyReplicationAndPromotion`,
  `TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`.
- **G-race** on `./internal/wal/ ./internal/executor/ ./internal/catalog/`;
  **G-unit** + pgbench smoke; **G-tpch** `scripts/tpch-spotcheck.sh`.
- New unit test: a native-only round-trip asserting **every emitted `(rmid,info)`
  replays to the correct handler** (guards R1).

## 8. Risks
- **R1 (critical):** incomplete `(rmid,info)` mapping or a missed replay site
  breaks goopg recovery. Land §4 last, incrementally; full G-crash before/after;
  the round-trip test in §7 must cover every emitted kind. Two review-found traps
  now folded in: (i) `DecodeXLogRecordHeader` rejects rmid>11 — must accept the
  custom range; (ii) native heap/btree bodies must reach the native `replayX`
  functions, **not** the FPI-only decoded arms (`recovery.go:9268-9299`), or
  mutations are silently dropped.
- **R2:** custom-rmgr opcode/id allocation for the ~110 no-analog kinds — the body
  `RecordKind` byte stays authoritative; document the allocation.
- **R3:** Ralph-loop concurrency on the same files — pause the loop or use a
  worktree before code edits. **Found 2026-07-15:** a stale, uncommitted
  worktree at `.claude/worktrees/wal-canonical-removal/` (branch
  `wal-canonical-removal`) is checked out at `e9884a60` — this repo's HEAD
  *before* the two "additive-first" slices (`1e275ea6`/`67117f9a`) landed —
  and stages the **opposite** ordering: it deletes `canonical.go`/its tests
  outright (subtractive-first) rather than following the additive-first
  sequencing this doc's §5.4 has used since. It predates the additive-first
  decision and was never referenced from `fix_plan.md`/the deferral ledger,
  so it is orphaned WIP from an earlier abandoned attempt, not active work —
  do **not** resume or merge it as-is; its removal ordering has to be
  re-derived against the current (later, more detailed) §3/§4/§5 content
  before any of it is reusable. Left untouched (not deleted) pending a
  human decision on whether to salvage or discard it.
- **R4:** old pgcompat data dirs (all `RM_XLOG/0xF0`) — fresh clusters assumed;
  retain the legacy decode arm (§4) as a cheap safety net or require re-init.
- **R5:** dropping `XLOG_PARAMETER_CHANGE` removes the `pg_control` GUC echo — verify
  no startup dependency.

## 9. Rollback
The change is additive-then-subtractive on one branch; revert the implementation
commits to restore canonical + knob + `0xF0` tagging. The frame is unchanged, so
no data-dir migration is needed to roll back (fresh clusters either way).
