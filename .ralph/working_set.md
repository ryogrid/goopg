# Working set — M0134-0001 S8 Slice 2c-i (LANDED `949a71f1`)

**Task:** M0134-0001 (aggregates.sql), slice **S8 Slice 2c-i — index-ordered
grouping input**. Selected per the Current Priority banner (M-NIGHTLY had no open
item; M0134 is next, and M0134-0001 is its topmost unchecked task).

**Landed:** `applyIndexOrderedGroupingRule`
(`internal/optimizer/groupagg_indexorder.go`, dispatched `planner.go:1296` BEFORE
the Slice 2a/2b rules). When every GROUP BY key is a plain column of the scanned
table and some ordering of them is exactly a leading prefix of a usable btree
index, the `*SeqScan` child becomes an ascending full-range
`*IndexOnlyScan`/`*IndexScan` and `Strategy` becomes `AggStrategySorted` with
**no `Sort`**. Port of the "path already sorted" half of
`get_useful_group_keys_orderings` (`postgres/.../path/pathkeys.c:466-550`), search
direction inverted (goopg has no path enumeration). Diff **1311/44/661 →
1296/44/651**; `btg` query (i) closes outright.

**Key design point (do not regress it):** `GroupExprs` is NEVER permuted —
`buildAggregateStage` binds every downstream target-list/HAVING/ORDER BY
`ColumnRef` to a GroupExpr's written position and `finalizeGroup` emits
`groupValues[i]` at output column `i`, so a permutation moves DATA between output
columns. The reorder exists only to print PG's `Group Key:` line, via the new
EXPLAIN-only `Aggregate.GroupKeyOrder` (nil = written order). Sibling half is the
`Group Key:` renderer in `internal/executor/operators_explain.go`.

**Files:** `internal/optimizer/groupagg_indexorder.go` (new) + `_test.go` (new, 5
plan-shape tests), `internal/executor/groupagg_indexorder_data_test.go` (new, 2
row-VALUE tests — the correctness gate for the no-permutation premise),
`internal/optimizer/plan.go` (`GroupKeyOrder`), `internal/optimizer/planner.go`
(dispatch), `internal/executor/operators_explain.go`,
`docs/design/0134-0001-p2-explain-format.md` (new §"S8 Slice 2c", accepted),
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

**Gates run:** `go build ./...` PASS; `internal/optimizer` + `internal/executor`
PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (cache
warm); **`scripts/tpch-spotcheck.sh` PASS — Q12 rows=2, Q13 rows=35, both matching
the canonical anchors**; pre-commit pgbench smoke PASS. `make ralph-state-guard`
OK after an automatic repair (previous loop's clean-exit marker).

**Deferral ledger:** 3 rows appended 2026-08-17 — (1) the rule is gated on
`enable_hashagg=off` pending a real cost comparison (firing unconditionally
regressed to 1386/47/687); (2) Slice 2c-ii partial-prefix needs an **Incremental
Sort** node goopg lacks entirely (owns 5 of the 7 `btg` EXPLAINs) + Slice 2c-iii
ORDER-BY-aware ordering; (3) re-attribution — `enable_hashjoin`/`enable_nestloop`
non-honoring, join-aware functional-dependency GROUP BY reduction, and a duplicate
residual `Filter` alongside an `Index Cond` are separate gaps, and
`agg_sort_order` is only the EXPLAIN underline-width formatter, not a plan
divergence.

**Next step (re-read the fix_plan banner first):** 6 new M-NIGHTLY items were
FILED this loop from nightly `20260817-011734` and are UNCHECKED, so M-NIGHTLY now
outranks M0134. Start with `race/internal/initdb` (AI-20260817-011734-001) — that
run forked before the sharding fix `83dd7ae8`, so it is expected stale; verify with
`make race-gate RACE_TIMEOUT=45m RACE_SHARD_ONLY=1` and close. Then the two
`TestE2E_PG{Cold,Crash}StartOnGoopgDataDir` items, which likely share a root cause
and were previously masked by a mid-stage build break.

**Delegation:** researcher `tmp/ralph-handoffs/m0134-0001-s8-slice2c/` (DONE, 2
rounds — the scope/attribution work); implementer
`tmp/ralph-handoffs/m0134-0001-s8-slice2c-i/` (DONE, 1 round); tester, same dir,
`gate-brief.md` (DONE, 1 round).

**In-flight:** none.
