# `timestamptz` output renders its zone — and leaves UTC (M0119-0006)

- status: accepted
- date: 2026-08-11
- supersedes: nothing; closes the deferral-ledger row of 2026-08-11
  ("goopg prints a `timestamptz` with NO zone suffix")
- related: `0097-0151-datestyle-partial-set-merge.md` (the DateStyle GUC merge),
  `0097-0032b-timezone-abbreviations-guc-and-verbose-offset.md`,
  `0125-0007-pg-faithful-date-field-decode.md` (the INPUT side of the same type)

## The gap

goopg rendered a `timestamp with time zone` exactly like a
`timestamp without time zone`: it printed the stored instant, in UTC, with no
zone marker and no regard for the session's `TimeZone` GUC.

```
-- PG 18.3                         -- goopg (before)
SET TimeZone='Asia/Kolkata';
SELECT a FROM t;                   SELECT a FROM t;
 2020-06-15 15:30:00+05:30          2020-06-15 10:00:00
```

The missing `+05:30` is not cosmetic. A client that reads that text back — and
every text-protocol client does — reads `2020-06-15 10:00:00` **in its session
zone**, i.e. 04:30 UTC. The value goopg returned therefore denoted a different
instant from the one it stored, and did so silently: no error, no warning, a
five-and-a-half-hour error in a plausible-looking timestamp.

Two behaviours are missing and neither is correct on its own:

1. **the conversion** — upstream converts the stored instant into the session
   `TimeZone` before formatting;
2. **the zone marker** — upstream then prints the zone.

Landing (1) without (2) would relabel the instant silently — strictly worse.
Landing (2) without (1) would print `+00` while the session says otherwise,
contradicting `SHOW TimeZone`. Upstream has them in one function
(`EncodeDateTime` with `print_tz=true`, reached from `timestamptz_out`), and so
does this change.

## What upstream actually does

`postgres/src/backend/utils/adt/datetime.c`, `EncodeDateTime` — the zone
spelling is **not** uniform across DateStyles, which is the part that is easy to
get wrong from memory:

| style | zone rendering |
|---|---|
| ISO (and XSD) | always the NUMERIC offset, jammed onto the seconds field, no space: `2020-06-15 03:00:00-07` |
| SQL, Postgres, German | the zone ABBREVIATION after a space: `06/15/2020 03:00:00 PDT`. Only when the zone has no abbreviation at all does it fall back to the numeric form — and only the Postgres arm gives that fallback a leading space (a 2001 comment: "to avoid formatting something which would be rejected by the date/time parser later") |

`EncodeTimezone` (same file) has three widths, all reachable from real tzdata:

- `HH` for a whole-hour offset — `+00`, `-08`
- `HH:MM` when there is a minute part — `+05:30` (Kolkata), `+05:45` (Kathmandu)
- `HH:MM:SS` when there is a second part — `+05:53:28`, Kolkata's local mean
  time, which any pre-1906 timestamptz in that zone renders with

Its sign test reads `tz <= 0 ? '+' : '-'` because upstream's `tz` counts seconds
WEST of UTC; goopg (like Go) counts east, so the condition inverts and a zero
offset takes the `+` branch (`+00`, never `-00`).

Finally, `" BC"` is appended **after** the zone, not after the seconds:
`0001-02-28 10:00:00+05:53:28 BC`.

## What landed

`internal/config/timestamptz_out.go`:

- `FormatTimestampTZ(t, style, order, zone)` — the `timestamptz_out` entry
  point. Converts through `sessionLocation(zone)`, formats the local wall clock,
  appends the zone per the table above, then the era marker.
- `encodeTimezone(offsetEast)` — the `EncodeTimezone` port, in east-of-UTC
  seconds (Go's `Time.Zone` convention).
- `sessionLocation` — `time.LoadLocation` behind a `sync.Map`, because output
  formatting runs once per cell per row and the uncached call parses a tzdata
  file.

`internal/config/datestyle.go` was split at the seam upstream already has:
`formatTimestampBody` returns everything before the zone plus the trailing era
marker separately, so `FormatTimestamp` (print_tz=false, unchanged behaviour)
joins them directly and `FormatTimestampTZ` splices the zone between them.

Both **output paths** were moved off the shared `case "timestamp", "timestamptz"`
onto separate arms — they are siblings and a divergence between them would mean
`COPY t TO STDOUT` and `SELECT * FROM t` disagreed about what the same stored
value is:

- `internal/server/dispatch.go` `appendTypedCellText` (wire output)
- `internal/executor/copy_text.go` `datumToCopyText` (COPY TEXT/CSV output),
  with `timeZone` threaded through `EncodeCopyTextRow` / `EncodeCopyCsvRow` /
  `RunCopyTo` beside the existing `dateStyle`/`dateOrder` pair

### The COPY path was reading no GUCs at all

Wiring the COPY half surfaced a second, pre-existing bug. `COPY … TO STDOUT`
issued as a standalone statement runs through `dispatchCopyViaExecutor`
(`internal/server/copy.go`), which builds its `executor.Context` by hand and
never attached the session GUC hooks — so `ctx.GetSetting` was nil and *every*
GUC lookup in `RunCopyTo` fell back to a boot default. `SET datestyle='German,
DMY'` had no effect on standalone COPY output either, though it worked for the
same statement inside a `\;`-joined batch (`runInlineCopy` borrows the batch's
already-wired context). The two are now wired the same way.

## Deliberately not in scope

The `::text` cast path (`formatTimeDatumDateStyle`, `internal/executor/expr.go`)
still renders a `timestamptz` without its zone. That path receives a bare
`Datum`, and goopg's `KindTime` carrier cannot tell `timestamp` from
`timestamptz`: `Datum.TimeSub` declares `TimeSubTimestampTZ` but no producer
sets it (the open half of the M0127-P5.9-u ledger row). The two paths fixed here
are the two that know the column's declared type. Adding a suffix off a guess
would mislabel plain `timestamp` output, which is the exact failure mode this
change exists to remove. Ledger row 2026-08-11.

Likewise, a `TimeZone` GUC set to a POSIX-style specification (`SET
TimeZone='+05:30'`, whose sign upstream inverts) is not resolvable by Go's
`time.LoadLocation` and falls back to UTC rather than erroring. Ledger row.

## Verification

`TestFormatTimestampTZAgainstPG18Oracle` (`internal/config`) pins 19 cells
captured live from a throwaway PostgreSQL 18.3 — every offset width, every
DateStyle, the BC-after-zone ordering, DST moving both clock and offset, and a
zone whose tzdata abbreviation is itself numeric (`+0545`) and is printed
verbatim in the abbreviation styles. `TestEncodeTimezoneWidths` exercises the
three widths and the zero-offset sign directly.
`TestFormatTimestampStillDropsTheZone` pins the other half of the split: a
refactor that routed plain `timestamp` through the tz path would relabel every
one of them.

`TestAppendTypedCellTextTimestampTZHonorsTimeZone` (`internal/server`) and
`TestEncodeCopyTextRowTimestampTZ` (`internal/executor`) are deliberate siblings
covering the two output paths, each also asserting that the `timestamp` column
in the same row does NOT move.

End-to-end on a live goopg (`SELECT`, `COPY … TO STDOUT`, `COPY … (FORMAT CSV)`
under UTC / Asia/Kolkata / America/Los_Angeles × ISO / German / Postgres): every
cell matches the PG 18.3 oracle.

Gates: `internal/config` + `internal/executor` + `internal/server` suites,
`TestPort_RegressSuite`, `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`, `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35).
