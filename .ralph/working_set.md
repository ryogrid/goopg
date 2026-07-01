Task: M0117-0007 Part C — lazy CLOG write-back for async commits +
checkpoint-driven flush (gap G8's latency win; the item the loop #49/#50
"Still open" section called the real remaining scope). COMPLETE and
committed/pushed this loop (#51), commit cda0cbe3. fix_plan's M0117-0007 box
is now `[x]` — fully closed.

Files: internal/mvcc/clog.go (`setStatusWithLSN` now short-circuits before
`c.groupUpdate` whenever `lsn != 0` i.e. async commit — leaves the page dirty
instead of forcing the synchronous group-commit flush; new `CLog.FlushAll()
error` thin wrapper over `pool.flushDirty()`, nil-safe pre-EnablePGSLRUMirror,
structurally satisfies `wal.DirtyPageFlusher` with no import); internal/wal/
checkpointer.go (`CheckpointerConfig.FlushCLOGFn func() error` new field,
called in `runCheckpoint`'s flush phase right after the primary
`c.flushDirty(pacer)` and BEFORE the redo LSN is sampled; unlike
PostCheckpointFn/TruncateCLOGFn its error FAILS the checkpoint); internal/
initdb/open.go (wires `FlushCLOGFn: clog.FlushAll` into the CheckpointerConfig
literal); internal/mvcc/clog_asynccommit_test.go (rewrote
`TestCLogSetCommittedWithLSNFiresBarrierOnFlush` → `...DefersFlush`: now
asserts SetCommittedWithLSN alone fires the barrier 0 times, and a subsequent
FlushAll() fires it once; new `TestCLogFlushAllBeforePoolExistsIsNoop`);
internal/wal/checkpointer_test.go (new `TestCheckpointerCallsFlushCLOGFn` +
`TestCheckpointerFlushCLOGFnErrorFailsCheckpoint`); internal/initdb/
xact_recovery_test.go (new `TestReplayCLogFromWAL_RecoversUnflushedAsyncCommit`
— proves the crash-safety invariant: an async-committed CLOG page that's
NEVER flushed is reconstructed after a simulated crash+restart purely from
the durable WAL record via the existing `replayCLogFromWAL` backstop);
docs/design/0117-0007-clog-async-commit-lsn.md (new "Part C" section incl. a
crash-safety proof paragraph + a documented residual race, status line and
Testing/Status-merge sections updated); docs/design/README.md (0117-0007 row
rewritten for Parts A-C); .ralph/fix_plan.md (M0117-0007 box checked `[x]`,
Part C paragraph appended; M0119-0002 entry updated — CLOG-tail latency
follow-up now DONE, only M0117-0008 Part B remains under that umbrella);
.ralph/deferral_ledger.md (new row: residual redo-pointer-sampled-after-flush
race in internal/wal/checkpointer.go's runCheckpoint, symmetric with the heap
pool's own checkpoint flushing, NOT CLOG-specific — deferred as a distinct
whole-checkpoint-subsystem redesign).

Key symbols: mvcc.CLog.setStatusWithLSN, mvcc.CLog.FlushAll,
wal.CheckpointerConfig.FlushCLOGFn, wal.Checkpointer.runCheckpoint,
initdb.xactStampAndAdvance/replayCLogFromWAL (the crash-recovery backstop
this change now leans on more heavily).

Findings: the only caller that ever passes lsn != 0 into setStatusWithLSN is
SetCommittedWithLSN (async commit path in open.go's xactMarker hook); every
sync caller (SetCommitted, aborts, subcommits) passes lsn == 0 and is
byte-for-byte unaffected by this change. GetStatus reads the resident buffer
pool directly (not disk), so in-memory status visibility is unaffected by
deferring the durable write-back. flushDirty() already drains ALL dirty
pages (not just the caller's own), so a later sync commit's groupUpdate
"rescues" any earlier async-dirtied pages for free — no correctness gap
there. Confirmed via a dedicated crash-simulation test that
replayCLogFromWAL (the pre-existing "CLOG is a write-behind cache, WAL is
authoritative" backstop, itself pre-existing infrastructure) fully covers
the crash-safety requirement this change now depends on more heavily; this
is a WIDER instance of an already-relied-upon narrow race (previously a few
nanoseconds mid-commit since M0117-0005, now up to checkpoint_timeout for
async-only workloads), not a new failure mode.

Next step: M0117-0007 is fully closed — no further work needed on that
milestone. Two independently-resumable options for next loop: (a)
M0117-0008 Part B (on-disk datfrozenxid persistence — needs a runtime
shared-catalog RelFileNode resolver + heap_inplace_update buffer-lock/WAL
path, 5-step plan already in design `0117-0008-*`); (b) the deferred
redo-pointer-sampled-after-flush checkpoint race just ledgered above (its
own dedicated design pass + full recovery/standby E2E gate — explicitly
NOT a CLOG-scoped follow-up, touches heap-page durability/retention/standby
attach too); (c) M0119-0004's remaining pg_dump/ACL items are unrelated,
independently resumable alternatives if neither (a) nor (b) is picked up.

Gates run this loop: go build ./... clean; go vet ./... clean; gofmt -l
clean on all touched files; go test -count=1 ./internal/mvcc/...
./internal/wal/... ./internal/initdb/... ./internal/server/...
./internal/executor/... PASS (including the 2 new packages' new tests); go
test -race ./internal/mvcc/... ./internal/wal/... PASS; go test -race -run
"TestE2E_PhysicalReplication|TestE2E_ChecksumStreamingGoopgToPG|
TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart" ./internal/testport/...
PASS; scripts/tpch-spotcheck.sh RESULT=PASS (Q12=2 rows/27.35s, Q13=33
rows/89.32s); make ralph-state-guard OK (self-repaired the usual stale
"completed" progress marker vs "running" status); pgbench smoke ran and
PASSED via the pre-commit hook at commit time (183-14000 TPS across the 3
builtin transaction types depending on workload, 0 failed). Committed
cda0cbe3, pushed to align-data-structure-with-pg.
