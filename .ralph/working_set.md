Loop #43 COMPLETE: M0118-0009 `horizons.spec` PROMOTED (failed→pass, all 5 perms),
design 0118-0105. Committed + pushed pending. This CLOSES the 0118-0104 perm-4 deferral.

What landed (two coupled changes):
- internal/server/dispatch.go: RR/SSI snapshot now pinned at the FIRST
  snapshot-taking statement of a batched `BEGIN ISOLATION LEVEL …` message
  (PG-correct timing) so `BEGIN RR; SELECT 1;` registers its proc-array xmin →
  OldestXmin/VACUUM retain rows (perm 4 Heap Fetches stays 2). Gated by new
  `stmtTakesSnapshot` allowlist (internal/server/notify.go: SELECT/INSERT/UPDATE/
  DELETE/MERGE/DECLARE CURSOR) so a trailing `SET …` does NOT over-pin
  (serializable-parallel s3 would else see Y=0 not Y=20).
- internal/executor/operators_storage.go: fixed a latent LOST-UPDATE bug the
  early pin exposed. All 5 EvalPlanQual write sites classified a settled xmax by
  snapshot membership (`!snap.HasInProgress(xmax)`⇒aborted); a tx that STARTED
  AFTER a frozen RR/SSI snapshot is also absent yet may have COMMITTED → row
  silently overwritten instead of 40001. New shared `epqXmaxSettled(ctx,xmax)`
  consults TxnMgr authoritatively (HasAbortedXID→proceed / IsXIDActive→retry /
  else committed→40001) for RR/SSI ONLY; RC path byte-identical (guarded). New
  `epqSerializationErr` centralises concurrent-delete-vs-update message.
- internal/testport/isolation_port_test.go: TestPort_IsolationHorizons soft→strict.
- docs/design/0118-0105 + README index; inventory CSV horizons failed→pass;
  coverage/inventory md regenerated.

Gates run (ALL PASS): TestPort_IsolationHorizons strict 5/5; EvalPlanQualTrigger
strict; broad RR/SSI+EPQ+row-lock+SSI-parallel+merge/insert-conflict/fk/deadlock/
skip-locked/nowait batch (~57 specs across two runs) no regression; -race
mvcc/server/executor; go vet + build + gofmt(edited regions) clean. pgbench smoke
= pre-commit hook (on commit).

NEXT (remaining M0118-0009, all Effort-L, distinct unbuilt subsystems):
- intra-grant-inplace (pg_class): shared-catalog MVCC-tuple rowmarks (FOR NO KEY
  UPDATE/KEY SHARE/UPDATE on pg_class) + inplace update (relhasindex) + LockTuple
  deadlocks. Hardest. db-sibling done via dbACLChangeXID shortcut (0118-0098) but
  pg_class needs real rowmark machinery — NOT a simple reuse.
- stats: full cumulative-stats subsystem (pg_stat_force_next_flush, track_functions,
  pg_stat_reset_single_function_counters, stats_fetch_consistency snapshot, SLRU
  stats, 2PC interaction). Large.
- prepared-transactions{,-cic}: 2PC (PREPARE TRANSACTION / COMMIT PREPARED).
