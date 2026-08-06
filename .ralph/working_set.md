(idle — nothing in flight)

Last loop: **M-NIGHTLY `regress/suite-wedge` — the CASUALTY half is FIXED.**
The wedge is two independent defects; this loop closed the one that made every
wedge unreadable.

1. Did NOT re-run the nightly blind (the baton's stale suggestion): run
   `20260806-191958`'s 9 remaining items are 1 wedge + 8 "casualties", and
   re-running with the casualty mechanism live reproduces the same 9.
2. The 8 casualties were **never truncated output** — they were REAL mismatches
   the harness manufactured. When a case wedges, the suite restarts the cluster
   (correct) and then re-ran `test_setup.sql`. A restart preserves the data dir,
   so the fixtures were never lost, and the re-run is not idempotent: psql has
   no `ON_ERROR_STOP`, so `CREATE TABLE` fails while the following
   `INSERT`/`COPY` succeeds. **Every fixture table doubles** — measured live:
   `onek` 1000→2000, `tenk1` 10000→20000, `int4_tbl` 5→10, `text_tbl` 2→4,
   `varchar_tbl` 4→8. The casualty set is exactly the cases reading those
   tables, which is why it tracks the wedge case as it moves.
3. Fix: `restoreRegressFixtures` + `ClusterRegressExecutor.fixturesPresent()` —
   recovery re-bootstraps ONLY when the fixtures are genuinely absent.
   A wedge now costs 1 action item instead of 9.

Files: `internal/testport/regress_suite_test.go`, new
`internal/testport/regress_suite_recovery_test.go`; design `ci/design/02 §A
"Wedge-recovery rule"` + `ci/design/README.md`; fix_plan (2 wedge items
annotated + M0127 S7 seventh-loop amendment); 1 ledger row.

Key symbols: `restoreRegressFixtures`, `ClusterRegressExecutor.fixturesPresent`,
`clusterPoisoned`, `runRegressSetup`,
`TestPort_RegressSuiteRecoveryKeepsFixturesPristine`.

Gates run: new guard PASS (2.0 s) and proven **non-vacuous** — removing the
guard fails it on all 5 tables; full `TestPort_RegressSuite` PASS (302 s, no
wedge locally); `go vet ./internal/testport` clean; units gate PASS;
`make ralph-state-guard`; pgbench smoke via the commit hook. No engine code
changed (test harness only), so SPOT/DS05 not applicable.

NEXT LOOP (banner: M0124 closed → M0125 closed → **M0127** → M-NIGHTLY → M0123).
S7 is still not met: **the wedge TRIGGER is unexplained** — `multirangetypes`
runs in 0.18 s standalone at HEAD, so it is a whole-suite resource/state
condition, and a nightly containing a wedge is still `fail`. Take that
(M-NIGHTLY, same carve-out): instrument `regress_suite_test.go` to dump session/
lock state + RSS when a case crosses ~60 s and keep the wedged cluster's log,
and check whether the case *preceding* the wedge leaks a backend. Then re-run
`make nightly-batch`.

In-flight: none.
