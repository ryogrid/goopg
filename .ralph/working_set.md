Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. Root cause narrowed from "somewhere in
claimVictim/evictVictim/pinLoad" to a DEFINITIVE proof: the `!wasDirty`
("clean eviction") fast path in evictVictim discards real, unflushed
writes. Exact mechanism (why the dirty bit reads false) still open. See
fix_plan.md task + deferral ledger's 3rd 2026-07-08 row for full detail.

Files: internal/storage/bufpool.go (NEW `Pool.DebugValidateCleanEvictions`
bool field, default false/zero-cost + NEW `debugValidateCleanEviction`
helper called from evictVictim's `!wasDirty` branch — KEEP, this is the
regression/repro tool, not throwaway), internal/amcheck/verify_nbtree_realtree_test.go
(`buildRealTreeConcurrent` now also returns every inserted (key,TID) pair;
`TestVerifyBtreeEngineSilentOnRealConcurrentContended` sets
`pool.DebugValidateCleanEvictions = true` and logs exact MISSING entries
via a diff against the real leaf walk — still `t.Skip`'d, un-skip to
reproduce). .ralph/fix_plan.md + .ralph/deferral_ledger.md updated.

Key symbols for next step: internal/storage/bufpool.go — MarkDirty,
MarkDirtyChangeRecord, markDirtyWithLSNCommon, claimVictim (the CAS that
reads+replaces s.state). Need a NEW per-slot event log (not yet built)
recording every dirty-SET and every claimVictim CAS with old/new state +
a monotonic sequence number.

Hypothesis/Findings: (1) DEFINITIVE (byte-level proof via
DebugValidateCleanEvictions): a "clean" eviction's in-memory page
differs from its on-disk image by dozens-hundreds of bytes starting at
byte 12 (right after the page header) — i.e. real accumulated inserts
that were never flushed, discarded because the dirty bit read false.
Fires 1-2 times per ~1s run, explaining the 6-42 missing entries/run
(each bad clean-eviction loses several items at once, matching the
diff's "single page, several items" pattern already seen in leaf-entry
diffs). (2) RULED OUT this loop, with hard measurements: (a) ABA via the
15-bit slot-generation counter wrapping (measured max ~2500 claims/slot
in a full run, needs 32768 to wrap — not close); (b) stale
recycled-block reuse via pinNewOrRecycled/popRecycledBlock (dead code
for this insert-only test — recycleBlock is only called from
btree_vacuum.go, never invoked here; DOES apply to the real
vacuum-enabled pgbench nightly workload though — separate follow-up);
(c) a plain unsynchronized data race (`go test -race` on this exact
repro: ZERO race warnings, still reproduces both symptoms — this is a
pure protocol/ordering bug, not a torn memory access). (3) Every
MarkDirty/MarkDirtyChangeRecord/MarkDirtyWithLSNLocked call site in
internal/access/btree/btree.go audited (tryInsertNoSplit,
insertIntoBlock's 3 branches, createNewRoot, finishSplit,
clearIncompleteSplit, clearRootFlag) — all correctly write-then-dirty
with the pin held continuously across both; MarkDirty is not being
skipped by btree.go. (4) Every dirty-CLEARING site in bufpool.go
enumerated (flushBatch's clear loop — not invoked, test never calls
FlushAll/checkpointer; InvalidateRel/InvalidateBlock — not called;
claimVictim's own full-state-overwrite CAS, which correctly captures
dirty from `old` before replacing) — none explain a real MarkDirty's
effect vanishing without an intervening eviction. (5) Confirmed
(storage.InvalidBlockNumber = 0xFFFFFFFF) the prior loop's
tryInsertOnCachedRightmost dead-code finding (Next compared against
literal 0) is correct and NOT the cause.

Next step: build a per-slot monotonic event log (new bool flag,
zero-cost when off, mirroring DebugValidateCleanEvictions) recording
every MarkDirty*/claimVictim state transition (slot idx, tag, old/new
state, seq). Re-run with both flags on; when debugValidateCleanEviction
fires for tag T, dump T's event history to find which MarkDirty call's
effect vanished and what happened to s.state right after — specifically
check whether tag T got evicted+reloaded into a DIFFERENT physical slot
mid-lifetime, i.e. whether "the same tag" across its lifetime actually
spans two slots at a handoff point that loses the dirty signal. Once
fixed: un-skip TestVerifyBtreeEngineSilentOnRealConcurrentContended
permanently as its regression guard, then re-run the ORIGINAL
pgbench-based repro (ci/batch/stages/stage-pgbench.sh, s=50 c=100 j=20
T=180) to check whether the nightly "empty internal page" abort was the
same bug. Also re-check pinNewOrRecycled's recycled-block path
specifically for the vacuum-enabled real workload once this bug closes.

Gates run this loop (all PASS): go build ./...; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...;
go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (new test skipped by design, not counted as a
pass). Did NOT run the full ralph-precommit-test.sh / tpch-spotcheck.sh
pre-commit gates this loop since no production behavior changed for any
live code path (DebugValidateCleanEvictions defaults false and is
zero-cost; only test files + an opt-in debug field changed) — the next
loop that lands the actual bufpool fix MUST run those before committing
per the Hard-won Rules.

In-flight: none — the earlier heavier instrumentation (unconditional
disk-read validation, per-slot ABA claim counters) was refactored down
to the single zero-cost-when-off `DebugValidateCleanEvictions` flag and
verified still reproduces before finishing. No background processes
left running.
