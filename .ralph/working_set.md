(idle — nothing in flight)

Last loop (#4, 2026-07-31) closed **M0125-0026**: goopg's EXPLAIN next to PG's
for the TPC-DS timeout class, 18 queries × 3 arms, classified, six per-class
tasks filed. Artifacts `analysis/m0125-0026-timeout-plans/`.

Banner's NEXT SELECTION is **`M0125-0037` stage (i)** — the EXPLAIN half only
(teach `internal/executor/operators_explain.go` to descend into set-operation
branches; goopg prints `*planner.SetOp` with no children, which left Q5/Q18/Q67
unclassifiable and blocks `M0125-0033`). Small, host-independent, EXPLAIN-only.
Re-read the banner before selecting — it outranks this note.
