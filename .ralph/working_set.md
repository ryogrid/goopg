Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
"item length mismatch keyLen=9 total=37" recurrence investigation
(NOT resolved; investigation-only loop, no functional change landed;
2nd consecutive investigation loop on this item).

Files: none changed in the final commit besides `.ralph/fix_plan.md` and
`.ralph/deferral_ledger.md` (bookkeeping). Temporarily edited and
REVERTED via `git checkout --` (not committed): `internal/storage/
bufpool.go` (unbounded debug trace ring buffer + tag-match panics at
all successful-pin sites + trace calls in claimVictim/evictVictim/
PinNew/pinLoad/tryPinSlot), `internal/storage/bufmap.go`
(`debugCountLive` duplicate-entry checker), `internal/access/btree/
btree.go` (trace calls bracketing insertIntoBlock/createNewRoot's
populate step + descendToLeaf's failure branch), `internal/access/
btree/multi_writer_stress_test.go` (un-skipped
`TestMultiWriterStress_M0055_Phase_C`, enabled tracing, dump-on-error).

Key symbols: `internal/storage/bufpool.go` `Pool.Pin`/`pinSlow`/
`pinLoad`/`tryPinSlot`/`claimVictim`/`evictVictim`/`PinNew` (all
re-read fully this loop, exhaustively). `internal/access/btree/
btree.go` `bt.pinR` (907, NOT YET instrumented — next step),
`descendToLeaf` (1272), `insertIntoBlock` (1426, right-sibling
creation via `pinNewOrRecycled`), `createNewRoot` (1822), `pinNewOrRecycled`
(646). `internal/storage/bufmap.go` (`Insert`/`Delete`/`Lookup`,
re-read fully, no bug found despite initial suspicion of Insert's
early-stop-at-first-tombstone loop — see Hypothesis/Findings).

Hypothesis/Findings: PREVIOUS loop's hypothesis ("PinNew publishes
valid+dirty into bm BEFORE the caller populates real content, letting
a concurrent reader reach a blank page") is REFUTED by direct dynamic
evidence, not just static reasoning. Built and ran 3 rounds of
instrumentation (all reverted before loop end):
1. `bufmap.debugCountLive(tag)` after every successful `bm.Insert` in
   PinNew+pinLoad, panic if count!=1 — NEVER fired across 2 repros.
   Rules out "duplicate live bufmap entry for the same tag". Also
   statically confirmed unreachable: `relFile.extend`/`Manager.relFile`
   are fully mutex-serialized (r.mu / m.mu), so two concurrent PinNew
   calls for the same rel CANNOT get the same block number — PinNew's
   own "another goroutine published this block while we were in
   Extend" fallback branch (bufpool.go ~1089) is dead code in practice.
2. Tag-match assertion (`s.tag != tag → panic`) at Pin's fast-path CAS,
   pinSlow's tryPinSlot call, AND inside tryPinSlot itself (covers
   pinLoad's internal re-check branch too) — NEVER fired across
   several 200-500-iteration repros (single-process AND 4-way
   parallel). Rules out "reader resolves to the wrong physical slot
   for its tag" (stale slotIdx/gen ABA via bufmap or claimVictim).
3. Unbounded cross-layer trace (storage.DebugTraceEvent, callable from
   btree.go too) capturing claimVictim/evictVictim/PinNew-publish/
   pinLoad-publish/tryPinSlot-hit, PLUS btree.go-side markers bracketing
   insertIntoBlock/createNewRoot's populate step and descendToLeaf's
   failure branch (logs cur/meta.Root/op.Level/op.IsRoot/op.Next).
   Got 3 CLEAN single-process reproductions (single-process only — a
   4-way-parallel run mixed logs across OS processes and gave
   misleading data on the first attempt, since dump-once fires
   per-process; don't reuse the same /tmp path across parallel `go
   test` processes). Full lifecycle every time: block created via
   PinNew (traced), read successfully 50-200x via fast-path (tag
   matched, content valid — traced), evicted DIRTY (flushed to disk),
   reloaded into a new slot cleanly, evicted a SECOND time CLEAN
   (dirty=false, no flush) — and after that second eviction, NO
   further PinNew/pinLoad/tryPinSlot/Pin-fast-path trace event for
   that tag EVER appears (checked against the full remaining trace,
   not just a truncated window), yet descendToLeaf's OWN failure-branch
   trace (fired from inside the SAME failing call, using the SAME
   `op`/`slot` that `bt.pinR(cur)` returned moments earlier) proves
   `bt.pinR()` on that exact block DID return successfully with ZERO
   content. This means the corrupted read evades all 4 instrumented
   "successful pin" return points in Pin/pinSlow/pinLoad/tryPinSlot —
   either there's a 5th un-instrumented return path (searched
   exhaustively, believed complete), or — the new leading hypothesis —
   btree.go itself reuses a STALE `*storage.Slot`/`.Page()` byte-slice
   handle from an earlier, legitimately-successful pin, without a
   fresh `Pin()` round-trip, when it reaches the failing block again.
   This REDIRECTS focus from "buffer-pool bufmap/eviction protocol"
   (M0118-0130 blocker (4)) toward "btree.go caller-side stale-handle
   reuse", a smaller/more-tractable subsystem.

Next step: instrument `bt.pinR` (btree.go:907) itself — log every
call with its `blk` argument — cross-referenced against a trace at
the TOP of `descendToLeaf`'s loop body (before `bt.pinR(cur)` is
called), using the SAME repro (`TestMultiWriterStress_M0055_Phase_C`,
un-skip `t.Skip`, `go test -run ... -count=400..500` SINGLE PROCESS,
~200-270s per run, ~1/150-500 failure rate, HIGHLY variable — some
400-iteration runs show zero failures, budget for several reruns).
This will show definitively whether `Pin()` is even being re-invoked
for the failing block's last access, or whether it's reusing an old
handle. If stale-handle reuse is confirmed, audit `finishSplit`/
`CompleteDeferredSplits` next (NOT reviewed this loop, time-boxed out
— more complex multi-pin choreography than insertIntoBlock/
descendToLeaf, a more probable location for such a bug). Do NOT
attempt a fix without this next instrumentation pass — still
investigation-only per the hard-won rule (a rushed buffer-pool/
btree-concurrency fix already caused a new panic class once, see
btree.go:601-613).

Gates run this loop: go build ./... clean (before AND after revert);
go test -count=1 ./internal/storage/... ./internal/access/btree/...
PASS (baseline, after full revert); make ralph-state-guard OK. No
executor/planner/codec changes this loop, so no TPC-H spotcheck
required (investigation + docs only).

In-flight: none — all temporary instrumentation to
internal/storage/{bufpool.go,bufmap.go} and internal/access/btree/
{btree.go,multi_writer_stress_test.go} was reverted via `git checkout
--` before this loop ended; only `.ralph/fix_plan.md` and
`.ralph/deferral_ledger.md` carry real changes this loop, both to be
committed.
