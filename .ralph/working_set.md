Task: M0129-S8.3 — cmin/cmax stamps + test fixes (partial: engine core DONE, test infra DEFERRED)

Files:
- internal/storage/command_id.go: InvalidCommandId changed 0→^uint32(0) (PG match)
- internal/mvcc/visibility.go: TupleVisible cmin/cmax check, comment updates
- internal/executor/executor.go: Run() advances command counter for test compat
- internal/executor/plpgsql_runtime.go: executeSQLRoutine + executeSQLProcedureCore:
  per-statement routineCommandCounterIncrement + parent cmdCounter sync (CmdID NOT synced)
- internal/executor/with_compat_test.go: runQuery advances counter
- internal/executor/time_text_cast_test.go: runSQL advances counter

Key symbols:
- CommandCounter, GetCurrentCommandId, CommandCounterIncrement
- TupleVisible, GetCmin, GetCmax
- executeSQLRoutine, executeSQLProcedureCore, routineCommandCounterIncrement
- InvalidCommandId, FirstCommandId

Hypothesis/Findings:
- Fence retirement is COMPLETE (10/10 CTE DML tests pass → 11/11 now)
- InvalidCommandId was 0, should be ~uint32(0) like PG → FIXED
- SQL volatile functions didn't advance counter per-statement → FIXED
- 102 executor unit tests FAIL because raw Build+Open+Next test helpers
  don't advance the command counter (pre-existing gap exposed by cmin check)
- Production dispatch path (dispatch.go:920-921) advances correctly → SPOT+DS05 PASS
- cmax stamp in stampUpdaterXmaxNonHOT not yet done (no test coverage needs it;
  old-tuple cmax=0 works correctly when curcid > 0)

Next step: Fix test infrastructure gap — add CommandCounterIncrement+GetCurrentCommandId
to test helpers that bypass dispatch (runQueryErr, drainScan wrappers, and ~100 raw
Build+Open+Next sites). Then run full executor unit suite + pre-commit gate.

Gates run:
- go build ./...: PASS
- 11/11 CTE DML tests: PASS
- 5/5 HOT tests: PASS
- mvcc tests: PASS
- storage tests: PASS
- SPOT (Q12/Q13): PASS (2/35 rows)
- DS05 SF0.5 sweep: PASS (95/95, 0 mismatches, plans identical)
- make ralph-state-guard: PASS (auto-repaired)

In-flight: none
