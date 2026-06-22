(idle — nothing in flight)

Last loop (#20, M0118-0008): committed the in-flight `sequence-ddl` promotion that
the prior loop built but left uncommitted (working_set wrongly said idle; tree had
WIP). Verified coherent + committed (design 0118-0028):
- `acquireSequenceLockTxn` (context.go): nextval RowExclusiveLock on the sequence
  rel — held to commit in an explicit txn (TxnLockBackendID), transient
  acquire+release under per-statement BackendID in autocommit (wait still happens
  on acquire). Self-compatible ⇒ concurrent nextval/SERIAL never block.
- `execAlterSequence` (operators_ddl.go): AccessExclusiveLock via acquireDDLLockTxn.
- `evalNextval` (operators_sequence.go): resolves seq Table via LookupTable→RelFileNode.
- `TestPort_IsolationSequenceDdl` strict PASS (5 perms).
GATES: build PASS; sequence-ddl strict PASS; -race lockmgr+executor PASS; doc regen
no-drift; pgbench smoke via pre-commit hook. CSV/coverage/inventory/port-status all
current. Committed+pushed (verify next loop).

NEXT loop candidates (remaining M0118-0008 DDL/VACUUM tail, all real-feature):
- biggest leverage = ROLE/ACL infra (CREATE ROLE/GRANT/SET ROLE/ALTER TABLE OWNER +
  permission-denied) unblocks truncate-conflict + vacuum-conflict + cluster-conflict
  {,-partition} (4 specs).
- alter-table-* need ADD/VALIDATE CONSTRAINT lock semantics. reindex-* need REINDEX
  SCHEMA CONCURRENTLY parsing + allow_system_table_mods. multiple-cic/partition need
  CIC waiting + ATTACH/DETACH. inherit-temp, plpgsql-toast distinct.
- Other groups: M0118-0002 predicate-hash (SSI granularity), M0118-0005 fk-deadlock/
  ri-trigger/fk-partitioned, M0118-0007 eval-plan-qual, M0118-0009 misc — all hard.

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). tuplelock-upgrade-no-
deadlock TIMING-SENSITIVE on WSL2 under load (not a regression). D-002 CSV is one
giant single-line row #13 (field 6 rationale COMMA-FREE; append before `,M0060-0004`).
regen: gen-oracle-port-status + gen-isolation-coverage + gen-oracle-inventory. PROBE:
throwaway zz_probe_test.go (RunAndCompare, log status+Diff). NEVER `cd` into postgres/.
never gofmt -w. Untracked postgres/ + weekly_loc.* + requirements.txt are stray — leave.
The "lone --live loop shows as 2 ralph_loop.sh" is the timeout subshell (ppid chain),
NOT a concurrent loop — verify ppid before fearing tree corruption.
