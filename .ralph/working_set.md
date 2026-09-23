Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Landed: slices 1, 2a, 2b, 3, 4; slice 5 re-run; index_update_stats;
planner toggles + eqsel isunique (8a8f1c11f).
Open: SAOP-prefix + range (TestSAOPWithConjunctMoves); owner multiplier call.

Files (this loop): internal/postmaster/dispatch.go (plannerSettingsFrom reads
enable_seqscan/indexscan/bitmapscan/sort), internal/optimizer/joinsearch.go
(prebuiltLeafDisabledNodes), selectivity.go (uniqueColumnTuples,
uniqueEqSelectivity), planner.go (seqWinsEqualityProbe synthetic scan carries
index uniqueness); tests unique_eqsel_test.go, plan_cache_cost_gucs_test.go.

Findings:
- The four toggles were never read into PlannerSettings, so the search
  ignored SET enable_* on both pipelines. Fixed.
- 292b1af2e (CREATE INDEX size recording) had REGRESSED the pass-required
  TestPort_IsolationMultipleRowVersions (1M-row PK point UPDATE seq-scanned
  because the key was priced at 1/200). Fixed via eqsel isunique; bisected.
  LESSON: run the TestPort_Isolation family for any change that alters
  table statistics or selectivity, not just the regress suite.
- TestPort_IsolationEvalPlanQual is intermittent at HEAD too (1/3 passed
  there) — the nightly item AI-20260922-004850-001.

Next step: per banner, M0145-0029 SAOP-prefix + range (an IN list followed by
a range on the next index column; needs a SAOP probe with a trailing bound in
the executor); or the owner's multiplier answer if given.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (knob plans identical),
TestPort_RegressSuite, TestPort_Isolation family (only the pre-existing
EvalPlanQual flake fails) — PASS.
In-flight: none.
