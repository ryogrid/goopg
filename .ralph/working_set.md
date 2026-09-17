Task: M0141-S7-exec-a — `IncrementalSort` optimizer Node type + the
executor operator, standalone/unit-tested, zero createPlanNode/Plan()
callers. LANDED AND COMMITTED this loop.

Files: internal/optimizer/incrementalsort.go (new: IncrementalSort Node
type, mirrors Sort + PresortedCount), internal/optimizer/incrementalsort_test.go
(new), internal/executor/operators_incremental_sort.go (new:
incrementalSortOp — groups via sortPrefixEqual, full-sorts each group,
streams in arrival order), internal/executor/operators_incremental_sort_test.go
(new, 5 tests incl. order-equivalence vs sortOp oracle),
internal/executor/operators_explain.go (2 new `case *optimizer.IncrementalSort:`
arms — describePlanMode label + planChildren walk — see Findings),
docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md
("Update 2026-09-17e" section), docs/design/README.md (m0141-s7 index row),
.ralph/fix_plan.md (exec-a checked off with landing note, exec-b gets a
sizing note re: nCommon/PresortedCount, exec-c scope narrowed to 4
remaining sites).

Key symbols: optimizer.IncrementalSort (Pos/Output), executor.incrementalSortOp
(Open/Next/Close/Schema, sortKeyVals/lessKeyVals/sortGroup),
executor.sortPrefixEqual (E-15, reused unmodified), executor.evalSortKeyValue
/compareDatum (reused unmodified).

Findings: adding the bare Go Node type (zero callers) still tripped two
HARD gates — TestEveryPlanNodeTypeHasAnExplainArm and
TestEveryPlanNodeWithChildrenIsWalked (internal/executor/explain_node_coverage_test.go)
— because they enumerate by type existence via reflection/AST-scan, not by
construction reachability. Fixed by adding exactly the 2 gated arms
(describePlanMode, planChildren), mirroring Sort's own arms exactly; this
pulls 2 of exec-c's originally-planned "6 operators_explain.go sites"
forward into exec-a's commit, narrowing exec-c to the remaining 4 (3
safe-to-decline, 1 real: the Presorted Key: line in emitNodeDetailLines).
Also found while reading incrementalsortpaths.go: addIncrementalSortPaths
computes nCommon locally but never stashes it on the *Path — exec-b will
need a Path field or a re-derivation at createPlanNode time.

Next step: M0141-S7-exec-b — createplansimple.go's createPlanNode arm for
PathIncrementalSort (resolve the nCommon/PresortedCount sizing question
first), both executor.go builder sites (classic buildNode :179, slab/tree
fast path :675), and the 4 mechanical tree-walkers (scan_deform.go x2,
subplan.go, operators_cte_dml.go). This is the correctness gate before
GOOPG_INCREMENTAL_SORT=on can ever be pointed at the corpus. Re-check the
fix_plan banner and re-read AGENT.md's plan-parity harness section again
before selecting (required every loop touching M0137-M0143).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/...` both green (new tests + full pre-existing suites).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
failure is the pre-existing, already-tracked `internal/parser`
GroupedJoinUnaliased AST-drift issue (fix_plan's "Manually discovered"
entry, filed 2026-09-15, confirmed untouched by this diff via package
isolation: optimizer/executor packages both `ok`). `scripts/tpch-spotcheck.sh`
SKIPPED (TPC-H bench schema not currently loaded on this host — CLAUDE.md's
M0142-0003k blocker, pre-existing/unrelated; moot regardless since this
change has zero production callers and cannot move any plan).
`make ralph-state-guard` — see below.

In-flight: none. Nightly triage (ci/logs/action-items.md run
20260917-004357, 17 items) was already filed into fix_plan.md's M-NIGHTLY
section by the PRIOR loop (same commit as the M0141-S7 exec scoping) —
verified via `git log -1 -- .ralph/fix_plan.md` showing it landed in
ff5234006, so no re-filing was needed this loop.
