# M0141-S7-exec-b — make `PathIncrementalSort` structurally and semantically reachable end to end

Status: accepted (landed `9697099c6`, 2026-09-17). Production change: `Path.PresortedCount`, `createIncrementalSortPlan`, the classic `executor.go` builder arm, and all four `*optimizer.Sort` tree-walkers mirrored for `*optimizer.IncrementalSort`.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-17f — M0141-S7-exec-b LANDED

**Sizing decision (the open question from -17e): stash, not re-derive.**
`Path.PresortedCount` (new field, `path.go`) is set once by
`addIncrementalSortPaths` (`incrementalsortpaths.go`) at the exact point
`nCommon` is already in scope, and read verbatim by the new
`createIncrementalSortPlan` arm. Re-deriving it at `createPlanNode` time via
`pathkeysCountContainedIn` against the winning candidate's own
`SearchCandidateKeys` entry was rejected: nothing guarantees the candidate
that survives `setCheapest` still indexes the same `SearchCandidateKeys`
slot it was built against (the tournament can re-order/mutate `Pathlist`
between the two points), so stashing is both cheaper and the only version
that is exact by construction rather than by coincidence.

Landed, in order:

- `internal/optimizer/path.go` — `Path.PresortedCount int` (documented,
  zero for every kind but `PathIncrementalSort`).
- `internal/optimizer/incrementalsortpaths.go` — `addIncrementalSortPaths`
  now stamps it; added `SetIncrementalSortPathsMode` (the same
  cross-package test hook `SetPartialSortPathsMode`/`SetPartialAggPathsMode`
  already provide, for a future consumer test to flip the flag without
  reaching into the package).
