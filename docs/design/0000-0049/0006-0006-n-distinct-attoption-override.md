# Per-column `n_distinct` Attribute-Option Runtime Enforcement (Milestone 0006 follow-up)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-07-12                                             |
| Milestone  | 0006 — Planner-Grade Statistics (M0110-0001 discovery) |
| Refines    | [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md) |
| Supersedes | —                                                      |

## Problem

`ALTER TABLE ... ALTER COLUMN ... SET (n_distinct = <v>)`
(`internal/parser/ddl.go`'s `parseColumnSetOptions`) has stored the option on
`catalog.Column.Options` (e.g. `"n_distinct=-0.5"`) since DU-002 slice 185, and
renders it into the `pg_attribute.attoptions` text array so it round-trips
through `pg_dump`. But the call-site comment said the quiet part out loud:
**"goopg does not act on these planner statistics hints; recorded purely so the
column round-trips through pg_dump."** `ANALYZE` computed
`ColumnStats.NDistinct` purely from the reservoir sample (`len(freq)`), so a
DBA's manual override was silently ignored by cardinality estimation. This was
flagged during the M0122 backlog audit (`unimplemented_feat.json`, task_id
`M0110-0001`, "Per-column n_distinct planner hints are stored in attoptions for
dump compatibility but are not used by the query planner for optimization";
`code_audit: confirmed-open: internal/executor/operators_ddl.go:7564 n_distinct
stored dump-fidelity only").

## Upstream reference

- `postgres/doc/src/sgml/ref/alter_table.sgml`: `n_distinct` "override[s] the
  number-of-distinct-values estimates made by subsequent `ANALYZE` operations."
  A **positive** value is an absolute distinct count. A value **below 0 and
  above or equal to -1** is a fraction: the planner multiplies its absolute
  value by the estimated row count (so `-1` ⇒ all rows distinct, `-0.5` ⇒ each
  value appears twice on average). `n_distinct_inherited` is the same override
  for the inheritance-tree / partitioned-table stats pass.
- `postgres/src/backend/access/common/reloptions.c`: `n_distinct` reloption is
  `RELOPT_TYPE_REAL`, default `0` (meaning "no override"), min `-1.0`, max
  `DBL_MAX`.
- `postgres/src/backend/commands/analyze.c:571-581`: the override is applied
  **at ANALYZE time** — after computing the sampled `stadistinct`, if the
  appropriate (`inh`-selected) flavor of the option is non-zero it overwrites
  `stats->stadistinct`, which is then stored in `pg_statistic`. The plan-time
  reader (`get_variable_numdistinct`, `selfuncs.c`) just consults the stored
  value and resolves the negative-fraction form against the current row count.

## Decision

Apply the override **at ANALYZE time**, exactly as upstream does, so no
plan-time code changes are needed — goopg's planner
(`internal/planner/cardinality.go` `columnNDistinctForChild`) already reads the
stored `Stats.Columns[i].NDistinct` for eq-selectivity (`1 / NDistinct`) and
join cardinality, and now transparently gets the overridden value.

`internal/executor/operators_analyze.go` gains `columnNDistinctOverride(col
*catalog.Column, rowCount int64) (int64, bool)`. Because goopg stores
`NDistinct` as an **absolute** `int64` (not upstream's signed
positive-absolute / negative-fraction encoding), the negative-fraction form is
resolved to an absolute count here, against the ANALYZE-time row count:

```go
// v == 0 / unset / other option ⇒ (0, false), no override
// v  > 0 ⇒ absolute distinct count (rounded)
// v  < 0 ⇒ |v| * rowCount, clamped to the valid [-1, 0) range, floored at 1
```

The per-column loop applies it right after `computeColumnStats`, only for
columns that are actually analyzed (so a `SET STATISTICS 0` column — excluded
by `columnStatsTarget` — is never given an override either, matching upstream's
"no `VacAttrStats` ⇒ no row" ordering):

```go
stats.Columns[i] = computeColumnStats(reservoir, i, colTarget)
if nd, ok := columnNDistinctOverride(&tbl.Columns[i], stats.RowCount); ok {
    stats.Columns[i].NDistinct = nd
}
```

Only the non-inherited `n_distinct` flavor is honored: goopg's `ANALYZE` is a
single-relation (non-inherited) scan, so `n_distinct_inherited` — which
upstream applies only during the inheritance-tree pass — never fires here.

### What this does *not* change

- **The override takes effect only on the next `ANALYZE`.** This matches
  upstream exactly (the option "override[s] the estimates made by *subsequent*
  ANALYZE operations"); setting the option without re-analyzing does nothing.
- **Existing table-wide behavior.** A column with no `n_distinct` option
  (`columnNDistinctOverride` returns `(0, false)`) takes the identical prior
  code path — TPC-H/pgbench schemas have no such option, so every pre-existing
  ANALYZE test and the Q12/Q13 spot-check are unaffected.

### Divergence from upstream (deferred)

goopg resolves the negative-fraction form to an absolute count **at ANALYZE
time** against the then-current `RowCount`, whereas upstream stores the raw
`-0.5`/`-1` in `pg_statistic.stadistinct` and re-resolves it against the
*current* `reltuples` at every plan time. For a table whose size changes
substantially between `ANALYZE` and query, upstream's `-0.5` tracks the new
size while goopg's baked-in absolute does not — the very "size of the table
changes over time" case the docs cite as the reason to use a fraction. Faithful
tracking would require `ColumnStats.NDistinct` to carry the signed
positive/negative encoding and every consumer (5+ sites keyed on `NDistinct >
0`) to resolve it at plan time; deferred as a larger representation change. A
corollary: `pg_stats.n_distinct` renders goopg's resolved absolute value rather
than the raw `-0.5` upstream would show. Recorded in `deferral_ledger.md`.

## Verification

New tests in `internal/executor/operators_analyze_test.go`:

- `TestColumnNDistinctOverride`: table-driven unit test of the value-parsing
  contract — positive absolute, rounding, `-0.5`/`-1` fractions, tiny-fraction
  floor-at-1, below-range clamp to `-1`, `0`/unset/other-option/malformed
  no-ops, case-insensitive key, and `n_distinct_inherited` not honored.
- `TestAnalyzeRespectsNDistinctOption`: `SET (n_distinct = 5)` on an otherwise
  fully-unique 1000-row column forces `NDistinct == 5` through the real
  `analyzeRelationWith` path, while a sibling column with no override keeps its
  sampled value.

Gates: `go build ./...` clean; `go vet ./internal/executor` clean; the ANALYZE
test group PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33 — no-op-path
regression check, no TPC-H column has the option); pgbench smoke via the
pre-commit hook.

## Cross-references

- `0006-0001-sampling-and-mcv-histograms.md` — the sampling/NDistinct design
  this refines.
- `0006-0005-per-column-stats-target-enforcement.md` — the sibling `SET
  STATISTICS n` runtime-enforcement follow-up; same per-column ANALYZE loop.
- `unimplemented_feat.json`, task_id `M0110-0001` — the audit entry this closes.
- `internal/parser/ddl.go` `parseColumnSetOptions` + `internal/catalog/
  catalog.go` `Column.Options` — the parser/catalog/dump-fidelity half (DU-002
  slice 185) that populates the option this now consumes.
