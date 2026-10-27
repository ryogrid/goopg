# M0147-0038 — retire `GOOPG_NARROW_UPPER_SORT` (upper-Sort narrowing site switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 13 → 12 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

B-01c slice (c) (`internal/optimizer/upper_narrow_chain.go`) narrows an upper `*Sort`'s input row. This is the
general ORDER BY sort: the site cuts in a narrowing `*Project` and re-bases the ancestor chain up to the node that
absorbs the change.

It shipped behind its own opt-out flag, `GOOPG_NARROW_UPPER_SORT` (`=0` disabled it), so an A/B could separate it from
slice (b)'s Aggregate site. `GOOPG_NARROW_UPPER=0` already disabled both.

The narrowing is goopg-private plumbing. PG narrows a sort's input through its target lists, and these Projects do
not show in EXPLAIN. Phase 2 keeps the shipped ON arm.

## Removed / changed

- `upper_narrow_chain.go`: `narrowUpperSort`, `narrowUpperSortFromEnv` and the `os` import. The
  `applyUpperSortNarrowing` gate now checks only `narrowUpper`, which M0147-0039 retires next.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0038)`). `scripts/planner-flags.env` is regenerated.

`deadcode ./cmd/...` was diffed before and after: no change.

## Tests

`TestApplyUpperSortNarrowingFlagOff` drops its `GOOPG_NARROW_UPPER_SORT=0` case and keeps the `GOOPG_NARROW_UPPER=0`
case. That case still proves the pass leaves the tree bit-identical when off.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to ON,
which is the only arm left. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0038 entry in `.ralph/fix_plan.md`.