- `internal/optimizer/createplansimple.go` — `createIncrementalSortPlan`,
  `createSortPlan`'s structural twin: same child recursion, same pathkey
  translation through `translateToLayout`, plus one extra precondition
  `PathSort` has none of — `0 < PresortedCount < len(Pathkeys)`
  (`IncrementalSort`'s own contract, `incrementalsort.go`) — checked and
  panicked on by name, matching every other `createPlan` arm's "a
  constructed-but-unhandled/malformed shape is a producer bug" convention.
  Wired into `createPlan.go`'s switch right after `PathSort`.
- `internal/executor/executor.go` — the classic `buildNode` arm
  (`*optimizer.IncrementalSort` case, right after `*optimizer.Sort`,
  identical shape: recurse on the deform-bound child, wrap in
  `newIncrementalSortOp`, run through `maybeInstrument`). **The slab/tree
  fast path (`BuildFast`'s `buildRec`) needed NO new code**: unlike
  `OpSort`, `IncrementalSort` is not one of the concrete-dispatch slab
  kinds (`OpSeqScan`/`OpFilter`/`OpProject`/`OpLimit`/`OpSort`/`OpUpdate`/
  `OpDelete`/`OpInsert`/`OpJoin`) — it falls through to `buildRec`'s
  existing `default:` arm, which calls the now-updated `buildNode` and
  wraps the result in `opAdapterState`. This is the SAME posture
  `*optimizer.Distinct`/`*optimizer.WindowAgg`/`*optimizer.SetOp` already
  have (confirmed by grep: none of the three has an explicit `buildRec`
  case either) — a new node kind gets slab-native dispatch only when a
  later phase specifically migrates it, not automatically. "Both
  `executor.go` builder sites" (the exec-b task line's own phrasing) is
  satisfied because both entry points (`Build` and `BuildFast`) now reach a
  working operator, not because both have bespoke code.
- Tree-walkers, all four, each mirroring the existing `*optimizer.Sort`
  arm exactly:
  - `scan_deform.go` `deformBoundBelow` — folds `Keys` the same way Sort
    does (the presorted prefix is still evaluated per row by
    `sortPrefixEqual`'s group split, so it is a genuine consumer too).
  - `scan_deform.go` `deformSideWidth` — descends through the child like
    every other single-child pass-through in that walk.
  - `subplan.go` `classifySubPlan` — classified `rescanCloseOpen`, same as
    `Sort` (not yet audited for bare re-Open safety despite `Open`
    resetting its own `groups`/`gi`/`ri` state, so treated identically
    until that audit happens — the conservative choice, not a proven one).
  - `operators_cte_dml.go` `planContainsWorkTableScan` — the one entry
    with a real correctness stake: a recursive CTE body that picks up an
    Incremental Sort over its `WorkTableScan` must still be detected and
    streamed, not silently materialized (`cteScanOp.Open`'s decision reads
    this function's answer directly).

**Verification.** New tests, all passing:

- `internal/optimizer/createplansimple_test.go` —
  `TestCreateIncrementalSortPlanOverPrebuilt` (recursion + pathkey
  translation + `PresortedCount` carry-through) and
  `TestCreateIncrementalSortPlanPanics` (6 precondition cases, including
  both `PresortedCount` boundary violations `PathSort` has no equivalent
  of).
- `internal/executor/operators_incremental_sort_build_test.go` (new file)
  — `TestIncrementalSortReachesBothBuilders`: a real 2-column, 4-group,
  80-row table (`inc_items`), rows inserted in ascending-`grp` BLOCKS with
  a deliberately unsorted `v` within each block (so a correct answer needs
  the operator's real per-group sort, not just "trust the input"). Takes
  the planner's OWN real `Sort` node for `SELECT grp, v FROM inc_items
  ORDER BY grp, v` (via `planOne`), swaps it for a hand-built
  `IncrementalSort{PresortedCount: 1}` over the identical child/keys, and
  checks (1) the rendered rows are byte-identical to the un-swapped
  planner Sort's own output — the value-correctness gate — and (2)
  `Build`/`Run` agrees with `BuildFast`/`RunFast` (`runBothAndCompare`,
  the existing Phase-C harness) — the "both builder sites" gate. The plan
  is hand-built rather than tournament-won for the same reason
  `TestBuildFastNodeKinds` (`phase_c_test.go`) already hand-builds its own
  cases: getting a real query to WIN the (still off-by-default) tournament
  is the broader M0141-S7 measurement goal, not exec-b's "does the wiring
  work" scope.
- `internal/executor/subplan_handle_test.go` —
  `TestClassifySubPlanKinds` gained an `IncrementalSort` case (asserts
  `rescanCloseOpen`, mirroring `Sort`'s existing case in the same table);
  new `TestPlanContainsWorkTableScanSeesThroughIncrementalSort` (positive
  and negative case for the correctness-stakes walker above).
- `internal/executor/scan_deform_bound_test.go` — the existing
  `sort-limit-lockrows-fold` subtest gained an `IncrementalSort` narrowing
  check (`Keys` bound 7/8, same as its `Sort` sibling immediately above
  it); `seqLeafBound`/`joinSideBounds` (the file's own operator-tree
  walkers) gained `*incrementalSortOp` cases alongside their existing
  `*sortOp` ones — without these the new build test's own deform-adjacent
  assertions would have false-failed with "no SeqScan leaf under
  `*executor.incrementalSortOp`" (caught during this loop; fixed before
  landing).

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...
./internal/executor/...` both green. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — only failure is the pre-existing,
already-tracked `internal/parser` `GroupedJoinUnaliased` AST-drift issue
(`.ralph/fix_plan.md`'s "Manually discovered" entry, filed 2026-09-15,
confirmed untouched: `internal/optimizer` and `internal/executor` both
`ok`). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench schema still not
loaded — CLAUDE.md's M0142-0003k blocker, pre-existing and unrelated) —
moot regardless: `GOOPG_INCREMENTAL_SORT` stays default-off
(`incrementalSortPathsMode` unchanged by this update), so no production
plan can move.

Resume point: **M0141-S7-exec-c** — the one substantive remaining
`operators_explain.go` item, `emitNodeDetailLines`'s `Presorted Key:` line
extension (`nodeIncrementalSort.c`/`explain.c` oracle), now unblocked
(exec-c's own fix_plan entry says it "needs exec-b: nothing to render
before a real node can reach EXPLAIN" — a real node can now reach EXPLAIN).
After exec-c, exec-d (deferred, ledger row already filed) is `sortOp`
feature parity (spill-to-disk, packed retention, ctid passthrough,
per-group `SortStat`) — measure against the corpus first per its own entry,
none of the 14 witnesses need it today.
