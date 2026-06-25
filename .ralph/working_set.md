Loop #45 COMPLETE: M0118-0007 group CLOSED — `eval-plan-qual.spec` PROMOTED
(failed→pass, all 50 perms strict), design 0118-0106. Committing + pushing.

What landed (EPQ recheck over a join — two coupled executor fixes):
- internal/executor/operators_lockrows.go: the index key condition was folded
  into the per-row EvalPlanQual recheck filter unconditionally; when the locked
  index scan is the inner of a join its key `ix.Key` is a join/correlated column
  ref (`jt.id = y`) whose ColumnRef.Index is in the JOIN coordinate space, but
  epqRecheckFilter decodes only the locked table's own columns → `y`(idx 1)
  misread as jointest.data → `jt.id=jt.data` → false → post-update row dropped
  (0 rows). Fix: fold idxPred ONLY when `ix.Key` is row-local/constant — new
  `exprRefsColumnOrOuter(expr)` guard. Non-key UPDATE preserves join key so
  skipping its recheck is correct; key changes still caught by CTID-chain.
  Also added `markJoinPreserveCTID` walker (sets preserveCTIDRel before child Open).
- internal/executor/operators_join_agg.go (SIBLING latent-bug fix): a lazy hash
  join whose locked relation lands on the BUILD side (drained+closed at Open)
  lost its currentTID → FOR UPDATE silently returned the STALE pre-update row.
  New joinOp fields preserveCTIDRel/preserveBuildSide/lazyHashCTID/lazyMatchCTIDs;
  buildHashRightWithCTID captures build-side ctids via drainRowsCtxCTID; nextLazy
  stamps the matched build row's ctid onto the emitted slot → lockRowsOp
  ms.hasCTID fallback recovers TID. nil/no-op for queries without FOR UPDATE →
  TPC-H hash-join hot path byte-identical.
- internal/testport/isolation_port_test.go: TestPort_IsolationEvalPlanQual
  soft→strict (runIsoSpecStrict).
- docs/design/0118-0106 + README index; inventory CSV failed→pass; D-002 narrative;
  coverage/inventory md regenerated. Isolation tally now 111 pass / 10 failed.

Gates run (PASS): TestPort_IsolationEvalPlanQual strict 50/50; EPQ-trigger +
lock-update-{delete,traversal} + update-locked-tuple + tuplelock-update +
partial-index + simple-write-skew + predicate-lock-hot-tuple PASS no regression;
-race executor join/lockrows; full executor unit suite; go build+vet clean;
gofmt my-regions clean (pre-existing compact-style lines NOT touched per version
rule). TPC-H spotcheck = known WSL2 startup-hang infra failure (not a regression;
change provably gated off for non-FOR-UPDATE queries); pgbench smoke = pre-commit hook.

NEXT (remaining M0118, all Effort-L distinct unbuilt subsystems):
- intra-grant-inplace (pg_class): runtime shared-catalog MVCC-tuple row locks.
- stats: cumulative function-stats + stats_fetch_consistency + 2PC interaction.
- prepared-transactions{,-cic}: 2PC (PREPARE/COMMIT PREPARED) — also gates stats.
- Non-0009: deadlock-parallel (lock groups), fk-partitioned-1/2 (ATTACH PARTITION
  + partitioned FK), index-only-bitmapscan (BitmapOr plan + EXPLAIN DECLARE CURSOR
  + VACUUM (TRUNCATE false)), predicate-gin/gist (AM granularity).
