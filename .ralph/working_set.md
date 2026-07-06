Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
"item length mismatch keyLen=9 total=37" / "empty internal page"
recurrence investigation (NOT resolved; 4th consecutive investigation
loop on this item, investigation-only, no functional change landed).

Files: none changed in the final commit besides `.ralph/fix_plan.md`
and `.ralph/deferral_ledger.md` (bookkeeping). Temporarily added and
FULLY REVERTED this loop (new file deleted, symbol bodies restored via
Serena `replace_symbol_body`, confirmed byte-identical via `git diff`):
`internal/storage/debugtrace_temp.go` (new file, deleted) providing
`DebugPinTrace`/`DebugPinTraceDump`/`DebugAllZero`, env-gated by
`GOOPG_PINTRACE=1`; call sites added then removed in
`internal/storage/bufpool.go`'s `Pin`/`pinSlow`/`pinLoad`/`tryPinSlot`/
`PinNew` (one trace call at each of the 5 success-return points) and
`internal/access/btree/btree.go`'s `pinR`/`descendToLeaf` (call/return/
loop-top/failure-branch-dump); `multi_writer_stress_test.go`'s
`TestMultiWriterStress_M0055_Phase_C` `t.Skip` removed then re-added.

Key symbols: `internal/access/btree/btree.go` `bt.pinR` (907),
`descendToLeaf` (1271), `pinNewOrRecycled` (645, recycle-zero-fill
lock-gap noted but NOT this bug's cause), `insertIntoBlock` (1422).
`internal/storage/bufpool.go` `Pool.Pin` (1153, fast-path CAS),
`pinSlow` (1194), `pinLoad` (1239, has TWO returns: early-tryPinSlot-
hit and final post-ReadBlock — the 2nd was never traced before this
loop), `tryPinSlot` (899), `PinNew` (1028, has 2 returns: collision-
fallback and fresh-publish). `internal/storage/smgr.go` `relFile.
extend` (720, confirmed properly `r.mu`-serialized, not the bug).

Hypothesis/Findings: PREVIOUS loop's hypothesis ("btree.go reuses a
stale *storage.Slot/.Page() handle from an earlier pin without a fresh
Pin() round-trip") is now MOOT/REDIRECTED by this loop's direct
dynamic evidence — the bug is NOT a stale reference to old content,
it's the SAME live, currently-RLock'd slot's content changing in
place. Built temporary instrumentation (GOOPG_PINTRACE=1-gated, fully
reverted after) covering ALL 5 success-return paths in Pin/pinSlow/
pinLoad/tryPinSlot/PinNew (previous loops only covered 4; pinLoad's
final post-ReadBlock return was never distinctly traced before) plus
pinR call/return and descendToLeaf loop-top. Ran
`GOOPG_PINTRACE=1 go test -run TestMultiWriterStress_M0055_Phase_C
-count=200 ./internal/access/btree/...` (repro rate jumped to ~100%
under tracing, vs. untraced ~1/150-500 — tracing itself perturbs
timing, a Heisenbug; budget -count=50 next time, not 200). Captured
one clean single-process failure (block 83, /tmp/pintrace-<pid>.log,
NOT preserved — re-capture needed):
  BTREE-PINR-RETURN blk=83 allzero=false   [seq 75631224]
  ... (descendToLeaf's very next line, `op := readOpaque(slot.Page())`,
      executes here, SAME goroutine, SAME still-held contentMu.RLock()
      from pinR — no unpinR call in between) ...
  BTREE-DESCEND-FAIL blk=83 err="btree: empty internal page"
      op={Prev:0 Next:0 Level:0 Flags:0 HighKey:[]}   [seq 75631247]
Only 23 trace-sequence-numbers apart, same goroutine, same call, same
continuously-held RLock. A sync.RWMutex cannot let a Lock()-holding
writer run concurrently with an active RLock() holder — so this PROVES
some code path mutates a pinned, RLock'd slot's `.page` byte array
WITHOUT ever acquiring `contentMu`. Audited every known locked-mutation
path this loop and found none that bypasses the lock:
`pinNewOrRecycled`'s zero-fill (confirmed NOT exercised at all by this
test — it's insert-only, no deletes/vacuum ever populate `bt.freeList`,
so `popRecycledBlock` always returns false and `pinNewOrRecycled`
always falls through to fresh `PinNew`), `PinNew`'s InitPage/Extend/
publish sequence, `relFile.extend`'s `r.mu`-serialized block-number
assignment (rules out duplicate block numbers), `claimVictim`/
`evictVictim`'s `statePin(old) != 0` pinned-slot exclusion (a pinned
slot can never be claimed as a victim), `InvalidateBlock`/
`InvalidateRel`'s same pinned-slot check, `bufmap.Delete`/`Lookup`'s
seqlock-style bucket protocol (looks correct). The actual unlocked-
write call site is still unidentified — this loop's contribution is
proving WHAT class of bug it is (unlocked write to a live pinned
slot), ruling out the prior loop's "stale handle" theory, not WHERE.
Separately noted (NOT this bug, latent/lower-priority): (a)
`pinNewOrRecycled` releases `slot.Lock()` after its zero-fill and the
caller (`insertIntoBlock`) only re-`Lock()`s several lines later before
`initPage` — a real gap once a VACUUM-concurrent-with-insert repro
exists; (b) `recycleBlock` has no PG-style safe-recycle deferral
(compare `_bt_pendingfsm_add`/`_bt_pendingfsm_finalize` in
postgres/src/backend/access/nbtree/nbtpage.c) before a page-deleted
block re-enters `bt.freeList`.

Next step: re-apply the SAME env-gated (`GOOPG_PINTRACE=1`)
instrumentation (recipe: this file's "Files:" section above + the 2nd
2026-07-07 deferral ledger row has the exact diff shape) to
`internal/storage/bufpool.go`'s 5 success-return points +
`internal/access/btree/btree.go`'s pinR/descendToLeaf, but ALSO add
trace calls to every WRITE-side page mutator: `insertItemSorted`,
`resetPageItems`, `writeOpaque`, `initPage` (btree.go), `InitPage`
(storage/page.go), and the `pinNewOrRecycled` zero-fill loop — each
logging slotIdx + calling-goroutine identity (use a per-goroutine
counter/tag if `runtime.Goexit`-style IDs aren't available; a simple
approach: pass a unique per-writer-goroutine int down through the
stress test's `wid` isn't visible from btree.go, so tag by a
monotonically increasing call-sequence number and correlate by
timing/slotIdx instead). Cross-reference against the confirmed zeroing
window on block 83 (this loop's session only — the /tmp/pintrace log
was deleted, not committed; re-capture is required, it reproduces
easily). Budget `-count=50`, not 200-500, since tracing inflates the
failure rate. If a specific unlocked writer is found, THEN it's safe to
attempt a fix (lock it properly) — do not fix blind.

Gates run this loop: go build ./... clean (before AND after full
revert); go test -count=1 ./internal/storage/... ./internal/access/
btree/... PASS (baseline, after revert); make ralph-state-guard OK. No
executor/planner/codec changes, so no TPC-H spotcheck required
(investigation + docs only).

In-flight: none — all temporary instrumentation was fully reverted
(new file deleted; 7 symbol bodies restored to their pre-loop content
in internal/storage/bufpool.go and internal/access/btree/btree.go,
confirmed via `git diff` showing zero changes to either file; test
skip re-added) before this loop ended. Only `.ralph/fix_plan.md` and
`.ralph/deferral_ledger.md` carry real changes this loop, both to be
committed.
