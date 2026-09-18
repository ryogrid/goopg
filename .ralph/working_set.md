Task: M-NIGHTLY-instrumentscope-race-fix — test-first observation
  step DONE (commit bc06cd3c0): new
  `TestExplainAnalyzeSubPlanScopeObservation`
  (`internal/executor/explain_subplan_test.go` ~L305-389) + fix_plan
  bullet (~L530). Open question RESOLVED; impl stays HOLD-blocked.

Files: explain_subplan_test.go (new test); .ralph/fix_plan.md
  (instrumentscope entry ~L485-550). No production changes.

Key symbols: `withInstrumentation` (instrument.go:323 — scope live
  only during top-level `Build`); `acquireSubPlanOp` (subplan.go:302 —
  `Build(plan)` from `existsImpl`/`subqueryImpl`/`collectInValues` at
  row-eval time); `instrumentScope`/`buildUnderNilScope`/
  `buildUnderFreshScope`; helpers `pinCorrelatedSubPlanPath`/
  `explainSubPlanFixture`/`joinedPlan`.

Hypothesis/Findings: CONFIRMED — SubPlan children are NEVER
  instrumented today: serial EXPLAIN ANALYZE + correlated EXISTS shows
  `SubPlan 1 (calls=2 rebuilds=1 rescans=1 ...)` (ctx.SubPlanStats,
  separate channel) with the subtree estimate-only, no `(actual ...)`.
  acquireSubPlanOp's Build runs after the scope restored to nil →
  forcing nil there under the planned `Context.instrumentScope` is
  bug-for-bug-compatible. Also source-confirms ledger row m0142-0004a
  (PG DOES instrument subplan children — separate fidelity gap).

Environment note: `data.HOLD` stands (re-imposed 2026-09-18T22:52 —
  owner inspect). ALL internal/ impl commits blocked; S2b-15's six
  staged optimizer files preserved (dance held). Nightly
  20260919-000526 still running — triage its AI when done. NOTE:
  ci/logs/launch.log gets auto-staged by the nightly — unstage before
  committing.

Gates run: `go test -run ...SubPlan...` PASS; gofmt/vet clean;
  pgbench smoke PASS (commit hook).
In-flight: none.
Next step: item-8 M-NIGHTLY next open (testport/
  TestE2E_PGColdStartOnGoopgDataDir topmost, then isolation items) —
  impl all blocked; legal = recon/test-only on those entries, or
  milestones M0119→M0122→M0134→M0095/M0110 (TAP ports, test-only).
