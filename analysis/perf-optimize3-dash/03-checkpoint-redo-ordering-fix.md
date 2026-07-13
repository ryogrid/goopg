# 03 — Checkpoint redo-ordering fix (the image-less replay window)

status: design · date: 2026-07-13 · base: `e453e3f2` · slice: S2
(mode-independent; lands while canonical is still ON) · gates: G-crash full
(incl. `TestKillKillRecovery`), G-race, G-unit

## 1. The latent bug (exists today, independent of this bundle)

Found by the adversarial review of the perf-optimize3/05 bundle and recorded
in the deferral ledger; under native-only it graduates from latent to
load-bearing (README R2).

`runCheckpoint` (`internal/wal/checkpointer.go`) today:

1. flush dirty data pages (:481) + CLOG (`FlushCLOGFn` :488-492)
2. **sample the redo LSN** (:506-523) — *after* the flush
3. append + flush the checkpoint record (:550-560), update pg_control (:577)
4. **reset the FPI epoch** (`Pool.ResetCheckpointEpoch` → bufpool.go:778,
   called at checkpointer.go:635) — *after the record lands*

The native FPI decision is purely the per-slot `fpiSinceCheckpoint` bool
(bufpool.go:1821/:1872) — there is **no `page_lsn ≤ redo` backstop**.
Consequence (**Window A**): a page already dirty in the previous epoch
(bool=true) that is modified between the redo sample (2) and the epoch reset
(4) emits **no image**, yet its LSN is greater than redo, so replay-from-redo
applies its incremental records — potentially onto a **torn page** (eviction's
`flushSlot` writes a single 8 KB block with no FPI, bufpool.go:2133-2156).
There is also a **Window B**: because redo is sampled *after* the flush, a
page re-dirtied concurrently with the flush gets an LSN below redo and is
never replayed from redo, yet can still be evicted-torn later.

**Why it is latent today**: the high-volume heap records are *canonical* with
unconditional images — replay restores those pages regardless. The window
practically exposes only native-gated paths (btree leaf inserts). Under
native-only, **every** heap record depends on the epoch machinery → the
window becomes the primary torn-page surface. Hence S2 strictly precedes S4.

## 2. PostgreSQL reference

`CreateCheckPoint` (`xlog.c`): the redo pointer is computed **under the
WAL-insert locks at checkpoint start**, published (`XLogCtl->RedoRecPtr` /
`Insert->RedoRecPtr`) **before** `CheckPointGuts` flushes buffers, and every
`XLogRecordAssemble` decision is the per-record test
`page_lsn <= RedoRecPtr` against the published pointer. There is no
separately-timed "epoch reset" event to misorder — publication IS the epoch
boundary.

## 3. Target design

Adopted from `../perf-optimize3/05-improvement-designs/01` §4.2(1) and README
X6 rev 2 (this doc restates it for the native family, which is now the sole
consumer):

1. **Publish the RedoRecPtr at checkpoint start** — sampled from the writer's
   published insert frontier *before* the buffer/CLOG flush begins; store it
   in an atomic the buffer pool can read.
