Loop #49 COMPLETE: M0118-0009 — same-backend two-phase commit ENABLER (design 0118-0110).
Committing + pushing. NOT a promotion (specs stay defer).

What landed (blast radius nil — handler returns handled=false for all non-2PC stmts):
- goopg had NO 2PC; `PREPARE TRANSACTION 's1'` did not even parse. Added the three
  statements `prepared-transactions{,-cic}` + `stats` need:
  - parser: AST PrepareTransactionStmt/CommitPreparedStmt/RollbackPreparedStmt;
    parsePrepare branches on KwTransaction; parseCommit/parseRollback branch on the
    unreserved `prepared` ident (peekIdentText); gid via parseStrLit.
    (internal/parser/{ast.go,parser.go,twophase_test.go})
  - server: connTxState.preparedGid + MarkPrepared/PreparedGid/ClearPrepared (End clears).
    execTwoPhaseStmt (internal/server/twophase.go): PREPARE TRANSACTION keeps the txn
    OPEN as the connection's active txn (writes/locks/SSI predicate locks persist) and
    records the gid; COMMIT/ROLLBACK PREPARED validate gid (42704 if unknown), clear it,
    then RE-ENTER executeOneSimpleStmt with a synthetic Commit/RollbackStmt → reuses the
    CANONICAL commit path (SSI check, deferred DDL, NOTIFY, connTx.End) — no sibling
    commit path. 25P01 outside txn block. isTwoPhaseStmt keeps them out of plan-cache
    pre-plan (dispatch.go, like isNotifyStmt).
  - docs/design/0118-0110 + README row; fix_plan + ledger rows.

Gates (PASS): TestParseTwoPhaseCommit; TestPort_TwoPhaseCommitSameBackend (commit-prepared
visibility incl. cross-session isolation of the uncommitted prepared row, rollback-prepared
discard, 25P01, 42704); internal/parser + internal/server units; build+vet+gofmt clean
(fixed one struct-alignment line my edit introduced). pgbench smoke = pre-commit hook.
Probe of prepared-transactions-cic: STATUS=defer but mechanism works — held txn keeps its
MVCC slot active so CIC waits + unblocks at COMMIT PREPARED; ONLY residual = CIC wait
doesn't honour lock_timeout (PG cancels cic2 with 55P03).

NEXT (all remaining M0118-0009 Effort-L):
- prepared-transactions-cic: thread session lock_timeout into the CIC active-slot wait
  (mvcc.WaitForSlotsToCommit / design 0118-0031) and abort with 55P03 "canceling
  statement due to lock timeout" — likely the cheapest next promotion (1 permutation).
- prepared-transactions: full 1500-perm SERIALIZABLE SSI verification across held
  prepared xacts (mechanism in place; validate byte-for-byte, close any first-committer/
  conflict-ordering gaps). Probe via throwaway zz_probe_test.go (IsolationRunner.RunAndCompare).
- stats: needs the cumulative pg_stat_* subsystem (function/relation/SLRU stats,
  pg_stat_force_next_flush, track_functions, stats_fetch_consistency) on top of 2PC.
- intra-grant-inplace: pg_class rowmark locking (Effort-L MVCC-tuple-lock core).
Other failing M0118 specs: index-only-bitmapscan, predicate-gin/gist, deadlock-parallel,
fk-partitioned-1/2 (all distinct unbuilt subsystems).
