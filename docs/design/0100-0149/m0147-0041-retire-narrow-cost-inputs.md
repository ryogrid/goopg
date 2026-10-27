# M0147-0041 — retire `GOOPG_NARROW_COST_INPUTS` (cost-input narrowing switch)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 10 → 9 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

R121 narrows the planner's cost inputs (`Path.NCols`, `AvgVarBytes`, `OutputWidth`) to the columns the statement
needs, so a join is priced at roughly the row width PG's pathtarget carries instead of the full concatenated relation
width (`internal/optimizer/narrowcostinputs.go`). R128 promoted it to default ON, and `GOOPG_NARROW_COST_INPUTS=0`
opted back out to full-relation widths. Five functions checked the switch: `relNarrowedColumns`,
`joinrelNarrowedWidths`, `narrowBaseRelCostWidths`, `narrowJoinWidths` and `inheritNarrowedWidths`.

## Removed / changed

- `narrowcostinputs.go`: the env read, `narrowCostInputsFromEnv` and `setNarrowCostInputsForTest`, plus the `os`
  import.
  - `narrowCostInputsEnabled()` now reads `narrowCostInputsForceOff`, an unexported test seam with no env or GUC
    route.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0041)`). `scripts/planner-flags.env` is regenerated.
- Comments in `narrowcostinputs.go`, `joinpathsmemoize.go` and `path.go` no longer describe the switch as live.

`deadcode ./cmd/...` was diffed before and after. The old test-only setter left the unreachable list; nothing was
added to it.

## Why a test seam survives

`TestNarrowCostInputsReachesTheLiveSearch` is the only end-to-end pin that the production call site in
`relfromjoinlist.go` still runs inside the live search. It plans one statement twice and requires a searched join cost
to move between the arms. That makes the un-narrowed arm a genuine oracle rather than a pin of the switch. The
precedent is `scanPrefilterForceLate` (M0147-0034): retire the env read, keep an env-free seam for the one test that
needs a reference arm. The test-file helper `forceNarrowCostInputsOffForTest` sets it.

## Tests

- 24 `defer setNarrowCostInputsForTest(true)()` pins are deleted. ON is the default.
- Deleted outright, because each only pinned the switch:
  - `TestNarrowCostInputsFlagIsOptOutAndDefaultOn` (env polarity);
  - `TestRelNarrowedWidthsInertWhenFlagOff`;
  - `TestInheritNarrowedWidthsInertWhenFlagOff`;
  - `TestRelNarrowedWidthsNonTableLeafInertWhenFlagOff`.
- `TestNarrowJoinWidthsIsIdempotentAndNilSafeAndFlagGated` loses its flag-off half and is renamed
  `…IdempotentAndNilSafe`.
- `TestGetMemoizePathDeclinesIndexOnlyInner` is kept. Its invariant now covers a rel that declines to narrow.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md` and `CLAUDE.md` finds
no setter; only the generated flag table names it. Unset, the variable resolved to ON, which is the only server arm
left. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0041 entry in `.ralph/fix_plan.md`.
