# 0097-0151 — `DateStyle` partial-`SET` merge semantics

Status: accepted
Milestone: M-NIGHTLY follow-up (source: `.ralph/deferral_ledger.md` 2026-07-14
row "M-NIGHTLY (run 20260714-011651 follow-up)")
Date: 2026-07-14

## Problem

`DateStyle` packs two independent components into one GUC string: a display
STYLE (`ISO`/`SQL`/`Postgres`/`German`) and a field ORDER (`YMD`/`DMY`/`MDY`),
e.g. `"ISO, MDY"`. A `SET datestyle = ...` is allowed to name only one
component — `SET datestyle = 'SQL'` changes just the style and must **keep**
the session's current order, matching `check_datestyle`
(`postgres/src/backend/commands/variable.c`).

Before this fix, `DateStyle` was a plain `TypeString` GUC: `canonicalize`
returned the input unchanged with no parsing at all. Consequences:

- `SET datestyle = 'SQL'` stored the literal string `"SQL"` — losing the order
  component entirely, so `SHOW datestyle` no longer even reported a valid
  order, breaking any code (or regress test) that expects the canonical
  `"<Style>, <Order>"` shape.
- `SET datestyle = 'ISO, SQL'` (two conflicting styles) or
  `SET datestyle = 'bogus'` (an unrecognized keyword) were silently accepted —
  PostgreSQL rejects both (`"Conflicting \"DateStyle\" specifications"` /
  `"Unrecognized key word"`).
- `GERMAN`'s implicit `DMY` order default (unless the same `SET` also names an
  order) was not implemented.

This is a real client-visible protocol/GUC-correctness bug, independent of
and narrower than the separately-tracked, much larger gap of actually
rendering date/timestamp *output* in the non-ISO styles (goopg's date/time
formatters are hardcoded to one literal layout regardless of the `DateStyle`
GUC — see the same 2026-07-14 ledger row's "corrected quick suite
classification" entry; that output-rendering project is intentionally **not**
touched here).

## Fix

`internal/config/datestyle.go` (new file):

- `parseDateStyleValue(s) (style, order string)` extracts the two components
  from an already-canonical `"<Style>, <Order>"` string (falls back to
  `ISO`/`MDY` for a component the string doesn't mention, so a malformed
  `current` never panics).
- `mergeDateStyle(current, bootVal, newValue) (string, error)` ports
  `check_datestyle` token-for-token: splits `newValue` on `,`, and each token
  sets either the style or the order, starting from `current`'s components
  (so an unspecified component survives). `GERMAN` also sets `DMY` unless the
  same call already saw an order token. `DEFAULT` recursively resolves
  against `bootVal`. A second token for an already-set component that
  disagrees is a conflict error; an unrecognized token is a keyword error.
  Returns the canonical `"<Style>, <Order>"` form, matching
  `assign_datestyle`'s `guc_malloc`'d result.

`internal/config/guc.go`:

- `Variable.canonicalize(value)` is now a thin wrapper over new
  `Variable.canonicalizeFrom(current, value)`, which special-cases
  `strings.EqualFold(v.Name, "DateStyle")` to call `mergeDateStyle` before
  falling through to the original by-`Type` switch for every other GUC.
  `canonicalize` passes `v.Value` as `current` — correct for every existing
  call site (`Variable.Set`, `Registry.ApplyReloadEntries`, `setFromFile`,
  `NewVariable`'s boot-value pass), where `v.Value` already *is* the current
  effective value.

`internal/config/session.go`:

- `SessionRegistry.Set` and `SessionRegistry.SetInternal` now fetch the
  session's **effective** current value via `s.Get(name)` first and call
  `v.canonicalizeFrom(current, value)` instead of `v.canonicalize(value)`.
  This distinction matters only for session/transaction-scoped GUCs: `v` is
  the shared global `*Variable`, so `v.Value` reflects the *global* default,
  not a prior `SET`/`SET LOCAL` override in this session. Merging a partial
  `SET datestyle = 'DMY'` against the stale global value instead of the
  session's actual current setting would silently discard an earlier
  `SET datestyle = 'SQL'` in the same session.

Every other GUC's `canonicalizeFrom` behavior is byte-for-byte identical to
the old `canonicalize` (the `current` parameter is ignored outside the
`DateStyle` branch), so this is a zero-behavior-change refactor for the rest
of the GUC table.

## Verification

- New `internal/config/datestyle_test.go`: partial-SET order preservation
  (`SQL` after a prior `DMY` keeps `DMY`), `GERMAN` implying `DMY` unless
  overridden, conflicting-spec rejection, unrecognized-keyword rejection,
  `DEFAULT` token merging against boot value, boot-value round-trip.
- Live end-to-end check against a real `cmd/goopg` binary via `psql`:
  `SET datestyle = 'SQL'` → `SHOW datestyle` = `SQL, MDY`; a subsequent
  `SET datestyle = 'DMY'` → `SQL, DMY` (order-only SET correctly preserved
  the just-set style); `SET datestyle = 'ISO, SQL'` →
  `ERROR: conflicting "datestyle" specifications`; `SET datestyle = 'nonsense'`
  → `ERROR: unrecognized key word: "nonsense"`.
- `go test ./internal/config/... ./internal/server/... ./internal/executor/...`
  all PASS (no regression in the shared `canonicalize` path used by every
  other GUC).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
  transactions, all 3 pgbench workloads).

