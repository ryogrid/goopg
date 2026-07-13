# C3 — B-tree LP_DEAD on-access dead-entry cleanup

status: design · date: 2026-07-13 · base: goopg `e453e3f2` · depends on:
nothing (parallel with C1; README X1/X3) · gates: see
[README](README.md#common-gates-referenced-as-g--by-every-slice-table)

## 1. Problem and numbers

During the 2-minute pgbench `-N` headline run, `pgbench_accounts_pkey` grew
**+166.8 MB (649 B/txn, file doubles)** on goopg while PostgreSQL's grew
**0 bytes** (`../01-results.md`). New leaf pages ≈ 8 % of txns (an upper bound
on the split rate). Each split logs 2–3 full page images
(`EncodeBtreeSplit`), compounding C1's WAL-volume mechanism, and descent/scan
paths lengthen over time — goopg's write throughput **degrades with runtime**.

Root cause: dead index entries (from non-HOT updates of unchanged keys) are
reclaimed **only by VACUUM**. PostgreSQL reclaims them on access: scans mark
entries LP_DEAD (`kill_prior_tuple`), and inserts purge marked entries before
splitting (`_bt_simpledel_pass`) — which is exactly why PG's pkey stays flat.

## 2. Current-code map (verified at `e453e3f2`)

- **Item format** (`internal/access/btree/btree.go:241-318`):
  `item{keyLen uint16, ptr ItemPointer, key []byte}`, prefix 8 B; `parseItem`
  enforces exact `keyLen + 8 == len(raw)` (:284) — the invariant that rules
  out the keyLen-high-bit trick (M0118-0130 territory).
- **Line pointers — the free flag bits** (`internal/storage/heap.go:386-424`):
  the shared page format's 2-bit `lp_flags` already defines
  `ItemIDUnused=0, ItemIDNormal=1, ItemIDRedirect=2, **ItemIDDead=3**`; btree
  items are stored via `PageInsertItemRawAt`/`PageAddItemRaw`, always Normal
  today. `writeItemID` (:1507) can write any flag; there is **no
  `PageSetItemIDDead` helper yet**.
- **Reader intolerance**: `PageGetItemRaw`/`NoCopy` hard-reject non-Normal
  slots (heap.go:1454/:1487); every btree consumer (`pageItems` btree.go:1733,
  `PageItemKeys` :1780, `PageLeafEntries` :1835, `readPageItem`, amcheck
  `VerifyBtreePage` (def verify_nbtree.go:79; the line-pointer walk that
  errors is at :133) / `VerifyBtreeItemOrder` (:199)) errors on
  a Dead slot.
- **Page opaque** (`btree.go:53-72,110-191`): flags through `BTHalfDead 0x20`;
  **bit `0x40` is free** for a `BTP_HAS_GARBAGE` hint.
- **Scan plumbing gap**: `RangeScan` (btree.go:3066-3177) reads leaves under
  `pinR` (shared RLock, :1605) and invokes `fn(key, ptr)` — **no (blk, slot)
  handle**; contract forbids re-entering the btree from `fn`. The executor is
  TID-eager: `indexScanOp` collects all TIDs in `openPrep`/`Rescan`
  (operators_index.go:358-363), unpins the btree, then visits the heap lazily
  in `Next()` — the visibility-failure moment (`followHOTChainNoCopy` `!found`
  at operators_index.go:436-437) is PG's `kill_prior_tuple` moment, but the
  index-entry coordinates are gone by then. Other RangeScan callers with the
  same shape: `updateViaIndex` (operators_storage.go:3781), non-HOT UPDATE
  probe (:7377/:7445), `indexOnlyScanOp.Rescan`, `upsertOp.probeArbiter`.
- **Dead-to-all oracle already exists**: `pagePruneCore`'s `isDead` predicate
  (`internal/storage/prune.go:85-111`: xmax set, not lock-only, effective xmax
  `< oldestXmin`, multixact-aware) against `mvcc.Manager.OldestXmin()`
  (manager.go:611-641). This is precisely the predicate VACUUM uses.
- **Pre-split hook already half-exists**: `insertIntoBlock`'s no-space path
  (btree.go:2237-2279) collects `pageItems`, runs `dedupConsolidate`
  (:2256/:2836 — posting-list merge, no visibility knowledge), and **skips the
  split entirely if the compacted page fits** (:2261-2279 via
  `resetPageItems` + reinsert). The deletion half is missing.
- **Page-rewrite WAL to reuse**: vacuum-style kept-items record
  (`LogBtreeVacuum`, btree_vacuum.go:124-135; replay `ReplayVacuumPage`,
  replay.go:36-47, `RecordKindBtreeVacuum`).
- **Concurrency reality**: buffers use a real `sync.RWMutex`; scans hold
  RLock. PG's trick of scribbling LP_DEAD under a shared lock is an
  **idempotent-byte race PG tolerates but Go's race detector (and memory
  model) does not** — marking must happen under an exclusive latch.
