# Implementation TODO — PG-identical WAL stream

Fine-grained execution tracker for the design bundle in this directory
([README](README.md), [01 Section A](01-record-content-parity.md),
[02 Section B](02-catalog-heap-journaling.md)). Mark `[x]` as each item lands green + committed.

**Sequencing: Part A first, then Part B.** Section B's catalog DML emits the same
`XLOG_HEAP_*` / `XLOG_BTREE_*` / `XLOG_XACT_COMMIT` records Part A makes byte-correct, so B rides on
already-correct bodies.

**Central discipline (Part A, doc 01 §3):** every record after A0 is an *atomic encode↔replay↔classifier
flip* — rewrite `Encode*`→PG body (block refs via A0's assembler), delete the native `Decode*`/`replay*`,
add a per-rmgr handler to `replayDecodedXLogRecord`, update pgoutput `classifier.go`. Never land a PG body
without its replay (a half-flip silently breaks recovery).

**Constraints:** implemented on branch `wal-pg-stream-impl` (worktree, off Ralph's baseline `344470fe`);
no `gofmt -w` (go1.25/1.26 mismatch); re-init data dirs after on-disk format changes; commits end with the
`Co-Authored-By: Claude Opus 4.8 (1M context)` trailer.

---

## Phase 0 — Setup
- [x] 0.1 Stop the Ralph loop (respawner-first kill order)
- [x] 0.2 Stash Ralph's main-tree WIP (SHA `8d8a32da`, tag `ralph-wip-pause-walpgstream-7d204969`) — restore at wrap
- [x] 0.3 Create this tracker

## Phase A — Record content parity (doc 01)
- [x] **A0** Block-ref/FPI encoder — `internal/wal/xlog_assemble.go`: `BlockRef`, `FullPageImage`,
  `assembleXLogRecord(mainData, blocks)` with pd_lower/pd_upper hole detection; round-trip test vs
  `pg_xlog_decode.go` (`xlog_assemble_test.go`, 6 cases). *Keystone; additive (flips no record).*
  Gate green: `go build ./...`, `go vet`, `go test`, `go test -race ./internal/wal/`.
  (Dropped the doc's illustrative `xid` param — it's a header field, threaded in A1 — and the
  caller-set `ForkFlags`/hole fields — derived from block contents / page header instead.)
- [ ] **A1** `xl_xid` threading through `Append`/`appendPGCompat`/`encodeRecordXLog`; stamp live xid at emit sites.
  > ⚠️ **Not standalone-additive (found 2026-07-15).** `nativeHeaderMatchesMainData` (`pg_xlog_decode.go:276`)
  > gates the native-replay fast-path on `header.XID == classifyXLogRecord(...)`, which returns **0**.
  > Stamping a *real* xid into the header while a record body is still native fails that check →
  > `decoded.Payload` goes nil → the record routes to FPI-only `replayDecodedXLogRecord` → silent recovery
  > corruption. So xid can only be stamped for records *already flipped* to PG bodies (blocks>0 bypasses the
  > fast-path). **⇒ Fold A1 into A2** (do the xid-stamp per record as it flips). The API plumbing exists
  > across **145 `.Append(` call sites** — thread it, but pass the live xid only from flipped emit sites; all
  > others keep 0 until they flip. Retire `nativeHeaderMatchesMainData` when the last native record is gone.
- [x] **A2-pre** t_ctid convention change (**prerequisite**, found 2026-07-15): PG `xl_heap_insert` carries no
  t_ctid; replay reconstructs self-pointing `{block,offnum}`. But goopg stores `{InvalidBlockNumber,0}` for
  fresh inserts and MVCC sites read that (`isChainTailCTID` handles both; `operators_fk.go:46`,
  `operators_lockrows.go:1967`, `operators_storage.go:255` epqSerializationErr, prune/visibility check
  `{Invalid,0}` specifically). Adopt PG self-pointing t_ctid on the insert path (stamp `{blk,lineSlot}` after
  `PageAddHeapTuple`) + route all convention consumers through a shared "no-successor = {Invalid,0} OR self"
  predicate. Gate: **full regress + `internal/testport` isolation + -race** (MVCC blast radius). Landed
  independently before the WAL flip. *(User-chosen 2026-07-15: do the full change now.)*
- [x] **A2** HeapInsert flip (rides on A2-pre) — **LANDED**. `xl_heap_insert{offnum,flags}` + blk0
  `xl_heap_header`+tuple, xl_xid=t_xmin. Null-safe `decodeXLogHeapInsertTuple` (verbatim concat). Live wiring:
  `logHeapInsert`→`EncodeHeapInsertPG` (open.go); `classifier.go` decoded-path (`classifyDecodedXLog`). Native
  `EncodeHeapInsert`/`replayHeapInsert`/ApplyRecord case left as dead fallback (retire later). **Gates all
  green:** wal unit+`-race`, executor, **initdb crash-recovery**, e2e **native replication+promotion**,
  **physical**, **logical** (classifier). Separate FPI kept (unification deferred). (doc 01 §5/§6)
- [x] **A3** HeapDelete flip — **LANDED**. `xl_heap_delete{xmax,offnum,infobits_set=0,flags}` + block-0 page
  ref + optional old tuple (logical). Built the net-new decoded replay (`replayDecodedXLogHeapDelete` reuses
  `PageSetHeapTupleXmax` = native parity; split the FPI-only dispatch). Live: `logHeapDelete`→`EncodeHeapDeletePG`;
  classifier decoded delete-path (reconstructs old tuple). **Gates:** wal+`-race`, executor, initdb
  crash-recovery (234s), e2e native/physical/logical repl, **full isolation + regress**. infobits_set=0 (no
  HEAP_KEYS_UPDATED — native delta) and ALL_VISIBLE_CLEARED not set = known PG-standby parity gaps.
- [x] **A4** HeapHotUpdate flip — **LANDED (HOT path)**. `xl_heap_update` (HOT opcode 0x40): main-data
  {old_xmax,old_offnum,old_infobits=0,flags=CONTAINS_NEW_TUPLE,new_xmax=0,new_offnum} + block-0 new tuple
  (same page; no block 1; prefix/suffix skipped). Built decoded `replayDecodedXLogHeapUpdate`
  (PageAddHeapTuple new + PageStampHotOldTuple old). Threaded `new_offnum` from the executor; extended the
  A2-pre self-`t_ctid` stamp to `markHeapHotUpdateDirty` (HOT new version). classifier decoded update-path.
  **Gates:** wal+`-race`, executor, server, initdb crash-recovery (228s), e2e native/physical/logical repl,
  full isolation (485s) + regress (288s). **Non-HOT update DEFERRED**: it already emits a PG-format
  Delete+Insert pair (A2/A3), not a single `xl_heap_update` — single-record conversion is an executor
  restructure, left as a parity gap (`RecordKindHeapUpdate`=27 is dead code).
- [x] **A5** BtreeInsert flip — **LANDED**. `xl_btree_insert{offnum=0}` + blk0 IndexTuple (as block data).
  Built decoded `replayDecodedXLogBtreeInsert` (reuses `btree.ApplyInsertRecord` = re-insert by key = native
  parity; FPI fallback); RmgrBtree dispatch now switches on opcode (INSERT_LEAF → new handler, split/newroot/…
  stay FPI). Live: `logBtreeInsert`→`EncodeBtreeInsertPG`. No classifier (index changes aren't logical
  user-data). **Gates:** wal+`-race`, executor, initdb crash-recovery (227s), e2e native/physical/logical
  repl, full isolation + regress. offnum=0 is a documented PG-standby parity gap (goopg replays by key).
- [x] **A6** XactCommit / Abort / CommitInval flip — **LANDED**. `xl_xact_commit`/`xl_xact_abort`
  (xact_time=0; **xid in the header xl_xid**, not the body). CommitInval → HAS_INFO + xinfo{HAS_INVALS} +
  empty invals array; decoded RmgrXact replay unlinks standby init files on HAS_INVALS (replaces native
  RecordKindXactCommitInval redo). No block ref → routes to decoded path (header.XID≠0 vs classifyXLogRecord's
  0). Most consumers were already header-ready (initdb xact-recovery CLOG stamp, stream_replayer); the one gap
  was `classifyDecodedXLog` (added RmgrXact branch → ApplyCommit/ApplyAbort by xl_xid). Fixed
  `wal_durability_test.go` (header-based commit detection). **Gates:** wal+`-race`(*), initdb crash-recovery
  (commit visibility), executor, server, e2e ×3, isolation+regress. *(-race: full-package flake in the
  pre-existing `TestDrainSafetyStress` WAL-drain concurrency test — passes isolated 3/3; A6 adds no
  concurrency.)* Deferred: xact_time, real subxact/inval arrays (not available at the commit emitter).
- [ ] **A2–A6 cross-cutting**: FPI↔logical unification (doc 01 §5); `predictXLogRecordLen` assembled-length
  fix; audit record-count / LSN-delta consumers (stream replayer, recovery-pass WAL-decode memoization,
  FPI-count test assertions).
- [ ] **A7** heap2 composite — fold HeapPruneOpt/HeapVacuum/HeapFreeze into `xl_heap_prune` (`XLHP_*`).
- [ ] **A8** btree structural — Split, NewRoot, Vacuum, UnlinkPage(36B), MarkPageHalfDead.
- [ ] **A9** smgr/clog/standalone-FPI/checkpoint-opcode/xact chunks; retire legacy native frame
  (`encodeRecord`/`decodeRecord`).
- [ ] **A-gate** Phase-A exit: `pg_waldump` structural+rmgr green for all §A records; goopg↔goopg standby +
  G-crash green; real PG 18 standby replays goopg WAL (`TestE2E_FailoverGoopgToPG`); byte-diff a segment vs PG.

## Phase B — Catalog heap journaling (doc 02, after Phase A)
- [ ] **B0** Enabler: generalize `loadUserTablesFromHeap`→per-catalog reload; wire catalog `XLOG_HEAP_UPDATE`;
  bootstrap base-catalog indexes in every DB; net-new `pg_filenode.map` writer + `XLOG_RELMAP_UPDATE` encoder.
- [ ] **B1** pg_namespace · pg_proc · pg_sequence (heap-write + index maint + write-through cache + generic
  heap-scan reload; delete bespoke RecordKind + `*_ddl_recovery.go` scanner + `VirtualRows` builder).
- [ ] **B2** type/operator families (pg_type/enum/range, pg_operator, pg_opclass/opfamily/amop/amproc,
  pg_cast, pg_conversion, pg_collation, pg_aggregate).
- [ ] **B3** extension/config (pg_ts_*, pg_transform, pg_event_trigger, pg_publication*/subscription*,
  pg_statistic_ext, pg_constraint/attrdef/depend).
- [ ] **B4** shared catalogs in `global/` (pg_database, pg_authid/auth_members, pg_tablespace,
  pg_foreign_*/user_mapping) + retire the postgres-DB mirror shim.
- [ ] **B5** Retire `RmgrGoopgCatalog=128` (now unused) — header-side parity complete.
- [ ] **B-gate**: per-catalog full regress + `internal/testport` isolation; `psql \d`/`\df`/`\dn` +
  `information_schema` parity vs PG 18.3; crash-after-DDL recovery via generic reload; re-init data dir.

---

## Log
- 2026-07-15: Phase 0 complete (Ralph paused, WIP stashed `8d8a32da`, tracker created). Implementing on
  branch `wal-pg-stream-impl` off `344470fe`. Starting A0.
- 2026-07-15: **A0 landed** (commit `83c04364`) — `internal/wal/xlog_assemble.go` (+`_test.go`). 6
  round-trip cases green; full `internal/wal` suite + `-race` green; `go build ./...` green.
- 2026-07-15: Found A1 is **not** standalone-additive (see A1 ⚠️ note); folded into A2. Session boundary
  taken at A0 (clean committed keystone). **Next session: A2** (HeapInsert flip incl. per-record xid stamp,
  replay dispatch, FPI/logical unification) — its own focused session with full crash-recovery gates.
- 2026-07-16: A2 investigation (agent-mapped emit path). Findings: HeapInsert emit = native `EncodeHeapInsert`
  → `Append`→`wrapXLogMainData`; a separate conditional FPI follows (post-insert page) via
  `MarkDirtyLogicalChange` — so a **minimal flip keeps that FPI** (no unification needed yet). Routing to the
  already-built `replayDecodedXLogHeapInsert` is automatic (`r.Payload==nil` when a block ref is present).
  **`classifier.go` must be taught the decoded form** (it gates on native `r.Payload`).
- 2026-07-16: **Discovered the t_ctid-convention dependency** (see A2-pre) → user chose the full CTID change.
- 2026-07-16: **A2a landed** — `internal/wal/pg_assembled_emit.go` (envelope + non-wrapping `encodeAssembledXLog`)
  + branches in `encodeRecordXLog`/`predictXLogRecordLen` + `pg_assembled_emit_test.go` (3 cases). Additive
  (wired to nothing). Gates green: build/vet/test/-race ./internal/wal/. Next: A2-pre (CTID), then A2b/c.
- 2026-07-16: **A2-pre landed** (audit-mapped, agent-verified). Stamp self-`t_ctid` in `markHeapInsertDirty`
  (page via existing `PageSetHeapTupleCtid` + `tupleBytes[12:18]` for redo consistency); swap the 4
  `Block==InvalidBlockNumber`-only delete-detectors (`operators_storage.go:259/4051/4581/4803`) to
  `isChainTailCTID`. New `insert_self_ctid_test.go` (plain + NULL rows self-pointing). **Gates all green:**
  storage + executor unit + `-race`; **full isolation 121/121**; **full regress ok**. (Isolation/regress need
  the `postgres` submodule — symlinked the worktree's empty `postgres/` to the main tree's REL_18_3 checkout.)
- 2026-07-16: **A2b-core landed** (dormant) — `EncodeHeapInsertPG` (`pg_assembled_emit.go`) builds the PG
  `xl_heap_insert` (main-data offnum/flags + blk0 `xl_heap_header`+tuple, xid from t_xmin) via A0/A2a; fixed
  `decodeXLogHeapInsertTuple` to reconstruct the tuple by **verbatim concat** (null-bitmap-safe; the old
  prefix-strip rejected non-zero bitmaps). `heap_insert_pg_test.go` round-trips plain + NULL tuples
  byte-for-byte through the real encode/decode path; existing decoded-replay tests stay green. Gates:
  build/vet/test/-race ./internal/wal/. **Remaining A2c** (final, live-stream): wire the `logHeapInsert`
  closure (open.go) to `EncodeHeapInsertPG` + non-wrapping Append, teach `classifier.go` the decoded form,
  gate on G-crash + goopg↔goopg.
- 2026-07-16: **A2c landed — HeapInsert flip is LIVE.** `logHeapInsert` (open.go) now emits
  `EncodeHeapInsertPG`; `classifier.go` gained `classifyDecodedXLog` (routes the PG record by xl_xid) +
  `heap_insert_pg_classify_test.go`. Every INSERT now writes a PostgreSQL `xl_heap_insert`. **Gates:** wal
  unit+`-race`, executor, initdb crash-recovery (237s), e2e native-replication+promotion / physical /
  logical — all green. **A2 DONE.** Next record: A3 HeapDelete (same pattern, no t_ctid landmine).
- 2026-07-16: **A3a landed** (dormant). Unlike HeapInsert, **no** decoded delete replay existed → built it:
  `EncodeHeapDeletePG` (main-data `xl_heap_delete{xmax,offnum,infobits=0,flags}` + optional old tuple as
  `xl_heap_header`+data for logical; block-0 page ref; xl_xid=xmax), `decodeXLogHeapDeleteMainData`,
  `replayDecodedXLogHeapDelete` (reuses `PageSetHeapTupleXmax` = native parity; FPI fallback). Split the
  decoded dispatch so `xlogHeapDelete` gets the new handler (update/hotupdate/inplace stay FPI-only).
  `heap_delete_pg_test.go`: insert→delete replay stamps xmax (xmin/data intact) + old-tuple carried. Gates:
  build/vet/test/-race ./internal/wal/.
- 2026-07-16: **A3b landed — HeapDelete flip is LIVE.** `logHeapDelete`→`EncodeHeapDeletePG` (open.go);
  `classifyDecodedXLog` gained the delete branch (reconstructs old tuple via `reconstructMarshaledTupleFromHeader`)
  + `heap_delete_pg_classify_test.go`. Every DELETE now writes a PostgreSQL `xl_heap_delete`. **Gates all green:**
  wal+`-race`, executor, initdb crash-recovery (234s), e2e native/physical/logical replication, **full isolation
  + full regress**. **A3 DONE.** Next: A5 BtreeInsert or A4 HeapHotUpdate (update needs 2 block refs + t_ctid chain).
- 2026-07-16: **A4 landed (HOT path).** A4a (encoder+decoded replay, dormant) + A4b (live wiring). Threaded
  `new_offnum` (bufpool `LogHeapHotUpdateFunc` + open.go closure + markHeapHotUpdateDirty + tryApplyHOTUpdate);
  self-`t_ctid` stamp extended to the HOT new version; classifier decoded HOT-update path. Every HOT UPDATE
  now writes a PG `xl_heap_update`. Gates all green (wal+race, executor, server, initdb 228s, e2e ×3,
  isolation 485s, regress 288s). Non-HOT single-record conversion deferred (already PG-format Delete+Insert).
  **Next: A5 BtreeInsert.**
- 2026-07-16: **A5 landed — BtreeInsert flip is LIVE.** A5a (encoder + `replayDecodedXLogBtreeInsert` via
  `btree.ApplyInsertRecord`, RmgrBtree opcode switch, dormant) + A5b (`logBtreeInsert`→`EncodeBtreeInsertPG`).
  Every leaf index insert now writes a PG `xl_btree_insert`. Gates all green (wal+race, executor, initdb
  crash-recovery 227s, e2e ×3, full isolation + regress — fresh `-count=1`). **A5 DONE.** offnum=0 parity gap
  documented.
- 2026-07-16: **A6 landed — Xact commit/abort flip is LIVE.** A6a (EncodeXactCommitPG/AbortPG, xid→header,
  HAS_INVALS redo, dormant) + A6b (wire SetXactMarkerLogger hook, classifyDecodedXLog RmgrXact branch,
  header-based `wal_durability_test`). Key insight: the xid moves body→header (xl_xid), but 3 of 4 consumers
  were already header-ready — only the logical-decoder classifier needed the RmgrXact branch. Every COMMIT/
  ABORT now writes a PG `xl_xact_commit`/`xl_xact_abort`. **A6 DONE.** **Part-A hot set (A2–A6) COMPLETE;
  remaining: A7 heap2, A8 btree structural, A9 smgr/clog/FPI/legacy-frame; then Part B.**