## Scope / what's still open

This fix does not flip the `guc`/`date`/`timestamp`/`timestamptz`/`horology`
regress suites to `pass` — their remaining divergence is the separately
tracked date/timestamp *output rendering* gap (goopg's formatters ignore the
`DateStyle` GUC entirely; see the 2026-07-14 ledger row). A deferral-ledger
row records that as the next step. Also out of scope (pre-existing, unrelated
to `DateStyle`): `SET LOCAL <var> = ...` issued outside an explicit
transaction currently persists until the next `SET`/`RESET` in this session,
whereas real PostgreSQL scopes it to only the current implicit
single-statement transaction (visible in the same `guc` regress diff via
`vacuum_cost_delay`, not touched by this change).

## Follow-up (2026-07-14) — DATE output-rendering, scoped to DATE only

Picked up the "still open" item above for the DATE type specifically (TIMESTAMP/
TIMESTAMPTZ deferred — see the ledger row below; PostgreSQL's `Postgres` style
adds day-of-week/month-name formatting for timestamps that DATE's
`EncodeDateOnly` doesn't have, a separate, larger unit of work).

**Two independent bugs found and fixed**, both surfaced while tracing the four
call sites that render a DATE datum to text
(`postgres/src/backend/utils/adt/datetime.c`'s `EncodeDateOnly` is the
oracle):

1. **`internal/server/dispatch.go`'s `appendTypedCellText`** (the live
   `SELECT`-output path for both the simple- and extended-query protocols) had
   a `"date"` case hardcoded to `"2006-01-02"` (ISO) — `SET datestyle = 'SQL'`
   (or `Postgres`/`German`) correctly updated `SHOW datestyle` but had zero
   effect on `SELECT` results. Fixed by parsing the case's already-available
   `getSetting("datestyle")` value via the newly-exported
   `config.ParseDateStyleValue` and rendering with a new
   `config.FormatDate(t, style, order)` helper that mirrors `EncodeDateOnly`'s
   `SQL`/`Postgres`/`German`/`ISO` × `MDY`/`DMY` matrix (German is always
   `DD.MM.YYYY` regardless of order, matching upstream).
2. **`internal/executor/copy_text.go`'s `datumToCopyText`** had no `"date"`
   case at all — a bare `date`-typed column fell through the type-name switch
   into the `default:` branch's `d.Kind` switch, which only handles
   `String/Bytes/Int/Bool/Numeric`, not `KindTime` — so **`COPY <table> TO`
   (text or CSV format) hard-errored on any table with a `date` column**, a
   more severe bug than the DateStyle mismatch (found while tracing the
   output call sites, not something the original ledger row anticipated).
   Fixed by adding the missing case, also DateStyle-aware. Both
   `EncodeCopyTextRow` and `EncodeCopyCsvRow` (the latter reuses
   `datumToCopyText`) gained trailing `dateStyle, dateOrder string`
   parameters; `RunCopyTo` (`internal/executor/copy.go`) resolves them once
   from `ctx.GetSetting("datestyle")` (falling back to `"ISO", "MDY"` when
   `GetSetting` is nil, e.g. a bare `NewContext()`).

**Deliberately left untouched this loop** (see the deferral-ledger row):
`internal/executor/datum.go`'s `Datum.Format()`/`AppendValueText()` (used by
~20 call sites across `CAST`/`to_char`/`plpgsql`/EXPLAIN/error-message
formatting, not just DATE columns — a much larger blast radius requiring
either a new parameter threaded through every caller or a package-level
"current session" accessor) and `internal/wal/pgoutput.go`'s logical-
replication text encoding (has zero session/GUC access today; also has a
second, independent bug where its `"date"` case reuses the full
`"2006-01-02 15:04:05.000000"` timestamp layout instead of a date-only one).

