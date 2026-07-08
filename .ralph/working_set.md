Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. This (9th) loop built reload-side instrumentation
and turned "reload side is implicated" from hypothesis into hard evidence:
the loss is a PERMANENT, non-recovering event, not a transient race. See
fix_plan.md's 9th-loop update + deferral ledger's 9th 2026-07-08 row for
full detail.

Files: internal/storage/bufpool.go (NEW `Pool.OnBlockReload func(tag, page)`
field, nil by default; wired in `pinLoad`'s cache-miss branch immediately
after `Manager.ReadBlock` succeeds, before slot publish. ALSO: moved the
existing `OnFlushSnapshot` call in `flushSlot` from BEFORE `WriteBlock` to
AFTER it succeeds — a real correctness fix to the 8th loop's own
instrumentation, see Hypothesis below). internal/access/btree/btree.go (NEW
`BTree.DebugTraceReloads` field + `ReloadSnapshotEvent` struct +
`RecordReloadSnapshot` method + `ReloadSnapshotRecordsForBlock` getter,
mirroring the flush-side siblings). internal/amcheck/
verify_nbtree_realtree_test.go (buildRealTreeConcurrent now sets
`bt.DebugTraceReloads=true` + `pool.OnBlockReload = bt.RecordReloadSnapshot`;
per-missing-entry diagnostic extended with a reload-snapshot cross-reference
block anchored on the last flush that had the entry present; doc comment +
skip message extended with this loop's finding; re-skipped, un-skip locally
to re-run). .ralph/fix_plan.md + .ralph/deferral_ledger.md updated.
KEEP OnBlockReload/RecordReloadSnapshot/DebugTraceReloads: reusable,
zero-cost-when-off, same pattern as the other DebugTrace*/DebugValidate*
aids in this thread. KEEP the OnFlushSnapshot post-write reordering: it is
a genuine bug fix (Seq now reflects "durably written", not "about to
write"), not merely an investigation aid.

Key symbols for next step: internal/storage/smgr.go's `relFile.writeBlock`
and `relFile.readBlock` (both already serialize under `r.mu` per relFile
instance) — NEITHER has a monotonic op-sequence counter local to the
relFile itself. `bt`'s `insertLogMu`-guarded `logSeqNext` (shared across
insertLog/rewriteLog/flushLog/reloadLog) still has residual cross-goroutine
lock-acquisition jitter even after this loop's pre/post-write fix, since
Seq is assigned by whichever goroutine happens to win `insertLogMu` next,
not by real IO completion order. A relFile-local counter, incremented
under the ALREADY-HELD `r.mu` at the exact instant each `WriteAt`/`ReadAt`
returns, would give jitter-free TRUE ordering for a specific block.

