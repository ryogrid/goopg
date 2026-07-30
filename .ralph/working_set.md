(idle — nothing in flight)

Last loop (#5, 2026-07-31) closed **M0125-0037 stage (i)** — the EXPLAIN half.
`describePlan`/`planChildren` in `internal/executor/operators_explain.go` now
carry a `*planner.SetOp` case (PG vocabulary captured from 18.3 on :65438:
`UNION ALL` → `Append` with left-deep chains flattened, `INTERSECT`/`EXCEPT
[ALL]` → `HashSetOp <cmd>`, JSON keeping `Node Type: SetOp` + `Strategy` +
`Command`). Q5 4 → 128 plan lines, Q18 → 91, Q67 → 94, Q14 → 815; all three
previously unclassifiable queries got a class. Re-capture
`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0037/`; design
`docs/design/0125-0037-explain-set-operations.md`; tests
`internal/executor/explain_setop_test.go`.

**The re-capture found a new class, filed as `M0125-0040`:** goopg expands
`ROLLUP` into a UNION ALL of one aggregate branch per grouping level, each
re-running the whole join subtree (Q67: 8 branches, 9 full 1.44 M-row
`store_sales` scans; Q18: 4 branches, 5 full `catalog_sales` scans) where PG
makes ONE `GroupAggregate`/`MixedAggregate` pass. Neither query has a `union
all` in its SQL text — it was hidden behind the C4 instrument defect. Likely
the proximate cause of both timeouts and of the Q18 warm regression
(`M0125-0033`).

Stage (ii) of `-0037` (the planner half) remains OPEN and is a later selection.
Per the banner's adopted order the **next selection is `M0125-0039`** (qualify
EXPLAIN column refs with their relation alias, so Q30's `ctr_state = ctr_state`
prints as `ctr1.ctr_state = ctr2.ctr_state`) — small, host-independent,
EXPLAIN-only. Re-read the banner before selecting; it outranks this note.

Host note: the nightly CI batch (run `20260731-001201`) was live all loop, in
its units/race/testport stages. The SF0.5 goopg cluster was started and stopped
with `GOOPG_BIN=tmp/goopg-m0125-0037-bin` so the shared `tmp/goopg-bench-bin`
was never rebuilt under the nightly; both goopg TPC-DS clusters are DOWN again,
as they were found.
