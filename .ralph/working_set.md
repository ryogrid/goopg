(idle — nothing in flight)

Last loop (#24, M0118-0008): promoted `alter-table-3` (6th M0118-0008
promotion, design 0118-0032). Committed + pushed.

## What landed (one task)
ENABLE/DISABLE TRIGGER lock + abort-time table-lock release.
- Parser (ast.go + ddl.go): ENABLE/DISABLE arm (was a pure no-op) now scans for
  a TRIGGER target and sets `AlterTableStmt.EnableDisableTrigger`; RULE/other
  variants keep the no-op.
- Executor (operators_ddl.go execAlterTable): when the flag is set, look up the
  table and take a txn-scoped `ShareRowExclusiveLock` via the existing
  `acquireDDLLockTxn` (mirrors PG AlterTableGetLockLevel). Conflicts with
  INSERT's RowExclusiveLock (waits), not FOR UPDATE's RowShareLock (proceeds).
- Server (conn_tx.go connTxState.Fail()): now releases the failed txn's
  tableLockMgr locks immediately via `ReleaseTableLocks(c.LockBackendID)`, GATED
  on `c.sess.SavepointDepth()==0`. Mirrors PG AbortTransaction dropping
  heavyweight locks at abort, NOT at the explicit ROLLBACK (empirically verified
  on real PG 18.3: zero pg_locks rows while `idle in transaction (aborted)`).
  Subtransaction abort (savepoint open) retains locks per PG. Idempotent with
  End(). Only tableLockMgr released (relation/advisory left to End()).
- TestPort_IsolationAlterTable3 strict PASS (48 perms).
GATES: build PASS; alter-table-3 strict PASS; lock-sibling (create-trigger,
sequence-ddl, reindex-schema, multiple-cic, lock-nowait, nowait×5, insert-
conflict×7) PASS; savepoint/abort (delete-abort-savept{,2}, aborted-keyrevoke)
PASS; SSI (simple-write-skew, receipt-report, read-only-anomaly{,2},
serializable-parallel{,2}) PASS; -race lockmgr+server; parser/executor units;
state-guard OK. CSV field count 7 verified; docs regenerated.

KEY METHODOLOGY: throwaway zz_probe_test.go ranked the M0118-0008 tail by
first-divergence cost — alter-table-3 diverged only at L62 (61 lines matched).
The 2nd fix (abort-release) needed empirical PG verification via FIFO psql.

NEXT loop candidates (remaining M0118-0008 tail — probe-first):
- `alter-table-4`: table INHERITS + ALTER TABLE INHERIT/NO INHERIT + child-lock
  identification ordering (inheritance reads parent+children: SUM=11 not 1). Med.
- `alter-table-1/2`: FK `NOT VALID` parsing + `ALTER TABLE VALIDATE CONSTRAINT` +
  ShareUpdateExclusive(VALIDATE)/ShareRowExclusive(ADD) lock semantics. Med-large.
- `*-conflict` family (truncate/vacuum/cluster): CREATE ROLE/SET ROLE/OWNER ACL
  infra. Large (biggest unblock leverage).
- `vacuum-no-cleanup-lock`: needs `vacuum_multixact_freeze_min_age` GUC accepted
  + reltuples accounting. GUC is the immediate blocker (probe L4).
- `detach-partition-concurrently-1`: DETACH PARTITION CONCURRENTLY parse (probe
  showed syntax error) + concurrent-detach semantics.

REUSABLE: `acquireDDLLockTxn`(ShareRowExclusive/AccessExclusive) +
`acquireWriteLockTxn`(RowExclusive) + `acquireScanReadLockTxn`(AccessShare) +
`acquireRelLockMaybeTransient` + `waitForRelationLockers`;
`mvcc.SnapshotActiveOtherSlots`/`WaitForSlotsToCommit`;
`planner.ExprContainsColumnRef`. NEW: connTxState.Fail() abort-release pattern.

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). real-PG manual
test needs `LD_LIBRARY_PATH=postgres/local_install/lib` (psql symbol-lookup err
otherwise) + FIFO for multi-session. D-002 CSV is giant single-line row #13
(field 6 rationale COMMA-FREE; append before `,M0060-0004`; verify
`awk -F, 'NR==13{print NF}'`==7). regen: gen-oracle-port-status +
gen-isolation-coverage --repo-root . + gen-oracle-inventory --repo-root .
NEVER `cd` into postgres/. never gofmt -w. Untracked postgres/ + weekly_loc.* +
requirements.txt are stray — leave. .ralph/progress.json driver-managed.
