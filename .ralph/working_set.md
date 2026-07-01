Task: M0117-0007 Part B (async-commit LSN, live `synchronous_commit=off`
wiring). COMPLETE and committed this loop (#49) — but only the MECHANICAL
wiring, not the promised latency reduction (see below). fix_plan item stays
`[ ]` (open) since the milestone's real goal — a faster async commit path —
is not yet achieved; only the sub-status text was updated.

Files: internal/mvcc/clog_bufferpool.go (+`SetFlushWALHook`);
internal/mvcc/clog.go (`setStatus` refactored to call new `setStatusWithLSN`;
+`SetCommittedWithLSN`, +`SetFlushWALHook`); internal/mvcc/manager.go
(`xactMarker` hook field/`SetXactMarkerLogger` gained a 3rd `bool`
waitLocalFlush param; `Commit`/`Rollback` pass true; new `CommitAsync` passes
false; `finish` threads it to the hook); internal/mvcc/manager_test.go (5
existing hook-closure test callers updated for the new signature; +
`TestCommitAsyncPassesWaitLocalFlushFalse`); internal/mvcc/
clog_asynccommit_test.go (NEW — 3 tests for the CLog-level API);
internal/initdb/open.go (`clog.SetFlushWALHook(walWriter.FlushUpTo)` right
after `EnablePGSLRUMirror`; xactMarker closure signature grew
`waitLocalFlush bool`, skips the inline `FlushUpTo` and calls
`SetCommittedWithLSN` instead of `SetCommitted` when false);
internal/executor/context.go (+`AsyncCommit bool` field, +`CommitTransaction`
method — the ONE call site every commit site should use);
internal/executor/operators_tx.go (explicit COMMIT → `CommitTransaction`);
internal/executor/commit_async_test.go (NEW); internal/server/dispatch.go
(+`sessionAsyncCommit` helper; `ectx.AsyncCommit = sessionAsyncCommit(sess)`
at ectx construction; 3 commit call sites → `ectx`/`ctx.CommitTransaction`);
internal/server/dispatch_extended.go (same: +AsyncCommit wiring, 1 call site);
internal/server/sync_commit_test.go (NEW — `TestSessionAsyncCommit`);
docs/design/0117-0007-clog-async-commit-lsn.md (Part B section rewritten:
what landed, why it does NOT yet cut latency, "Still open" section, Testing +
Status/merge sections updated); docs/design/README.md (0117-0007 row
updated); .ralph/fix_plan.md (M0117-0007 + M0119-0002 entries updated, box
stays unchecked); .ralph/deferral_ledger.md (old Part-A/B-deferred row
flipped `resolved`; new row appended describing the 2 residual gaps).

Key symbols: mvcc.Manager.CommitAsync/Commit (waitLocalFlush true/false),
CLog.SetCommittedWithLSN/SetFlushWALHook, executor.Context.AsyncCommit/
CommitTransaction, server.sessionAsyncCommit (distinct from
sessionSyncCommitMode — "local" is NOT async, only literal "off" is).

Findings: implemented the correct, safe, fully-tested wiring (barrier now
live, LSN association, GUC read for local-flush decision, all interactive
commit call sites — explicit COMMIT/simple+extended autocommit/PL-pgSQL
commit chain/2PC via shared paths — consistently wired). Discovered via
tracing (not assumed) that this does NOT yet reduce commit latency: CLOG's
`groupUpdate`→`pool.flushDirty()` runs SYNCHRONOUSLY on every commit
(M0117-0005's design, unlike PG's lazy SLRU write-back), so the barrier fires
immediately inside the same commit call regardless of waitLocalFlush,
forcing the identical flush the skipped explicit call would have made. COPY
(4 call sites in copy.go) deliberately left unwired — safe conservative
default, narrower follow-up, recorded not silently skipped. Genuinely cutting
latency needs (a) `groupUpdate` to skip the durable write-back for an async
commit and (b) a checkpoint-driven CLOG flush (`CLog` implements no
`wal.DirtyPageFlusher` — checkpointer today only flushes the heap pool) to
bound how long a deferred-dirty CLOG page can stay unflushed. That is a
separate, larger, checkpoint-subsystem-touching change, intentionally NOT
attempted this loop (would have meant shipping an unreviewed high-blast-radius
CLOG-durability behavior change).

Next step: two follow-ups now available, both self-contained: (1) the
checkpoint-integration follow-up above (Effort L, dedicated full-gate
session — this IS what would finally let M0117-0007's fix_plan box be
checked); (2) COPY's 4 commit call sites (Effort S — thread `asyncCommit
bool` onto `copyInState`, set `ectx.AsyncCommit` in `runInlineCopy*` which
currently don't read `synchronous_commit` at all). M0117-0008 Part B
(datfrozenxid persistence) remains the other M0119-0002 sibling, independent
of this. M0119-0004's "still open" list (dump ordering, btree/hash
amadjustmembers, builtin-operator catalog) are unrelated, independently
resumable alternatives if CLOG work is not picked up next.

Gates run this loop: go build ./... clean; go vet ./... clean; gofmt -l
clean on all touched files; go test -count=1 ./internal/mvcc/...
./internal/executor/... ./internal/server/... ./internal/initdb/... PASS;
go test -race ./internal/mvcc/... ./internal/wal/... PASS;
TestE2E_PhysicalReplication{,Sync} + TestE2E_StandbyAttachRetainsUpstream
RowsAfterRestart + TestE2E_ChecksumStreamingGoopgToPG PASS;
scripts/tpch-spotcheck.sh RESULT=PASS (Q12=2 rows/28.01s, Q13=33 rows/89.73s);
make ralph-state-guard OK (self-repaired usual stale "completed" marker).
pgbench smoke runs at commit time via the pre-commit hook.
