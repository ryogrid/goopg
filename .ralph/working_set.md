Task: M0141-S2b-2a — the plumbing-only step of S2b-2's decomposition
(`docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-2 result"
item 1). Per fix_plan banner item 4 (M0141's remaining slices). DONE and
committed this loop.

Files: internal/optimizer/path.go (new `RelOptInfo.SearchCandidates []*Path`
field, "travels as DATA on the rel" precedent alongside NeededCols/OutputCols),
internal/optimizer/upperordered.go (`createOrderedPaths` now calls
`searchedRelOf(input)` where `seed` is built and stores the result onto
`ordered.SearchCandidates`), internal/optimizer/upperordered_test.go (2 new
tests + `searchedPricedNode` fixture), docs/design/0100-0149/
m0141-s2b-scoping-decomposition.md ("S2b-2a landed" section),
docs/design/README.md (m0141-s2b row appended), .ralph/fix_plan.md (S2b-2a
checked off with full landing note).

Key symbols: `searchedRelOf` (searchedtree.go:169, pre-existing R21 slice 2a
accessor — unchanged), `RelOptInfo.SearchCandidates` (new), `createOrderedPaths`
(upperordered.go:64, only production write site this loop),
`addOrderedPaths` (upperordered.go:122, UNCHANGED body — reads nothing new;
it already takes `ordered *RelOptInfo` as its first param, so the new field
is reachable there with zero signature change and zero of its 3 production +
6 test call sites touched).

Findings: chose "carry as a struct field" over "add a new parameter to
addOrderedPaths" specifically to avoid touching upperordereddistinct.go's and
upperorderedgrouping.go's own addOrderedPaths calls (neither has a
`searchedRelOf`-shaped input — they already loop real candidates a different
way, S2b-1/S2b-5's mechanism) for zero benefit this loop. This is a narrower,
lower-risk reading of the filed task text ("thread ... into
createOrderedPaths/addOrderedPaths") than a literal new-parameter thread
would have been, and it is fully precedented by the existing NeededCols/
OutputCols/JoinKeep fields' own doc comments ("travels as DATA on the rel...
added first and separately so commit changes one thing"). Verified via two
unit tests: a searched-root input's search-rel Pathlist lands on
`ordered.SearchCandidates` by identity (2 entries), while `ordered.Pathlist`
itself stays at exactly 1 (the seed) — i.e. still *Sort* elected, still
byte-identical to a non-searched input. A non-searched input leaves the
field nil.

Next step: S2b-2b (materialize-on-demand: defer `createPlanNode` on any
non-seed candidate until `setCheapest` has chosen a winner, and generalize
`validatedSearchPathkeys` (upperorderedinput.go) to run per-candidate rather
than once for the single seed) is now unblocked and can read
`ordered.SearchCandidates` directly instead of re-deriving `searchedRelOf`.
It is still "no behavior change" (same gate as 2a) since S2b-2c (the actual
tournament, blocked on M0141-S7's `addOrderedPaths` third arm / prefix-match
Incremental Sort candidate) is the step that can move a plan. Read
`upperorderedinput.go`'s `validatedSearchPathkeys` and its two-rule contract
doc comment before starting 2b — it validates ONE seed's pathkeys against the
coordinate schema the boundary publishes today; 2b needs that generalized to
run once per `SearchCandidates` entry, not just the winner. Alternatively,
since S7's own row-3 wiring (the `addOrderedPaths` third arm itself) is the
piece that actually needs a real multi-candidate Pathlist to build a
presorted-prefix candidate over, it may be more direct to skip 2b's pure
materialize-on-demand refactor and go straight to scoping S2b-2c/S7's wiring
together, now that 2a supplies `ordered.SearchCandidates` as the exact input
both would consume — worth 10 minutes of design-doc re-reading before
picking one over the other next loop. Read AGENT.md's plan-parity harness
section again before selecting (required every loop touching M0137-M0143).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` full
package PASS (includes the 2 new tests + all pre-existing S2b/S7/upperordered
tests). TPC-DS SF0.25 sweep (private bin `tmp/goopg-s2b2a-bin`, since
nightly batch `ci/batch/nightly-scheduler.sh` was live and shares
`tmp/goopg-bench-bin`; binary deleted after the run): PASS=96 MISMATCH=0,
PLAN-SHAPE changed=0 — byte-identical as predicted, confirming the S2b-2
recon's Finding 2. tpch-spotcheck.sh not run (pre-existing blocker
M0142-0003k, TPC-H bench data still empty — unrelated to this change,
optimizer-package-only edit with no TPC-H-specific surface). `gofmt -l`
flagged only the same pre-existing go1.26.3-vs-go1.25 drift in
upperordered_test.go noted by prior loops (two unrelated struct-literal
alignment lines, not touched by this edit — left alone per CLAUDE.md).
`make ralph-state-guard` self-repaired the same stale running/completed
mismatch seen in prior loops, then passed.

In-flight: none.
