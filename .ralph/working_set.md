Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. 13th loop on the reopen. Attempted the 12th
loop's exact next step (land a synchronization fix in claimVictim/
evictVictim/pinLoad) but did NOT land any source-code change: a
from-scratch hand-trace of the whole protocol could not reproduce (on
paper) the gap the 12th loop asked to be fixed, and landing an unverified
guess into this heavily-audited hot path was judged riskier than another
investigation-only loop. Filed a genuinely NEW, separate, confirmed
finding (AIO/relFile.mu bypass) as its own fix_plan task instead of
conflating it with this one.

Files (all committed, docs-only): .ralph/fix_plan.md (13th-loop update
under the pgbench/nightly-reopen-20260708 task + new unchecked item
`storage/aio-relfile-mu-bypass`), .ralph/deferral_ledger.md (13th
2026-07-08 row). No source files modified this loop.

Key symbols audited this loop (all read, none edited): storage.bufmap's
Insert/Delete/Lookup/compact (bufmap.go) — correctly serialize tag
ownership under bufmap.mu, ruled out as a double-mapping source by
inspection (not yet by live tracing — see Next step). storage.Pool's
claimVictim/evictVictim/pinLoad/pinSlow/Pin (bufpool.go ~1055-1595) —
hand-traced every interleaving I could construct; the per-slot IO-inflight
bit + semaphore-wait + bufmap-delete-after-flush protocol appears to
correctly prevent a reload from observing pre-flush bytes in the
SYNC-ONLY path (no checkpointer/AIO). bt.pinW/unpinW (btree.go ~1492) —
confirmed `s.Lock()`/`s.Unlock()` ARE `contentMu.Lock/Unlock`
(bufpool.go:87-88), so writer mutations are properly excluded from the
checkpointer's flushBatch (contentMu.RLock) by Go's RWMutex semantics —
ruled out "torn write during checkpointer flush". Pool.Unpin (bufpool.go
~1651) panics on underflow — ruled out silent double-unpin corruption
(would crash instead). storage.Manager.WriteBlockAIO/PrefetchBlock
(smgr.go ~183-273) — NEW FINDING: bypass relFile.mu entirely when
`m.aio != nil` (calls `eng.Submit(...)` directly, skipping
`f.writeBlock`/`f.readBlock`); internal/aio/aio.go's Engine.Submit
(~459-471) has no per-file/per-offset ordering (registerInFlight is
stats-only). This IS wired in production (internal/initdb/open.go:303
`mgr.SetAIO(...)`) but NOT in this unit test's pool (buildRealTreeConcurrent
never calls SetAIO, never invokes FlushAllPaced during the race window) —
so it cannot explain THIS test's reproducible loss, and even in
production, contentMu's RWMutex still forces any evictor/reloader to wait
for flushBatch's real Wait() before touching page bytes, so no concrete
corruption path was found from it either, in the time available.

Hypothesis/Findings: after 13 loops (12 via live instrumentation, this one
via pure hand-trace of the code), NO concrete code-level defect has been
identified in the sync-only claimVictim/evictVictim/pinLoad/bufmap/
relFile/contentMu/pinMu protocol that the skipped test exercises — every
invariant I could construct holds. This is a genuinely surprising/notable
result: it means the bug may NOT be a simple lock-ordering gap in the
mechanisms every prior loop has focused on. Do NOT re-open (all
conclusively settled across 13 loops now): claimVictim's pin-count
exclusion, fast-path insert sites, split/dedup-rewrite path, clean-eviction
path, relFile.readBlock/writeBlock's own r.mu serialization, pinW/unpinW
vs pinLoad as separate contentMu choke points, Unpin double-decrement,
torn writes during checkpointer flush. Two NEW, not-yet-tried angles this
loop identified but did not execute (time-boxed out): (a) bufmap has NEVER
been directly instrumented with per-call Insert/Delete/Lookup logging in
13 loops — every loop, including this one, only inferred its correctness
by reading the code; (b) the 15-bit slot generation counter's wraparound
was checked historically only for same-slot reuse frequency (~2500 claims
per slot, far below the 32768 wrap threshold), never for a CROSS-slot gen
COLLISION (two different slots coincidentally holding the same gen value
at the same time, which could let a stale tryPinSlot/CAS succeed against
the wrong slot).

Next step: pick ONE of the two next-step candidates above and execute it
directly (do not re-derive the whole investigation from scratch — this
working_set + the 13th deferral-ledger row already contain the full
ruled-out list): (a) add direct Insert/Delete/Lookup call logging to
bufmap.go (new debug hook, same zero-cost-when-off pattern as every prior
DebugTrace*/DebugValidate* aid in this thread) and re-run the repro to
verify blk=377's tag truly never has 2 live mappings simultaneously; or
(b) instrument claimVictim's gen assignment and tryPinSlot's gen check to
log every (slotIdx, gen) pair assigned/checked and scan for a collision
across DIFFERENT slot indices sharing the same gen concurrently. Separately
(independent task, not this loop's priority): the new
`storage/aio-relfile-mu-bypass` fix_plan item needs its own design pass
(per-(rel,block) in-flight-AIO registry, not a blanket per-file mutex) —
pick that up as its own unit of work, not mid-stream inside the M-NIGHTLY
investigation.

Gates run this loop: go build ./... clean (no code changed, docs-only
commit). No test run needed (no code change to verify); the skipped test
remains skipped and unmodified in behavior.

In-flight: none. No background processes left running. All work from
this loop (fix_plan.md + deferral_ledger.md updates) is about to be
committed.
