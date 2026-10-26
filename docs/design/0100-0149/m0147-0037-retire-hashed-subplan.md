# M0147-0037 — retire `GOOPG_HASHED_SUBPLAN` (hashed IN-probe switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 14 → 13 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

Stage 11 (`internal/executor/subplan_hash.go`) loads an uncorrelated, hashable IN / NOT IN SubPlan's output into a hash
table once and probes it per outer row, keeping NOT IN's NULL semantics. That is PG's hashed SubPlan:
`buildSubPlanHash` and `ExecHashSubPlan` in `nodeSubplan.c`. EXPLAIN labels it `(hashed SubPlan N)` through
`subPlanUsesHashTable` (`operators_explain.go`).

`GOOPG_HASHED_SUBPLAN=off` (default on) forced every IN onto the linear scan over the cached value slice. That slice
stays as the non-hashable fallback in production: correlated INs, non-equality ANY/ALL forms, and unhashable kinds.

## Removed / changed — the env lever goes, the test oracle stays

Like M0147-0034, this OFF arm is a genuine oracle. `subplan_hash_test.go` states it outright: "the linear path is the
correctness oracle". `runBothHashPaths`, `TestBigNumericInProbeMatchesLinearOracle` and
`TestDMLSublinkEngineSettingsAgree` each compare the hashed result with the forced-linear one. So:

- `subplan_hash.go`: `hashedSubPlanOn`, its `init` env read, the exported test-only `SetHashedSubPlanEnabled`, and
  the `os` import are removed. The new unexported, env-free test knob `hashedSubPlanForceLinear atomic.Bool` (default
  false) drives `hashedSubPlanEnabled()`. The probe and the EXPLAIN label both follow it, so the forced-linear arm
  stays coherent end to end.
- The three test files switch to the knob, with the polarity inverted.

`internal/optimizer/exists_to_any.go`'s comment still says "same pattern as GOOPG_HASHED_SUBPLAN". It is left alone
so this commit stays executor-only, and is harmless as history.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). The knob defaults to false,
which is the old ON arm. This executor change is values-checked by the sf025 sweep and the acceptance arm, and the
EXPLAIN label is checked by the census, the sf025 plan shape and RegressSuite.

## Gates

See the M0147-0037 entry in `.ralph/fix_plan.md`.
