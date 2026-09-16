Task: M0142-0008a-3i-verify (next) — design doc §12 corrected the -3(i) recon
witness and re-sized option 1's blast radius; the concrete follow-up needs a
live instrumented probe, not more static reading.

Files: docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md (§12
added), docs/design/README.md (index row extended), .ralph/fix_plan.md
(M0142-0008a-3i-recon2 marked [x], M0142-0008a-3i-verify filed). No .go
files changed — design-only recon per the task's own filing.

Key symbols: `extractSearchLeaves` (joinsearchseam.go:1070, the walk that
must stop at x.Right), `EstimateRows` (cardinality.go:43, has a `*Join` case
already covering Q69's witness), `baseSeqScanCostInputs` (joinsearch.go:479,
already-generic fallback for any non-*SeqScan leaf), `PathPrebuilt`/
`createPlan` (path.go:49, createplan.go:13 — already returns any wrapped
Node unchanged), `cteScanOp` (operators_cte_dml.go:306, the reuse TRAP —
materializes+caches by `DeclKey()`).

Hypothesis/Findings: (1) §11's own named witness (query10/query35) is WRONG
— PG's actual chosen plan for both has ZERO Semi/Anti join nodes; it's
M0142-0008c's `create_unique_path` mechanism instead. Q69 (same census,
tmp/m0142-0008a-census/) is the correct witness: PG's plan has a genuine
`Parallel Hash Semi Join` + two `Nested Loop Anti Join`s, each RHS a real
2-relation `channel ⋈ date_dim` body, and goopg already independently lands
on the matching Hash Semi/Anti Join node kind there. (2) Re-reading (not
grepping) the four call sites §11 worried about shows 3 of 4 are ALREADY
fully generic over an arbitrary wrapped Node (baseSeqScanCostInputs,
PathPrebuilt/createPlan, and EstimateRows's own `*Join` case covers Q69's
concrete top-node kind) — the real blast radius is much smaller than §11
estimated. (3) Reusing the existing `*CTEScan` wrapper (the obvious
shortcut) is a correctness TRAP: its materializing cache is keyed by
`DeclKey()`, and Q69 needs 3 independent RHS wraps that would collide on the
same bare-name key. (4) A plain no-op `*Filter{Predicate:nil, Child:x.Right}`
looks like the correct minimal wrapper (stops extractSearchLeaves's walk;
EstimateRows's Filter case + filterSelectivity(nil)=1.0 is an exact
pass-through; Filter is already generic everywhere else) — UNVERIFIED, static
read only, no test run. `reresolveJoinByName`'s post-search-splice problem
(§11) is untouched by any of this and still separately open.

Next step: M0142-0008a-3i-verify (filed this loop) — build a small throwaway
instrumented probe against Q69 confirming (a) all three EXISTS bodies' top
nodes are really `*Join{Inner}`, (b) wrapping one in
`*Filter{Predicate:nil, Child:x.Right}` and splicing it in as a leaf
reproduces the SAME EstimateRows/baseSeqScanCostInputs numbers goopg computes
for it today. Only after that should real DP-search plumbing ((a) RelOptInfo/
SJInfo registration, (b) legality wiring, (c) reresolveJoinByName's
post-search splice) be attempted. Alternatives if blocked: M0142-0008c
(create_unique_path scoping, independent, same census) or M0142-0005
(per-worker Memoize scoping/floor-measurement, independent) are both open
and unblocked. M0142-0003i/0003k remain BLOCKED on a human-authorized shared
`:65433` cluster reload — do not attempt.

Gates run: `make ralph-state-guard` (self-repaired the same stale
progress.json "completed" marker pattern as last loop, then OK). No go test
run — zero .go files changed this loop (design doc + fix_plan.md + README
index only). Pre-commit hook's pgbench smoke gate PASSED (docs-only commit).

In-flight: none.
