# 0097-0032b — `timezone_abbreviations` GUC + verbose `utc_offset` parity for timezone system views

Status: accepted
Milestone: M0097-0032 (Port `sysviews` regress test)
Date: 2026-05-25

## Problem

`sysviews.sql` exercises the timezone system views. Two distinct goopg gaps
produced four diff lines (after the M0097-0032 `pg_settings` fix took the case
from 73 → 41):

1. **Unrecognised GUC.** The script runs

   ```sql
   set timezone_abbreviations = 'Australia';
   set timezone_abbreviations = 'India';
   ```

   goopg had no `timezone_abbreviations` GUC registered, so each `SET` failed
   with `ERROR: unrecognized configuration parameter "timezone_abbreviations"`
   (2 error lines).

2. **`pg_timezone_abbrevs` output formatting.** The script's stable-output
   probe

   ```sql
   select * from pg_timezone_abbrevs where abbrev = 'LMT';
   ```

   expects

   ```
    abbrev |          utc_offset           | is_dst
   --------+-------------------------------+--------
    LMT    | @ 7 hours 52 mins 58 secs ago | f
   ```

   goopg's virtual table stored the offset as a clock string `-07:52:58` and
   `is_dst` as the literal `false`, and goopg emits virtual-table strings
   verbatim (no type-aware reformatting), so the row read
   `LMT | -07:52:58 | false`.

   The expected verbose form is not the GUC default (`postgres`): **`pg_regress`
   forces `intervalstyle=postgres_verbose`** via `PGOPTIONS`
   (`postgres/src/test/regress/pg_regress.c:794`), which is why the upstream
   `.out` uses `@ 7 hours 52 mins 58 secs ago`. goopg's regress runner
   (`ClusterRegressExecutor`) does **not** set that PGOPTIONS, and goopg does
   not reformat interval-typed virtual columns per the GUC.

## Fix

### GUC

Register `timezone_abbreviations` (`internal/config/defaults.go`) as a
`TypeString`, `ContextUserset` GUC with `BootVal "Default"`, scoped to
session/transaction. Upstream is not `GUC_REPORT`, so no `FlagReport`. goopg
accepts any string value (PostgreSQL validates against an abbreviation file;
goopg's `pg_timezone_abbrevs` is a static stub, so the value is inert). Added
the matching commented entry to `internal/config/postgresql.conf.sample` to
satisfy `TestSampleConfigCoversRegistry`.

### Verbose offset rendering

Because goopg emits stored strings verbatim and the harness expects
`postgres_verbose`, the timezone views now store their `utc_offset` column
**pre-rendered** in verbose form via a new helper
`verboseIntervalOffset(totalSecs int)` (`internal/catalog/catalog.go`). It
mirrors `EncodeInterval`'s `INTSTYLE_POSTGRES_VERBOSE` arm
(`postgres/src/backend/utils/adt/datetime.c`): `@`-prefixed, ` <n> hour[s]`,
` <n> min[s]`, ` <n> sec[s]` fields (plural unless `== 1`), ` ago` suffix when
negative, `@ 0` for zero. Both `pg_timezone_names` and `pg_timezone_abbrevs`
now build offsets through it and store `is_dst` as `"f"` (matching the
existing `pg_settings` bool convention) instead of `"false"`.

`count(distinct utc_offset) >= 24` still holds (32 distinct verbose strings),
so the three count probes are unaffected.

## Verification

- `sysviews` regress diff **41 → 33** (verified end-to-end via
  `GOOPG_REGRESS_DIFF_DIR`; the LMT row and both `SET` errors are gone).
- `guc` unchanged at 592 (no regression from the new GUC).
- Tests: `TestVerboseIntervalOffset`, `TestPgTimezoneAbbrevsLMTRow`
  (`internal/catalog/catalog_test.go`); existing
  `TestSampleConfigCoversRegistry` (`internal/config`) re-passes.

## Remaining `sysviews` gaps (33 diff lines, separate subsystems)

- `pg_backend_memory_contexts`: `TopMemoryContext total_bytes >= free_bytes`
  reports `f`; the Bump-allocator `Caller tuples` and `CacheMemoryContext`
  children rows are empty (real memory-context introspection — a Go-runtime
  design constraint).
- `pg_hba_file_rules` `no_err` FILTER reports `f` (errors column non-null).
- `pg_wait_events` `group by type order by type COLLATE "C"` query errors
  (`'*' is not allowed here` / `trailing junk after numeric literal`) instead
  of returning the 9 wait-event-type rows.
