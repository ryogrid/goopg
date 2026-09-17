Task: M0141-S7 — "the executor operator" (Finding 3 row 5). Attempted to
start directly, found its integration surface far exceeds Finding 3's
estimate, and split it into 4 loop-sized sub-tasks instead (scoping/recon
only this loop, no production code changed). Per fix_plan banner item 4.

Files: docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md
(new "Update 2026-09-17d" section — the full touch-point census),
docs/design/README.md (m0141-s7 index row updated with the same summary),
.ralph/fix_plan.md (M0141-S7 entry gets the 2026-09-17d update + 4 new
unchecked sub-task bullets: M0141-S7-exec-a/b/c/d), .ralph/deferral_ledger.md
(new row: M0141-S7-exec-d, the sortOp-parity features deferred out of the
metric-moving path).

Key symbols: none touched this loop (recon only). Next loop's targets:
new `IncrementalSort` optimizer.Node type (mirror `Sort`, plan.go) + a new
executor operator built on `sortPrefixEqual` (internal/executor/sort_presorted.go,
E-15) for M0141-S7-exec-a.

Findings: "the executor operator" is not one loop-sized unit once the goal
is "safe to flip GOOPG_INCREMENTAL_SORT=on and measure the corpus": TWO
execution engines build a Sort node (executor.go:179 classic buildNode,
:675 slab/tree fast path — pattern_sibling_paths_must_agree class), 4
mechanical `case *optimizer.Sort:` tree-walkers where an omitted arm is a
SILENT WRONG-ANSWER risk not a panic (scan_deform.go x2 — deform pushdown,
subplan.go — rescan-kind classification, operators_cte_dml.go — work-table
detection), 6 operators_explain.go sites (5 trivial, 1 needs a new PG
`Presorted Key:` line), and stats-map plumbing (context.go's
SortStats/SortWorkerStats, parallel_worker_ctx.go's worker mirror). None of
this changes Finding 3's algorithmic verdict (grouping via sortPrefixEqual +
per-group full sort via existing sortOp machinery really is the easy part)
— it changes the sizing of the plumbing around it.

Next step: implement M0141-S7-exec-a — the `IncrementalSort` optimizer.Node
type + the executor operator, built and unit-tested STANDALONE (constructed
directly in tests, zero createPlanNode/Plan() callers, same "zero production
callers yet" posture pathkeysCountContainedIn/costIncrementalSort used).
Scope EXCLUDES spill-to-disk/packed-tuple/ctid passthrough (M0141-S7-exec-d,
deferred). After exec-a lands, exec-b (createPlanNode arm + both executor.go
builder sites + the 4 tree-walkers — the correctness gate before
GOOPG_INCREMENTAL_SORT=on can ever be measured) is next, then exec-c
(EXPLAIN rendering). Re-check the fix_plan banner and re-read AGENT.md's
plan-parity harness section again before selecting (required every loop
touching M0137-M0143).

Gates run: `make ralph-state-guard` — found the same stale
running/completed mismatch seen in prior loops, self-repaired, then passed.
No go build/test/tpch-spotcheck/sf025 gates run this loop (zero Go/SQL
files touched — pure fix_plan/ledger/design-doc scoping work; `git status`
confirms only the 4 markdown/doc files listed above are modified).

In-flight: none.
