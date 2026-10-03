# M0146-0009j — VACUUM (ANALYZE) collects column statistics

Status: done (2026-10-04, `ac4ee94d7`). Parent: M0146-0009.

## The divergence

After `VACUUM ANALYZE tenk1`, goopg's `pg_stats` held no rows for tenk1;
after `ANALYZE tenk1` it held 16. goopg's `vacuumOp` ran only the
relation-size pass (`vacuum.Analyze`: reltuples and relpages).

Regress `test_setup.sql` runs `VACUUM ANALYZE` on every shared table, so
every regress plan on goopg ran without column statistics. M0146-0005dk saw
it first: regress `join`'s `tenk1 a WHERE unique1 IN (SELECT unique2 …)`
plans flipped away from PG's Hash Semi Join because the planner had no
statistics.

## PG behaviour

`vacuum()` (`./postgres/src/backend/commands/vacuum.c`) loops over the
targets. For each one it calls `vacuum_rel` when VACOPT\_VACUUM is set, then
`analyze_rel` when VACOPT\_ANALYZE is set. `VACUUM ANALYZE t` therefore
leaves the same per-column statistics as `ANALYZE t`. For a partitioned
table, analyze also gathers the inheritance-tree statistics.

## Change

- **`analyzeTableStats(ctx, tbl)`** is ANALYZE's per-table step, moved out
  of `analyzeOp`. It samples the table (`analyzeRelationCtx`), installs the
  statistics, persists them to `pg_statistic`, and reports the analyze to
  the cumulative statistics.
- **`rollupPartitionedParentStats`** and **`catalogPartitionChildren`**
  hold the partitioned-parent roll-up, also moved out of `analyzeOp`.
- **`vacuumOp`** runs `analyzeTableStats` on each target whose vacuum pass
  succeeded. With ANALYZE it also rolls up partitioned parents, after the
  inheritance-scan wait it already did.

## Effect

Regress A/B against HEAD covered the 61 pass-required regress cases and 16
planner-heavy ones.

- The same 26 cases pass on both builds.
- Lines diverging from PG's expected output fell from 21221 to 21149:

  | case | before | after |
  |---|---|---|
  | join | 15037 | 14979 |
  | create\_index | 1777 | 1764 |
  | misc\_functions | 448 | 444 |
  | subselect | 1553 | 1555 |
  | window | 2036 | 2037 |
  | union | 370 | 370 |

- The subselect and window lines that grew are plans moving from one non-PG
  shape to another:
  - window: a nested loop where PG builds a hash join;
  - subselect: two parallel shapes where PG runs a serial `GroupAggregate`.

  Their cost elections are a separate question.

The TPC-DS and TPC-H bench clusters take their statistics from plain
`ANALYZE`, so their plans do not change.

Test: `TestVacuumAnalyzeCollectsColumnStatistics` fails before the change.
Without it, `VACUUM ANALYZE vas` leaves no column statistics.

## Not done (ledgered)

- **Column lists.** A target's column list (`VACUUM ANALYZE t (a)`) is
  validated but not used to narrow the analysis, the same as for
  `ANALYZE t (a)`; every column is analyzed.
- **`analyze_rel` for partitioned parents.** PG runs it on a partitioned
  parent itself and gathers inheritance-tree column statistics. goopg rolls
  up only the size.
