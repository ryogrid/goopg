# 0097-0032 — pg_settings enable_* GUC completeness & sorted output

**Status:** accepted
**Milestone:** M0097-0032 (port `sysviews` regress test)
**Date:** 2026-05-24

## Problem

`sysviews.sql` runs, without an `ORDER BY`:

```sql
select name, setting from pg_settings where name like 'enable%';
```

and expects PostgreSQL 18's **24** planner `enable_*` GUCs in **alphabetical
order**. PostgreSQL's `pg_settings` view is backed by the GUC table, which is
kept sorted by name, so even an unordered query returns name-sorted rows.

goopg's `pg_settings` virtual table (`internal/catalog/catalog.go`,
`registerSystemTables`) hand-coded only **20** `enable_*` rows in *registration*
order, and one was mis-named: `enable_gather_merge` instead of PostgreSQL's
`enable_gathermerge`. The result diverged on both **content** (4 GUCs missing,
1 wrong name) and **order** (registration vs. alphabetical), contributing ~30
diff lines to the `sysviews` case.

## Fix

In the effective `pgSettings.VirtualRows` closure (the second of two
assignments — the first is dead code, overwritten before the table is
registered):

1. Renamed `enable_gather_merge` → `enable_gathermerge` (matches PG's GUC name).
2. Added the 4 missing GUCs: `enable_distinct_reordering`,
   `enable_group_by_reordering`, `enable_self_join_elimination`,
   `enable_tidscan` (all default `on`).
3. Sorted the returned rows by name (`sort.Slice` on column 0) so the output
   honours PostgreSQL's sorted-GUC-table contract regardless of the literal
   order in the source. This is the robust fix — it benefits every
   `pg_settings` consumer, not just this one query.

The full `enable_*` set now matches PostgreSQL 18 exactly (24 rows, sorted).

## Scope / non-goals

This closes the `enable_*` portion of `sysviews` only. The case still defers on
unrelated gaps tracked under M0097-0032: `pg_backend_memory_contexts`
introspection (`ident`/Bump-context rows), the `pg_wait_events` SRF (9 wait-event
type rows), the `timezone_abbreviations` GUC, and `pg_timezone_abbrevs`
interval/boolean output formatting (`utc_offset` as `@ 7 hours …`, `is_dst` as
`f`). Each is a separate subsystem fix.

`sysviews` normalized diff: **73 → 41** lines.

## Tests

- `TestPgSettingsEnableGUCsCompleteAndSorted`
  (`internal/catalog/catalog_test.go`) pins the exact 24-GUC set, alphabetical
  order, and the overall name-sort contract of the virtual rows.
- `guc` regress case re-checked: unchanged at 592 diff lines (no regression;
  it does not query the `enable_*` set this way).
