# M0147-0036 — retire `GOOPG_SUBPLAN_RESCAN` (legacy per-call SubPlan lifecycle)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 15 → 14 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

Stage 9's SubPlan handle engine (`internal/executor/subplan.go`) builds a sublink's operator tree once per statement
and re-runs it per outer row by re-Open or Close+Open. That is the shape of PG's `nodeSubplan.c`, which only ever
`ExecReScan`s. The handle also carries the cacheability gate: a correlated sublink over a volatile function or a
LockRows node is never served from a value cache.

`GOOPG_SUBPLAN_RESCAN=off` (default on) restored the pre-Stage-9 lifecycle:
- a fresh `Build` + `Open` + `Close` per call;
- for index-scan-based correlated scalar subqueries, the `Context.CorrSubqOps` registry of pre-built operators;
- `subPlanResultCacheable` always true, with no volatility gate.

PG never runs that lifecycle, so phase 2 deletes it.

## Removed / changed

- `subplan.go`: `subPlanRescanOn`, its `init` env read, `SetSubPlanRescanEnabled` (exported, test-only),
  `subPlanRescanEnabled`, and the `os` and `sync/atomic` imports.
  - `subPlanResultCacheable` always consults the handle.
  - `acquireSubPlanOp` loses its per-call Build/Close branch.
- `expr.go` (`subqueryImpl`'s correlated index-scan path): the `CorrSubqOps` branch is gone, and the handle serves
  the path unconditionally. Two comments that described the registry are updated.
- `context.go`: the `CorrSubqOps` field is removed, since nothing populates or reads it. The `SubPlanSiteStats.Rescans`
  comment now names the handle path.

`deadcode ./cmd/...` was diffed before and after. Only `SetSubPlanRescanEnabled` left its list.

## Tests

- `TestHandleKillSwitchLegacyLifecycle` is deleted. It pinned the legacy lifecycle: Rebuilds == Calls, no handles.
- `TestDMLSublinkEngineSettingsAgree` keeps its hashed-IN half (`SetHashedSubPlanEnabled`, retired separately by
  M0147-0037) and drops the rescan-engine half.
- `TestSubPlanStatsCorrelatedScalarRescans` is unchanged in substance: the handle path still reports one Rebuild and
  Rescans for the rest. Its comment and message no longer say CorrSubqOps.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to the
handle engine, which is the only path left. This executor change is values-checked by the sf025 sweep and the
acceptance arm.

## Gates

See the M0147-0036 entry in `.ralph/fix_plan.md`.
