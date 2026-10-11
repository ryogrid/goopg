# M0122-0008 — SET TIME ZONE INTERVAL and check\_timezone

Status: landed 2026-10-12.
Parent: M0122-0008.

## Problem

`SET TIME ZONE INTERVAL '+02:00' HOUR TO MINUTE; SHOW timezone` echoed the
statement text back; PG 18.3 shows `<+02>-02`. The TimeZone GUC stored any
spelling it was handed. Rendering then fell back to UTC for everything Go's
tz database did not know, so a fixed-offset zone printed timestamps in UTC.

PostgreSQL takes three steps here:

1. **gram.y `zone_value`.** `INTERVAL 'x' [HOUR | MINUTE | HOUR TO MINUTE]`
   and `INTERVAL(p) 'x'` become a TypeCast to a ConstInterval with that
   typmod. Any other qualifier is a syntax error. `LOCAL` and `DEFAULT` are
   both a NULL value, which means a reset.
2. **`flatten_set_variable_args`** (guc\_funcs.c) runs `interval_in` with the
   typmod, then `interval_out`. It hands `INTERVAL '02:00:00'` to the GUC.
   The same flattened text is what `ALTER ROLE ... SET TIME ZONE` stores in
   `setconfig`.
3. **`check_timezone`** (commands/variable.c) accepts three forms:
   - an `INTERVAL '...'` string. Months or days are rejected, each with its
     own DETAIL.
   - a number, read as hours east of UTC.
   - a zone name, resolved by `pg_tzset`: a tz file, or else a POSIX TZ
     spec checked by `tzparse`.

   Offsets go through `pg_tzset_offset`, which names the zone
   `<+hh[:mm[:ss]]>-hh...`. The stored and SHOWn value is
   `pg_get_timezone_name`.

## Design

- **Grammar** (`grammar/goopg_ext.y`). There are three new `SET [LOCAL] TIME
  ZONE INTERVAL` arms. `tzIntervalQualOK` raises gram.y's 42601 at the
  qualifier. `tzIntervalValue` (support.go) runs `ParseIntervalBodyWithDefault`
  with the qualifier's unit, applies `AdjustIntervalForTypmod`, then
  `FormatInterval`: the same interval\_in/out pipeline the `::interval` cast
  uses. `tzSetStmt` now treats `LOCAL` like `DEFAULT`.
- **Routing.** Both wire fast paths (`handleQuery`, `executeExtendedQuery`)
  used a text split for `SET TIME ZONE`. When storage is present, they now
  send the statement through the grammar and the executor's SET. ALTER
  DATABASE / ROLE `SET TIME ZONE` text goes through
  `parser.TimeZoneSetValue`, which parses it as the SET statement would.
- **check\_timezone** (`internal/utils/misc/timezone_check.go`):
  - `checkTimeZone` covers the INTERVAL, numeric and name forms;
    `checkLogTimeZone` covers names only.
  - `zoneName` tries `lookupZoneFile` first: a case-insensitive tz file
    match that must load as a zone, so `zone.tab` is rejected. Next it tries
    `parsePOSIXTZ`, a port of tzparse's acceptance rules, upper-cased as
    pg\_tzset stores it. tzparse allows an empty std abbreviation, so `+3`
    and `-3` are valid POSIX specs. Even `INTERVAL '02:00:00'` parses as a
    spec for log\_timezone, as it does in PG.
  - `offsetZoneName` is pg\_tzset\_offset, limited to 167:59:59.
  - `interval_in` lives in the parser package, which imports misc. The parser
    therefore registers `misc.IntervalParser` at init. A bad body raises
    interval\_in's own 22007, which is why `ValidationError` now carries
    `Code`.
  - `ValidationError` also gained `Detail`, for GUC\_check\_errdetail. Every
    SET error path forwards Code, Detail and Hint: the simple and extended
    fast paths (`gucSetError`), the executor, ALTER SYSTEM, function SET, and
    ALTER ROLE SET.
- **Rendering.** `sessionLocation` / `ZoneLocation` fall back to
  `posixLocation` when Go's tz database has no file for the name.
  `posixLocation` builds a one-type TZif v2 image whose footer is the POSIX
  string. Go's own footer interpreter then applies any DST rules, so
  `EST5EDT,M3.2.0,M11.1.0` switches to daylight time in July. EXTRACT /
  date\_part (`sessionTimeZoneLocation`) uses the same resolver, so output and
  field extraction agree.

## Verification

- Probe scripts `tmp/tz.sql` and `tmp/tz2.sql` (about 45 statements) were run
  against scratch PG 18.3. Output is byte-identical except for the items in
  the next section.
- That includes `pg_db_role_setting` after
  `ALTER ROLE/DATABASE ... SET TIME ZONE INTERVAL ...`.
- Tests: `TestSetTimeZoneIntervalFlattened`, `TestSetTimeZoneLocalAndDefault`,
  `TestSetTimeZoneIntervalQualifierRejected` (parser). `TestCheckTimeZone`,
  `TestCheckTimeZoneZoneFiles`, `TestZoneLocationPOSIX`, `TestParsePOSIXTZ`
  (misc). `TestSetRestTimeZoneFlattened` (postmaster).

## Residuals (filed or ledgered)

- **Function SET clauses are never applied at call time.** goopg has no
  fmgr\_security\_definer, so `SET search_path` and `SET timezone` on a
  function do nothing. This gives wrong results; filed as S2 M0146-0155.
- **initdb does not write the host time zone**, which PG's
  `select_default_timezone` does. Also, goopg's TimeZone boot value is `UTC`
  where PG's is `GMT`. `SET TIME ZONE LOCAL` therefore resets to a different
  zone than PG does.
- **SET of a SIGHUP / POSTMASTER parameter raises 22023** where PG raises
  55P02.
- **ALTER DATABASE ... SET TIME ZONE drops the check hook's DETAIL**:
  `databaseDDLError` has no detail field. A bad interval qualifier there is
  reported as an invalid value rather than the grammar's 42601.
