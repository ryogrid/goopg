# M0140-0006a — expose each UNION ALL branch's searched RelOptInfo on the SETOP rel

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0140-0006a
**Outcome:** production change, pure plumbing. No cost producer, no executor
node, no plan can move — confirmed by the acceptance gates below.

## The task as filed

`m0140-0006-decomposition-into-a-b-c.md` split M0140-0006 ("the partial-Append
producer, implementation") into three loop-sized pieces. This is the first:
*"expose SetOp-branch `PartialPathlist`* instead of collapsing straight to a
finished `Node` via `planSelectWithSettings`, so `createSetOpPaths` has
something other than `seedPathForNode`'s opaque-`Node` wrap to read a partial
candidate from."

## The recon's premise was stale — `searchedRelOf` already exists

M0140-0004's recon (2026-09-15) stated: *"there is no channel today for a
branch's own partial path ... to survive past \[the nested
`planSelectWithSettings`\] call and reach the SetOp rel's
`RelOptInfo.PartialPathlist`."* That claim predates a mechanism landed nine
days earlier and never referenced in the recon:

- `6a51087fb` (2026-09-09, "R21 slice 2a — carry the search rel on the tag,
  not through planner.go") added `searchedTree.searchRel *RelOptInfo` and
  `setSearchRel`/`searchedRel` — the tag `createplanroot.go:188`
  (`s.setSearchRel(p.Rel)`) already stamps on every search root in
  production.
- `6302fb8d6` (2026-09-09, "R21 slice 2b prerequisite — searchedRelOf") added
  `searchedRelOf(n Node) *RelOptInfo` (`searchedtree.go:169`) — a pure
  function that recovers the search's own upper rel (PartialPathlist and
  all) from ANY node reachable through a single-child pass-through chain
  (`boundaryWalkChildren`'s enumeration: `*Project`, `*Filter`, `*Sort`,
  `*Gather`, `*GatherMerge`, `*Aggregate`, ...).

This is already the load-bearing mechanism for two OTHER upper stages
(`searchedtree.go:169`'s own doc comment): `addPartialAggSplitPath` reaches
partial-aggregation candidates through it, and `createOrderedPaths`
(`upperordered.go:113`, M0141-S2b-2a) threads `searchedRelOf(input).Pathlist`
onto the ORDERED rel's `SearchCandidates` field for exactly the same
reason — "a later consumer does not need to re-derive `searchedRelOf`
itself."

**A UNION ALL branch is planned by the identical `planSelectWithSettings`
recursion** every other SELECT is (`planner.go`'s `planSegment` closure calls
it with no special-casing), so a branch that is a searched-tree root (a plain
`SELECT ... FROM t WHERE ...` — the common UNION ALL branch shape, including
TPC-DS Q5/Q76's) carries the SAME tag and is reachable through the SAME
accessor. The gap M0140-0006a actually needed to close was narrower than the
recon believed: `createSetOpPaths` (`windowsetoppaths.go`) never called
`searchedRelOf` on its two branches at all — not "no channel exists", but "no
code reads the channel that already exists here."

## What landed

Mirrors `upperordered.go`'s `SearchCandidates` precedent exactly, adapted
from the ORDERED rel's one input to the SETOP rel's two:

1. **`RelOptInfo.LeftBranchRel` / `RightBranchRel`** (`path.go`, new fields,
   next to `SearchCandidateKeys`): each branch's own `*RelOptInfo` from
   `searchedRelOf(setOpNode.Left/.Right)`. nil when that branch is not a
   searched-tree root (e.g. it collapsed to a legacy-built
   Aggregate/Sort/Values shape the search never reached, or its own
   statement had too few relations for the search to run — `minSearchRels()`).
2. **`createSetOpPaths`** (`windowsetoppaths.go`) now sets both fields on
   `setOpRel` right after building `lseed`/`rseed`, before `addSetOpPaths`
   runs. Two lines, no control flow change.
3. Two new tests in `windowsetoppaths_test.go`:
   `TestCreateSetOpPathsThreadsBranchRelsOntoSetOpRel` (both branches tagged
   via the existing `searchedPricedNode` test fixture from
   `upperordered_test.go`, each carrying a distinct `*RelOptInfo` with its
   own `PartialPathlist`; asserts the SETOP rel picks up both by identity)
   and `TestCreateSetOpPathsLeavesBranchRelsNilForNonSearchedBranches` (the
   negative case: plain untagged branches leave both fields nil).

`M0140-0006b`'s own scope (seed `setOpRel.PartialPathlist` from these two
fields' `.PartialPathlist`, PG's `cost_append` partial-path arithmetic) is
unaffected by this landing — this task only makes the branch rels reachable
without re-deriving `searchedRelOf`; nothing today or after this commit reads
`LeftBranchRel`/`RightBranchRel` outside the two new tests.

## Why no plan can move

`addSetOpPaths` is unchanged: it still builds exactly the one
`PathSetOp` candidate it always has, from `lseed`/`rseed`, never consulting
the two new fields. The new fields are pure DATA on the rel, the same
posture `SearchCandidates`/`SearchCandidateKeys` already established — set,
never read by a producer.

## Gates run (2026-09-18)

- `go build ./...` clean; `go vet ./internal/optimizer/...` clean.
- `go test ./internal/optimizer/...` — full package green (no `-count=1`),
  including the two new tests.
- `go test ./internal/executor/...` — full package green (sibling-path
  audit: no executor change was needed or made; checked anyway per Hard-won
  Rule #2).
- `scripts/tpch-spotcheck.sh` — PASS, `Q12=2/Q13=34` (canonical), against
  the staged tree (gate-stamp `code_tree` verified to match
  `sha256(git ls-files -s -- internal cmd go.mod go.sum)` of the staged
  index).
- `scripts/tpcds-sf025-regression.sh sweep` — `PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE queries=99 same=99 changed=0`
  — the exact "byte-identical" acceptance bar
  `m0140-0006-decomposition-into-a-b-c.md` states for this sub-task.
- `scripts/tpch-acceptance-arm.sh` (`PGSHAPED=1`, the production default —
  the first attempt used the script's own `PGSHAPED=0` default and got a
  `BOTH-ERROR` on Q9 from an unrelated pre-existing 600s-timeout artifact
  identical on both arms, not a real divergence; re-run with `PGSHAPED=1`
  to match `tpch-spotcheck`'s actual default and avoid that artifact)
  comparing a HEAD baseline binary (`git stash push -- <the three touched
  files>` / build / `git stash pop` / re-`git add`, same technique
  `m0139-0007c`'s design doc recorded) against this staged tree on the
  private port 5583 clone: **VERDICT PASS, 24/24 labels MATCH**.

All three gate stamps (`tmp/gate-stamps/{tpch-spotcheck,tpcds-sf025,
tpch-acceptance-arm}.json`) verified `code_tree` == the staged index hash
before commit.

## Resume point for M0140-0006b

`M0140-0006b` (the partial-Append cost producer) reads
`setOpRel.LeftBranchRel.PartialPathlist` / `.RightBranchRel.PartialPathlist`
directly — no further plumbing needed here. It still needs its own
`cost_append` partial-path arithmetic and must not go live ahead of
`M0140-0006c` (the executor claim-set fix for `setOp` under `Gather`), per
the parent decomposition doc's ordering constraint.
