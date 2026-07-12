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
   - **(b) — adopted**: replace the per-slot bool with a per-record
     `page_lsn <= publishedRedo` test inside `MarkDirtyChangeRecord` /
     `MarkDirtyLogicalChange` / `maybeEmitFPI`, against the single
     atomically-published pointer. No sweep, no separately-timed reset event
     — publication IS the epoch boundary, exactly PG's shape. Scope: the test
     applies to heap/btree/TOAST main-fork pages (which carry `pd_lsn`);
     **FSM is excluded** (snapshot-persisted, never WAL-logged — unchanged by
     this bundle) and **VM** keeps its own native record
     (`RecordKindHeapVisible`).
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
4. **Ordering invariant (restated)**: publish redo → flush data+CLOG (flush
   now trivially covers everything ≤ redo) → append record with published
   redo → pg_control. `FlushCLOGFn`'s error-fails-checkpoint contract is
   untouched (C2's dependency, perf-optimize3/05/02 §I3).

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
