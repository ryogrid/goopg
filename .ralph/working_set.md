Task: M0134-0082 (explain.sql) — sizing-only, PARKED 2026-08-23. No code fix
landed; case stays `failed` (CSV unchanged, 869-line diff at
/tmp/explain-diffs/explain.diff, not preserved under ci/logs/ — throwaway run).

Files: .ralph/fix_plan.md (M0134-0082 PARKED note, 7-bucket summary),
.ralph/deferral_ledger.md (2026-08-23 M0134-0082 row, full bucket detail +
resume points). No source files touched.

Key symbols: internal/executor/operators_explain.go — planToJSON /
planToJSONWithStats (line ~1684, the structured-format JSON/XML/YAML node
builder, feature-poor vs the text renderer) and describePlan (line ~1858,
returns "Projection" for *optimizer.Project with no skip, unlike the text
path's documented skip at lines 388/412). internal/executor/
operators_explain_format.go renders whatever map planToJSON produced (XML/
YAML formatting itself looked correct on spot check — the gap is upstream,
in what fields get INTO the map).

Findings (live-repro'd via scripts/pg-regress-runner.sh --verbose explain
against a throwaway cgroup-capped server): (A, largest) planToJSON/
planToJSONWithStats emit only "Node Type"+"Plan Rows" per node — missing
Startup/Total Cost, Relation Name, Alias, Plan Width, Parallel Aware, Async
Capable, Disabled, and (for ANALYZE) most buffer/IO-timing fields the text
renderer already computes. (B) sibling-path bug: text renderer skips
*optimizer.Project wrapper nodes, planToJSON's describePlan call does not —
every structured-format output gets a spurious extra "Projection" node.
(C) SERIALIZE EXPLAIN option unparsed (0% implemented). (D) GENERIC_PLAN is
a no-op stub — always falls back to custom plan w/ wrong shape, and the
ANALYZE+GENERIC_PLAN mutual-exclusion check is missing (goopg instead tries
to bind the raw SQL text as a param, throwing 22P02). (E) MEMORY option only
wired inside the ANALYZE+MEMORY combo — plain `explain (memory)` and
`EXPLAIN (MEMORY) EXECUTE <prepared>` silently drop the Memory: line.
(F) WindowAgg missing "Window:"/"Storage:" detail lines (0% implemented,
no per-window tuplestore accounting) AND separately mis-resolves verbose
Output columns for WindowAgg nodes (shows raw base columns, not the window
exprs) — two likely-independent root causes, neither investigated yet.
(G) EXPLAIN of EXECUTE/CREATE TABLE AS renders the unplanned statement node
("Utility *parser.ExecuteStmt" / "DDL *parser.CreateTableStmt") instead of
unwrapping to the inner plan tree.

Next step: per banner, next unparked M0134 task by ID ascending =
M0134-0083 (uuid.sql). Bucket B above (skip *optimizer.Project in
planToJSON/planToJSONWithStats, mirroring the existing text-renderer skip)
is the smallest CONTAINED slice and best candidate for a future dedicated
M0134-0082 resume — won't flip the case alone since A/C/D/E/F/G remain.

Gates run: make ralph-state-guard PASS. No source changes this loop, so no
unit/spotcheck/pgbench gates were run (nothing to regress-test).

Nightly triage: both 20260823-011911 AI items already filed in fix_plan.md
M-NIGHTLY section (verified this loop, no new run posted since) — no
filing action needed.

Delegation: none active.

In-flight: none.
