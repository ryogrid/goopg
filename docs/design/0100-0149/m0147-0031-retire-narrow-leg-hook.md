# M0147-0031 — retire `GOOPG_NARROW_LEG_HOOK` (join-leg narrowing switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 20 → 19 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

`narrowJoinLeg` (`internal/optimizer/joinleghook.go`) is the attachment point that narrows a join leg
`joinInputsFor` would otherwise pass along bare:
- a hash join's outer (probe) side;
- both sides of a plain nested loop;
- the outer side of an NLI join.

It wraps the leg in a `*Project` naming only the needed columns. It uses the same keep-set chain as
`narrowBuildInput`.

M0139-S1 stood the hook up as count-only. M0139-S2 made it load-bearing. It ran behind `GOOPG_NARROW_LEG_HOOK`,
default on, where `=0` opted out. The `flaglabels.go` comment still described the count-only S1 state ("never changes
a node"). The M0147-0030 inventory repeated that claim, and both are corrected here: the OFF arm left legs
un-narrowed, which is a plan difference, not just a counter.

The narrowing is goopg-private plumbing. PG prunes columns through each path's target list and `use_physical_tlist`,
not through Project nodes, and these Projects do not appear in EXPLAIN. Phase 2 keeps the shipped ON arm and deletes
the OFF arm.

## Removed / changed

- `joinleghook.go`: `narrowLegHook`, `narrowLegHookFromEnv` and the `os` import. `narrowJoinLeg` no longer checks a
  flag. `legHookFireCount` and `legHookFireCountAndReset` stay as the measurement counter.
- `flaglabels.go`: the stale resolver row is deleted. The variable moves to `flagProvenanceRetired`
  (`retired(M0147-0031)`), and `scripts/planner-flags.env` is regenerated.
- `analysis/m0147/m0147-0030/INVENTORY.md`: the 0031 row now describes the OFF arm correctly.

`deadcode ./cmd/...` was diffed before and after: no change. The resolver was reachable only through `flaglabels`.

## Tests

- `TestNarrowJoinLegCountsAndNarrows` drops its "flag off" case and the per-case flag column. The other eight cases
  are unchanged.
- `TestNarrowJoinLegFiresAndNarrowsOnLiveJoinSearch` drops the hook-off plan. The fixture carries `jlh1_unused` by
  construction, and the test still requires the hook to fire and to cut that column from the join's child.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to ON,
which is the only arm left. The fire-set A/B and the census confirm no plan change (see fix_plan).

## Gates

See the M0147-0031 entry in `.ralph/fix_plan.md`.
