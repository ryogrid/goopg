(idle — nothing in flight)

Last loop (#47): M-NIGHTLY triage of nightly run 20260725-011243 (26 items, sha
`55809fbf` = a pre-master-merge tpcds-fix2 tip; HEAD `e7d9b88e`). One product
change landed: root-0029, the regress "wedge cascade" misreport.

- 001/002 units+race internal/executor (`TestVerifyHeapam_LateralCommaJoinViaFastPath`)
  — STALE, passes at HEAD; the nightly running during this triage
  (20260728-121843 @ e7d9b88e) reports units PASS / race PASS.
- 003/005/006/007 testport Amcheck/InsertConflict/PartitionDropIndex/PgAmcheck002
  — STALE, all 4 PASS at HEAD.
- 008..026 the 19 `regress/<case> … output mismatch` items — ROOT-CAUSED + FIXED
  (`docs/design/root-0029-nightly-regress-wedge-cascade.md`). 36 cases merely
  burned their 120s budget; the harness diffed psql's TRUNCATED transcript
  against the full expected .out and blamed the normalization rules. Fix:
  `framework.ErrExecTimeout` short-circuits before the diff, `ExecuteSQL` honours
  the ctx deadline, `clusterPoisoned` restarts the cluster after a timeout, and
  `summarize.py` collapses the cascade into one `regress/suite-wedge` item
  (replayed on the real log: 26 items → 17; inert on pre-fix logs).
- The wedge's OWN cause is NOT fixed — ledger row 2026-07-28: orphaned backend
  (no client-disconnect abort in goopg) vs GOMEMLIMIT saturation, indistinguishable
  from per-case durations alone.

Next (two open M-NIGHTLY tasks, in priority order — they preempt M0124):
1. `TestPort_IsolationEvalPlanQual` — CONFIRMED deterministic at HEAD (21.5s).
   `wnested2` permutation: goopg's EPQ recheck evaluates the nested trigger quals
   against the pre-update tuple (L415 `upid: … f` vs PG `lock_id: … t`), final
   read `checking|400` vs PG `checking|-800`. Genuine EPQ gap, own loop.
2. The 9 genuine sub-timeout regress divergences (errors/index_including/
   portals_p2/select/select_distinct still diverge at HEAD full-suite but pass in
   isolation ⇒ suite-ordering state leakage, not normalization). Re-run with
   `-timeout 60m` + `GOOPG_REGRESS_DIFF_DIR` to capture the actual diffs.
   Note: the full-suite re-run hit go test's 10m default inside `tidscan`; use an
   explicit `-timeout` and do NOT run it while a nightly batch is live (co-load).

Gates run: build ./... OK; `go test ./internal/testport/framework/` OK (incl. new
TestRunRegressSubsetTimeoutIsNotOutputMismatch); `go vet ./internal/testport/` OK;
regress-suite smoke (boolean, case) PASS; summarize.py replayed against the real
nightly log both with and without the new rationale.
In-flight: none.
