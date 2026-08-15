# Working set — M0134-0001 aggregates.sql (S8 Slice 1 sorted-agg executor LANDED)

**Task:** M0134-0001 aggregates.sql EXPLAIN-format digestion (P2). This loop
landed **S8 Slice 1 (class 6 executor half)** — the sorted/GroupAggregate
(AGG_SORTED) execution capability, planner-inert.

**Status:** S8 Slice 1 COMPLETE + committed (`c6bea890`). `Aggregate.Strategy`
(`AggStrategyHashed`/`AggStrategySorted`, zero value = Hashed), `openSorted`
run-collapsing walk + `sameGroupKey`, shared `finalizeGroup` emit, EXPLAIN
`GroupAggregate` label. **Planner-inert: nothing sets `Strategy`**, so every
existing query keeps the hash path byte-identically.

**Files:** `internal/planner/plan.go` (AggStrategy type + `Aggregate.Strategy`);
`internal/executor/operators_join_agg.go` (`openSorted`, `sameGroupKey`,
`finalizeGroup`, `groupRuntime`→package-level); `internal/executor/
operators_explain.go` (GroupAggregate/HashAggregate branch);
`internal/executor/operators_join_agg_sorted_test.go` (5 tests);
`docs/design/0134-0001-p2-explain-format.md` (S8 Slice 1 landed note).

**Key symbols:** `openSorted` (gated `Strategy==Sorted && GroupingSets==nil &&
len(GroupExprs)>0 && Mode==AggModeSimple`), `sameGroupKey` (element-wise parts
compare), `finalizeGroup` (shared emit), `planner.AggStrategy`.

**Findings (this loop):**
- `evalGroupExprs` allocates a fresh `parts` slice per call (operators_join_agg.go
  ~2242) — safe to retain `curParts = parts` in `openSorted` (no aliasing).
- Hash path's `setGroupKey` string-join of `datumKey` (`"s:"+value` / `"x:"+value`,
  no escaping, `|` separator) can COLLIDE distinct text/bytea groups whose keys
  contain `|s:`/`|x:` — a latent pre-existing hash bug. Sorted's element-wise
  `sameGroupKey` is collision-free → hash-vs-sorted would diverge once the planner
  picks strategy. **Deferral-ledger row appended.**
- Prior loop's "aggregates.diff = 746" is STALE: current clean-HEAD baseline is
  1369 lines (tester verified at `85d59ce2`); S8 adds zero new hunks.

**Next step:** brief + delegate **S8 Slice 2 (class 6 planner half)** — group-key
pathkeys (`pathkeysForSortKeys`/`pathkeysContainedIn` over the existing `PathKey`
model in `internal/planner/pathkeys.go`), `Sort` emission when the child isn't
presorted, the hash-vs-sort strategy choice (PG `add_paths_to_grouping_rel` +
`cost_agg` AGG_SORTED/AGG_HASHED arms), and reorder `GroupExprs` into sort/pathkey
order (moves EXPLAIN `Group Key:` + output order together). Load the
`executor-planner-change` + `perf/TPC-H` practice cards; BOUND it per the
M0072-0002 hang trap (incremental, verify each step). `cost_agg` is
`cost_funcs.go:452` (currently AGG_HASHED-only, no production caller). Decide
local choice inside `buildAggregateStage` (bounded) vs promoting grouping into the
Path/add_path search (large) — prefer the local choice.

**Gates run (this loop):** `go test ./internal/executor/ ./internal/planner/`
PASS; `scripts/pg-regress-runner.sh aggregates` byte-identical to clean HEAD (no
new hunks); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); pre-commit pgbench
smoke PASS (0 failed txns).

**Delegation:** implementer `0134-0001-s8-sorted-agg` DONE (round 1); tester
(long gates) DONE. Both reports inline (handoff-dir Write sandbox-blocked).

**In-flight:** none.
