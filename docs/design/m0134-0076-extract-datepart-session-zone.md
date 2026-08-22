# `EXTRACT` / `date_part` on `timestamptz` must use the session TimeZone (M0134-0076)

- status: accepted
- date: 2026-08-22
- supersedes: none

## SQL surface

Two spellings of the same extraction, both over a `timestamptz` source:

```sql
EXTRACT(hour FROM ts)            -- ts is timestamptz
date_part('hour', ts)
EXTRACT(timezone FROM ts)        -- session UTC offset, seconds east
date_part('julian', ts)          -- Julian day number
EXTRACT(msec FROM ts)            -- field aliases
```

`timestamptz.sql` exercises all of these against a table of instant values, in
a session whose `TimeZone` is `America/Los_Angeles` (the runner exports
`PGTZ`; commit `569cb0ce`).

## Divergence (goopg ↔ PG 18.3)

1. **Session-zone bypass** — calendar fields (`hour`/`day`/`year`/`doy`/…) are
   extracted in **UTC**, not the session zone. `date_part('hour',
   '2001-02-16 20:38:40+00'::timestamptz)` returns `20` where PG returns `12`
   (PST = UTC−8). This is the dominant diff driver (~460-line region).
2. **Missing field aliases** — `msec`, `usec`, `julian` fall through to
   `date field %q is not supported in v0` (`+ERROR`), where PG returns values.
3. **Timezone fields wrong** — `timezone`/`timezone_hour`/`timezone_minute`
   return a silent `0` (`date_part`) or error `22023` (`EXTRACT`) for a
   `timestamptz`, where PG returns the session offset (e.g. `-28800` for PST).
4. **No infinity arms** — `date_part('year', 'infinity'::timestamptz)` renders
   the carrier's boundary time (`2262-…`) instead of PG's `±Infinity` for the
   monotonically-increasing fields and `NULL` for the oscillating ones (see
   the oracle split below).

## Root cause

`extractTimestampField` (`internal/executor/expr.go:5810`) opens with
`u := t.UTC()`, and both callers also do `u := … .UTC()`:
`evalExtract` (`expr.go:5567`) and `evalDatePart` (`expr.go:5907`). For a
`timestamptz` the carrier stores a UTC **instant** (`NewTimestampTZDatum`,
`datum.go:542`), so `.UTC()` strips any chance of session-zone rendering.

PG oracle: `timestamp_part_common` / `timestamp_part`
(`postgres/src/backend/utils/adt/timestamp.c:5499` / `:5757`). For a
`timestamptz`, the caller `timestamptz_part` passes
`timestamp2tm(dt, NULL, &tm, &fsec, NULL, NULL)` with `NULL` zone → the
**session** TimeZone (`session_timezone`); the field switch covers
`msec`/`usec`/`julian`; `DTK_TZ`/`DTK_TZ_HOUR`/`DTK_TZ_MINUTE` return the
resolved offset; a non-finite input returns `NULL` for calendar fields and
`±Infinity` for `epoch`/`julian` (`TIMESTAMP_NOT_FINITE` arms).

## Fix (slice F — CONTAINED)

All in `internal/executor/expr.go`; the two sibling paths change together
(pattern_sibling_paths_must_agree).

1. **Thread the session zone.** Only a `timestamptz` source applies it:
   - `evalDatePart`: `src.TimeSub == TimeSubTimestampTZ` (or `src.IsTimestampTZ()`).
   - `evalExtract`: `srcType == "timestamptz"` (`strings.ToLower(x.SourceTypeName)`).
   For those, resolve the zone string (`timeZoneFromCtx(ctx)`,
   `expr.go:4152`) to a `*time.Location` and extract from `t.In(loc)`; every
   other kind (`timestamp`/`date`/`time`/`timetz`) keeps its stored wall-clock
   (no conversion). `epoch` is zone-independent (`Unix()`), so it is unaffected.
   Zone resolution must match the renderer's semantics — `""` → UTC, else
   `time.LoadLocation` (POSIX-style spellings fall back to UTC, as the renderer
   `sessionLocation` does) — so a fixed-offset session and `FormatTimestampTZ`
   agree.
