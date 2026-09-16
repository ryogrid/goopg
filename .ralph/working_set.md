Task: M0141-S7 groundwork, continuing the Finding-3 implementation-order
chain from the prior loop's `pathkeysCountContainedIn` landing. Landed the
second primitive: `costIncrementalSort` (per fix_plan banner item 4, M0141
remaining slices before M0142).

Files: internal/optimizer/cost_funcs.go (new `costIncrementalSort`, right
after `sortByteBranch`, before `costAgg`), internal/optimizer/
cost_incremental_sort_test.go (new, 4 unit tests), internal/optimizer/
sort_pgrelationbytes_test.go (census guard `cost_funcs.go` count 2->3),
docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md
(new "Update 2026-09-17b" section), docs/design/README.md (m0141-s7 row
appended), .ralph/fix_plan.md (S7 entry appended, stays unchecked).

Key symbols: `costIncrementalSort(cp costParams, inputCost Cost, inputTuples,
inputGroups float64, ncols int, avgVarBytes, limitTuples float64, width int)
Cost` — ports PG's `cost_incremental_sort` (`costsize.c:2000-2126`),
composing the existing `costSortRunWithWidth` (`cost_tuplesort`) as the
per-group full-sort price. Zero production callers — does not touch
`addOrderedPaths`, so it cannot move any plan yet.

Findings: resolved the open question the prior loop's working_set left —
whether this Finding-3 step needs S2b's real multi-candidate Pathlist to be
testable, or is standalone-testable like the prefix-count helper. Answer:
standalone. Every input to `cost_incremental_sort` is a plain scalar except
`input_groups`, which upstream computes via `estimate_num_groups` *inside*
the same function; this composition splits that into a caller-supplied
`inputGroups` parameter (same split `costSortRunWithWidth` already uses for
`ncols`/`avgVarBytes`/`width`), so the formula is fully pinnable against an
independent transliteration of PG's C with synthetic numbers. Only the later
*wiring* step (Finding 3 table row 3-4: the `addOrderedPaths` third arm,
which calls `estimateNumGroups` over a real presorted-key prefix) needs
S2b's rel to exist — not this one. Two things caught while writing the
independent pin (both now documented in code comments + design doc):
(1) `comparisonCost` is always 0 at PG's only real call site
(`costsize.c:3701`) and `cost_tuplesort`'s internal `+= 2*cpu_operator_cost`
mutates its own local copy, never the caller's, so the per-tuple overhead
term correctly uses 0, matching `costSortRunWithWidth`'s existing "no
external comparisonCost parameter" convention; (2) the formula is NOT
monotonic in `inputGroups` — cost falls as groups grow (smaller per-group
sorts dominate) until fixed per-group reset overhead turns the curve back up
near one-row-per-group (verified by direct probing at inputTuples=50000: min
near groups=25000, rises again by groups=50000, but stays below the
groups=1 baseline throughout) — an initial test asserting plain
monotonicity was wrong and was corrected before landing.

Finding 3's table now has rows 1-2 landed (prefix-count helper, cost
composition), both zero-caller, both independently tested. Rows 3-6
(`PathIncrementalSort`/`addOrderedPaths` third arm, executor operator,
`createplansimple.go` wiring, EXPLAIN rendering) remain, and row 3 genuinely
needs M0141-S2b's Pathlist-forwarding surgery to land first (per witness
group, per the S7 design doc's NARROWED mapping) — there is nothing yet for
a presorted-prefix candidate to be built over without it.

Next step: before attempting row 3, re-check whether row 5 (the executor
operator, built on the already-landed zero-caller `sortPrefixEqual`/E-15) or
row 6 (EXPLAIN rendering) can ALSO be staged standalone ahead of the
`addOrderedPaths` wiring, following the same "independently testable
primitive first" pattern used for rows 1-2 — read `sortPrefixEqual`'s
contract doc comment (`internal/executor/sort_presorted.go`) and
`operators_explain.go`'s existing `Sort`/`Sort Key:` rendering to judge
whether an operator/rendering arm can be pinned by a synthetic-input unit
test without a real `PathIncrementalSort` Path.Kind existing yet (it likely
CANNOT for the executor operator specifically, since an operator needs a
concrete plan node type to attach to — check before assuming). If both turn
out to require the Path.Kind/addOrderedPaths wiring to exist first, this
chaining approach has reached its natural end and the next loop should defer
to M0141-S2b's relevant sub-task (S2b-0 for the 5 GroupAggregate witnesses,
S2b-2 for MergeJoin/NestedLoop/SubqueryScan) landing first, or fall back to
M0142's remaining open items per the banner (M0142-0016c has no stated
blocker; M0142-0005/M0142-0008a-3 need their own recon;
M0142-0008c-1a/-3d/-4 not ready). Read AGENT.md's plan-parity harness
section again before selecting (required every loop touching M0137-M0143).

Gates run: `go build ./...` clean, `go build ./internal/optimizer/...`
clean, `go test ./internal/optimizer/...` PASS (full package, not just new
tests — this run also caught and required fixing a census guard test,
`TestCostSortRunWithWidthProductionCallersAreComplete`, which pins the exact
count of `costSortRunWithWidth` call sites per file; updated `cost_funcs.go`
2->3). TPC-DS SF0.25 sweep: PASS=96 MISMATCH=0, PLAN-SHAPE changed=0.
tpch-spotcheck.sh: SKIPPED (known pre-existing blocker M0142-0003k, not this
change). `make ralph-state-guard` self-repaired the same stale
running/completed mismatch seen in prior loops, then passed. `gofmt -l`
flagged pre-existing unrelated formatting drift in `cost_funcs.go` (go1.26.3
local vs go1.25 repo baseline — left untouched per CLAUDE.md); one alignment
issue in `sort_pgrelationbytes_test.go` WAS caused by my own edit (a long
trailing comment shifted a map literal's column width) and was fixed
manually (not via `gofmt -w`). Commit going through the pre-commit hook's
mandatory pgbench smoke.

In-flight: none.
