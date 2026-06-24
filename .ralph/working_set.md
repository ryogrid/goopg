Loop #42 COMPLETE: M0118-0009 horizons MVCC pruning-horizon core ENABLER landed
(design 0118-0104 — NOT a promotion). 4/5 horizons permutations now match PG 18.3;
horizons stays `failed` on perm 4 only. Committed + pushed.

What landed (the TEMPORARY half + no-vacuum permanent permutations):
- internal/mvcc/manager.go: OldestXminForProc(procNum) — session-local horizon
  min(nextXID, slot.xid, slot.xmin) for ONE backend (falls back to global);
  ignores other backends' snapshots but respects the owner's own in-progress txn.
- internal/vacuum/vacuum.go: VacuumOptions.Horizon override (0 ⇒ mgr.OldestXmin()).
- internal/executor/operators_vacuum.go: temp targets vacuum at OldestXminForProc.
- internal/executor/operators_indexonly.go: (a) counting rule — skip an index
  entry whose root line pointer is LP_UNUSED/LP_DEAD without a Heap Fetches tally
  (kill_prior_tuple analog); (b) pruneTouchedTempPages — after the scan, prune the
  temp heap blocks fetched at the session-local horizon (PageVacuumPrune +
  LogHeapPruneOpt), NO VM ALL_VISIBLE set (keeps next scan on fallback path).
- internal/mvcc/manager_test.go: TestOldestXminForProc_SessionLocalIgnoresOtherSnapshots.
- docs/design/0118-0104 + README; inventory CSV stays failed; fix_plan + ledger.

RESIDUAL BLOCKER (perm 4 only — perm-table VACUUM-respects-older-snapshot, Heap
Fetches must stay 2): lifeline's batched `BEGIN ISOLATION LEVEL REPEATABLE READ;
SELECT 1;` never registers the RR tx's snapshot xmin in the proc array (goopg
captures an RR snapshot lazily at the first SEPARATE-message statement; here
SELECT 1 shares BEGIN's message). The fix — capture the RR snapshot at the
batched first statement (PG-correct, in server/dispatch.go per-statement loop ~L728
`else if !autoCommit` SnapshotFor(ectx.Tx)) — was IMPLEMENTED and REVERTED: it
regresses eval-plan-qual-trigger (whose s2_b_rr is the same batched shape) by
exposing a LATENT goopg bug: goopg fails to raise 40001 "could not serialize
access due to concurrent update" for an RR UPDATE of a concurrently-updated row
when the snapshot is captured at the correct (earlier) time. Current late-capture
compensates.

NEXT (separate, deeper change): fix goopg's RR concurrent-update (40001) detection
to be robust to snapshot-capture timing, THEN re-apply the batched-BEGIN RR
snapshot-pinning in dispatch.go → perm 4 passes → promote horizons to runIsoSpecStrict
+ flip CSV failed→pass + regen coverage. Start: probe eval-plan-qual-trigger with
the dispatch change re-applied to see the exact concurrent-update divergence; the
RR update-conflict path is in operators_storage.go epqWait / concurrentModifierXID.

Other remaining M0118-0009 (all Effort-L): intra-grant-inplace (pg_class xmax-wait —
runtime shared-catalog MVCC-tuple row locks), stats (pg_stat_force_next_flush +
cumulative stats), prepared-transactions{,-cic} (2PC).

Gates run: TestPort_IsolationHorizons (soft 4/5 — SKIP) + new unit test PASS;
-race mvcc/vacuum/storage + executor/server PASS; predicate-hash + eval-plan-qual-
trigger NON-REGRESSION confirmed PASS; build+vet+gofmt clean; pgbench smoke =
pre-commit hook. vacuum-skip-locked/vacuum-concurrent-drop fail identically on
clean HEAD (pre-existing timing flakes, unrelated).
