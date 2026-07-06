Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
keyLen-mismatch corruption. 16th consecutive loop, pure investigation (no
code landed). Also triaged 7 new AI items from tonight's nightly run
(20260707-000712) into fix_plan.md's M-NIGHTLY section as new bullets
(race/internal/wal, 2x testport isolation-eval-plan-qual, tpch Q21/Q15b/
Q9/Q20) — none started yet, this loop finished the already-in-flight
pgbench/nightly task first per the preemption rule.

What this loop did: executed loop 15's handoff — read `insertOp.Next` and
`updateOp.updateViaIndex` (operators_storage.go) end-to-end, the exact
executor paths pgbench's plain INSERT/UPDATE-by-pkey take. REFUTED the
heap-vs-index RelFileNode-crossing hypothesis for these two call sites
(both `rel` and `idxRel` are always freshly/independently derived, no
caching; RangeScan's callback only collects into a `pendingUpdate` slice,
defers all writes until after RangeScan fully returns/unpins — honours
the "none re-enter the btree" contract). Also re-derived (not just
re-cited) that the buffer pool's claimVictim/tryPinSlot/Unpin/PinNew/
pinLoad CAS state machine is correct and storage.InitPage fully zeroes
new pages. NEW proof: `BTree.freeList`/`pinNewOrRecycled`'s recycle path
is structurally UNREACHABLE for this workload (every `btree.Open()` call
allocates a fresh `*BTree` with an empty freeList; only VacuumIndexPages's
single long-lived handle can bridge push+pop, and no VACUUM runs during
the 25s pgbench window) — upgrades this from "assumed inert" to "proven
impossible". Also ruled out `upsertOp.leafTrees` (operator-scoped, and
TPC-B never hits ON CONFLICT anyway). Full detail: deferral_ledger.md's
16th-consecutive-loop row (2026-07-07) and fix_plan.md's "update #7".

Next step (two options, priority order — see ledger row for full recipe):
1. Loop 12's still-outstanding checksum/line-pointer-sample instrumentation:
   sample storage.PageLinePointerCount / a byte hash of s.page immediately
   before evictVictim's flushSlot call (bufpool.go), and again immediately
   after relFile.writeBlock's WriteAt / relFile.readBlock's ReadAt
   (smgr.go), keyed by blk — this dichotomy (in-memory-already-wrong vs.
   disk-roundtrip-loses-it) was NEVER actually run (loops 13-15 pivoted
   onto other threads before reaching it) and is the single most decisive
   unrun measurement.
2. Audit emitCanonicalHeapInsert/emitCanonicalHeapDelete and the shared
   MarkDirtyChangeRecord (btree.go) / MarkDirtyLogicalChange (bufpool.go)
   logical-record plumbing for a mis-routed WAL-replay/change-record path
   (flagged unaudited by loop 14, still untouched by any loop since).
Use the now-committed `GOOPG_BTREE_PARSE_ERR_DUMP=1` dump tool
(internal/access/btree/parse_err_dump.go) plus the cheap repro (pick a
free port via `ss -ltn`; `pgbench -i -s 50` once ~4min; then `pgbench -c
100 -j 20 -T 25 -P 5`, ~10-25s to failure) to correlate either
investigation's findings against a fresh forensic dump.

Gates run this loop: `go build ./...` clean (no code changes this loop,
investigation-only). `make ralph-state-guard`: run next, see status block.

In-flight: none. No servers/binaries/background processes started or left
running this loop (pure static code reading via Read/Serena, no build/test
execution needed since nothing changed). Separate live nightly CI batch
(`ci/batch/run-nightly.sh`) and the protected `goopg-wp.scope` on port 5544
were not touched.