### Verification

- New `internal/executor/copy_text_test.go` `TestEncodeCopyTextRowDate`: all
  4 styles × both orders round-trip through `EncodeCopyTextRow` to PG's exact
  text (previously this construction wasn't even encodable — hard error).
- New `internal/server/date_output_test.go`
  `TestAppendTypedCellTextDateHonorsDateStyle`: same style/order matrix
  through `appendTypedCellText`, plus a nil-`getSetting` case confirming the
  ISO/MDY boot-default fallback.
- Live end-to-end against a real `cmd/goopg` binary via `psql`: `SELECT d FROM
  dtest` after `SET datestyle='SQL'` → `07/14/2026`; `'Postgres, DMY'` →
  `14-07-2026`; `'German'` → `14.07.2026`; `COPY dtest TO STDOUT` (text and
  CSV) — previously an outright error — now emits `2026-07-14` /
  `2026-07-14` correctly under `ISO`.
- `go build ./...` clean; `go vet` clean on `internal/config`,
  `internal/executor`, `internal/server`; `go test
  ./internal/config/... ./internal/executor/... ./internal/server/...` all
  PASS (full packages, no regressions).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
  failed, all 3 pgbench workloads).

## Follow-up (2026-07-15): TIMESTAMP/TIMESTAMPTZ DateStyle output

Picked up the "DATE-only" resume point from the follow-up above: TIMESTAMP
and TIMESTAMPTZ output rendering was still entirely ISO-hardcoded (`SET
datestyle` had no effect on either), the same class of bug the DATE fix
closed. Added `config.FormatTimestamp(t, style, order)`
(`internal/config/datestyle.go`) mirroring PostgreSQL's `EncodeDateTime`
(`postgres/src/backend/utils/adt/datetime.c`) with `print_tz=false`: ISO →
`YYYY-MM-DD HH:MM:SS.ffffff`; SQL → `MM/DD/YYYY HH:MM:SS.ffffff` (or DD/MM
for DMY); Postgres → `Dow Mon DD HH:MM:SS.ffffff YYYY` (or `Dow DD Mon ...`
for DMY, using the 3-letter day/month abbreviations `EncodeDateTime` derives
from its `days[]`/`months[]` tables); German → `DD.MM.YYYY HH:MM:SS.ffffff`.

Wired into the same two call sites the DATE fix touched:
`internal/server/dispatch.go`'s `appendTypedCellText` gained a
`"timestamp", "timestamptz"` case (previously absent — both fell through to
the default, hardcoded-ISO `AppendValueText` branch); and
`internal/executor/copy_text.go`'s `datumToCopyText` `"timestamp",
"timestamptz"` case (which already existed but ignored its `dateStyle,
dateOrder` parameters entirely) now calls `config.FormatTimestamp`.

**Deliberately scoped out this loop**, matching the type's pre-existing
behavior so nothing regresses:

- **No session-timezone-aware conversion or offset for TIMESTAMPTZ.** Real
  PostgreSQL converts a stored UTC instant to the session's `TimeZone` GUC
  and appends an offset (e.g. `+00`) for TIMESTAMPTZ specifically (`print_tz
  =true` in `EncodeDateTime`). goopg has no such conversion anywhere today —
  TIMESTAMPTZ has always rendered identically to TIMESTAMP (raw stored UTC
  instant, no offset). This fix keeps that invariant; the timezone-plumbing
  gap is separate, larger, and unrelated to DateStyle.
- **Fractional seconds were originally always 6 digits, not PG's
  trim-trailing-zeros behavior** (`AppendSeconds` in `datetime.c` strips
  trailing zero fraction digits, and omits the decimal point entirely for a
  zero fsec) — fixed in the 2026-07-15 fractional-seconds follow-up below.
- `Datum.Format()`/`AppendValueText()`'s ~20-call-site DateStyle-unawareness
  (CAST/to_char/plpgsql/EXPLAIN/error-message paths) and `pgoutput.go`'s gap
  remain open, as recorded in the prior follow-up — TIMESTAMP joins DATE as
  a second type now diverging between the fixed SELECT/COPY paths and these
  still-ISO-only paths.

### Verification (2026-07-15 follow-up)

- New `internal/executor/copy_text_test.go` `TestEncodeCopyTextRowTimestamp`:
  4 styles × 2 orders through `EncodeCopyTextRow`.
- New `internal/server/date_output_test.go`
  `TestAppendTypedCellTextTimestampHonorsDateStyle`: same matrix through
  `appendTypedCellText`, for both `timestamp` and `timestamptz` type names,
  plus the nil-`getSetting` fallback case.
- Live end-to-end against a real `cmd/goopg` binary via `psql` (isolated data
  dir/port, cleaned up after): `SELECT t, tz FROM tsdt` for a
  `2026-07-14 09:05:03` value under `ISO` →
  `2026-07-14 09:05:03.000000`; `SQL, MDY` → `07/14/2026 09:05:03.000000`;
  `SQL, DMY` → `14/07/2026 09:05:03.000000`; `Postgres, MDY` →
  `Tue Jul 14 09:05:03.000000 2026`; `Postgres, DMY` →
  `Tue 14 Jul 09:05:03.000000 2026`; `German` →
  `14.07.2026 09:05:03.000000` — identical for both columns (confirming
  TIMESTAMPTZ's no-offset behavior is unchanged). `COPY tsdt TO STDOUT`
  (text and CSV) under `ISO`/`Postgres, MDY` emit the same styled text.
- `go build ./...` clean; `go vet` clean (`internal/config`,
  `internal/executor`, `internal/server`); `go test
  ./internal/config/... ./internal/executor/... ./internal/server/...` PASS
  (full packages, no regressions).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`: first
  run showed 1 transient failed TPC-B transaction (0.009%, unrelated schema
  — TPC-B has no date/timestamp columns); two immediate re-runs both PASS (0
  failed, all 3 workloads), confirming the flake was unrelated to this
  change.

