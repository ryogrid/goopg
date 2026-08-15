# Working set — M0134-0001 P2 S6 Slice 2 (Backward max rewrite)

**Task:** M0134-0001 aggregates.sql — S6 Slice 2 (port the Backward half of
`preprocess_minmax_aggregates`: `max(<col>)` → `Result → InitPlan 1 → Limit →
Index Only Scan Backward`).

**Status:** Slice 2 LANDED + committed this loop. `max()` now rewrites via
**materialised-slice-reverse** (`IndexOnlyScan.Backward` + reverse iteration of the
already-materialised `o.rows`), NOT a true backward btree walk (deferred — ledger row).

**Files (this loop):** `internal/planner/plan.go` (`IndexOnlyScan.Backward`),
`internal/planner/planner.go` (`rewriteMinMaxAggregates` accepts max / `Backward:isMax` /
`Sort{DESC NULLS FIRST}`), `internal/executor/operators_indexonly.go` (reverse `o.idx`
step, Cond kept verbatim), `internal/executor/operators_explain.go` (`" Backward"` token),
`internal/planner/minmax_rewrite_test.go` (+4 max tests), `internal/executor/subplan_stats_test.go`
(now 2 instrumented sublinks), `docs/design/0134-0001-p2-explain-format.md` (Slice 2
landed + divergence note), `.ralph/deferral_ledger.md` (true-backward-walk deferral).

**Key symbols:** `rewriteMinMaxAggregates`, `indexOnlyScanOp.Next`/`Open`,
`IndexOnlyScan.Backward`, `describePlan`/`describePlanVerbose`.

**Findings:** materialise-reverse is byte-correct because goopg's IOS materialises the
whole range in `Open` (`operators_indexonly.go:297`); the one correctness trap (NULL leak)
is guarded by keeping the `Cond` (`col IS NOT NULL`) check in the reverse loop.

**Next step — S6 Slice 3 (edge cases):** constant `max(100)` (block 14), composite-prefix
index probing (block 7), inheritance/MergeAppend (blocks 15/16 + partial-index bug),
scalar-subquery nesting class-8 (blocks 8/17). Plus the `create_index.sql` prerequisite
for the regress runner (S1 deferral row) so the `Index Only Scan Backward` + `Index Cond`
text is verified end-to-end.

**Gates run (this loop):** `go test ./internal/planner/` PASS (8 minmax tests);
`go test ./internal/executor/` PASS; `scripts/pg-regress-runner.sh aggregates` 1537→1543
(5 max blocks now emit Result/InitPlan 1/Limit; scan line stays SeqScan fallback —
expected, no create_index.sql); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35).

**Delegation:** researcher S6-S2 research DONE (materialise-reverse verdict);
implementer S6 Slice 2 DONE. Handoffs: `tmp/ralph-handoffs/0134-0001-p2-s6-s2-{research,s2}/`.

**In-flight:** none.
