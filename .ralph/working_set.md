Task: M0117-0007 — COPY commit-site follow-up (Effort S, flagged in loop #49's
design-doc "Still open" list). COMPLETE and committed/pushed this loop (#50).
fix_plan's M0117-0007 box stays `[ ]` (open) — this closed only the COPY-wiring
gap; the real remaining scope (latency reduction) is unchanged and still open.

Files: internal/server/copy.go (`copyInState` +`asyncCommit bool` field;
+`commitCopyTx(mgr,tx,asyncCommit)` helper; `dispatchCopyViaExecutor` gained a
`sess *config.SessionRegistry` param, computes `asyncCommit :=
sessionAsyncCommit(sess)`, sets `ectx.AsyncCommit`, `CopyTo`/file-`CopyFrom`
branches now call `ectx.CommitTransaction(tx)` instead of
`s.cfg.TxnMgr.Commit(tx)`; `copyInState{}` construction site stores
`asyncCommit`; `handleCopyInFrame`'s 2 `st.mgr.Commit(st.tx)` call sites →
`commitCopyTx(st.mgr, st.tx, st.asyncCommit)`; `handleQueryOrCopy`'s call site
passes `sess` through); internal/server/copy_async_commit_test.go (NEW —
`TestCommitCopyTxRespectsAsyncCommit`, mirrors
`TestContextCommitTransactionRespectsAsyncCommit`'s waitLocalFlush-sequence
assertion for the new helper); docs/design/0117-0007-clog-async-commit-lsn.md
(new "COPY's own commit call sites (LANDED 2026-07-02, loop #50)" paragraph;
"Still open" COPY bullet struck through/closed; Testing + Status/merge
sections updated); docs/design/README.md (0117-0007 row updated); .ralph/
fix_plan.md (M0117-0007 + M0119-0002 entries updated — box stays unchecked,
only the COPY sub-item is marked done); .ralph/deferral_ledger.md (new row
appended cross-referencing the loop #49 row; item (1) — the checkpoint/
lazy-write-back latency gap — explicitly called out as still fully open and
unaffected by this loop).

Key symbols: server.commitCopyTx, server.copyInState.asyncCommit,
server.dispatchCopyViaExecutor(..., sess), executor.Context.CommitTransaction
(the pattern this mirrors), server.sessionAsyncCommit (unchanged, reused).

Findings: `runInlineCopy`/`runInlineCopyFromStdin` (the multi-statement-batch
COPY path used by psql's `\;`-joined commands) needed NO change — they already
share the batch's `ectx`, which already had `AsyncCommit` set and is committed
once by the dispatch loop via `ectx.CommitTransaction` (confirmed by reading
dispatch.go's multi-statement commit call sites before touching anything).
Only the single-COPY path (`dispatchCopyViaExecutor` + `handleCopyInFrame`,
reached from `handleQueryOrCopy`'s non-multi-statement branch) had raw
`TxnMgr.Commit`/`mgr.Commit` calls bypassing the session's synchronous_commit
setting. This item is now fully closed — the M0117-0007 "Still open" list has
exactly ONE remaining entry (the checkpoint-integration / lazy-CLOG-write-back
latency work), no other loose threads.

Next step: the one remaining follow-up for M0117-0007's fix_plan box to close
is the Effort-L checkpoint-integration item — needs a dedicated full-gate
session: (a) `CLog.setStatusWithLSN`'s call to `groupUpdate` becomes
conditional (skip durable write-back for an async commit, leave page dirty);
(b) `CLog`/`clogBufferPool` gains a `FlushAll`-style entry point implementing
`wal.DirtyPageFlusher`, registered with `wal.Checkpointer` alongside the heap
pool, wired at `internal/initdb/open.go`'s Checkpointer construction site.
Resume point fully detailed in the design doc's "Still open" section and the
loop #49 ledger row. M0117-0008 Part B (datfrozenxid persistence) remains the
other M0119-0002 sibling, independent of this. M0119-0004's dump-ordering /
btree-hash amadjustmembers / builtin-operator-catalog items are unrelated,
independently resumable alternatives if CLOG work is not picked up next.

Gates run this loop: go build ./... clean; go vet ./... clean; gofmt -l clean
on all touched files; go test -count=1 ./internal/server/... ./internal/mvcc/...
./internal/executor/... PASS; go test -race ./internal/mvcc/... ./internal/wal/...
PASS; scripts/tpch-spotcheck.sh RESULT=PASS (Q12=2 rows/27.52s, Q13=33
rows/88.92s); make ralph-state-guard OK (self-repaired the usual stale
"completed" progress marker vs "running" status); pgbench smoke ran and PASSED
via the pre-commit hook at commit time (188-245 TPS across the 3 builtin
transaction types, 0 failed). Committed 68529194, pushed to
align-data-structure-with-pg.