- **VACUUM/recycle invariants**: `VacuumIndexPages` (btree_vacuum.go:32-179)
  rewrites survivors, two-phase page deletion (`BTDeleted|BTHalfDead` →
  `unlinkEmptyLeaf`), free-list recycle (`pinNewOrRecycled` btree.go:1328
  zeroes pages under the content lock).

## 3. PostgreSQL reference

- `access/nbtree/nbtutils.c` `_bt_killitems`: executed when a scan releases a
  page it had marked candidates on — re-latches the page, **re-verifies the
  page hasn't split/changed** (LSN check), sets LP_DEAD +
  `BTP_HAS_GARBAGE`. Hints are **not WAL-logged** and not replicated.
- `access/nbtree/nbtinsert.c` `_bt_delete_or_dedup_one_page` →
  `_bt_simpledel_pass` / `_bt_dedup_pass`: on a full page, first purge LP_DEAD
  items (WAL-logged as a deletion record), then try dedup, only then split.
- `kill_prior_tuple` (index AM API): the "previous index tuple's heap tuple
  was dead to my snapshot" signal from the heap re-check — goopg's analog is
  the `!found` visibility outcome, upgraded to dead-to-all via the OldestXmin
  predicate (stronger, and required, since goopg has no per-snapshot kill
  channel).
- Posting tuples (deduped): PG only marks a posting tuple LP_DEAD when **all**
  its TIDs are dead.

## 4. Target design

Three cooperating pieces, PG-shaped but adapted to Go's memory model:

**(a) Scan-side kill-list collection.** `RangeScan` (and `Search` where
useful) gains a callback variant carrying `(leafBlk, slot, ptr)`. The executor
records these alongside `o.tids`; at the heap-visibility step, a TID whose
tuple fails `isDead(hdr, OldestXmin)`-style **dead-to-all** testing (NOT the
scan snapshot) is appended to a per-scan kill list keyed by leaf block.

**(b) Deferred exclusive-latched marking pass** (the `_bt_killitems` analog).
At scan end (operator Close/Rescan), for each leaf with kills: `pinW` the
leaf, **re-verify identity keyed on the captured page LSN** (D7 — `pd_lsn`
must equal the value captured at scan time; TID/key match is only a cheap
pre-filter), then set `ItemIDDead` on matched slots via a new
`PageSetItemIDDead` helper and set opaque `BTP_HAS_GARBAGE`.
Marks are **unlogged hints** (PG parity): lost on crash/replay
(`ReplayVacuumPage`/`ReplayNewRootPage` reconstruct Normal slots) and simply
re-derived later.

**(c) Pre-split simple deletion** (the `_bt_simpledel_pass` analog). In
`insertIntoBlock`'s no-space path, **before** the dedup bail-out: if
`BTP_HAS_GARBAGE`, drop `ItemIDDead` items from the collected set —
**structural only, trusting the marks** (the btree layer performs no heap
access, exactly like PG) — WAL the rewrite as a vacuum-style kept-items
record, retry the space check, and only split if still full.

