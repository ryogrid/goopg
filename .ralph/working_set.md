Task: M-NIGHTLY — fix 49 nightly regressions (20260809-020705). M0129-S8.3 command counter fix landed.

Files:
- internal/executor/context.go: added cmdCounterExt + SetCmdCounter() + pointer delegation
- internal/executor/session.go: added cmdCounter field to BasicSession; reset on begin/end
- internal/server/dispatch.go: seed ectx cmdCounter from session

Key symbols: SetCmdCounter, BasicSession.CmdCounter, CommandCounterIncrement, GetCurrentCommandId

Hypothesis/Findings:
- ROOT CAUSE: Each simple-query message creates a fresh executor.Context with cmdCounter
  starting at 0. The cmin/cmax visibility check in TupleVisibleSubxact (S8.3g) makes
  cmin >= curcid → invisible. With curcid=0, all self-inserted tuples (cmin >= 0) are
  hidden from subsequent DML scans (scanMatching). DELETE/UPDATE find no victims → no
  xmax stamp → deferred uniqueness checks at COMMIT find duplicate live tuples → 23505.
- FIX: Store CommandCounter on BasicSession (per-transaction), seed fresh Context from
  session via SetCmdCounter pointer delegation. Reset on tx begin/end.
- Also fixes INSERT-time immediate NND checks that scan via scanMatching.

Verified fixed (13 tests):
  All 5 deferred constraint tests: DeferredNNDMultiColumn, InitiallyDeferred{Exclusion,FK,NND,Unique}Commit
  Isolation: FkSnapshot, InsertConflictDoUpdate{2,3,4}, LockUpdateDelete, Merge{Delete,InsertUpdate,Join,MatchRecheck,Update}, PropagateLockDelete, Stats, TotalCash
  SetConstraintsDeferral

Still failing (pre-existing, NOT caused by M0129):
  - TestPort_IsolationEvalPlanQual (AI-007): failing since 20260808 nightly
  - TestPort_IsolationPlpgsqlToast (AI-019): possible separate issue

Not yet verified:
  - DetachPartitionConcurrently1/3 (AI-005,006), EvalPlanQualTrigger (AI-008)
  - 26 regress tests (AI-024–049): likely same root cause, should be fixed

Next step: Run full testport suite to confirm remaining fixes; update fix_plan.md M-NIGHTLY
  items with findings; investigate any remaining failures.

Gates run:
- UNITS: PASS
- SPOT: PASS (Q12=2, Q13=35, 29.5s)
- pgbench smoke: PASS (14,477/17,392/366,793 tps, 0 failures)
- ralph-state-guard: REPAIRED+OK

In-flight: none
