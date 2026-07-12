# C1 — Incremental canonical heap WAL records

status: design (rev 2 — adversarial review incorporated) · date: 2026-07-13 ·
base: goopg `e453e3f2` · depends on: nothing (S1/S2a/S2b may run in parallel
with C2) · gate names: see
[README](README.md#common-gates-referenced-as-g--by-every-slice-table)

> **rev 2 note**: the first draft gated the canonical FPI on the *shared*
> buffer-pool `fpiSinceCheckpoint` bool. Adversarial review broke that design
> two independent ways (§5.1): native-only page touches poison the shared bool
> without putting an image on the canonical stream, and the epoch reset is
> ordered after the redo sample, opening an image-less replay window. The
> design now uses a **canonical-stream image token compared against a
> RedoRecPtr published at checkpoint start** (PG's actual semantics).

## 1. Problem and numbers

goopg emits **33,004 WAL bytes per pgbench `-N` transaction** vs PostgreSQL's
1,801–2,853 (LSN-delta meters; ~12–18×; ~31× vs PG's `pg_stat_wal` counter —
see `../01-results.md`). Two causes, both in the canonical (PG-format) WAL
emission path:

1. Every canonical **heap** record embeds an unconditional **8 KB full-page
   image**, regardless of checkpoint state.
2. Every heap write is **logged twice**: once as a native goopg logical record
   and once as the canonical FPI record.

Costs: ~75 % of `memmove` (11.3 % of `-N` CPU) is WAL record assembly; the
write statements carry 5–7× per-statement latency vs PG; each group-commit
cycle drains ~width × 33 KB. **Scope note**: C1 does *not* shrink the per-call
fsync floor (`../02` M1) — its END-latency win arrives through faster
statements, smaller drains, and wider emergent commit groups. C1 composes
with C2; it does not replace it.

## 2. Current-code map (verified at `e453e3f2`)

**One physical WAL stream, two record families** (PageHeaders mode,
production):

| family | first payload byte | producers | consumers |
|---|---|---|---|
| native logical | `RecordKind*` = 1…~60 (`internal/wal/recovery.go:20-500`) | buffer-pool emit hooks wired in `internal/initdb/open.go:420-639` (`logFPI`→`EncodePageImage`, `logHeapInsert`:449, `logBtreeInsert`:459, `logHeapDelete`:471, `logHeapHotUpdate`:589, …) | goopg crash recovery (`ApplyRecord` native switch, recovery.go:8756) + logical decoding (`Classify`, classifier.go:35 → pgoutput) |
| canonical PG-format | `RecordKindCanonical` = `0xFE` (recovery.go:1387), envelope `kind+rmgr+info+xid+XLogRecord` (`buildCanonicalPayload`, canonical.go:497) | `executor.Context.LogCanonical` emitters (below); hook only non-nil in PageHeaders mode (open.go:2060-2066) | real PG 18 standby + `pg_waldump` (via `classifyXLogRecord`, format.go:217 — native kinds are wrapped as `RmgrXLog`/info `0xF0` at :219, which PG no-ops) + goopg's own `replayDecodedXLogRecord` (recovery.go:9235) |

**The unconditional FPI**: `buildCanonicalSingleFPIBody`
(`internal/catalog/canonical.go:460-491`) — one block-ref header with
`HasImage|MainFork` **hardcoded** (:472) and `ImageApply` (:476), the full
8,192-byte page, then short main data. Used by the heap builders
`BuildCanonicalHeapInsertPayload` (:104, `xl_heap_insert` = offnum+flags,
flags=0 with the comment *"XLH_INSERT_CONTAINS_NEW_TUPLE not needed when FPI
restores the page"*), `…Inplace` (:156), `…Delete` (:263), `…Prune` (:319).
Block-data length is currently always 0 (:473).

**The emitters** (`internal/executor/operators_storage.go`):
`emitCanonicalHeapInsert` (:8253; call sites 2075, 2127, 4247, 4975, 6085,
7710), `emitCanonicalHeapHotUpdate` (:8283 → `PgCanonicalHeapInplace` at
:8294; called at :3404 immediately after the native `markHeapHotUpdateDirty`
at :3400 — the double log), `emitCanonicalHeapDelete` (:8339; call sites 4244,
4972, 5575, 6082, 6300), `emitCanonicalHeapPruneLocked` (:8317). Each re-pins
the page, makes an inline 8 KB copy, and fires unconditionally (gated only on
`LogCanonical != nil`).

**Native FPI-epoch machinery (context — NOT the canonical gate; see §5.1)**
(`internal/storage/bufpool.go`): per-slot `fpiSinceCheckpoint atomic.Bool`
(declared :72), cleared for all slots by `ResetCheckpointEpoch` (:778, called
from checkpointer.go:635 after the checkpoint record lands). Three mark-dirty
variants:

- `MarkDirtyChangeRecord` (:1820) — first dirty in epoch → native FPI, else →
  the small native logical record (used by btree leaf insert, prune, row-lock
  paths).
- `MarkDirtyLogicalChange` (:1868) — always emits the native logical record
  AND adds a native FPI on first dirty (heap insert/delete;
  `docs/design/0103-0018-heap-fpi-and-logical-record-coexistence.md`).
- `maybeEmitFPI` (:1907) — side-effect-only native epoch FPI (plain
  `MarkDirty`, e.g. row-lock xmax stamps).

**The dead incremental redo that already exists**:
`replayDecodedXLogHeapInsert` (`internal/wal/recovery.go:9392`) — when the
block ref has **no** ImageApply, it parses `xl_heap_insert` main data
(`decodeXLogHeapInsertMainData` :9489) + `xl_heap_header`(5 B: infomask2,
infomask, hoff) + tuple bytes (`decodeXLogHeapInsertTuple` :9496) and applies
via `PageInsertItemRawAt` (`storage/heap.go:653`), honouring
`xlogHeapInit`/WillInit (:9424). It is unreachable today because the emitter
always sets HasImage. **Producer and consumer formats already agree** — the
insert slice is producer-side only.

Canonical delete/update/hot-update/inplace have **only** the FPI-restore
branch (`replayDecodedXLogHeapFPIBlocks`, recovery.go:9275-9310 →
`restoreDecodedXLogBlockImage` :9466); incremental redo appliers for those
kinds must be written.

**Roundtrip tests already exist**: `internal/wal/canonical_heap_roundtrip_test.go`
(`TestCanonicalHeapInsertWALRoundTrip`, `TestCanonicalHeapInplaceWALRoundTrip`)
— extend per kind, do not create.

**Tuple material** for the block-data assembly: `EncodeRowPG`
(`internal/executor/codec.go:119`), `NullBitmapPG`,
`storage.HeapTuple.MarshalBinary` (PG-compatible header bytes).

**Checkpointer ordering today** (`internal/wal/checkpointer.go`
`runCheckpoint` :469): flush data pages :481 + CLOG :488-492 → **then** sample
redo :508-523 → append+flush checkpoint record :552-560 → pg_control
(`UpdateControlFile` :577) → `ResetCheckpointEpoch` :635. §5.1-F2 explains why
this order is part of what C1 must fix.

**Basebackup**: `replyBaseBackup` (`internal/server/basebackup.go:174`) forces
an immediate checkpoint and samples `startLSN = redoLSN`, but has **no
`forcePageWrites`-for-backup-duration equivalent** — safe today only because
every canonical record carries an image (§5.1-F5).

## 3. PostgreSQL reference

- `access/transam/xloginsert.c` `XLogRecordAssemble`: backup block attached
  iff `page_lsn <= RedoRecPtr` (or `forcePageWrites`). **RedoRecPtr is
  published atomically at checkpoint start** (`xlog.c` `CreateCheckPoint`
  computes the redo pointer under the WAL-insert locks *before*
  `CheckPointGuts` flushes buffers) — the property goopg must reproduce (§4.2).
- `access/heap/heapam.c` `log_heap_update` / `heap_insert`'s
  `xl_heap_insert` + `xl_heap_header` (`heapam_xlog.h`):
  `XLH_INSERT_CONTAINS_NEW_TUPLE = 0x02`; update uses two block refs (new
  page block 0, old page block 1) with per-block backup-image decisions;
  prefix/suffix suppression on same-page updates.
- `access/heap/heapam.c` `heap_redo` appliers: the shapes goopg's new
  incremental redo appliers mirror.
- `do_pg_backup_start` (`xlog.c`): forces `full_page_writes` behavior for the
  duration of a base backup so the fuzzy file copy is repairable from WAL —
  goopg needs the canonical-stream analog (§5.1-F5).
- `full_page_writes=off` is only safe on atomic-write storage; goopg keeps it
  effectively always-on (D3).

## 4. Target design

### 4.1 Emission

Canonical heap emission moves into the page-modification critical section
(today's separate re-pin + unconditional-image `emitCanonical*` passes are
deleted), and each canonical heap record makes a per-record decision:

- **image-bearing** (today's shape) if the canonical stream has not imaged
  this page since the current RedoRecPtr (§4.2);
- **incremental** (main data + `xl_heap_header` + tuple block-data, `HasData`
  without `HasImage`) otherwise.

Native records are byte-for-byte unchanged in every slice. Record order within
one logical page modification is fixed: native first, canonical second (D5).

### 4.2 The gating mechanism (rev 2 — replaces the shared-bool design)

Two pieces, mirroring PG's `page_lsn <= RedoRecPtr` semantics for the
canonical stream specifically:

1. **RedoRecPtr publication at checkpoint start.** `runCheckpoint` computes
   and publishes the redo pointer (atomic in the WAL insert ordering — i.e.
   sampled from the writer's published frontier *before* the buffer/CLOG flush
   begins) instead of sampling it after the flush. The checkpoint record then
   carries this pre-flush redo. This matches PG's `CreateCheckPoint` order and
   is a prerequisite shared with the correctness of the *native* epoch reset
   (see §5.1-F2 — the current post-flush sample + post-record reset has a
   latent image-less window even for the native family; C1 fixes the shared
   root cause). C2's checkpoint invariant is preserved: with redo chosen
   *before* the flush, the flush covers everything ≤ redo a fortiori (README
   X6 rev 2).
2. **Per-slot canonical-image token.** Each buffer slot carries
   `canonicalImageLSN` (the end-LSN of the last **canonical** image emitted
   for the page occupying the slot; zero on load/eviction/reset). The
   canonical emit decision is: image-bearing iff
   `canonicalImageLSN < RedoRecPtr(published)`, and emitting an image stores
   the new LSN. Only canonical image emission ever sets the token —
   **native-only page touches (row locks, `maybeEmitFPI`, nil-hook fallbacks)
   cannot poison it** (§5.1-F1), and slot eviction/reload conservatively
   re-arms it (token zeroed ⇒ next canonical record re-images; safe
   direction, same as today's slot-keyed native flag).

The native family keeps `fpiSinceCheckpoint` initially, but its reset is
re-keyed to the published RedoRecPtr as part of the same checkpointer change
(replacing the post-record `ResetCheckpointEpoch` call with
publication-ordered semantics) — one ordering fix serving both families.

### 4.3 Record shapes goopg will emit (per kind)

| kind | incremental canonical layout (PG parity) | redo applier |
|---|---|---|
| insert | `xl_heap_insert{offnum,flags=XLH_INSERT_CONTAINS_NEW_TUPLE}` + block 0 data = `xl_heap_header` + tuple body | **exists** (recovery.go:9392) |
| delete | `xl_heap_delete{xmax,offnum,infobits,flags}`, no block data | new: stamp xmax/infomask at offnum |
| HOT / inplace update | same-page: `xl_heap_inplace`-shaped (offnum) + block data carrying the new tuple image at that offnum | new |
| full update | `xl_heap_update` with two block refs (block 0 new page incl. `xl_heap_header`+tuple, block 1 old page xmax/ctid stamp), **independent per-block image decisions** | new |
| prune | `xl_heap_prune`-shaped redirect/dead/unused arrays | new |

### 4.4 Decision log

| # | decision | rationale |
|---|---|---|
| D1 (rev 2) | **Canonical-stream-specific image token vs published RedoRecPtr** — NOT the shared native bool. | The shared bool is poisoned by native-only touches (a row-lock `maybeEmitFPI` flips it with no canonical image → the standby would receive image-less incrementals; adversarial finding F1), and its free-running reset at checkpointer.go:635 opens an image-less window after the redo point (F2). Keying both streams on one *published RedoRecPtr* gives the "no drift" property the original D1 wanted, without sharing the poisonable flag. |
| D2 | **Native family retained unchanged.** Retiring it (single-stream) needs a canonical→`Change` decoder for pgoutput — explicitly out of scope (§9 O-C1-7). | pgoutput/`Classify` consume native records exclusively (classifier.go:35-144). |
| D3 | **`full_page_writes=off` stays unsupported**; guard at GUC level (reject or warn-and-force-on) in S1. | The image machinery is load-bearing for both consumers; goopg has no atomic-write story. |
| D4 | **First-touch duplication accepted initially**: first canonical image + the native FPI record (2×8 KB once per page per checkpoint where both fire). Optimizing to one shared image is a follow-up, not a blocker. | Keeps slices producer-local. Measured in S8; flagged as O-C1-2. |
| D5 | **Record ordering fixed**: native first, canonical second, same page-lock section, adjacent in the stream. Assert in the roundtrip test. | Deterministic replay reasoning + pg_waldump goldens. |
| D6 (rev 2) | **Canonical emit failure re-arms the image token** (zero `canonicalImageLSN`) so the next canonical record re-images the page. | Post-C1 an emit-error hole is no longer healed by the next record's unconditional image (adversarial F6); re-arming restores the self-healing property. |
| D7 (rev 2) | **Base backups force canonical images for their duration** (a `forceCanonicalImages` flag raised by `replyBaseBackup` around the file copy, PG's `do_pg_backup_start` analog), OR S8 must prove the token machinery alone covers the fuzzy copy. | basebackup's torn fuzzy copy is only repairable if every page's first post-`startLSN` canonical record carries an image (adversarial F5). |

## 5. Invariants and failure modes

### 5.1 The two adversarial breaks the rev-2 design closes (read first)

- **F1 — shared-bool poisoning (was FATAL).** Native-only heap-page touches —
  row-lock xmax stamps via plain `MarkDirty`→`maybeEmitFPI`
  (operators_lockrows paths), the `logHeap == nil` fallbacks
  (operators_storage.go:8188/:8216), freeze — emit a **native** FPI (which a
  PG standby no-ops as `RmgrXLog/0xF0`) yet flipped the shared
  `fpiSinceCheckpoint`. Under the rev-1 design the next canonical record would
  then go incremental with **no canonical image this epoch** → silent standby
  corruption, invisible to every goopg-side test. Closed by D1 rev 2: only
  canonical image emission sets `canonicalImageLSN`.
- **F2 — reset-after-redo window (was FATAL/MAJOR).** Today redo is sampled
  *after* the flush (:508 after :481) and the native epoch resets *after* the
  record lands (:635), with no `page_lsn <= redo` backstop in
  `MarkDirtyChangeRecord` (:1821). A page dirty in epoch E−1 and modified
  between redo-sample and reset emits **no image**, yet its LSN > redo means
  replay-from-redo applies it — onto a potentially torn page (eviction's
  `flushSlot` bufpool.go:2133 writes a single 8 KB block with no FPI, and a PG
  standby's restartpoint flushes tear the same way). **This is latent today
  for the native family (btree-leaf records)**; C1 would have extended it to
  the high-volume canonical heap stream. Closed by §4.2's
  publication-at-checkpoint-start (redo before flush, decisions keyed on the
  published pointer — no separately-timed reset). README X6 is rewritten
  accordingly (rev 2).

### 5.2 Standing invariants

- **I1 (standby torn-page)**: for every page P, the first canonical record
  touching P with LSN > RedoRecPtr(C) carries a full image, for every
  checkpoint C a standby restartpoint or crash recovery can anchor at.
  Enforced by §4.2; proof obligation for restartpoint placement remains
  O-C1-3 and now explicitly includes the basebackup fuzzy-copy case (D7).
- **I2 (goopg self-recovery unchanged)**: crash recovery still applies the
  native family; canonical records replayed by `replayDecodedXLogRecord` stay
  idempotent via the page-LSN interlock (`pd_lsn >= EndLSN` skip, the native
  appliers' guard at recovery.go:10062). Every new incremental applier must
  implement it. Double-apply semantics: O-C1-1.
- **I3 (checkpoint ordering)**: README X6 rev 2 — redo published first, flush
  covers ≤ redo, record carries the published redo. C2 depends on the same
  statement.
- **I4 (slot-keyed tokens)**: both the native flag and the canonical token are
  slot-keyed; eviction/reload re-arms conservatively (extra image, never a
  missing one). Document; do not "fix" in C1.
- **F-crash (mid-slice)**: every slice leaves the stream a valid mix of
  image-bearing and incremental records; PG replays mixed streams natively,
  goopg replays natives. Revert re-widens records — no data-dir migration
  either direction.
- **F-emit-error**: D6 — canonical emit failure re-arms the token; the
  error-path hole is bounded to the single failed record.

## 6. Migration slices (rev 2 — 9 slices)

| # | slice | content | gates |
|---|---|---|---|
| **S1** | HasImage plumbing + resurrect the dead redo (behavior-identical) | Parameterize the hardcoded `HasImage`/`ImageApply` (canonical.go:472/:476) with all callers passing image=true; add `xl_heap_header`+tuple block-data encoders; extend `canonical_heap_roundtrip_test.go` to drive `replayDecodedXLogHeapInsert`'s incremental branch directly; add the `full_page_writes` guard (D3). | G-unit, G-race; **G-waldump byte-identical** |
| **S2a** | Emitter-contract plumbing (behavior-identical) | Thread the image verdict + a canonical-emitter parameter through the `MarkDirty*` emitter contract and the `markHeap*Dirty` helper family (operators_storage.go:8180/:8218 are ctx-free today; `markHeapDeleteDirtyAndClearVM` :8225 already takes ctx — follow it). **Note**: no layering problem — emitters are executor-side closures of type `func() (storage.LSN, error)`; `storage` never imports `executor`. **Ripple**: the emitter-signature change touches all ~15 closure callers incl. btree (btree.go:1681/2118/2181/2799), btree_vacuum.go:126, vacuum.go:153/201 — mechanical, but budget for it. Sub-slicing by caller family permitted. | G-unit, G-race, full btree+vacuum suites |
| **S2b** | Canonical emission relocation (bytes unchanged) | Move canonical heap emission into the heap closures next to native emission (D5 order); delete the re-pin+copy `emitCanonical*` passes; every record still image-bearing. | G-crash, G-standby, G-waldump (re-baseline if adjacency shifts goldens), pgoutput/logical suites, G-race, G-unit |
| **S3** | **RedoRecPtr publication + canonical-image token** (checkpointer ordering fix) | Publish redo at checkpoint start (before flush); checkpoint record carries the published redo; add per-slot `canonicalImageLSN`; re-key the native epoch reset to the publication; canonical emission still always-image (token is written, not yet consulted). Fixes the latent native F2 window as a side effect — add a regression test for the window (page dirtied between old-order redo-sample and reset). | G-crash (+ new window test), G-race, G-standby, G-unit; C2 invariant test (`TestCheckpointerCallsFlushCLOGFn` family) stays green |
| **S4** | First incremental kind: **INSERT** | Consult the token: emit incremental `xl_heap_insert` (+`XLH_INSERT_CONTAINS_NEW_TUPLE`) when `canonicalImageLSN ≥ RedoRecPtr`; image + set token otherwise; D6 re-arm on error; page-LSN interlock in the (existing) redo; extend G-standby with inserts spanning a checkpoint boundary. | G-standby (extended), G-crash, G-waldump re-baselined, roundtrip, G-race, G-unit, smoke; G-perf partial (history INSERT 8 KB → ~100 B post-first-touch) |
| **S5** | Incremental **DELETE** | New redo applier (xmax/infobits at offnum) + gated emit. | as S4 |
| **S6a** | Incremental **HOT/inplace update** — *the perf slice* | New-tuple-at-offnum layout + redo; pgbench `-N` UPDATE is HOT-dominated. | as S4 + G-tpch; **G-perf full re-measure** |
| **S6b** | Incremental full **UPDATE** | Two-block `xl_heap_update`, independent per-block image decisions (old/new pages have independent tokens). | as S6a |
| **S7** | Incremental **prune** | Redirect/dead/unused arrays + redo. | as S4 + `pgwaldump_vacuum_prune_test.go` |
| **S8** | Cleanup + certification | Retire `buildCanonicalSingleFPIBody` for heap kinds (keep for remaining non-heap users); D7 basebackup decision (force-images flag or proof) + a G-standby test cloning via BASE_BACKUP **taken mid-epoch under write load**; measure D4 duplication; final aux2 bytes/txn vs the 33 KB baseline. | full gate set incl. G-perf + heavy crash matrix |

Sequencing rationale: S2a/S2b before S4 so the gating decision lives where
emission happens; S3 before S4 because the token/redo machinery is the
correctness foundation for every incremental kind; insert first because its
redo exists; one kind per slice keeps any red gate revertable to a still-valid
mixed stream.

## 7. Test-impact matrix

| test | impact |
|---|---|
| `TestE2E_FailoverGoopgToPG`, `e2e_standby_attach_roundtrip_test.go` | acceptance gate every slice from S2b; extended in S4 (checkpoint-crossing inserts) and S8 (mid-epoch BASE_BACKUP clone under load). **Activation**: not `-short`, real PG 18 binaries on PATH, `GOOPG_SKIP_M0102_E2E` unset — see README gate table |
| `pgwaldump_savefullpage_test.go` | still passes under gating (≥1 image per page per checkpoint); after S4 additionally assert NOT one image per record |
| `pgwaldump_port_test.go`, `pg_waldump_compat_test.go` | re-baseline per slice if golden-file (verify which — O-C1-6) |
| `internal/wal/canonical_heap_roundtrip_test.go` (exists: `TestCanonicalHeapInsertWALRoundTrip`, `TestCanonicalHeapInplaceWALRoundTrip`) | extend per kind (S1, S4–S7) |
| `docs/design/0103-0018` coexistence tests | unchanged (native family untouched) |
| pgoutput/logical suites (`pgoutput_interop_test.go`, `e2e_replication_test.go`) | must stay untouched-green (D2 proof) |
| crash/recovery suites | per-slice; one new test per new redo applier (S5–S7) + the S3 window regression test |
| btree/vacuum suites | S2a emitter-signature ripple only (mechanical) |

## 8. Performance verification

Signatures, via `run_rw50.sh` + `aux2_fsync_probe.sh` after S4/S6a/S8:

- WAL bytes/txn: 33 KB → **~1–2 KB** (bounded below by PG's 1.8–2.9 KB; D4's
  first-touch duplication adds a small epoch-amortized term — measured in S8).
- `memmove` share of `-N` CPU: 11.3 % → a few %; UPDATE statement latency
  toward PG's 0.155 ms.
- Emergent group width (aux2 txns/fsync) rises from ≈6; END falls through
  width and drain, not the fsync floor.
- 06-appendix `wal_fpi` counter: track as a **trend only** until S8 — it
  counts native + canonical images and overcounts vs PG's `wal_fpi` by the
  D4 duplication factor.

## 9. Open questions (for the implementer — flagged, not resolved)

- **O-C1-1**: exact consumer set of `replayDecodedXLogRecord` on the goopg
  side and the double-apply/idempotency proof for a page receiving both a
  native and a canonical application of the same change.
- **O-C1-2** (D4): measure the first-touch duplication (native FPI +
  canonical image) in S8; decide whether a shared-image optimization is
  warranted.
- **O-C1-3** (I1): restartpoint-placement proof — which canonical record
  anchors a PG standby's redo start, and why it can never land after a redo
  point's image but before its incrementals; **now explicitly includes the
  BASE_BACKUP fuzzy-copy obligation (D7)**.
- **O-C1-4**: current state of the `full_page_writes` GUC in
  `internal/config/defaults.go` / pg_control (inert or behavioral?) — S1's
  guard must match reality.
- **O-C1-6**: are the pg_waldump parity tests golden-file (per-slice
  re-baseline burden) or structural?
- **O-C1-7**: end-state of the two-family stream — retiring the native family
  behind a canonical→`Change` decoder is future work, explicitly out of C1.
- **O-C1-8** (rev 2): does the native family migrate from
  `fpiSinceCheckpoint` to the published-RedoRecPtr comparison outright in S3,
  or keep the bool re-keyed to publication? (Either closes F2; outright
  migration retires one mechanism but widens S3's diff into btree/vacuum
  emit paths.)