### Follow-up (2026-07-15, fractional seconds) — `config.FormatTimestamp`
trim-trailing-zeros

Picked up the "Fractional seconds are always 6 digits" scope-out from the
row directly above. Added `fracSecondsSuffix(t time.Time) string`
(`internal/config/datestyle.go`) mirroring PostgreSQL's `AppendSeconds`
(`postgres/src/backend/utils/adt/datetime.c:458`): a zero fsec returns `""`
(no decimal point at all), a non-zero fsec returns `.` followed by only its
significant microsecond digits (goopg stores microsecond resolution,
matching `AppendTimestampSeconds`'s `MAX_TIMESTAMP_PRECISION=6` ceiling).
`FormatTimestamp` now formats the date+`HH:MM:SS` portion via
`time.Format` (fixed layouts with the `.000000` suffix removed) and appends
`fracSecondsSuffix` — same trim-then-append pattern
`appendTimeText`/`appendTimeOnlyValueText` already use for TIME/TIMETZ, so
all four now agree ([[pattern_sibling_paths_must_agree]]). `FormatDate` is
unaffected (DATE has no time-of-day component).

Both call sites wired to `config.FormatTimestamp` in the row above
(`internal/server/dispatch.go`'s `appendTypedCellText`,
`internal/executor/copy_text.go`'s `datumToCopyText`) picked up the fix for
free — no call-site changes needed.

Tests: `internal/config/datestyle_test.go`
`TestFormatTimestampFractionalSeconds` (zero/half-second/trailing-zeros/
full-microsecond cases against `FormatTimestamp` directly); updated the two
existing whole-second fixtures (`TestEncodeCopyTextRowTimestamp`,
`TestAppendTypedCellTextTimestampHonorsDateStyle`) to expect no `.000000`
suffix, and added `TestEncodeCopyTextRowTimestampFractionalSecondsTrimmed`
for the COPY path. Live `psql` verification against a real `cmd/goopg`
binary (isolated data dir, port 5535, cleaned up after): 3 rows
(`09:05:03`, `09:05:03.5`, `09:05:03.123456`) under `ISO, MDY` render as
`2026-07-14 09:05:03` / `...09:05:03.5` / `...09:05:03.123456` (previously
all three padded to `.000000`/`.500000`/`.123456`); `Postgres, DMY` renders
the same three fsec shapes with the day/month/year reordering; `COPY ...
TO STDOUT` matches the SELECT output.

Still open, unchanged by this loop: `Datum.Format()`/`AppendValueText()`'s
~20-call-site DateStyle-unawareness (also still hardcoded to fixed-6-digit
fractional seconds — a live sibling-path divergence from the two fixed call
sites, now on two axes instead of one), TIMESTAMPTZ's missing
timezone-aware conversion/offset, and `pgoutput.go`'s gap — all as recorded
in the prior follow-up.

Gates: `go build ./...` clean; `go vet` clean (`internal/config`,
`internal/executor`, `internal/server`); `go test
./internal/config/... ./internal/executor/... ./internal/server/...` PASS
(full packages, no regressions); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads).
