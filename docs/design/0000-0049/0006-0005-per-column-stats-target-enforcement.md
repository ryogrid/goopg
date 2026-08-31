# Per-column `SET STATISTICS` Runtime Enforcement (Milestone 0006 follow-up)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-07-06                                             |
| Milestone  | 0006 — Planner-Grade Statistics (M0119-0004 discovery) |
| Refines    | [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md) |
| Supersedes | —                                                      |

## Problem

`ALTER TABLE ... ALTER COLUMN ... SET STATISTICS n` (`internal/executor/
operators_ddl.go`'s `AlterTableSetStatistics` case) has stored the override on
`catalog.Column.StatTarget` since DU-002 slice 184, and re-syncs the
`pg_attribute` heap row so the value round-trips through `pg_dump`. But the
comment at the call site said the quiet part out loud: **"goopg does not
sample per-column statistics targets — dump-fidelity only."** `ANALYZE`
(`analyzeRelationCtx` → `analyzeRelationWith`) only ever consulted the
table-wide `Context.StatsTarget` (`default_statistics_target`); every column
got the same histogram bucket count / MCV cap regardless of its own
`attstattarget`. This was flagged during the M0122 backlog audit
(`unimplemented_feat.json`, task_id `M0119-0004`, "Runtime enforcement of
ALTER STATISTICS SET STATISTICS target during DDL operations").

This is exactly the gap `0006-0001`'s own "Out of scope" section named:
*"Per-column statistics targets ... Parser stub only in this milestone, full
plumbing deferred."* The parser/catalog/dump-fidelity plumbing landed later
(DU-002 slice 184); this loop closes the remaining runtime-enforcement half.

## Upstream reference

`postgres/src/backend/commands/analyze.c`:

- `examine_attribute`: reads `attstattarget` from `pg_attribute` (NULL → -1).
  **`attstattarget == 0` skips the column entirely** — no `VacAttrStats` is
  built, so no `pg_statistic` row is ever written for it.
- `do_analyze_rel` (~line 1897-1899): **`attstattarget < 0` (unset) falls back
  to `default_statistics_target`.**
- `compute_scalar_stats`/`compute_distinct_stats`: `num_mcv = num_bins =
  stats->attstattarget` — a *positive* override changes the MCV cap and
  histogram bucket count for that column only, independent of every other
  column's target.

## Decision

`internal/executor/operators_analyze.go` gains `columnStatsTarget(col
*catalog.Column, tableTarget int) (target int, ok bool)`, mirroring
`examine_attribute` exactly:

```go
func columnStatsTarget(col *catalog.Column, tableTarget int) (target int, ok bool) {
    if col.StatTarget == nil {
        return tableTarget, true // unset ⇒ default_statistics_target
    }
    if *col.StatTarget == 0 {
        return 0, false // SET STATISTICS 0 ⇒ don't analyze this column
    }
    return *col.StatTarget, true // positive override, used verbatim
}
```

`analyzeRelationWith`'s per-column loop now calls this per column instead of
passing the table-wide `target` straight through:

```go
for i := range tbl.Columns {
    colTarget, ok := columnStatsTarget(&tbl.Columns[i], target)
    if !ok {
        continue // leaves stats.Columns[i] at its zero value
    }
    stats.Columns[i] = computeColumnStats(reservoir, i, colTarget)
}
```

`persistStatsToPGStatistic` skips writing a `pg_statistic` row for a column
whose override is `0`, matching upstream's "no `VacAttrStats` ⇒ no row"
behavior instead of writing a misleading all-zero row.

### What this does *not* change

- **Sample size** (`targrows = tableTarget * 300`, the reservoir cap) stays
  table-wide — upstream's `acquire_sample_rows` also samples once per table
  scan, not once per column; only the *post-sample* MCV cap / histogram
  bucket count is per-column.
- **Extended statistics objects** (`CREATE STATISTICS`, `pg_statistic_ext`).
  `ALTER STATISTICS ... SET STATISTICS n` (`execAlterStatistics`,
  `0119-0004-alter-statistics-set-statistics.md`) remains dump-fidelity only
  — goopg has no extended-statistics *computation* at all yet (no
  multi-column NDistinct/dependencies/MCV), so there is no sampling step for
  that target to influence. Out of scope here; tracked separately.
- **Existing table-wide behavior.** A table with no column overrides
  (`StatTarget == nil` everywhere, the common case — TPC-H/pgbench schemas)
  takes the exact same code path as before (`columnStatsTarget` returns
  `(tableTarget, true)` for every column), so `TestAnalyzeRespectsStatsTarget`
  and every other pre-existing ANALYZE test is unaffected.

## Verification

New tests in `internal/executor/operators_analyze_test.go`:

- `TestAnalyzeRespectsPerColumnStatTarget`: `SET STATISTICS 5` on one column
  of a 1000-row uniform table caps its histogram at 6 boundaries while a
  sibling column with no override is unaffected.
- `TestAnalyzeSetStatisticsZeroDisablesColumn`: `SET STATISTICS 0` leaves that
  column's `ColumnStats` at the zero value while a sibling column still gets
  real stats.

Gates: `go build ./...` clean; `go test ./internal/executor/...` PASS (full
package, no regressions); `scripts/tpch-spotcheck.sh` (Q12/Q13 row-count
spot-check) — TPC-H/pgbench schemas have no column-level overrides, so this
is a no-op-path regression check.

## Cross-references

- `0006-0001-sampling-and-mcv-histograms.md` — the sampling/MCV/histogram
  design this refines; names this exact gap as out of scope.
- `.ralph/unimplemented_feat.json`, task_id `M0119-0004` — the audit entry
  this closes.
- `internal/executor/operators_ddl.go`'s `AlterTableSetStatistics` case — the
  DDL/dump-fidelity half (DU-002 slice 184) that populates
  `catalog.Column.StatTarget`.