### Decision log

| # | decision | rationale |
|---|---|---|
| D1 | **Deferred marking pass, not inline-under-RLock.** | Go RWMutex + race detector: a write under RLock is a data race, full stop. The deferred `pinW` pass is PG's own `_bt_killitems` shape anyway. |
| D2 | **Hints unlogged; purge logged.** | PG parity; hints are recoverable by construction, the purge changes page contents durably and must replay (reuse `RecordKindBtreeVacuum`). |
| D3 | **Purge trusts marks — no re-verification against the heap at purge time.** | Dead-to-all under `OldestXmin` is monotone (a tuple dead to every snapshot stays dead); marks were verified at mark time under the exclusive latch. This is exactly PG's trust model for LP_DEAD. |
| D4 | **Flag carrier = line-pointer `ItemIDDead` + opaque `0x40`** (no item-body or keyLen changes). | Zero on-disk format migration; old data dirs readable; `parseItem`'s exact-length invariant untouched. |
| D5 | **Never empty a page on-access.** If a purge would remove the last live item, keep one (or skip the purge) and leave page deletion to VACUUM's two-phase machinery. | Page unlink concurrency (half-dead states, sibling relink, recycle) is VACUUM's domain; PG's simple deletion has the same restriction. |
| D6 | **Oracle = `OldestXmin`, never the scan snapshot.** | Keeps on-access deletion a strict subset of what VACUUM may reclaim; `heapallindexed` amcheck semantics stay valid. |
| D7 (rev 2) | **The deferred-mark re-verify keys on the captured page LSN** (`pd_lsn` equality); TID/key match is only a cheap pre-filter, never sufficient alone. The unlogged mark pass must NOT bump `pd_lsn` (it would self-invalidate). | Adversarial finding: TID+key alone is breakable — VACUUM can recycle heap TID T and a re-insert of key K can legitimately re-create a **live** `(K,T)` entry between capture and mark; purge compaction shifts slot numbers similarly. Marking that live entry dead = silent row loss invisible to amcheck (which skips Dead after S1). Page LSN is monotone under WAL-logged changes, and `pinNewOrRecycled` (btree.go:1328) zeroes recycled pages ⇒ LSN mismatch fails safe. Cost: a benign concurrent WAL-logged change drops that leaf's pending kills — hint loss, not corruption. |

## 5. Concurrency, invariants, failure modes

