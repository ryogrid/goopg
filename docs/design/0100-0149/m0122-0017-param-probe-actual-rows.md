# M0122-0017 — EXPLAIN ANALYZE: a parameterised probe's rows after its moved Filter

Status: landed 2026-10-27.

## Problem

M0146-0005aj made EXPLAIN place a parameterised nested loop's residual where PG places it. A conjunct that reads only
the inner relation and the probe's outer parameters prints as the inner scan's `Filter:`, as it does in PG
(`create_scan_plan` appends the path's `ppi_clauses` to the scan qual). Under ANALYZE, that conjunct's rejections
print as the scan's `Rows Removed by Filter`.

goopg still evaluates the conjunct at the join, so the scan's own row counter (`nodeStats.rowsOut`) counts every
candidate the probe returned, before the filter. EXPLAIN ANALYZE therefore printed a pre-filter `actual rows` beside a
non-zero `Rows Removed by Filter`. TPC-DS Q17, Q25 and Q29 show
`Index Scan using catalog_sales_pkey … actual rows=25.63 loops=7511` with `Rows Removed by Filter: 26`, where PG
prints `rows=0.00`. PG counts what the scan returns: `InstrStopNode` runs after `ExecScan`'s qual.

## Fix

`walkPlanAnalyzeFiltered` (`internal/executor/operators_explain.go`) walks the inner probe with
`attachedFilterNode == n` and `filterRowsRemoved` set to the probe's share of the residual's rejections
(`splitParamQualRejections`). No other walk passes the node itself as its own attached filter. On that branch the
line's row count is `rowsOut − filterRowsRemoved`, floored at 0, before the per-loop average. The rejection count
and every other line are unchanged.

## Verification

- `TestExplainAnalyzeParamProbeRowsAfterMovedFilter` uses `o JOIN i ON i.k = o.k AND i.v > o.id + 40` over the
  shared NL fixture. The probe returns 50 candidates in 100 loops, and the moved qual rejects 41. The test pins
  `Index Scan … (actual rows=0.09 loops=100)` and `Rows Removed by Filter: 0`.
- Scratch PG 18.3 prints exactly those two lines for the same nested-loop shape (merge and hash joins off). The
  fixture's own stats elect the nested loop in goopg.
- The regress suite's 81 mismatching cases are unchanged; only the `plpgsql` flapper moved.

## Not done (ledgered)

The JSON, YAML and XML ANALYZE renderer (`planToJSONWithStatsNamed`) does not implement M0146-0005aj's split. It keeps
the whole residual on the join, with `Rows Removed by Join Filter`, and reports the probe's pre-filter rows. That is
consistent within that format but differs from PG's placement.

## Gates

See the M0122-0017 entry in `.ralph/fix_plan.md`.
