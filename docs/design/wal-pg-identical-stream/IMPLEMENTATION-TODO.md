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
- [ ] **A2** HeapInsert flip — `xl_heap_insert{offnum,flags}` + blk0 `xl_heap_header`+tuple; FPI first-touch. (doc 01 §6)
- [ ] **A3** HeapDelete flip — `xl_heap_delete{xmax,offnum,infobits_set,flags}`.
- [ ] **A4** HeapHotUpdate flip — `xl_heap_update` + 2 block refs; route real non-HOT updates here.
- [ ] **A5** BtreeInsert flip — `xl_btree_insert{offnum}` + blk0 `IndexTupleData`.
- [ ] **A6** XactCommit / CommitInval flip — `xl_xact_commit{xact_time}` + xinfo/invals/subxact chunks.
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
- 2026-07-15: **A0 landed** — `internal/wal/xlog_assemble.go` (+`_test.go`). 6 round-trip cases green;
  full `internal/wal` suite + `-race` green; `go build ./...` green. Next: A1 (xl_xid threading).