2. The checkpoint record carries the **published** redo (unchanged encoding —
   `EncodeCheckpointCompat`'s redo field just gets the earlier value).
3. **Re-key the FPI decision to the published pointer — option (b) is
   MANDATORY** (rev 2; adversarial review F-1 rejected option (a)):
   - **(b) — adopted, rev 4 token form**: the per-record test is
     `slot.nativeImageLSN <= publishedRedo` inside `MarkDirtyChangeRecord` /
     `MarkDirtyLogicalChange` / `maybeEmitFPI`, where `nativeImageLSN` is a
     slot-level watermark advanced ONLY by native image emission (logFPI,
     the WithLSN variants' image-bearing multi-page records, ForceFPI) and
     zeroed on slot reuse. No sweep, no separately-timed reset — publication
     IS the epoch boundary. **Why not raw `pd_lsn` (rev 3's shape)**: pd_lsn
     is stamped by BOTH record families, so a canonical record's stamp
     satisfied the test and suppressed the native first-touch image the
     native replay depends on — the same cross-family poisoning class the C1
     review rejected as F1, rediscovered live by the S3a crash-sim tests
     (single-DDL crash lost the table). FSM excluded (snapshot-persisted,
     never WAL-logged); VM keeps its own native record.
   - **(a) — rejected**: "clear the per-slot bool at publication time" is
     unimplementable as an atomic event: `ResetCheckpointEpoch` is a
     **lock-free O(nslots) sweep** (bufpool.go:776-782, plain
     `atomic.Bool.Store(false)` per slot) racing concurrent writers with no
     pool-wide lock. A writer hitting slot j between publication and j's
     clear reads the stale `true` → emits an image-less record with
     `LSN > redo` → Window A merely shrinks to sweep-duration, it does not
     close. Since the load-bearing catalog-insert path
     (`MarkDirtyLogicalChange`) sits exactly in that window (doc 02 §1a),
     shrink-not-close is unacceptable.
4. **Publication barrier (rev 3 — implementation review F1)**: option (b)
   alone still left a decide→append race: a writer whose `needsImage` ran
   against the OLD redo could be descheduled, the publication land, and its
   record then append at an LSN ≥ the NEW redo with no image (PG guards this
   with the `fpw_lsn` recheck under the WAL insert locks,
   xlog.c XLogInsertRecord). Implemented as `Pool.fpiPublishMu`
   (sync.RWMutex): the three MarkDirty* variants hold RLock across
   decision→append; `PublishRedoBarrier(sample)` takes the exclusive lock,
   THEN samples the WAL frontier, then stores — so every straddling writer's
   record is below the sampled frontier and covered by the previous epoch's
   image on any replay from the new redo. `MarkDirtyForceFPI` needs no lock
   (it always images). Pinned by
   `storage.TestPublishRedoBarrierWaitsForInFlightDecision`.
5. **Ordering invariant (restated)**: publish redo (under the barrier) →
   flush data+CLOG (flush now trivially covers everything ≤ redo) → append
   record with published redo → pg_control. `FlushCLOGFn`'s
   error-fails-checkpoint contract is untouched (C2's dependency,
   perf-optimize3/05/02 §I3).

### Interaction with C2 (CLOG fsync removal)

C2's invariant "pg_xact on disk covers everything before redo" is *stronger*
under the new order: redo is chosen first, the flush happens after, so the
flush covers ≤ redo a fortiori. No C2 change needed; its S-numbering simply
inherits this fix if C2 lands after S2 (both designs reference the same
invariant text).

## 4. Failure modes closed / introduced

- Closed: Window A (image-less post-redo incrementals) — fully, because the
  per-record test has no reset sweep to race; Window B (pre-redo mutations
  lost to replay yet evictable-torn).
- Introduced: none functional. Cost: one atomic load + `pd_lsn` read per
  MarkDirty (option (b)); pages dirtied during the flush phase re-image on
  next touch (they always should have); at 24 h checkpoints this is noise.
- The old `fpiSinceCheckpoint` bool and `ResetCheckpointEpoch` are retired
  once (b) lands (keep temporarily behind the same slice for revert).
- Crash **during** checkpoint: the record never lands → recovery uses the
  previous checkpoint's redo; the early-published pointer is process-local
  state, never persisted except via the record — no new crash state.

## 5. Verification

- **New regression test** (the window test): force the old-order scenario —
  page dirty in epoch E−1, checkpoint runs, page modified in the
  redo…reset window, crash + evict-torn simulation, replay — must produce a
  correct page. Under the old code this FAILS with a native-only stream.
  Include the **catalog variant** (doc 02 §1a: pg_attribute page dirtied in
  E−1, catalog insert in-window — the worst-blast-radius instance).
- Full G-crash both modes; `TestKillKillRecovery`; goopg→goopg physical
  replication e2e (standby replays the reordered stream); G-race.
- pg_waldump W-001 (structural) unaffected — record shapes unchanged, only
  the redo value inside the checkpoint record shifts earlier.

## 6. Open questions (flagged)

- **O-03-1**: exact publication mechanism — a new atomic on the Pool
  (readable at one-atomic-load cost from every `MarkDirty*`), seeded at
  startup from the last checkpoint's redo.
- **O-03-2**: does the checkpointer's `redoLSN0` page-header adjustment
  (:508-523) need re-derivation when sampled pre-flush? (The adjustment logic
  moves with the sample; verify the segment-boundary edge cases it comments.)

## Rev 5 (2026-07-13, S4 gate finding): watermark must survive slot eviction

The rev-4 `nativeImageLSN` watermark lives on the buffer-pool Slot. On slot
eviction + reload the watermark reset to zero, re-arming first-touch imaging
for a page that already has an image in the WAL since redo — a hot
sys-catalog page cycling through the pool re-imaged on every reload. Observed
in the S4 gate run (regress suite): 55,838 images = 97.5% of 497MB retained
WAL, one page imaged 19,306 times; the flooded WAL turned a 212s checkpoint
into a never-completing one and wedged the server.

Fix: `Pool.evictedImageLSN` map (bufpool.go) — stash the watermark under the
page's BufferTag before re-tagging the slot, consume it when the page is
re-tagged back in, delete on PinNew (truncate-then-re-extend must re-arm),
clear wholesale inside `PublishRedoBarrier` (stale entries are <= the old
redo and would re-image against the new redo anyway). Errs only toward extra
images (a consumed-then-lost stash on a bmInsert race re-images once).
Pinned by `TestFPIWatermarkSurvivesEviction` (fpi_redo_window_test.go).

PG needs no analog: its decision reads `pd_lsn`, which lives on the page and
survives eviction; goopg cannot use raw `pd_lsn` because canonical-family
stamps poison it (rev 4).
