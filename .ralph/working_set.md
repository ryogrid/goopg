(idle — nothing in flight)

Last loop (#23, M0118-0008): promoted `multiple-cic` (5th M0118-0008
promotion, design 0118-0031). Committed + pushed.

## What landed
- `execCreateIndex` (operators_ddl.go): const-fold a partial-index WHERE
  predicate that references NO table columns — evaluate once via
  `evalExpr(pred, nil, ctx)` (new exported `planner.ExprContainsColumnRef`
  guard), mirroring PG `eval_const_expressions` in BuildIndexInfo. Stored
  `idx.Predicate` untouched (pg_get_indexdef/pg_dump unaffected). Makes the
  IMMUTABLE advisory-lock predicate fire on an EMPTY table so s1i blocks.
- `CreateIndexStmt.Concurrently` added (parser/ast.go) + recorded in
  `parseCreateIndexTail` (parser/ddl.go). CIC now captures active-txn slots at
  START (`mvcc.SnapshotActiveOtherSlots`, refactored out of
  `WaitForOlderSlotsToCommit`) and drains them after build
  (`WaitForSlotsToCommit`) → newer build completes after older. Start-time
  snapshot avoids mutual wait between two simultaneous CICs.
- Runner (isolation_runner.go): `(*)` star branch now drains UNGATED pending
  steps before the star step's own completion; `partitionGatedOn` keeps
  `BlockerStepComplete`-gated steps (deadlock-hard s7a8(s8a1)) reported AFTER.
- `TestPort_IsolationMultipleCic` strict PASS.
GATES: build PASS; multiple-cic strict PASS; ALL strict `(*)` specs
(deadlock-{hard,soft,soft-2}, classroom-scheduling, project-manager,
serializable-parallel{,-2}, temporal-range-integrity, multixact-no-deadlock,
tuplelock-upgrade-no-deadlock, timeouts) PASS; lock-sibling regression PASS;
-race mvcc/lockmgr; parser/planner/executor units; pgbench smoke (pre-commit).
CSV field count verified 7; coverage/inventory/port-status regenerated.

KEY METHODOLOGY: throwaway zz_probe_test.go ranked the M0118-0008 tail by
first-divergence cost. multiple-cic was 1 line off (only completion order) once
const-fold + CIC-drain landed.

NEXT loop candidates (remaining M0118-0008 tail — probe-first):
- biggest leverage = ROLE/ACL infra (CREATE ROLE/GRANT/SET ROLE/ALTER OWNER +
  permission-denied 42501) unblocks truncate-conflict/vacuum-conflict/
  cluster-conflict. goopg has only a wire-layer string CREATE/DROP ROLE
  (server/role_ddl.go); no GRANT/SET ROLE/privilege model. Large.
- `alter-table-1/2`: parser gaps (FK `... NOT VALID` errors "expected keyword
  deferrable"; `ALTER TABLE ... VALIDATE CONSTRAINT` errors "expected ADD or
  DROP") + ShareUpdateExclusive(VALIDATE)/ShareRowExclusive(ADD) lock semantics
  + 140/48 perms. Medium-large.
- `inherit-temp`: goopg includes ANOTHER session's temp child in a parent
  SELECT (global catalog, no per-session temp ownership). Needs session-scoped
  temp tables + RELATION_IS_OTHER_TEMP inheritance exclusion. Large.
- `reindex-concurrently-toast`: needs `allow_system_table_mods` GUC + real TOAST
  rels in pg_class (reltoastrelid) + pg_toast.<name> reindex. Large.
- partition specs / vacuum-skip-locked / vacuum-concurrent-drop: PARTITION BY
  LIST + partitioned VACUUM log parity.

REUSABLE: `acquireRelLockMaybeTransient` + `waitForRelationLockers` (locks);
`mvcc.SnapshotActiveOtherSlots`/`WaitForSlotsToCommit` (CIC drain);
`planner.ExprContainsColumnRef` (const-fold guard).

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). D-002 CSV is one
giant single-line row #13 (field 6 rationale COMMA-FREE; append before
`,M0060-0004`; verify `awk -F, 'NR==13{print NF}'` == 7). regen: gen-oracle-
port-status + gen-isolation-coverage --repo-root . + gen-oracle-inventory
--repo-root . NEVER `cd` into postgres/. never gofmt -w. Untracked postgres/ +
weekly_loc.* + requirements.txt are stray — leave. .ralph/progress.json is
driver-managed — don't commit it.
