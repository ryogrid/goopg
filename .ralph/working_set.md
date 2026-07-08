Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. This (6th) loop REFUTED the 5th loop's
"split/redistribution" localization with direct evidence: the loss happens
via the FAST-PATH single-item insert path, not insertIntoBlock's split
branch. See fix_plan.md's 6th-loop update + deferral ledger's 6th
2026-07-08 row for full detail.

Files: internal/access/btree/btree.go (NEW `BTree.RewriteLogEvent` +
`traceRewrite` + `RewriteLogRecordsForBlock` + `RewriteSnapshotHasTID` —
deep-copy snapshots of `allItems` right after pageItems()+appendSorted()
and right after dedupConsolidate(), for every insertIntoBlock rewrite
(dedup-recovery no-split path AND real split path); `insertLog` and the
new `rewriteLog` now share one monotonic `logSeqNext` counter instead of
each using len()-based Seq, so events from both logs compare for true
temporal order). internal/amcheck/verify_nbtree_realtree_test.go
(per-missing-entry diagnostic extended to call `RewriteLogRecordsForBlock`
and report presence/absence at each snapshot; doc comment on
TestVerifyBtreeEngineSilentOnRealConcurrentContended extended with this
loop's finding; test re-skipped, un-skip locally to re-run).
.ralph/fix_plan.md + .ralph/deferral_ledger.md updated.
KEEP RewriteLogEvent/traceRewrite: reusable, zero-cost-when-off, same
pattern as DebugTraceInserts/DebugTraceSlotEvents/DebugValidateCleanEvictions.

Key symbols for next step: internal/access/btree/btree.go's
`tryInsertNoSplit` (~1578-1619), `insertIntoBlock`'s own no-split fast path
(the `if pageHasSpaceFor(...) { lineIdx := insertItemSorted(...) ...}`
branch near the top of insertIntoBlock, before the split logic),
`tryInsertOnCachedRightmost` (currently believed dead code per the 8th
2026-07-07 loop — NOT re-verified this loop). `storage.PageInsertItemRawAt`
(internal/storage/heap.go:652) — re-read this loop, arithmetic looks
internally sound for a single isolated call.

Hypothesis/Findings: DEFINITIVE this loop — every rewrite event (dedup-
recovery or split) on a block that later loses an entry shows
presentAfterPageItems=false with postPageItemsCount == preLineCount+1
(matches PageLinePointerCount exactly, no undercount) — meaning
pageItems() correctly reads whatever IS physically on the page; the page
ITSELF already lacks the lost entry's line pointer before any rewrite
touches it. Several missing entries have NO rewrite event at all after
their insert (block never split again) yet still lose the entry — so the
loss must happen via ordinary same-page fast-path insertItemSorted calls,
not the split/dedup rebuild. This REFUTES the 5th loop's headline
conclusion (which was itself evidence-based, just wrong once tested
further — the "no second insertItemSorted call for that TID anywhere"
finding was real, but its interpretation as "must be the split rewrite"
was the wrong branch). RULED OUT this loop: a plain data race — ran this
EXACT contended-duplicate-key repro under `-race` (GORACE=halt_on_error=0
to run to completion) for the FIRST TIME in this whole investigation
thread (all 20+ prior -race-clean runs used the DISJOINT-key
TestMultiWriterStress_M0055_Phase_C repro instead, which never touches
this narrow-key-contention page-sharing pattern) — exactly ONE race fired,
inside dumpCrossSlotEventsForTag/traceSlotEvent (bufpool.go), the
DebugTraceSlotEvents debug tool's own cross-slot ring scan; its doc
comment already documents same-slot torn reads as an accepted best-effort
tradeoff, and the cross-slot case is a stricter variant of the same
accepted tradeoff, not a new bug class — it does not explain or correlate
with the missing entries. No other race fired. This is the THIRD
independent confirmation (different repros/tooling each time) that this
is a pure protocol/ordering logic bug in properly-synchronized code, not
a torn/unsynchronized memory access. PageInsertItemRawAt itself (re-read
this loop) is internally consistent for one isolated call: reads
Header(p) once, derives count/lower from it, unconditionally sets
Lower() to lower+itemIDSize on success — a bug here would need a second
concurrent mutator of the SAME page bypassing pinW's exclusive lock,
which the clean -race run makes unlikely (though not impossible — a
logic-level double-entry rather than a byte-level race could still evade
the race detector).

Next step: apply the SAME before/after pageItems()-snapshot technique
built this loop, but to the fast-path single-item insertItemSorted call
sites (tryInsertNoSplit, insertIntoBlock's own no-split branch,
tryInsertOnCachedRightmost) instead of the rewrite path. Concretely: for
the specific blocks already known (post-hoc, from this loop's diagnostic
— e.g. blk=285 seq~394620, blk=311 seq~421999, blk=31, blk=139, blk=196,
etc., new run will differ) to lose an entry, snapshot pageItems() (or at
minimum a hash/count) immediately before and immediately after each
fast-path insertItemSorted call touching that block, to find the exact
consecutive pair where a previously-present (key,TID) silently
disappears between two calls that never reset the page. This is
expensive per-call (full page decode) so it's fine to gate it to only
active AFTER a first pass has identified which blocks matter (two-phase:
first run un-gated DebugTraceInserts/RewriteLogEvent as-is to find which
blocks lose entries with NO rewrite event, then re-run with a NEW,
block-filtered fast-path snapshot flag active only for those specific
block numbers to keep the trace affordable). Do NOT re-open the
split/rewrite path (this loop's RewriteLogEvent instrumentation
conclusively clears it) or re-run -race on the disjoint-key repro
(already exhaustively clean, ~1180+ iterations across loops 1-5 of the
prior investigation thread).

Gates run this loop (all PASS): go build ./...; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...;
go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (target test re-skipped by design, ran clean);
go test -race -run TestVerifyBtreeEngineSilentOnRealConcurrentContended
./internal/amcheck/... (run manually with the test temporarily un-skipped,
for investigation only — NOT part of the committed gate set, re-skipped
before commit); scripts/tpch-spotcheck.sh (Q12=2/Q13=33 PASS); make
ralph-state-guard pending (run before finishing this loop, see below).

In-flight: none. No background processes left running. The test file's
temporary un-skip used during this loop's investigation was reverted
(t.Skip restored with an updated message) before the gates above were
re-run and before commit.