2. **`extractTimestampField`** (`expr.go:5810`): drop the internal `.UTC()`
   (callers pass the already-zone-adjusted time); add `msec`/`usec` (aliases of
   `milliseconds`/`microseconds`) and `julian` (port `date2j` + the
   midday-noon offset; PG `timestamp_part` `DTK_JULIAN`).
3. **Timezone fields** — handle in the callers (which know the source type),
   not the shared int64 helper: for a `timestamptz`, `_, off := u.Zone()` →
   `timezone` = `off`, `timezone_hour` = `off/3600`, `timezone_minute` =
   `(off%3600)/60` (seconds east, matching Go's `Zone()` and PG's convention);
   for a `timestamp`/`date`, raise `22023 unit %q not supported for type …`.
   Remove the silent `return 0` in `extractTimestampField`.
4. **Infinity arms** — in both callers, before extraction: if
   `src.IsTimestampNotFinite()`, dispatch through a new `extractNonFiniteField`
   porting `NonFiniteTimestampTzPart` (`timestamp.c:5441`): the
   **oscillating** units (microsec/millisec/sec/min/hour/day/month/quarter/
   week/isoweek/dow/isodow/doy/timezone*) → `NULL`, the **monotonically-
   increasing** units (year/decade/century/millennium/isoyear/julian/epoch) →
   `±Infinity`. (The earlier "calendar fields → NULL" phrasing was wrong — the
   regress expected output proves `year`/`isoyear`/`epoch`/`julian` return
   `±Infinity`, only the oscillating units return `NULL`.) Carried as the
   strings `"Infinity"`/`"-Infinity"` because goopg's numeric has no
   `Infinity` wire type — which forces a companion `round()` pass-through
   (below).

5. **`round(±Infinity)` pass-through** — `evalFuncCall`'s `round` case
   (`expr.go:12956`) must return an `"Infinity"`/`"-Infinity"` string datum
   unchanged (PG `dround`/`numeric_round` return ±Infinity), since the regress
   extract block wraps the `julian` column in `round(...)` and goopg's
   `ParseFloat`/`FormatFloat` would mangle it to `"+Inf"`/`"-Inf"`.

## Deferred (ledgered separately)

- **`date_part` wire type** — `planner.go:12610` declares `date_part` →
  `int8`; PG `pg_proc.dat` declares `prorettype => 'float8'` (all six
  overloads, e.g. lines 2470/2476/2642/2989/2995/6323). Correcting this
  requires the runtime `evalDatePart` to return float8 for *all* fields (today
  calendar fields return `KindInt`), an orthogonal wire-kind change left out of
  this slice to keep it focused on the value-correctness fixes above.
- **B2** — the `infinity` *renderer* (`FormatTimestampTZ`/`FormatTimestamp`/
  `FormatDate`) still prints boundary times on the wire; separate one-line
  sentinel fixes, needed before the `d1` column's infinity rows collapse.
- **A** (input literal tokenizer) and **G** (`to_char` template engine) are
  REFACTOR-tier and dominate the rest of the diff; own tasks.

## Acceptance

`scripts/pg-regress-runner.sh --verbose timestamptz` at HEAD: the
`msec`/`usec`/`julian` `+ERROR` lines vanish, the extract fields of every
loaded row match PG byte-for-byte (session-zone values, `±Infinity`/`NULL`
non-finite arms), and the residual in the extract region is limited to the B2
boundary-time *renderer* rows (`d1` column) and the A missing-table rows.
Unit guard: extract `hour`/`day` under `TimeZone=America/Los_Angeles` returns
the PST answer for a stored UTC instant, plus `msec`/`usec`/`julian` and the
infinity arms.

Measured: total diff **3969 → 3945**, extract region **455 → 418** lines. The
originally-projected ≥250-line collapse does not materialise because only 7 of
66 `TIMESTAMPTZ_TBL` rows load (bucket A input-parser gaps — ~59 expected rows
per block are unreachable `-` lines) and the B2 `infinity` renderer leaves 2
boundary rows per block; those are the next two slices, not this one.
