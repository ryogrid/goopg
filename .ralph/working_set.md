Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. This (8th) loop built flush-time instrumentation
and found the CLEANEST signature yet: all 18/18 missing entries in a fresh
repro are present at the first post-insert flush of their block and already
gone by the very next flush of that block. See fix_plan.md's 8th-loop update
+ deferral ledger's 8th 2026-07-08 row for full detail.

Files: internal/storage/bufpool.go (NEW `Pool.OnFlushSnapshot func(tag,
page)` field, nil by default; wired in `flushSlot` immediately before the
`WriteBlock` call). internal/access/btree/btree.go (NEW `BTree.
DebugTraceFlushes` field + `FlushSnapshotEvent` struct + `RecordFlushSnapshot`
method (matches `OnFlushSnapshot`'s signature, filters to `bt.rel`, decodes
`pageItems()`, shares `logSeqNext`) + `FlushSnapshotRecordsForBlock` getter).
internal/amcheck/verify_nbtree_realtree_test.go (buildRealTreeConcurrent now
sets `bt.DebugTraceFlushes=true` + `pool.OnFlushSnapshot = bt.
RecordFlushSnapshot`; per-missing-entry diagnostic extended with a
flush-snapshot cross-reference block; doc comment extended with this loop's
finding; test re-skipped with an updated skip message, un-skip locally to
re-run). .ralph/fix_plan.md + .ralph/deferral_ledger.md updated.
KEEP OnFlushSnapshot/RecordFlushSnapshot/DebugTraceFlushes: reusable,
zero-cost-when-off, same pattern as the other DebugTrace*/DebugValidate*
aids in this thread (DebugTraceInserts, DebugVerifyFastPathInserts,
DebugValidateCleanEvictions, DebugTraceSlotEvents).

Key symbols for next step: internal/storage/bufpool.go's `pinLoad` (the
cache-miss reload path invoked from `Pin`/`PinNew` when a tag isn't already
resident) and `Manager.ReadBlock` (internal/storage/smgr.go) — NEITHER is
yet instrumented to snapshot what bytes a reload actually reads and compare
against the last known-good FlushSnapshotEvent for that exact block. This is
the one remaining candidate mechanism in the loss window (see Hypothesis
below).

Hypothesis/Findings: DEFINITIVE this loop — wrapped `flushSlot` (called from
both `evictVictim`'s dirty branch AND the bgwriter's `WriteDirtyPages`) with
an `OnFlushSnapshot` hook giving the exact tag+bytes about to hit disk;
`BTree.RecordFlushSnapshot` decodes `pageItems()` from those bytes when the
tag belongs to this BTree's relation. Re-ran the 200000-insert/64-writer
repro: it lost 18 real entries this run, and for EVERY SINGLE ONE (not a
subset — this is the first loop in the whole 8-loop thread to check ALL
missing entries, not just 1-2 spot checks), the exact same signature held:
the entry IS present in the earliest flush-snapshot of its block recorded
after its insert, and is ALREADY ABSENT in the very next flush-snapshot of
that same block, often only 40-800 `Seq` ticks later (smallest gap seen:
seq 235964->236047 for TID={57,1107} on blk=110, 83 ticks). Item counts
between the two flushes are NOT always a clean -1 (sometimes +1, e.g.
332->333 for blk=192) because other writers' concurrent fast-path inserts
land in the same window — but our specific entry is always gone by the
second flush regardless. This RECONCILES the 7th loop's zero-
`FastPathViolation`s result (previously read as "fast-path is clean, so the
loss must be in the unpin/re-pin gap somewhere") into something sharper: the
entry is ALREADY missing by the time ANY fast-path call next touches the
reloaded page, so of course no fast-path call's own before/after check ever
sees a violation — the violation already happened, upstream of the fast
path, during the reload itself. Combined with the already-cleared rewrite
path (6th loop), clean-eviction path (4th loop), fast-path insert sites (7th
loop), claimVictim's pin-exclusion (this loop, quick audit), and now the
dirty-flush WRITE side (this loop — flushes always faithfully write
whatever bytes are in memory, confirmed 18/18), the ONLY remaining
uninstrumented step in the eviction cycle is the READ side: does `pinLoad`'s
cache-miss branch (or `Manager.ReadBlock` underneath it) ever serve stale or
wrong bytes for a block that was just correctly flushed?

Next step: instrument `Pool.pinLoad`'s cache-miss reload branch (bufpool.go)
— or `Manager.ReadBlock` itself (smgr.go) if it's easier to hook centrally —
to snapshot `pageItems()` on every block reload for a tag belonging to
`bt.rel`, gated by a new `BTree.DebugTraceReloads` (or similar) flag
following the exact same wiring pattern as `RecordFlushSnapshot`
(`pool.OnBlockReload = bt.RecordReloadSnapshot`, a new `Pool.OnBlockReload
func(tag BufferTag, page Page)` hook mirroring `OnFlushSnapshot`, fired
right after the disk read completes and before the slot is published/
Pin-returned). Then extend the per-missing-entry diagnostic with a third
cross-reference: find the reload event on the lost entry's block
immediately after the "present" flush-snapshot and immediately before the
"absent" flush-snapshot already logged this loop, and check whether pageItems()
on THAT reload already lacks the entry — if so, this is the smoking gun (a
stale/wrong read), and the very next step after that would be comparing the
reloaded bytes against what disk actually holds via a raw re-read (like
`debugValidateCleanEviction` already does for the clean-eviction path) to
determine whether the read itself is buggy or the disk write from the prior
flush never actually landed before the reload raced it. Do NOT re-open
claimVictim, the fast-path insert sites, the split/dedup-rewrite path, the
clean-eviction path, or the dirty-flush write side — all five conclusively
cleared across loops 4, 6, 7, and this loop.

Gates run this loop (all PASS): go build ./...; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...;
go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (target test re-skipped by design, ran clean);
go test -v -run TestVerifyBtreeEngineSilentOnRealConcurrentContended
./internal/amcheck/... (run manually with the test temporarily un-skipped,
for investigation only — NOT part of the committed gate set, re-skipped
before commit; confirmed 18 missing entries, 0 FastPathViolations, 18/18
flush-snapshot LOCALIZED hits); scripts/tpch-spotcheck.sh (Q12=2/Q13=33
PASS); make ralph-state-guard pending (run before finishing this loop, see
below).

In-flight: none. No background processes left running. The test file's
temporary un-skip used during this loop's investigation was reverted
(t.Skip restored with an updated message) before the gates above were
re-run and before commit.
