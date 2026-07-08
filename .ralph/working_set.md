(idle — nothing in flight)

M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) CLOSED
this loop (14th loop on the reopen) after 13 prior investigation-only
loops. Root cause: storage.bufmap.Insert (internal/storage/bufmap.go)
stopped at the FIRST tombstone-or-empty bucket in its open-addressing
probe chain, contradicting Lookup's/Delete's own "tombstones do NOT
terminate probing" invariant — let two different buffer-pool slots
simultaneously "own" the same BufferTag, which then raced a disk reload
against a legitimate flush of the same block, permanently discarding a
write. Found via NEW direct bufmap Insert/Delete instrumentation
(storage.Pool.OnBufmapInsert/OnBufmapDelete, BTree.DebugTraceBufmap/
CheckBufmapExclusivity) — the one angle none of the 13 prior loops had
tried. Fixed Insert to scan to a true-empty terminator before deciding,
remembering the first tombstone/empty as the write target (standard
open-addressing-with-tombstones algorithm).

Verification this loop: TestBufmapInsertSkipsPastTombstoneToExistingKey
(new, confirmed fails pre-fix/passes post-fix) — go test ./internal/
storage/... ./internal/access/btree/... ./internal/amcheck/...
./internal/executor/... PASS — TestVerifyBtreeEngineSilentOnRealConcurrentContended
permanently un-skipped, 6/6 clean runs + 1 -race run (176s, zero races) —
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33) — RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh PASS (0 failed, all 3 workloads) — AND the
exact authoritative nightly repro (ci/batch/stages/stage-pgbench.sh,
scale=50 clients=100 threads=20 duration=180x3) PASS, 0 failed
transactions. docs/design/root-0005-buffer-manager.md's Concurrency Model
section updated with the new invariant. fix_plan.md task marked [x]
CLOSED with full writeup. No deferral-ledger row needed (complete fix +
permanent regression test, not a partial/deferred one).

Files modified (all committed by end of this loop): internal/storage/
bufmap.go (the fix), internal/storage/bufmap_test.go (new regression
test), internal/storage/bufpool.go (bmInsert/bmDelete wrapper +
OnBufmapInsert/OnBufmapDelete hooks), internal/access/btree/btree.go
(BufmapEvent/RecordBufmapInsert/RecordBufmapDelete/CheckBufmapExclusivity
debug aids), internal/amcheck/verify_nbtree_realtree_test.go (wired debug
aids + permanently un-skipped the repro test), docs/design/
root-0005-buffer-manager.md, .ralph/fix_plan.md.

Next step for a future loop: pick up the separate, still-open
`storage/aio-relfile-mu-bypass` fix_plan item (storage.Manager.
WriteBlockAIO/PrefetchBlock bypass relFile.mu when an AIOEngine is
attached; needs its own per-(rel,block) in-flight-AIO registry design —
not proven related to the just-fixed bug, do not conflate). Also
consider whether the new OnBufmapInsert/OnBufmapDelete debug hooks
should be left in place (zero-cost when nil, matches the existing
Debug*/On* pattern) or trimmed — current judgment: leave them, they are
now permanent regression-test infrastructure (CheckBufmapExclusivity),
same as OnFlushSnapshot/OnBlockReload before them.

Gates run this loop: go build ./... clean; go vet ./internal/storage/...
./internal/access/btree/... ./internal/amcheck/... clean; full test
suite as listed above all PASS; make ralph-state-guard PASS (auto-
repaired a stale status/progress marker from a prior loop, unrelated to
this loop's work).

In-flight: none. No background processes left running.
