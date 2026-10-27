# M0147-0039 — retire `GOOPG_NARROW_UPPER` (upper-narrowing pass switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 12 → 11 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

B-01c's applying half narrows the input row of upper plan nodes to what their ancestors read. It has two sites:
- slice (b): the Aggregate site (`applyUpperNarrowing`, `internal/optimizer/upper_narrow_apply.go`);
- slice (c): the ORDER BY Sort site (`applyUpperSortNarrowing`, `upper_narrow_chain.go`).

Both ran behind `GOOPG_NARROW_UPPER`, default on, where `=0` opted out of both. M0147-0038 already retired the Sort
site's own flag.

The narrowing is goopg-private plumbing. PG narrows through target lists, and these Projects do not appear in
EXPLAIN. Phase 2 keeps the shipped ON arm.

## Removed / changed

- `upper_narrow_apply.go`: `narrowUpper` and `narrowUpperFromEnv`. Both drivers (`applyUpperNarrowing` and
  `applyUpperSortNarrowing`) now return early only for a nil root.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0039)`). `scripts/planner-flags.env` is regenerated.
- The comments in `upper_narrow_chain.go` and `joinleghook.go` no longer describe the flags as live.

`deadcode ./cmd/...` was diffed before and after: no change.

## Tests

`TestApplyUpperNarrowingFlagOff` and `TestApplyUpperSortNarrowingFlagOff` are deleted. Each pinned only that `=0`
restored the un-narrowed tree. The narrowing behaviour itself stays covered by the rest of both test files.

## Inert proof (AGENT.md C6)

No gate, CI config, bench script or env file sets the variable (M0147-0030 inventory). Unset, it resolved to ON,
which is the only arm left. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0039 entry in `.ralph/fix_plan.md`.
