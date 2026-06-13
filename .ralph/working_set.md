(idle — nothing in flight)

Last completed (loop #49): M0110-0004 — RW-002 (a). Enabled + validated the
maximal SLRU-derived-override FINAL RESTART in TestPort_PgResetwal001BasicServer
(was deferred/hung). Committed + pushed.

What this loop did: root-caused the "startup looks hung after pg_resetwal
--next-transaction-id" deferral. After the override advances NextXID ~1M past
the bootstrap pg_xact segment, initdb.Open's implicit-abort sweep
(CLog.MarkUnknownAsAborted, [1,NextXID)) stamped ~1M XIDs and the old per-XID
SLRU mirror (mirrorToSLRUUnlocked) called f.Sync() on EVERY one → ~1M fsyncs.
Fix: removed the per-XID mirror from the sweep hot loop; new
CLog.mirrorTerminalRangeBatchedUnlocked projects the swept range into pg_xact/
SLRU with ONE fsync per ~1M-XID segment, OR-merging onto existing bytes
(idempotent, byte-identical final state, skips xid<FirstNormalTransactionID).
Files: internal/mvcc/clog.go (fix), internal/mvcc/clog_test.go
(TestCLogMarkUnknownAsAbortedBatchedSLRU regression, cross-segment),
internal/testport/pgresetwal_port_test.go (restart enabled + comments),
docs/design/0110-0004-pg-resetwal-tap-port.md, .ralph/fix_plan.md,
.ralph/deferral_ledger.md.
Gates: gofmt+vet clean; all 3 TestPort_PgResetwal* PASS (3.5s);
go test -race ./internal/mvcc ./internal/initdb PASS (mvcc 2.4s, initdb 157s).

RW-002 remainder now: ONLY (b) the unclean-shutdown/--force branch of
001_basic.pl — blocked on goopg v0 having no crash/unclean shutdown state
(every stop writes a graceful DB_SHUTDOWNED checkpoint). RW-002 stays `defer`.

Other open tasks (blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump catalog parity), M0110-0002 (pg_waldump index
AMs), M0110-0003 (pg_amcheck verify_heapam).

⚠️ TREE NOTE: a SEPARATE manual claude session still has ~930 lines uncommitted
WIP across internal/{executor,planner,catalog,analyzer,parser,mvcc,server}/ +
untracked test files (executor/partition_gen_override_test.go,
parser/gen_override_test.go, postgres/, validate-ralph-state binary). NOT
ralph's — stage your own files explicitly (git add <paths>), never `git add -A`.
