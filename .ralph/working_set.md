# Working Set (carried from loop 4, 2026-06-13)

## Done this loop — M0100-0007 CLOSED + M0100-0005 E2E confirmed (not yet acceptable)

**M0100-0007 (MergeUpdate) marked `[x]`** — was stale; the test PASSES.
Implemented across `3c931d05` (MERGE RETURNING old/new aliases + merge_action())
and `01356f1c` (cross-partition routing + deferred duplicate-source error).
`TestPort_IsolationMergeUpdate` PASS (4.74s).

**M0100-0005 E2E run confirmed**: ran all 22 dedicated `TestPort_Isolation*`
functions → 22/22 PASS, 0 FAIL / 0 SKIP (`ok …/internal/testport 126.455s`).
The 21 RC specs from M0096-0001 all pass.

## ⚠️ BLOCKER discovered — do NOT close M0100-0005 / accept milestone yet
- **HEAD (`920d03f2`) does NOT build standalone.** Committed
  `internal/executor/operators_storage.go` references `ctx.CTENewToOld` /
  `ctx.CTESelfModifiedErrors`, which exist only in the UNCOMMITTED
  `internal/executor/context.go` diff. `git stash`-ing the `internal/` changes
  makes `go build ./...` FAIL. Split-brain commit — matches the
  `concurrent_ralph_loops_corrupt_tree` hazard.
- 788 uncommitted insertions (analyzer/catalog/executor/parser/planner/mvcc +
  an unrelated `gen_override` feature + 2 new test files) are NOT owned by the
  isolation work. 3 `.claude/worktrees/agent-*` entries modified → concurrent
  agents likely active. I did NOT commit any code; only `.ralph` docs touched.

## Gates run this loop
- go build ./... : PASS (with uncommitted changes present)
- go build clean HEAD (stash internal/) : **FAIL** (CTENewToOld undefined) — restored via stash pop
- 22 dedicated TestPort_Isolation* : PASS (22/22)
- make ralph-state-guard : auto-repaired status/progress inconsistency → OK
- Committed: only `.ralph/fix_plan.md` + `.ralph/working_set.md` (no code)

## Next task (next loop)
M0100-0005 final closure, BLOCKED until the contaminated tree is committed/cleaned
by its owner and HEAD builds standalone. Then: re-run 22 tests on clean HEAD,
verify gofmt/vet + pgbench-S TPS≥2000, reconcile the spurious `Depends: Close of
M0107` note (M0107 unstarted; 0100 milestone-doc DoD does not list it), update
status in `docs/test-port/postgres-oracle-port-status.csv` (NOT the no-status-column
`executable-isolation-tests.md`), mark M0096-0005/M0096-0013 `[x]`, set milestone
0100 + README to `accepted`.
