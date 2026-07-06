Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 8th consecutive loop. LANDED a small, verified, safe fix
(pinNewOrRecycled lock-gap hardening) and ruled out two hypotheses
empirically, but the nightly item is STILL open — the third root
cause (active since loop 5/update #3) is not yet identified.

Files changed (committed): `internal/access/btree/btree.go`
(`pinNewOrRecycled` now returns its slot still content-locked in both
branches via a new `pinNewLocked` helper; single call site in
`insertIntoBlock` no longer re-`Lock()`s). `.ralph/fix_plan.md` +
`.ralph/deferral_ledger.md` bookkeeping (2026-07-07 update #6 + new
ledger row).

Key symbols: `pinNewOrRecycled`/`pinNewLocked` (btree.go, ~line 641)
— the fix. `tryInsertOnCachedRightmost`/`descendToLeaf`'s cache-populate
branch (btree.go:1299, 1998) — found dead-code sentinel bug
(`op.Next == 0` should be `op.Next == storage.InvalidBlockNumber`),
NOT fixed (confirmed via probe test this whole fast path never
engages today; fixing it activates a dormant path and deserves its
own dedicated verification loop, not a tail-end change).
`insertIntoBlock` (btree.go ~1420-1660, the split path under
`bt.splitMu`) — next investigation target, see below.

Findings this loop:
1. LANDED: `pinNewOrRecycled`'s recycled-block branch used to zero a
   page under `slot.Lock()` then `Unlock()` *before* the caller
   re-`Lock()`'d to stamp real opaque/header — a real, previously-
   flagged (ledger, 2nd 2026-07-07 row) gap. Now both branches
   (recycled + fresh-PinNew via new `pinNewLocked`) return the slot
   still locked; caller no longer double-locks. Verified: build clean,
   `go test -count=1 ./internal/access/btree/... ./internal/amcheck/...
   ./internal/executor/... -run Vacuum` PASS, `go test -race -count=1
   ./internal/access/btree/...` PASS (18.2s, zero races). Re-ran the
   full authoritative `stage-pgbench.sh` (s=50 c=100 j=20 T=180x3)
   post-fix: STILL FAILS identically (`keyLen=9 total=37` etc. on the
   very first workload) — this fix does NOT close the nightly item,
   it just closes an adjacent, real gap.
2. RULED OUT (empirically, not just by static read): (a) posting-list
   re-encoding in the live insert/split path —
   `appendTIDToPosting`/`promoteSingleToPosting` (posting.go) are dead
   code outside tests; `dedupConsolidate` (btree.go) only drops exact
   (key,ptr) dupes, never re-marshals as posting bytes — postings only
   ever get written by `BulkCreate`. (b) `rightmostLeafBlk` insert
   fast-path cache — wrote a throwaway probe test (5000 sequential
   `bt.Insert` calls forcing many splits, then read
   `bt.rightmostLeafBlk.Load()`) and confirmed it stays 0 forever: the
   cache-populate/staleness checks (btree.go:1299/1998) compare
   `op.Next` against `0` instead of the real "no right sibling"
   sentinel `storage.InvalidBlockNumber` (confirmed via every
   `BTPageOpaque{Next: ...}` write site in btree.go/bulkload.go/
   btree_vacuum.go) — so this whole fast path is 100% dormant today.
   Left unfixed (see "Next step").
3. NEW, HIGH-VALUE FINDING — a much cheaper repro, and proof the
   corruption is concurrency-triggered (not load-time): built a local
   server, ran a single-threaded `pgbench -i -s 50` (no concurrent
   clients), then `bt_index_check`/`bt_index_parent_check` on all 3
   pkey indexes — ALL CLEAN. Reusing that SAME loaded DB, ran
   `pgbench -c 100 -j 20 -T 25 -P 5` (just 25s) — reproduced the exact
   `keyLen=9 total=37` failure within ~10s (vs. 15-30+ min for the
   full authoritative gate). Post-failure `bt_index_check` on
   `pgbench_accounts_pkey` shows WIDESPREAD "item order invariant
   violated" across hundreds of distinct leaf blocks (as low as block
   5) PLUS one genuinely byte-corrupt page (block 1096, same
   keyLen=9/total=37 signature) — suggests one structural mis-split's
   effects cascade across the whole sibling chain (amcheck's order
   check compares HighKey/sibling links), or multiple independent
   occurrences. All diagnostic artifacts removed (binary, data dir,
   server killed, RUN_DIR removed).

Next step: use the NEW cheap repro (not the 15-30 min authoritative
gate, not the unreliable btree-only unit test) to instrument the
SPLIT path specifically: `insertIntoBlock` (btree.go ~1420-1660,
under `bt.splitMu`) — pgbench_accounts has 5M rows at scale=50 so
splits are frequent/constant during the workload, unlike the tiny
branches/tellers tables (which may never split at all). One concrete,
NOT-yet-proven lead: `tryInsertNoSplit`/`tryInsertOnCachedRightmost`
(the no-`splitMu` fast path) never check `op.HasIncompleteSplit()` or
call `finishSplit`, unlike the documented crash-recovery contract for
`BTIncompleteSplit` (confirmed via grep — no such check exists in
either function) — a page mid-split (flag set, HighKey/Next already
updated, but parent downlink not yet inserted, and briefly UNLOCKED
between `bt.unpinW(slot)` and the parent insert a few lines later,
lines ~1638-1657) could be reached by a concurrent fast-path insert;
not yet confirmed this actually produces the observed corruption
signature — verify by instrumenting/logging around that exact window
using the cheap repro before attempting a fix. Full repro recipe (build
path, PATH/LD_LIBRARY_PATH setup, exact pgbench invocations) + full
evidence is in `.ralph/deferral_ledger.md`'s 2026-07-07 (8th loop) row.

Gates run this loop: `go build ./...` clean. `go test -count=1
./internal/access/btree/... ./internal/amcheck/... ./internal/executor/...
-run "Vacuum|Btree|Index"` PASS. `go test -race -count=1
./internal/access/btree/...` PASS (no race reports). Full authoritative
`stage-pgbench.sh` re-run post-fix: still RED (expected — third root
cause not yet found, not a regression). `make ralph-state-guard` — run
before finalizing; auto-repaired a pre-existing stale progress.json
marker (unrelated to this loop's changes), reports consistent. No
executor/planner/codec change, so no TPC-H spotcheck required beyond
the above.

In-flight: none. All diagnostic artifacts removed: `/tmp/goopg-diag2`
binary + `/tmp/diag-data2` datadir (server force-killed via captured
PID, directory rm -rf'd, confirmed gone), `/tmp/tmp.1ONIVLHqDV`
(stage-pgbench.sh RUN_DIR from the authoritative re-run, rm -rf'd
after extracting log excerpts — its own teardown already killed the
port-5571 server and removed the nightly data dir). The separate,
unrelated live nightly CI batch (`ci/batch/run-nightly.sh`, PID
264733) was observed still running on port 65434 throughout this
loop — NOT touched, left running.
