Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 9th consecutive loop. LANDED a real, independently-verified
fix (vacuum's two-phase-unlink TOCTOU race) and refuted the prior loop's
leading lead, but the nightly item is STILL open — found a NEW, cheaper,
STILL-FAILING repro that proves a distinct bug survives.

Files changed (committed): `internal/access/btree/btree_vacuum.go` —
`VacuumIndexPages` now calls `unlinkEmptyLeaf` INLINE per scan-loop
iteration (was: batched into an `emptyLeaves` slice, unlinked only after
scanning the WHOLE index); `unlinkEmptyLeaf` re-verifies the leaf is
still physically empty (line-pointer count == 0) immediately after
acquiring `splitMu`, reverting the Phase-1 BTDeleted|BTHalfDead marking
if a concurrent insert repopulated it, instead of blindly unlinking and
discarding the new tuple. `.ralph/deferral_ledger.md` (2026-07-07, 9th
loop row, appended at end in correct chronological order).

Key symbols: `VacuumIndexPages`/`unlinkEmptyLeaf` (btree_vacuum.go) —
this loop's fix. `tryInsertNoSplit` (btree.go ~1400) — confirmed the
fast insert path never checks `BTHalfDead`/`BTDeleted` (that's WHY the
vacuum race was exploitable) nor `HasIncompleteSplit()` (refuted lead,
see below). `TestMultiWriterStress_M0055_Phase_C`
(multi_writer_stress_test.go:24, currently `t.Skip`'d at line 40) — THE
cheap repro for next loop: pure 32-writer/1000-inserts-each disjoint-key
stress, NO vacuum/delete at all, ~180s for `-count=400`, failed once
this loop with "btree: empty internal page" / `inserts ok=31199, want
32000` even AFTER this loop's fix and loop 8's `pinNewOrRecycled` fix.

Findings this loop:
1. REFUTED (not just re-flagged) the 8th loop's "high-value lead":
   `tryInsertNoSplit`/`tryInsertOnCachedRightmost` never calling
   `HasIncompleteSplit()`/`finishSplit` IS a real gap vs. real PostgreSQL
   (verified: `postgres/src/backend/access/nbtree/nbtinsert.c:1146`
   `Assert(!P_INCOMPLETE_SPLIT(opaque))`, enforced by `_bt_moveright`'s
   `forupdate` branch in `nbtsearch.c:290-302`) — but is NOT
   independently exploitable today because `bt.splitMu` is held for the
   ENTIRE structural window of both `Insert`'s split path and
   `finishSplit` (through the parent-insert recursion and final
   `clearIncompleteSplit`), so no second split-needing insert on the
   SAME page can ever observe the flag mid-flight. Real risk only if a
   split's parent-insert recursion errors out before clearing the flag
   (permanently stuck; no `CompleteDeferredSplits`-analogue exists to
   repair it — confirmed absent despite a comment claiming one was
   planned). Recorded as a latent gap, not the active bug.
2. LANDED: found and fixed a genuine, independently-confirmed TOCTOU in
   `VacuumIndexPages`'s two-phase design (see Files changed above) — a
   concrete, real bug regardless of whether it's THE nightly root cause.
   Verified: `go build ./...` clean; `go test -count=1
   ./internal/access/btree/...` PASS (1.9s); `go test -count=1
   ./internal/amcheck/... ./internal/executor/... -run
   "Vacuum|Btree|Index"` PASS; `go test -race -count=1
   ./internal/access/btree/...` PASS (17.0s, zero races); `go vet
   ./internal/access/btree/...` clean.
3. NEW FINDING (unresolved, high value): temporarily un-skipped
   `TestMultiWriterStress_M0055_Phase_C` and ran `-count=400` AFTER
   landing this loop's fix — still failed once ("btree: empty internal
   page"). Since this test never calls `VacuumIndexPages` at all (pure
   disjoint-key inserts, no deletes), this PROVES a third, still-open
   bug lives purely in the concurrent split/root-lift/parent-downlink-
   insert machinery, independent of both loop 8's and this loop's fixes.
   Test file fully reverted to its original skipped state before
   finishing (confirmed via `git status`/`git diff --stat` showing zero
   diff on `multi_writer_stress_test.go`).

Next step: use `TestMultiWriterStress_M0055_Phase_C` as the repro (NOT
pgbench — this one needs no server/cluster setup, just `go test`).
Un-skip it (remove the `t.Skip(...)` at multi_writer_stress_test.go:40),
run `go test -run TestMultiWriterStress_M0055_Phase_C -count=400
./internal/access/btree/...` (~180s, observed failure rate 1/400).
Since it's insert-only with disjoint keys, the bug must be in
`insertIntoBlock`'s root-lift branch (btree.go ~1636-1652),
`createNewRoot`, `clearRootFlag`, or `finishSplit`'s independent
parent-insert path — NOT vacuum- or duplicate-key-related. First
question to answer: does every internal-page mutation really go through
`insertIntoBlock` under `splitMu`, or is there some non-splitMu-guarded
path that can touch an internal page (mirroring the leaf-level
`tryInsertNoSplit` gap from finding 1)? Re-add `t.Skip` before
committing until actually fixed. Full recipe + all analysis is in
`.ralph/deferral_ledger.md`'s 2026-07-07 (9th loop) row.

Gates run this loop: `go build ./...` clean. `go test -count=1
./internal/access/btree/... ./internal/amcheck/... ./internal/executor/...
-run "Vacuum|Btree|Index"` PASS. `go test -race -count=1
./internal/access/btree/...` PASS (no race reports). `go vet
./internal/access/btree/...` clean (pre-existing unusedfunc diagnostics
on posting.go/btree.go unrelated to this loop's changes). `make
ralph-state-guard` — run before finalizing. Pre-commit hook will run the
pgbench smoke gate (`scripts/ralph-precommit-test.sh`) automatically at
commit time. No executor/planner/codec change, so no separate TPC-H
spotcheck required beyond the above.

In-flight: none. All diagnostic artifacts removed: the un-skipped
`multi_writer_stress_test.go` was restored from `/tmp/multi_writer_stress_test.go.bak`
(itself removable, not needed further). No servers, data dirs, or
background processes started this loop. The separate, unrelated live
nightly CI batch (`ci/batch/run-nightly.sh`) was not checked this loop —
not touched either way.
