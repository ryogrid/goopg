(idle — nothing in flight)

Last loop (#17, M0118-0008 PARTIAL): probe-first ranked all 25 DDL/VACUUM specs —
NONE passed as-is (this group is the hard tail). Promoted `create-trigger` to
pass-required (design 0118-0027) by adding the write+DDL siblings of the existing
read-side `acquireScanReadLockTxn` (0118-0018, txn-scoped AccessShare):
- `Context.acquireWriteLockTxn(rel)` → txn-scoped RowExclusiveLock, wired at
  insertOp/updateOp/deleteOp.Open (operators_storage.go).
- `Context.acquireDDLLockTxn(rel,mode)` → txn-scoped mode, wired at
  execCreateTrigger (operators_ddl.go) with ShareRowExclusiveLock.
Same confinement as read sibling (no-op when TxnLockBackendID==0 or system
catalog). RowExclusiveLock self-compatible ⇒ concurrent DML never blocks at table
level (pgbench TPC-B 0-failed). `TestPort_IsolationCreateTrigger` strict PASS.
GATES: -race lockmgr/mvcc/executor PASS; full executor suite PASS; row-lock/
deadlock/merge/timeout batch PASS; pgbench smoke 0-failed. CSV D-002 + coverage +
inventory + port-status regenerated. Committed+pushed? (verify).

NEXT loop candidates (remaining M0118 groups, all real-feature tails):
- M0118-0008 remaining 24 specs (deferred, ledger 2026-06-22): biggest leverage =
  ROLE/ACL infra (CREATE ROLE/GRANT/SET ROLE/ALTER TABLE OWNER + permission-denied)
  unblocks truncate-conflict + vacuum-conflict + cluster-conflict{,-partition} (4
  specs). sequence-ddl (ALTER SEQUENCE lock-on-nextval) is self-contained-ish.
  alter-table-* need ADD/VALIDATE CONSTRAINT. reindex/partition/CIC/inherit-temp all
  distinct features.
- M0118-0002 predicate-hash (SSI granularity), M0118-0005 fk-deadlock/ri-trigger/
  fk-partitioned, M0118-0007 eval-plan-qual, M0118-0009 misc (LISTEN/NOTIFY, 2PC,
  pg_stat_*, horizons) — all deferred, all hard.

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). tuplelock-upgrade-
no-deadlock is TIMING-SENSITIVE — defers under heavy parallel test load on WSL2 but
passes in isolation (NOT a regression; verify isolated before blaming a change).
D-002 CSV is one giant single-line row #13 (field 6 rationale COMMA-FREE; append
before `,M0060-0004`). regen: gen-isolation-coverage + gen-oracle-inventory +
gen-oracle-port-status. PROBE pattern: throwaway zz_probe_test.go in
internal/testport importing internal/testutil/cluster + framework; RunAndCompare,
log status+Diff; delete after. NEVER `cd` into postgres/ in Bash (cwd persists →
breaks `go test ./...`). never gofmt -w. Untracked postgres/ + weekly_loc.* +
requirements.txt are stray — leave them.
