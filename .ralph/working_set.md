Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. PIVOTED this loop: the "clean eviction discards
unflushed writes" mechanism (3rd loop's headline finding) is now PROVEN NOT
to be the dominant cause of the lost B-tree entries. Next loop should chase
a genuine lost btree.Insert via a structural split/redistribution race in
internal/access/btree, NOT the storage/eviction layer. See fix_plan.md's
4th-loop update + deferral ledger's 4th 2026-07-08 row for full detail.

Files: internal/storage/bufpool.go (NEW `Pool.DebugTraceSlotEvents` bool,
default false/zero-cost + `slotEventRing`/`slotEvent`/`traceSlotEvent`/
`dumpSlotEvents`/`dumpCrossSlotEventsForTag` — wired into MarkDirty,
MarkDirtyWithLSNLocked (via markDirtyWithLSNCommon), MarkDirtyChangeRecord,
claimVictim, evictVictim (clean+dirty), pinLoad-publish, PinNew-publish
(+ its "another goroutine published" discard branch), releaseVictimSlot
(+ pinLoad's "Insert failed" discard branch). Added `Slot.idx int32` (set
once in NewPool) so MarkDirty* — which only receive a *Slot — can address
the per-slot ring without pointer arithmetic. KEEP all of this: reusable,
zero-cost-when-off investigation tool, same pattern as
DebugValidateCleanEvictions), internal/amcheck/verify_nbtree_realtree_test.go
(buildRealTreeConcurrent now also sets pool.DebugTraceSlotEvents=true
unconditionally, mirroring the existing DebugValidateCleanEvictions=true;
big doc-comment above TestVerifyBtreeEngineSilentOnRealConcurrentContended
extended with this loop's findings; test itself re-skipped, unchanged
otherwise). .ralph/fix_plan.md + .ralph/deferral_ledger.md updated.
Committed as f9df1f51.

Key symbols for next step: internal/access/btree/btree.go — insertItemSorted
(loop 2 built-and-reverted a write-log hook here proving every lost item
DOES reach insertItemSorted), the split/redistribution code around
insertIntoBlock (~1420-1660, under bt.splitMu), tryInsertNoSplit,
finishSplit, clearIncompleteSplit. internal/storage/bufpool.go's new
DebugTraceSlotEvents/dumpSlotEvents (reusable now — no new instrumentation
needed to trace a specific page's storage-layer history once you know
which page a lost key landed on).

Hypothesis/Findings: (1) DEFINITIVE (this loop): a real clean-eviction
content mismatch was caught with FULL per-slot event history — the slot
had ZERO writes (no MarkDirty at all) during its occupancy of the mismatched
tag, ruling out "lost MarkDirty for THIS occupancy" as that specific
mismatch's cause. (2) RULED OUT this loop (all audited, all clean): bufmap.go
Insert/Delete/Lookup/compact (mutex-serialized, no duplicate-tag window
possible); relFile.readBlock/writeBlock/extend (smgr.go) — share ONE
per-relation r.mu, use ReadAt/WriteAt (not Seek+Read, so no torn-offset
race); Manager.relFile's single-relFile-instance-per-rel cache (mutex-
guarded map); arena.slot's non-overlapping three-index byte slicing.
(3) DECISIVE (this loop): ran the repro 3 more times, recording
(DebugValidateCleanEviction mismatches, missing-entry count) pairs:
(1,12)/(0,20)/(0,13). The run with the MOST missing entries (20) had
ZERO mismatches. Mismatch-firing and data-loss magnitude are UNCORRELATED
— the clean-eviction mechanism is at most a minor/coincidental contributor,
not the primary bug. (4) Everything from loops 1-3 about claimVictim/
evictVictim/pinLoad/PinNew/bufmap/MarkDirty* call sites remains valid (no
bug found there after 4 full loops of dedicated audit) — the eviction path
should now be considered thoroughly investigated and NOT the next place to
look.

Next step: pivot to internal/access/btree's structural write path. Reuse
loop 2's reverted insertItemSorted write-log hook (temporary instrumentation
that logs every item actually written, with page tag + slot offset), but
this time DON'T revert it — keep it live for the length of one debugging
session, cross-reference its full write log against
TestVerifyBtreeEngineSilentOnRealConcurrentContended's final leaf walk to
identify the EXACT (page tag, slot-offset) a lost key was written to. Then
call the now-available `pool.DebugTraceSlotEvents` +
`pool.dumpSlotEvents`-equivalent (may need a small helper to dump-by-tag
rather than by-victim-slot, since the tag's CURRENT slot isn't known ahead
of time — consider adding a `Pool.DumpEventsForTag(tag)` convenience that
scans all rings, similar to the existing dumpCrossSlotEventsForTag) on that
specific page's full history to see whether a LATER split/redistribution
call overwrote or dropped the item. If the storage-layer trace shows nothing
wrong for that specific page either, the bug is purely logical inside
internal/access/btree's split/redistribution arithmetic (pageItems/
parseItem/parsePostingRaw or the split-point/copy logic), not a race at
all — focus single-threaded logic review there next.

Gates run this loop (all PASS): go build ./...; go vet ./internal/amcheck/...
./internal/storage/... ./internal/access/btree/...; go test
./internal/storage/... ./internal/access/btree/... ./internal/amcheck/...
(new/updated test re-skipped by design); scripts/tpch-spotcheck.sh
(Q12=2/Q13=33 PASS); pre-commit pgbench smoke (via git commit hook, PASS,
0 failed across all 3 workloads); make ralph-state-guard (self-repaired a
stale status/progress marker from the previous loop, then reported OK).

In-flight: none. No background processes left running. The test file's
temporary un-skip + DebugTraceSlotEvents-enabling used during this loop's
investigation was reverted/finalized before commit (t.Skip restored,
DebugTraceSlotEvents=true left ON in buildRealTreeConcurrent permanently,
matching the existing DebugValidateCleanEvictions=true precedent — this is
intentional, not leftover WIP).
