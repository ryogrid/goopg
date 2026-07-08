Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. This (7th) loop REFUTED the 6th loop's proposed
fast-path-insertItemSorted mechanism with a clean negative result, and
pinned the loss window down to an unpin/re-pin gap on the same block —
i.e. it happens during (or across) a buffer-pool eviction, most likely the
DIRTY flush-then-evict path (never yet directly instrumented). See
fix_plan.md's 7th-loop update + deferral ledger's 7th 2026-07-08 row for
full detail.

Files: internal/access/btree/btree.go (NEW `BTree.DebugVerifyFastPathInserts`
field + `FastPathViolation` struct + `insertItemSortedVerified` +
`checkFastPathSurvivors` + `FastPathViolations()` getter — wraps all 3
fast-path single-item insertItemSorted call sites with a pageItems()
before/after snapshot; zero cost when the flag is off). internal/amcheck/
verify_nbtree_realtree_test.go (buildRealTreeConcurrent now also sets
bt.DebugVerifyFastPathInserts=true; per-run unconditional FastPathViolations
dump + per-missing-entry cross-reference added to
TestVerifyBtreeEngineSilentOnRealConcurrentContended; doc comment extended
with this loop's finding; test re-skipped, un-skip locally to re-run).
.ralph/fix_plan.md + .ralph/deferral_ledger.md updated.
KEEP DebugVerifyFastPathInserts/insertItemSortedVerified: reusable,
zero-cost-when-off, same pattern as DebugTraceInserts/RewriteLogEvent/
DebugTraceSlotEvents/DebugValidateCleanEvictions.

Key symbols for next step: internal/storage/bufpool.go's `evictVictim`
(~1123-1185, the `wasDirty` branch that calls `flushSlot`) and `flushSlot`
itself — NOT yet instrumented to compare "bytes about to be written to
disk" against "the last known-good in-memory pageItems() snapshot" for the
same block. Also `claimVictim` (bufpool.go) — NOT yet re-verified this loop
whether it correctly excludes slots with pinCount>0 from victim selection
(a quick audit worth doing before building new instrumentation).

Hypothesis/Findings: DEFINITIVE this loop — wrapped all 3 fast-path
single-item insertItemSorted call sites (tryInsertNoSplit, insertIntoBlock's
no-split branch, tryInsertOnCachedRightmost) with a pageItems() snapshot
immediately before and after each call, asserting every pre-existing
(key,TID) survives (a plain insert only ever adds a line pointer). A full
200000-insert/64-writer run that still lost 12 real entries recorded ZERO
violations — every fast-path call at every site preserved every
pre-existing entry in its own pre/post pair. This REFUTES the fast-path
hypothesis (6th loop's proposed next mechanism) just as cleanly as the 6th
loop refuted the 5th loop's split/rewrite hypothesis. For one traced
example (key=47087, TID={3,1508}, inserted blk=16 lineIdx=6 seq=216430,
564 later inserts touch blk=16, one split event at seq=315252 whose own
pageItems() read already lacked it — per the 6th loop's RewriteLogEvent
data, re-confirmed this loop): the entry survived every fast-path call's
own check up to blk=16's LAST fast-path touch before the split, then
vanished in the gap between that call's unpinW and the split-triggering
call's pinW+pageItems() read. Since pinW/unpinW correctly serialize access
to a PINNED page (confirmed clean under -race, 6th loop and earlier), the
only thing that happens while NOBODY holds a pin on a block is a
buffer-pool eviction (page leaves the pool, possibly gets flushed to disk,
and is reloaded fresh on the next pin). pool.DebugValidateCleanEvictions
(loops 3-4, still active this run) fired once but on an UNRELATED block
(133, not 16) — consistent with loop 4's "mismatch-firing and
missing-entry count are uncorrelated" finding. That instrumentation only
checks the `!wasDirty` (skip-flush) eviction fast path; the DIRTY
flush-then-evict path (evictVictim's `wasDirty` branch, which calls
flushSlot) has NEVER been directly instrumented to verify the bytes it
writes to disk actually match the last known-good in-memory content for
that block. This is now the leading, still-unproven hypothesis.

Next step: instrument evictVictim's dirty branch (or flushSlot itself,
bufpool.go ~1123-1185) to snapshot pageItems() on the page being flushed
immediately before the WriteAt call, keyed by block+seq, and compare
against the most recent fast-path/rewrite/violation "post" snapshot
already recorded for that block — insertLog, rewriteLog, and
fastPathViolations all share one logSeqNext counter, so a caller can
determine true temporal order across all of them plus a new flush-time
log. A mismatch there would directly catch a stale/torn flush. Before
building that (cheaper first pass): re-verify claimVictim actually
excludes slots with a nonzero pin count from victim selection — if it
doesn't, that alone is the bug, independent of flush correctness. Do NOT
re-open the fast-path single-item insertItemSorted call sites (this
loop's DebugVerifyFastPathInserts instrumentation conclusively clears
them) or the split/dedup-rewrite path (6th loop conclusively cleared it).

Gates run this loop (all PASS): go build ./...; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...;
go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (target test re-skipped by design, ran clean);
go test -v -run TestVerifyBtreeEngineSilentOnRealConcurrentContended
./internal/amcheck/... (run manually with the test temporarily un-skipped,
for investigation only — NOT part of the committed gate set, re-skipped
before commit; confirmed 12 missing entries, 0 FastPathViolations);
scripts/tpch-spotcheck.sh (Q12=2/Q13=33 PASS); make ralph-state-guard
pending (run before finishing this loop, see below).

In-flight: none. No background processes left running. The test file's
temporary un-skip used during this loop's investigation was reverted
(t.Skip restored with an updated message) before the gates above were
re-run and before commit.
