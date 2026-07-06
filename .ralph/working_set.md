Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 10th consecutive investigation loop. NO code landed this
loop (pure investigation) — refuted two more concrete hypotheses for
the still-open "empty internal page" bug (`TestMultiWriterStress_
M0055_Phase_C`, insert-only, no vacuum) and narrowed the search space.

Files touched this loop (all reverted before finishing, zero diff):
`internal/access/btree/btree.go` (`pinR`/`pinW` temporarily got a
tag-match panic, reverted), `internal/access/btree/multi_writer_stress_
test.go` (temporarily un-skipped, `t.Skip` restored). `git diff --stat`
confirmed clean on both. Only real change this loop: appended a new
row to `.ralph/deferral_ledger.md` (2026-07-07, 10th loop).

Key symbols audited (read-only, no changes): `insertIntoBlock`,
`createNewRoot`, `clearRootFlag`, `clearIncompleteSplit`, `finishSplit`,
`descendToLeaf`, `tryInsertNoSplit`, `tryInsertOnCachedRightmost`,
`byteAwareSplitLoc`, `pageItems`/`parseItem` (all in btree.go); `Pin`/
`PinNew`/`claimVictim`/`evictVictim`/`tryPinSlot` (bufpool.go);
`Insert`/`Lookup`/`Delete` (bufmap.go).

Findings this loop (all REFUTED/ruled out, not yet a fix):
1. `tryInsertOnCachedRightmost` reconfirmed 100% dead code (independent
   re-derivation of loop 8's finding: `rightmostLeafBlk.Store` only
   fires on `op.Next==0`, which never happens, so the cache is always
   0 and `tryInsertNoSplit` never takes that branch).
2. ALL internal-(non-leaf)-page mutations provably go through
   `insertIntoBlock` under `bt.splitMu` (recursive parent-insert, or
   `createNewRoot`'s fresh-root population) — no fast path ever writes
   an internal page. This REFUTES the hypothesis loop 9 ended on
   ("does an internal page get a non-splitMu insert path").
3. `byteAwareSplitLoc` can never produce an empty half of a split
   (explicit `split<1→1` / `split>len-1→len-1` clamps) — ordinary
   splits cannot themselves create a 0-item page.
4. Re-tested "reader resolves to wrong physical slot for tag"
   (previously refuted by loop 9 for the DIFFERENT pgbench keyLen-
   mismatch symptom) specifically against THIS bug: added a tag-match
   panic in `pinR`/`pinW` (`s.Tag() != BufferTag{Rel,blk}` → panic).
   Reproduced the failure once under plain `go test -count=300`
   (`writer 3 insert 253: btree: empty internal page`, 139s) — panic
   never fired. Also ran `go test -race -timeout 25m -count=250`
   (770s) — zero failures, zero races. REFUTED for this symptom too.

Next step: test the STALE-`[]byte`-ALIAS-ACROSS-SLOT-REUSE hypothesis
(never yet tested for either symptom) — audit every `slot.Page()`-
derived byte slice in btree.go for one that escapes past its
`unpinW`/`unpinR` without a defensive copy (`pageItems`/`parseItem`
were checked this loop and DO copy via `append([]byte(nil), ...)` —
ruled out; `parseItemNoCopy` is unused by this test's path but worth
enumerating other call sites, e.g. anything touching `op.HighKey`/
`sepKey`/`sepItem.key` across an unpin boundary). If that comes up
clean too, add a per-pin generation sentinel stamped into unused
page-trailer bytes on every `PinNew`/recycle and assert unchanged
around every `pinR`/`pinW` critical section — catches physical buffer
aliasing that a tag/lock check cannot (tag/lock correctness doesn't
prove no OTHER code path writes the same backing array outside
`Pin()`). Cheap repro: `go test -run TestMultiWriterStress_M0055_
Phase_C -count=300 ./internal/access/btree/...` (~140-180s, un-skip
line ~40 first, re-skip before committing), observed failure rate
~1/250-1/300.

Gates run this loop: `go build ./...` clean. `go test -count=1
./internal/access/btree/... ./internal/amcheck/...` PASS. No
executor/planner/codec change, so no TPC-H spotcheck required.
`make ralph-state-guard` run before finalizing.

In-flight: none. No servers/data dirs/background processes started
this loop. Separate live nightly CI batch (`ci/batch/run-nightly.sh`)
not touched.
