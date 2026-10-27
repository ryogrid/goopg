# M0147-0046 — retire `GOOPG_GATHER_PATHS` `off`/`top` (C-19d admission mode)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arms). Lever count 5 → 4 (`scripts/lever-count.sh`). AGENT.md C4 allowed retiring these arms once their tests use
`max_parallel_workers_per_gather = 0` (owner decision 2026-10-21). Inventory row:
`analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

PG's `generate_useful_gather_paths` (`postgres/src/backend/optimizer/path/allpaths.c`) offers Gather and Gather Merge
paths at every rel that has partial paths. The serial search is simply `max_parallel_workers_per_gather = 0`: no
partial path is built.

C-19d shipped admission as a goopg-private MODE, `GOOPG_GATHER_PATHS`, with three values:
- `off`: no Gather paths, and the partial join and SetOp producers stand down;
- `top`: Gathers only at the search's final rel;
- `all`: PG's behaviour, default since M0140-0003.

Ten gates read the mode:
- `generateUsefulGatherPaths`, `generateUpperRelGatherPaths` and `addBaseRelGatherPaths` (`gatherpaths.go`);
- the partial hash, merge, nestloop and merge-outer producers (`joinpathsparallel.go`, `joinpathsnli.go`,
  `joinpathsmergeouter.go`);
- `addPartialSetOpPath` (`windowsetoppaths.go`).

## Removed / changed

- `gatherpaths.go`: `gatherPathMode` with its constants, the `gatherPathsMode` env read, `gatherPathModeFromEnv`,
  `gatherPathModeLabel`, `setGatherPathsModeForTest`, the exported `SetGatherPathsMode`, and the `os` and `strings`
  imports. The three gather generators keep only PG's gates (`parallelModeOK`, `ConsiderParallel`).
- The four partial producers and `addPartialSetOpPath` lose their `off` early returns.
- `flaglabels.go`: the resolver row is deleted, the order-list entry is marked retired, and the variable moves to
  `flagProvenanceRetired` (`retired(M0147-0046)`). `scripts/planner-flags.env` is regenerated.
  - The same edit removes three resolver-map comment blocks orphaned by this and the two previous retirements:
    NARROW_BUILD, NARROW_COST_INPUTS and GATHER_PATHS.
- Comments in `relfromjoinlist.go`, `joinpaths.go`, `joinsearchlevel.go` and `partialaggpaths.go` that described the
  mode as live are updated. Dated mentions such as "fired under `GOOPG_GATHER_PATHS=all`" are left as history.

`deadcode ./cmd/...` was diffed before and after. Both setters, already unreachable, left the list; nothing was added.

## Tests

- `gpSearch` and `phjJoinrel` lose their mode parameter, since `all` is the only arm. Callers that passed `off` only
  wanted the base-rel partial paths, and those exist under `all` too.
- Deleted, because each pinned an `off`/`top` arm itself:
  - `TestGatherPathsModeGovernsAdmission`;
  - `TestGenerateUpperRelGatherPathsRefusesUnderTop`;
  - `TestPartialHashJoinIsInertUnderTheDefaultMode`;
  - `TestAddPartialSetOpPathRefusesUnderGatherPathsOff` (its producer has no GUC gate of its own; branch partial
    paths exist only under parallel mode);
  - the partial-merge "mode off" subtest;
  - the `off`-scoped join-rel loop in the considerparallel test.
- Rebased onto `max_parallel_workers_per_gather = 0`:
  - `TestGenerateUpperRelGatherPathsRefusesUnderOff`;
  - the executor consumer checks `TestC19fPathModelGatherExecutesAsAParallelHashJoin` and
    `TestParallelHashPathModelWinner`, whose serial control arm is now `c19fSerialPlan`.
- The two uniqueify tests that set `top` only to let the producer run lose that pin.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `CLAUDE.md` and `docs/wiki` finds
no setter. `AGENT.md` names the variable only in C4, which authorises this retirement. Unset, the mode resolved to
`all`, which is the behaviour kept. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0046 entry in `.ralph/fix_plan.md`.
