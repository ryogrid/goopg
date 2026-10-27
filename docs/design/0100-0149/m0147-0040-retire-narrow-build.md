# M0147-0040 — retire `GOOPG_NARROW_BUILD` (build-side narrowing switch)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 11 → 10 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

take2 P4-01 rev 10 narrows join inputs to the columns the statement still needs. `joinInputsFor`
(`createplanjoin.go`) calls two helpers in `internal/optimizer/narrowoutput.go`:
- `narrowBuildInput` handles a hash join's build (inner) side;
- `narrowMergeInput` handles both sides of a merge join.

Both helpers checked `narrowBuild`, which `GOOPG_NARROW_BUILD` set once per process. It was default ON since P4-A §18
step 5, and `=0` opted out to the un-narrowed arm. The narrowing is goopg-private plumbing: PG narrows through target
lists, and these Projects do not appear in EXPLAIN. Phase 2 keeps the shipped ON arm.

## Removed / changed

- `narrowoutput.go`: the `narrowBuild` variable, `narrowBuildFromEnv` and the `os` import. Both helpers now decline
  only on their structural conditions: not a hash join, an unknown needed set, or a nil node, side or rel.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0040)`). `scripts/planner-flags.env` is regenerated.
- Comments in `narrowoutput.go`, `joinleghook.go` and `path.go` no longer describe the switch as live.

`deadcode ./cmd/...` was diffed before and after: no change.

## Tests

The 34 test references were checked one by one; none used the OFF arm as an oracle.
- Nine `old := narrowBuild; narrowBuild = true; defer …` pins in `pathtarget_test.go` are deleted. ON is now the
  only arm.
- `TestNarrowBuildInputWiring` loses its `flag` column and its "flag off leaves the pair untouched" case. The
  structural declines stay: merge join, unknown needed set, nil rel, nil path, nil node.
- The `narrowMergeInput` subtest "declines keyless and flag-off" keeps only its keyless half.
- `TestNarrowBuildFromEnvPolarity` is deleted along with the function it tested.

## Inert proof (AGENT.md C6)

`git grep` finds the variable only in historical artefact stamps (`# planner-flags:` lines in old sweep files), in
docs, and in the provenance table. No gate, CI config, bench script, env file, git hook or Makefile sets it. Unset,
it resolved to ON, which is the only arm left. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0040 entry in `.ralph/fix_plan.md`.
