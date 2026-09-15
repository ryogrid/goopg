# M0137-0016 — add the `*Gather` arm to `pushConjunctTraced` (O15)

Status: accepted

## Task

`.ralph/fix_plan.md` M0137-0016, filed as a no-owner ledger row by M0137-0012
(`m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced`, recording
mechanism O15 per
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md:366-367`,
C2). The row's stated gate — "requires M0137-0010's qual-placement census to
exist first" — is satisfied: M0137-0010 is complete and every M0139 slice ran
the census. The row carries the recipe directly: `pushConjunctTraced`'s
`switch x := n.(type)` (`internal/optimizer/inner_join_qual_pushdown.go:341`)
has descent cases for `*Filter`, `*Project` and `*Join` only; a `*Gather` in
the descent path fell through to the terminal-target check
(`innerJoinPushEligibleInput`) and the push declined there, silently keeping
a PG-placeable restriction as a post-parallel residual instead of moving it
below the Gather onto the scan each worker runs.

Same defect class as R56's Q78 loss: three `Filter:` lines dropped from a
plan capture while the row-count sweep stayed green — a values-only gate
cannot see a qual-placement divergence from PG, only a plan-text diff can.

## The fix

```go
case *Gather:
	repl, ok := pushConjunctTraced(x.Child, c, st)
	if !ok {
		return n, false
	}
	x.Child = repl
	return x, true
```

Added immediately before the existing `*Join` case in
`inner_join_qual_pushdown.go`'s switch.

Why this shape and not the `*Join` arm's coordinate math: `Gather.Output()`
is exactly `Child.Output()` (`NewGather`, `plan.go:2758-2760`) — a Gather adds
parallel execution, not a schema change, so there is no side to choose and
no index to shift. It is also simpler than the `*Project` arm: `Project` can
re-express columns (a target need not be a bare `ColumnRef`) and its arm
must fail closed on a computed target, an `IsolatedScope` wrapper, or a
`Targets`/`Output` length mismatch before calling
`remapConjunctThroughProjection`. A Gather passes its child's rows through
unchanged, so none of those failure modes exist and the arm is a bare
pass-through recursion — the shortest of the three descent cases.

`st` (the C-02c/d move-proof accumulator) is left untouched deliberately,
unlike the `*Project` arm which clears `st.proven` on every crossing because
its remap has an unnamed-ref fail-open seam. A Gather crossing changes
nothing about the conjunct's coordinate space or its relation-set identity,
so whatever the deeper descent proves about the join spine below the Gather
remains a valid proof above it too.

## Verification

**Unit tests**
(`internal/optimizer/pushdown_gather_crossing_test.go`, new file, mirroring
the existing `pushdown_project_crossing_test.go` pattern):

- `TestPushdownCrossesGatherToTheCorrectLeaf` — `Join{ Gather{a}, b }`: a
  conjunct naming `a` must cross the Gather and land in a `Filter` directly
  above the `a` scan, with the `Gather` preserved above the pushed filter
  (not replaced or dropped).
- `TestPushdownThroughGatherIsCopyOnlyNotAMove` — with a fully-attributed
  INNER-path conjunct, `st.proven` must survive the Gather crossing intact
  (contrast with the Project arm, which clears it on every crossing for an
  unrelated reason — the two arms' proof handling differ because their
  failure surfaces differ, and this pins that the difference is intentional).
- `TestPushdownDescendsGatherThenJoinSpine` — a Gather sitting partway down a
  two-level join spine (`(Gather{a} JOIN b) JOIN c`, residual on `a`) must
  still be crossed and the conjunct must reach the scan two levels below,
  composing correctly with the pre-existing multi-level descent
  (`TestInnerJoinQualPushDescendsJoinSpine`'s sibling for a Gather-bearing
  spine).

All three pass; the full `internal/optimizer` package (`go test
./internal/optimizer/...`) stays green, and `go test ./internal/executor/...
-run Explain` (the EXPLAIN consumer side) is unaffected — this task changes
only where a `Filter` node is placed in the tree, not the EXPLAIN rendering
of a `*Gather` itself.

**Corpus-wide blast radius**, measured directly from the committed PG-oracle
reference plans (`bench/tpch/plans-pg/*.txt`, `bench/tpcds/plans-pg/*.txt` —
no server needed, mirroring M0137-0015's methodology): counted every query
whose PG plan text has a `Filter:` line attached to a scan that sits at or
below a `Gather` node (i.e. PG itself pushes a restriction below the Gather
boundary onto the per-worker scan) —

- **TPC-H: 0 of 22.** No TPC-H query's PG reference plan places a local
  filter beneath a Gather; every TPC-H `Gather`/`Gather Merge` in the corpus
  sits over an already-filter-free scan or a join whose own restrictions are
  applied above the parallel boundary.
- **TPC-DS: 37 of 99.** A meaningful fraction of the corpus's parallel plans
  carry exactly the shape this task fixes.

This is a measurement of how often PG's own plans use the shape goopg's
descent previously declined to reproduce, not a claim about how many TPC-DS
categories move — M0140-0003's own experience (`GOOPG_GATHER_PATHS=all`
default-on, category count moved 525 → 540 net-worse while `match` held) is
the standing caution here: parallel-boundary changes are frequently visible
in category counts as *new* divergence surfacing, not as new divergence
being introduced. This task closes an instrument gap in the qual-placement
pass; it does not claim a specific TPC-DS category-count effect, and none is
asserted.

## Gates

`go build ./...` clean. `go test ./internal/optimizer/...` full package
`ok` (includes the three new tests). `go test ./internal/executor/... -run
Explain` `ok`. `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=34,
canonical; private port 5580, clone `tmp/goopg-spotcheck-tpch-data`, shared
`:65433` never touched).

## Filed as

Closes `.ralph/deferral_ledger.md` row
`m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced` (marked
`resolved` in this loop) and `.ralph/fix_plan.md` M0137-0016 (checked `[x]`).
