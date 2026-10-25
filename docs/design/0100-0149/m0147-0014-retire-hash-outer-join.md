# M0147-0014 — retire `GOOPG_HASH_OUTER_JOIN` (hash-or-merge choice for RIGHT/FULL in the post-search assignment)

Status: landed 2026-10-25.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 27 → 26
(`scripts/lever-count.sh`).

## What was retired

M0127-P4.2 made the hash executor able to fill the preserved side of a RIGHT or FULL join. It also added a
hash-or-merge choice for those two join types to the post-search algorithm assignment in `planner.go` — the switch
that stamps `Join.Algo` on join nodes the PG-shaped search did not build. The choice (`chooseOuterFillJoinAlgo`)
compared goopg's unit-row hash and merge costs. It stayed behind `GOOPG_HASH_OUTER_JOIN=1` (default OFF), because
flipping it moved the regress `join` test 210 diff lines further from upstream (deferral ledger 2026-08-04,
M0127-P4.2). The take2 P6-06 re-measure (ledger 2026-09-03) found the flip a wash, and it was never promoted.

Neither arm is PG's logic. PG prices hash, merge and nested-loop paths for RIGHT and FULL joins inside
`add_paths_to_joinrel` (`postgres/src/backend/optimizer/path/joinpath.c`: `sort_inner_and_outer`,
`match_unsorted_outer` and `hash_inner_and_outer`; FULL is admitted to the merge and hash arms even when
`enable_mergejoin` or `enable_hashjoin` is off). goopg's PG-shaped search ports that logic. The post-search
assignment serves only the trees the search did not build. Under R3, nothing here is a PG arm to promote, so the
lever is deleted. Its shipped default (merge) is kept.

## Removed

- `internal/optimizer/joincost.go`: `hashOuterJoinEnabled`, `hashOuterJoinFromEnv`, `chooseOuterFillJoinAlgo`,
  and the `os` import. `chooseInnerJoinAlgo`, `costInnerHash`, `costInnerMerge`, `costInnerNestLoop` and
  `rowSortCost` stay, because the INNER arm uses them.
- `internal/optimizer/planner.go`: the RIGHT/FULL arm of the post-search assignment is now just
  `jn.Algo = JoinAlgoMerge`. The `BuildLeft = true` branch for a hash RIGHT join was reachable only through the
  hash arm and is deleted with it. The executor's hash RIGHT/FULL fill (M0127-P4.2) is untouched, because the
  search's hash paths use it.
- `internal/optimizer/flaglabels.go`: `GOOPG_HASH_OUTER_JOIN` moves from `flagResolvedState` to
  `flagProvenanceRetired` (`retired(M0147-0014)`). `scripts/planner-flags.env` is regenerated.
- The `upperorderedinput.go` comment that cited `chooseOuterFillJoinAlgo` now states the unconditional merge.

`deadcode ./cmd/...` was diffed before and after the change and found no newly unreachable helper.

## Tests

These tests exercised only the hash arm and are deleted:

- `joincost_test.go`: `TestChooseOuterFillJoinAlgo`, `TestChooseOuterFillJoinAlgoNeverPicksNestedLoop` and
  `TestChooseOuterFillJoinAlgoDeclinesWhileGated`. The function they call no longer exists, and the default they
  guarded is now the only code path.
- `planner_test.go`: `TestPlanJoinRightFullTakesHashOnceCostable`, which forced the flag on.

## Inert proof (AGENT.md C6)

- `hashOuterJoinFromEnv("")` was false, so `chooseOuterFillJoinAlgo` returned `(JoinAlgoMerge, false)` at its
  first statement on every default run. The caller then kept the `JoinAlgoMerge` it had already assigned, and the
  `BuildLeft` branch (`jn.Algo == JoinAlgoHash`) was never entered.
- The variable is set nowhere in scripts, CI, bench configs, git hooks or the Makefile (`git grep`). The only
  consumers were the four tests above, which assigned `hashOuterJoinEnabled` directly.

## Gates

See the M0147-0014 entry in `.ralph/fix_plan.md` for the gate list and results of the landing commit.

## Supersedes

The 2026-08-04 M0127-P4.2 ledger row ("delete gate once M0127-P5 path costs price sorts PG-style") and the
2026-09-03 take2-P6-06 row ("flag left OFF deliberately"). The gate is deleted, and the default it protected is
permanent. PG-faithful RIGHT/FULL method choice belongs to the search, not to this assignment. M0147-0030's
phase-2 inventory lists the goopg-private post-search assignment itself (`chooseInnerJoinAlgo` and this switch).
