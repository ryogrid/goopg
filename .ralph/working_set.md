Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. This (5th) loop PROVED the dominant loss
mechanism is inside internal/access/btree's split/redistribution rewrite
(insertIntoBlock's split branch), not the storage/eviction layer — with
direct evidence, not inference. See fix_plan.md's 5th-loop update + deferral
ledger's 5th 2026-07-08 row for full detail.

Files: internal/access/btree/btree.go (NEW `BTree.DebugTraceInserts bool`
+ `insertLog []btreeInsertLogEvent` + `traceInsert`/`InsertLogRecordsForTID`/
`InsertLogRecordsForBlockAfter` — an unbounded global log of every
insertItemSorted call (block, 0-based line-pointer idx, key, TID); wired
into ALL 6 insertItemSorted call sites (tryInsertNoSplit, insertIntoBlock's
no-split fast path, insertIntoBlock's dedup-recovery rebuild loop,
insertIntoBlock's split left/right fill loops, tryInsertOnCachedRightmost);
insertItemSorted itself now RETURNS the 0-based lineIdx it inserted at
(callers use it for tracing). internal/storage/bufpool.go (NEW
`Pool.DumpEventsForTag(tag) []string` — exported, string-returning sibling
of the existing private `dumpCrossSlotEventsForTag`, for cross-package
lookups). internal/amcheck/verify_nbtree_realtree_test.go
(buildRealTreeConcurrent now also returns `*btree.BTree` and sets
`bt.DebugTraceInserts = true`; TestVerifyBtreeEngineSilentOnRealConcurrentContended's
missing-entry loop extended with a full diagnostic: insert-log lookup,
later-insert-to-same-block count + reappearance check, CURRENT on-disk
same-key-entries dump, storage-event dump — all only active when the test
is un-skipped; test re-skipped, doc-comment extended with this loop's
findings). .ralph/fix_plan.md + .ralph/deferral_ledger.md updated.
KEEP all new instrumentation: reusable, zero-cost-when-off, same pattern
as DebugTraceSlotEvents/DebugValidateCleanEvictions.

Key symbols for next step: internal/access/btree/btree.go's
`insertIntoBlock` (~line 1461-1703, especially the split branch ~1523-1574:
`pageItems`, `appendSorted`, `dedupConsolidate`, the left/right
`insertItemSorted` refill loops) — this is now the CONFIRMED location of
the bug, not a hypothesis. `bt.InsertLogRecordsForBlockAfter(blk, seq)` /
`bt.InsertLogRecordsForTID(tid)` (already built, reusable without new
instrumentation) to correlate a specific lost entry against the block's
full write history.

Hypothesis/Findings: DEFINITIVE this loop (hard evidence, not inference):
for every missing entry in a fresh repro — (1) insertItemSorted was called
EXACTLY ONCE for that (key,TID), confirmed physically written
(PageInsertItemRawAt panics on failure; none occurred); (2) a global scan
of the ENTIRE run's insert log for that TID across ALL blocks found no
second occurrence anywhere — rules out "moved to a new block during a
later split" (which would log a second insertItemSorted call with the same
Ptr); (3) the origin block received hundreds more successful inserts
afterward (healthy, not abandoned/evicted); (4) the block's CURRENT
on-disk bytes at test end genuinely lack the TID — in several cases with a
SIBLING entry sharing the identical duplicate key still present, proving a
single-entry drop during a page rewrite, not a whole-page loss. Since a
plain insertItemSorted call only ADDS a line pointer (shifts others, never
deletes existing tuple bytes), the only mechanism that can make a
previously-written entry vanish without a trace is insertIntoBlock's split
branch: resetPageItems wipes the line-pointer array, then
pageItems+appendSorted+dedupConsolidate recompute the survivor set from
scratch and reinsert it split across left/right. RULED OUT this loop (by
code reading, not new instrumentation): postings (marshalPosting is never
invoked from the online Insert path — dedupConsolidate's own comment says
so, confirmed no call site reachable from Insert); a concurrent-root-lift
race (bt.splitMu fully serializes the ENTIRE split-path recursion —
descendToLeaf + insertIntoBlock + createNewRoot all run under one lock, so
the createNewRoot/finishSplit defensive "meta.Root != leftBlk" branches are
for a future splitMu-removal stage, unreachable today). Re-read
pageItems/dedupConsolidate/appendSorted function-by-function — no
single-threaded logic flaw spotted by inspection alone (dedup only drops
EXACT (key,ptr) duplicates via the standard safe in-place `items[:0]`
filter idiom; appendSorted is a plain sorted binary-search insert into a
copy). All 4 prior loops' eviction-path ruling-out (bufmap/relFile/arena/
claimVictim/evictVictim/pinLoad/PinNew) remains valid and should NOT be
re-derived.

Next step: instrument insertIntoBlock's split branch directly (gated by
DebugTraceInserts or a sibling bool) — log PageLinePointerCount(slot.Page())
immediately before the split starts, len(allItems) right after
pageItems(), and len(allItems) again after dedupConsolidate(allItems), for
whichever block a lost entry's original insert targeted. Use
bt.InsertLogRecordsForBlockAfter(blk, seq) to find the FIRST split burst
after the lost insert (identifiable as a tight run of consecutive
insertItemSorted calls with monotonically-increasing lineIdx starting from
0 — the left-refill loop's signature, distinct from steady-state
single-item inserts scattered across a wide seq range) and check whether
the lost key is present in that exact pageItems() read. If pageItems()
already undercounts at that moment, the bug is in the page read/decode
(check for a torn/stale read despite pinW's exclusive lock — would be a
NEW, distinct storage-layer finding after 4 loops of clean audit there).
If dedupConsolidate receives the correct full set but emits fewer, re-audit
it for a three-or-more-way-duplicate-run edge case despite this loop's
structural read finding it sound.

Gates run this loop (all PASS): go build ./...; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...;
go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (target test re-skipped by design, ran clean);
scripts/tpch-spotcheck.sh (Q12=2/Q13=33 PASS); make ralph-state-guard
(self-repaired a stale status/progress marker, then reported OK). Pre-commit
pgbench smoke gate + final commit still pending as of this write (run them
before finishing the loop).

In-flight: none. No background processes left running. The test file's
temporary un-skip used during this loop's investigation was reverted
(t.Skip restored with an updated message) before commit;
DebugTraceInserts=true is left ON in buildRealTreeConcurrent permanently,
matching the DebugValidateCleanEvictions/DebugTraceSlotEvents precedent —
intentional, not leftover WIP.
