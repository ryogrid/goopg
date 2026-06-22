(idle — nothing in flight)

Last loop (#30): M0118-0008 — promoted `vacuum-concurrent-drop` (design
0118-0035). Committed + pushed pending. Eighth M0118-0008 promotion.

What landed:
- `internal/executor/operators_vacuum.go` / `operators_analyze.go`: on the
  NON-SKIP_LOCKED path VACUUM/ANALYZE now take a BLOCKING per-relation
  ShareUpdateExclusiveLock (acquireRelLockMaybeTransient) so they wait behind a
  concurrent `LOCK part1 IN SHARE MODE` (`<waiting ...>`). New helper
  `relationStillExists` (InMemory.LookupTableByOID) re-checks each target after
  the lock; a target DROPped by the committing session is skipped — WARNING
  "skipping vacuum/analyze of X --- relation no longer exists" for an explicit
  target, silent for an expanded partition child.
- `TestPort_IsolationVacuumConcurrentDrop` strict (6 perms PASS).
- CSV: target-inventory row → status=pass; D-002 rationale appended; 3 md
  regenerated. fix_plan + design README + design 0118-0035.

Gates run: strict new test PASS; vacuum-skip-locked PASS (no regression);
executor vacuum/analyze units; `-race` lockmgr; pgbench smoke.

Next loop: pick another M0118 deferred spec. Remaining M0118-0008 tail is hard
(probed loop #30): alter-table-{1,2,4} (ADD/VALIDATE CONSTRAINT lock semantics),
*-conflict family (role/GRANT privilege infra), partition ATTACH/DETACH (need
snapshot-aware partition visibility — see detach-partition-concurrently-1),
vacuum-no-cleanup-lock, inherit-temp (inheritance scanning), plpgsql-toast.
Other groups: fk-deadlock (FK KEY-SHARE wait over-conflicts + deadlock-timeout
interplay — deep MVCC), eval-plan-qual (EPQ-over-join), predicate-hash (SSI
granularity), horizons (dollar-quote lexer + EXPLAIN JSON), intra-grant-inplace
(catalog-tuple xmax locks — hardest). No cheap no-engine-change wins remain.