- **I1 (mark validity)**: a set `ItemIDDead` implies the heap tuple was
  dead-to-all at mark time ⇒ forever. Enforced by D6 + the exclusive-latch
  re-verify keyed on the captured **page LSN** (D7; TID/key only a pre-filter)
  — the ABA hazards (leaf split/vacuum/**recycle**, heap-TID reuse re-creating
  a live `(key,TID)` pair, purge-compaction slot shift) all fail safe on the
  LSN check, not by pinning across the scan.
- **I2 (readers)**: all page readers and amcheck skip Dead slots; a Dead slot
  is never returned to a scan. Sibling-path audit: `pageItems`,
  `PageItemKeys`, `PageLeafEntries`, `readPageItem`, `PageGetItemRaw(NoCopy)`
  callers, replay readers, `VerifyBtreeItemOrder` — one slice (S1) updates all.
- **I3 (Lehman-Yao)**: the marking pass takes single-leaf exclusive latches
  only (no descent, no couple), so it cannot deadlock with insert/split paths;
  right-link movement between collection and marking is handled by re-verify
  (a moved item simply fails the match and is skipped — hints are best-effort).
- **I4 (purge safety)**: purge runs inside `insertIntoBlock`'s existing
  exclusive context; the rewrite preserves item order (compaction), the high
  key lives in the opaque area (naturally untouched); `MaxItemsPerPage` and
  amcheck order checks hold. Exempt `IsDeleted`/`IsHalfDead` pages.
- **I5 (unique checks)**: `maintainUniqueIndexesForInsert` and any
  unique-probe path must treat Dead entries as absent (or heap-recheck) —
  audit in S1 (O-C3-4).
- **F1 (crash)**: marks lost (unlogged) → re-derived; a purge is WAL-replayed
  via the kept-items record; kill-9 across a purge is a G-crash case in S4.
- **F2 (stale BTP_HAS_GARBAGE)**: harmless — the purge scan finds no Dead
  items and clears it (who clears: purge and VACUUM both; O-C3-5).
- **F3 (standby)**: hints are never replicated (PG parity). The purge record
  is native-family WAL; whether user-index pages need a canonical counterpart
  for a real PG standby is README X3 / O-C3-2 — **settle before S4**.

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| **S1** | Reader tolerance (behavior-identical) | `PageSetItemIDDead` helper; all btree readers + amcheck + replay + vacuum tolerate/skip `ItemIDDead`; recognize opaque `0x40`; unique-probe audit (I5); synthetic-page unit tests. No writer sets the flag yet. Sub-slicing per reader family permitted if the diff grows. | full btree suite, amcheck suites, G-race(+btree), G-unit |
| **S2** | Scan plumbing | Add a **new additive `RangeScan` variant** carrying `(blk, slot, ptr)` — the existing signature and its **10 non-test callers** (operators_index.go:363, operators_indexonly.go:263, operators_upsert.go:673/:866/:1371, deferred_exclusion.go:193, deferred_unique.go:239, operators_storage.go:3781/:7377/:7445) stay untouched; `indexScanOp` alone migrates first and collects the kill list via the `isDead`/`OldestXmin` oracle at the visibility site. No marking yet. | G-unit, G-race, G-tpch |
| **S3** | Deferred marking pass | Scan-end re-latch + **D7 page-LSN re-verify** + mark + `BTP_HAS_GARBAGE`; unlogged (must not bump `pd_lsn`). Mandatory tests: (a) heap-TID recycle re-creating a live `(K,T)` between capture and mark → mark dropped; (b) purge-compaction slot shift → mark dropped; (c) subtransaction-writer dead-to-all case (effective-xmax via parent). | **G-race(+btree) is the headline gate**; `multi_writer_stress_test`; vacuum-race suites; amcheck; G-crash (hints lost → recovery unaffected); smoke |
| **S4** | Pre-split simple deletion | Purge Dead slots in `insertIntoBlock`'s no-space path before dedup; kept-items WAL + replay; retry insert; D5 never-empty rule; settle O-C3-2/X3 first. | btree vacuum WAL/replay tests, G-crash (kill-9 across purge), amcheck, G-waldump (if record dump-visible), G-standby, G-race, G-unit |
| **S5** | Perf certification | Soak = `DURATION=600 analysis/perf-optimize3/scripts/run_rw50.sh` (TPS-over-time from the `-P 30` progress lines; **lift the script's hard-coded 90 s pprof window** or it truncates); surface `bt.stats.splits` (counter exists at btree.go:1424, no read-out today — expose via stats view or test hook, cf. 06-appendix); G-perf. | G-perf: pkey growth ~0 over the run, split rate ↓, TPS-over-time flat |

## 7. Test-impact matrix

| test | impact |
|---|---|
| `internal/access/btree` full suite incl. `multi_writer_stress_test.go`, `race_on_test.go`, `size_check_test.go` | S1-S4; size_check may need widening if Dead slots change accounting |
| `btree_vacuum_*` (incl. `btree_vacuum_wal_test.go`, race/cascade tests) | S4 reuses the vacuum record; extend replay tests for the purge emitter |
| `internal/amcheck/verify_nbtree*` + `internal/testport/pgamcheck_btree_port_test.go` | S1: accept Dead-but-present entries; item-order check skips Dead |
| `RangeScan` callers | S2 is **additive** — no signature change; the 10 existing callers + ~14 test files untouched; later slices migrate callers opportunistically |
| `TestPort_` index/isolation suites touching FOR UPDATE / unique | S1 I5 audit targets |
| kill-9 matrices | S3/S4 additions |

## 8. Performance verification

- aux2 / `run_rw50.sh` signatures: `pgbench_accounts_pkey` growth
  +166.8 MB/2 min → **≈0**; split counter (`bt.stats.splits`) collapses;
  UPDATE statement latency down (fewer split-path entries + shallower tree);
  TPS-over-time flat in a ≥10-minute soak (the property PG has and goopg
  lacks today).
- Secondary: btree split WAL volume (2–3 page images each) disappears from the
  aux2 bytes/txn residual after C1 lands — measure separately per README X3.

## 9. Open questions (flagged, not resolved)

- **O-C3-1**: posting-list tuples — adopt PG's all-TIDs-dead rule for marking
  a posting tuple, or split posting tuples on partial death? (Recommend
  all-dead; decide with posting.go's owner.)
- **O-C3-2** (README X3): how do user-index page changes reach a real PG
  standby today (canonical replication of user btrees appears absent) — if
  absent, S4's purge is standby-invisible by construction; confirm and record.
- ~~O-C3-3~~ **resolved as D7** (rev 2): page LSN is the token; recycle
  zeroing makes it fail-safe; tests specified in S3.
- **O-C3-4**: `maintainUniqueIndexesForInsert` / unique-probe behavior over
  Dead entries (absent vs heap-recheck) — audit outcome drives S1.
- **O-C3-5**: `BTP_HAS_GARBAGE` clearing protocol (purge clears when no Dead
  remain; VACUUM clears unconditionally; stale-set harmless) — document the
  final protocol in S3.

## 10. Implementation notes (S1-S4, 2026-07-13)

- S1 d78d0199, S2 3c193124, S3 d8d450c1 (see each commit message for the
  review fallout). Key deviations from the design as written:
  - The S4 "pre-split simple deletion" needed NO new purge pass: S1's
    reader skip makes every no-space rewrite (dedup-recovery, split) drop
    Dead items structurally, and the S3 blocker-A fix routes the
    dedup-recovery rewrite through the LogBtreeVacuum kept-items record —
    so the purge is logged and crash-replayable via the existing
    ReplayVacuumPage path. Pinned by TestNoSpaceRewritePurgesDeadItems.
    D5 (never-empty) holds trivially: the rewrite set always contains the
    incoming item.
  - D7 gained a THIRD leg beyond the LSN token: KillItems refuses unless
    LogBtreeVacuum+LogPageImage+WALFrontier are all wired, and
    Pool.hintFlushBarrier defers every page write-back until WAL is
    flushed past the mark-time frontier (async-commit durability hole —
    S3 review; PG SetHintBits/XLogNeedsFlush analog).
  - TupleDeadToAll now requires a COMMITTED deleter via the
    storage.XidCommitted hook (S3 blocker B) — this also fixed a
    pre-existing prune/VACUUM hazard (aborted DELETE reclaiming a live
    row). Sub-xid CLOG lanes are not stamped in production yet: subxact
    deleters stay conservatively unreclaimable (ledger row; resume =
    TransactionIdCommitTree parity).
  - Posting-list kills deferred (O-C3-1): KillItems skips posting slots.
- **O-C3-2 / README X3 settled**: under the native-only WAL default
  (perf-optimize3-dash S4, commit dff94fca), user-index pages are not
  canonically replicated to real PG at all — the purge record is
  native-family WAL replayed by goopg standbys via AppendRaw byte copy;
  no canonical counterpart is required until the GOOPG_WAL_CANONICAL=on
  resume path lands C1, which will need a canonical XLOG_BTREE_DELETE
  sibling at that point (deferral ledger, dash rows).

