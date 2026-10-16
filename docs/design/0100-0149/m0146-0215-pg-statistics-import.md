# M0146-0215 — PostgreSQL 18 statistics import (`pg_restore_attribute_stats`, `pg_restore_relation_stats`)

Status: slice 2 landed 2026-10-16 (functions). Importing the reference statistics into the bench corpora is an
owner action and has not been done. On a private clone, that import takes SF0.25 from 86 to 88 / 99 (§5).

## 1. Why

Slice 1 (`analysis/m0146/m0146-0215/`) showed that TPC-DS Q76's divergence is ANALYZE sample variance, not planner
logic. goopg's and PG's 30000-row samples of the same table give `null_frac` 0.00503 and 0.0044 on
`cs_bill_customer_sk`, and the 512-vs-585-row estimate flips a join. A PG 18.3 cluster given goopg's three null
fractions picks goopg's exact plan. PG seeds its sampler from a per-process random state
(`pg_global_prng_state`), so no port of PG's code reproduces PG's sample. PG's own mechanism for carrying exact
statistics across clusters is statistics import: `pg_dump --statistics` emits `pg_restore_relation_stats` and
`pg_restore_attribute_stats` calls (`src/backend/statistics/relation_stats.c`, `attribute_stats.c`,
`stat_utils.c`). goopg had both names only in its `pg_proc` seed; a call failed with 42883.

## 2. Semantics ported

Argument handling (`stat_utils.c`):

- Arguments are VARIADIC name/value pairs.
  - An odd count is an ERROR ("variadic arguments must be name/value pairs").
  - A NULL name, or a name that is not text, is an ERROR.
  - A NULL value means the argument is absent; `version` is accepted and ignored.
  - An unknown name, or a value of the wrong type, is a WARNING. That argument is skipped and the function
    returns false.
- Types are checked statically, as PG's `extract_variadic_args` types do, through `optimizer.ExprResultType`.
  The display names are `format_type_be`'s: real, integer, smallint, boolean, text, real[].
- Required arguments: `schemaname`, `relname`, and for attributes `inherited` ("argument \"%s\" must not be
  null").
- The target is resolved by schema and name and locked ShareUpdateExclusive, like ANALYZE. Permission is
  ANALYZE's: `maintenancePermitted`, which is owner or MAINTAIN.

`pg_restore_attribute_stats` (`attribute_statistics_update`):

- The column comes from either `attname` or `attnum`.
  - Both given, or neither: ERROR.
  - Unknown or dropped: ERROR 42703.
  - A system column: ERROR 0A000.
- Values start from the column's existing statistics. Only the given fields are replaced: `null_frac`,
  `avg_width`, `n_distinct`, the MCV pair, `histogram_bounds`, `correlation`.
- Value checks are WARNINGs, as PG's are:
  - an array argument must be one-dimensional and contain no nulls;
  - `most_common_vals` and `most_common_freqs` must come together;
  - histogram and correlation need a less-than operator;
  - range statistics need a range type.
  On any of these, that statistic is skipped and the result is false.
- Each value element goes through the column type's input with its typmod, as `text_to_stavalues` → `array_in`
  does. An input error is a WARNING and skips that statistic. Text-like types use `coerceTextLikeDatum`, so
  char(n) is padded and varchar(n) checked. `numeric(p,s)` gets its typmod. The element is then rendered exactly
  as ANALYZE renders values (`formatDatumDateStyle`).
- The result is installed in the live catalog, and one pg_statistic row is written for the column, through the
  same writer ANALYZE uses.
- `n_distinct` is converted by `catalog.ColumnStatsDistinct`. Startup's pg_statistic reload uses the same
  function, so an imported column reads the same before and after a restart.

`pg_restore_relation_stats` (`relation_statistics_update`):

- `relpages` and `reltuples` set `TableStats.Pages` and `RowCount`, and persist to the size sidecar heap that
  ANALYZE writes.
- `reltuples < -1` is a WARNING. `reltuples = -1` means "never analyzed".
- An index name is accepted. Its statistics are not applied, because goopg's planner reads index size physically
  (as `plancat.c` does).

## 3. Deferred (ledger rows)

- `relallfrozen`: goopg has no consumer for it; accepted and ignored. `relallvisible` was in the same state until
  M0146-0216 stored it (`docs/design/0100-0149/m0146-0216-physical-parity-relallvisible.md`). It is now applied.
- Element and range statistics (`most_common_elems`, `most_common_elem_freqs`, `elem_count_histogram`, the
  `range_*` kinds): goopg's ColumnStats does not represent them. Accepted and ignored for array and range
  columns; a WARNING otherwise, as PG gives.
- `inherited = true` rows: goopg keeps no inheritance statistics. Accepted and ignored.
- Attribute statistics on index expression columns: accepted and ignored.
- `pg_clear_relation_stats` / `pg_clear_attribute_stats`: not ported, and pg_dump does not emit them.
- Changes are not transactional: they publish immediately, as goopg's ANALYZE does. PG updates pg_class and
  pg_statistic in the transaction.

## 4. Pins

- `TestPgRestoreAttributeStatsRoundTrip`: ANALYZE a table of mixed types and read `pg_stats`. Then wipe the
  in-memory statistics, re-import every column from the `pg_stats` text, and compare. The imported columns must
  equal what a server restart reconstructs from ANALYZE's own pg_statistic rows.
- `TestPgRestoreAttributeStatsSemantics`: the merge (unspecified fields kept), the WARNING-and-false paths, the
  ERROR paths and the `n_distinct` sign convention.
- `TestPgRestoreRelationStats`: relpages and reltuples, the index-name acceptance, and the `reltuples < -1`
  warning.
- The planner sees imported statistics: a `col IS NULL` estimate follows an imported `null_frac`.

## 5. Measured (private clone of the SF0.25 corpus)

- **Import:** the statistics section of `pg_dump --statistics` from `:65438 tpcds025` was extracted (474 calls: 49
  relation, 425 attribute) and run on a private clone of goopg's SF0.25 corpus. It took 1.5 s; every call
  returned true, with no WARNING.
- **Census:** with the reference's statistics, SF0.25 PLAN-PARITY went from **86 to 88 / 99**: Q76 and Q99 now
  match, and no query regressed (`analysis/m0146/m0146-0215/import-census/`). CATEGORIES-EXCL-MATCH:
  join-order 12 → 11, join-method 7 → 6, scan-type 4 → 2; parameterisation 5 → 6, within noise.
- **Durability:** the imported values and Q76's PG-identical plan survive two restarts, through the shared
  `pg_statistic` writer/reload pair and the size sidecar.
- **Not changed:** the shipped corpora. Importing the reference statistics into the bench clusters is an owner
  action (it replaces their ANALYZE statistics). The remaining SF0.25 residuals are therefore not sample variance;
  M0146-0216's physical inputs are the next candidate.

`pg_stats.avg_width` now renders the integer `stawidth` the writer stores. It used to show the float, and a
`pg_dump --statistics` of goopg then emitted `'7.25'::integer`, which a restore rejects.