Hypothesis/Findings: DEFINITIVE this loop — built `OnBlockReload`
(bufpool.go) + `BTree.RecordReloadSnapshot` (btree.go), decoding
`pageItems()` from the exact bytes a disk reload just read. While wiring
this, found the 8th loop's `OnFlushSnapshot` placement was itself buggy for
Seq-ordering purposes: it fired BEFORE flushSlot's `WriteBlock` call
(pre-write "intent" time), while `OnBlockReload` fires AFTER `ReadBlock`
returns (post-read "completed" time) — comparing Seq across the two logs
as originally built was apples-to-oranges (a reload's real disk read could
complete before a flush's real disk write yet log a HIGHER Seq purely from
`insertLogMu` lock jitter). Fixed by moving `OnFlushSnapshot` to fire AFTER
`WriteBlock` succeeds. Re-ran the 200000-insert/64-writer repro AFTER this
fix: 14 real entries lost, and for EVERY ONE, the FIRST reload of the
affected block recorded after the last flush that had the entry ALREADY
lacks it (e.g. blk=142: flush seq=609678 itemCount=360 present=true; next
reload seq=609829 itemCount=359 present=false — one item short of what was
just durably written). Critically: this is NOT a one-off race that later
reads recover from — EVERY subsequent reload of the same block for the
REST of the run (3179 reload-events checked across all 14 missing entries,
Seq deltas from a few hundred to 100000+ ticks later) still lacks the
entry, while item count keeps climbing as other writers insert on top. A
transient read/write race would show SOME later reloads recovering the
entry once real disk state settles; a permanent, unrecovering loss instead
means either (a) the "good" flush's WriteBlock did not durably land the
entry despite its in-memory snapshot having it, or (b) a second,
older/stale write to the same block landed on disk AFTER the good flush
and clobbered it (a classic lost-update pattern). `slotEvent` traces for
one affected block (173) show THREE different slot indices (17, 25, 31)
holding the same tag across ~3000 Seq ticks — dirty eviction (flush)
immediately followed by a fresh pinLoad (reload) then an immediate CLEAN
eviction (no insert landed while resident) then another pinLoad — the
block churns through eviction/reload cycles on the 64-slot pool under
64-writer contention fast enough for back-to-back cycles on DIFFERENT
physical slots to be routine. Manual review of evictVictim's dirty branch
and smgr.go's relFile.readBlock/writeBlock (both properly serialized
per-relFile under r.mu; evictVictim's bm.Delete(oldTag) happens strictly
AFTER flushSlot's WriteBlock returns) did not surface an obvious lock gap
for hypothesis (a); hypothesis (b) remains unrefuted and is the more
likely mechanism, but proving it needs relFile-local wall-clock/op-order
instrumentation (see Next step) since even the fixed Seq counter has
residual cross-goroutine jitter.

Next step: instrument `relFile.writeBlock` and `relFile.readBlock`
directly (internal/storage/smgr.go, both already under `r.mu`) with a
monotonic op-sequence counter LOCAL to the relFile struct itself (a new
`relFile.ioSeq uint64` field incremented under the already-held `r.mu`
right when each WriteAt/ReadAt call returns) — this eliminates ANY
cross-goroutine insertLogMu lock-ordering jitter since it's assigned
atomically with the actual syscall completion, under the SAME lock that
serializes the syscalls themselves. Expose a small hook similar to
OnBlockReload/OnFlushSnapshot (e.g. `relFile.onIOEvent func(kind string,
blk BlockNumber, ioSeq uint64, buf []byte)` or reuse the existing
Manager.OnBlockWritten/OnReadWait-style hooks if sufficient) so a test can
log every real WriteAt/ReadAt in true order for a specific block, then
re-run this repro and check whether two writeBlock calls for the SAME
block are ever interleaved such that an OLDER in-memory copy's write
lands on disk after the newer one's — confirming hypothesis (b) directly
and identifying which caller (likely a second concurrent
evictVictim/WriteDirtyPages/flushBatch invocation holding a stale slot)
is responsible. Do NOT re-open claimVictim, the fast-path insert sites,
the split/dedup-rewrite path, the clean-eviction path, or the dirty-flush
write side — all five conclusively cleared. Do NOT re-litigate whether the
reload path is implicated — it now has hard, permanent-loss evidence, not
just a hypothesis.

Gates run this loop (all PASS): go build ./...; go vet
./internal/amcheck/... ./internal/storage/... ./internal/access/btree/...;
go test ./internal/storage/... ./internal/access/btree/...
./internal/amcheck/... (target test re-skipped by design, ran clean);
go test -v -run TestVerifyBtreeEngineSilentOnRealConcurrentContended
./internal/amcheck/... (run manually with the test temporarily un-skipped
TWICE — once before the OnFlushSnapshot post-write fix, once after — for
investigation only, NOT part of the committed gate set, re-skipped before
commit; both runs confirmed the smoking-gun signature, second run stronger
with 3179/3179 reload-events after a good flush still lacking the entry);
scripts/tpch-spotcheck.sh (Q12=2/Q13=33 PASS); make ralph-state-guard
pending (run before finishing this loop, see below).

In-flight: none. No background processes left running. The test file's
temporary un-skip used during this loop's investigation was reverted
(t.Skip restored with an updated message) before the gates above were
re-run and before commit.
