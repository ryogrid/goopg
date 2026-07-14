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

## Follow-up (2026-07-15): `CAST`-to-text DateStyle-awareness (`evalCast`)

Picked up the "investigate call-site reachability first" resume point from
the ledger row above item (b) — the ~20-call-site `Datum.Format()`/
`AppendValueText()` DateStyle-unawareness. Rather than touch those two
Datum methods directly (still deferred — see below), this slice targets the
single highest-traffic, most PG-visible divergence: `x::text`,
`CAST(x AS text)`, and the `text(x)`/`varchar(x)` function-call cast forms.
Before this fix `evalCast`'s `KindTime` branch under `case "text", "varchar",
"bpchar", "char"` (`internal/executor/expr.go`) called `d.Format()`
unconditionally, which hardcodes ISO for TIMESTAMP/TIMESTAMPTZ and
Postgres-style MDY for DATE (M0097-0063) regardless of `SET datestyle` — so
`SELECT ts::text` under `SET datestyle='SQL, DMY'` disagreed with plain
`SELECT ts` (already fixed by the two 2026-07-14/07-15 follow-ups above).

**Reachability finding:** `evalCast(d, targetType, pos)` had no `*Context`
parameter at all, and is called from only 6 production call sites — far
fewer than the ~20-site `Format()`/`AppendValueText()` figure, because most
of those 20 sites don't route through a type-declared CAST. Of the 6,
4 already had a `ctx`/`o.ctx` in scope at the call site (`expr.go`'s two
`Cast`-AST-node and type-function-call evaluation sites; `operators_ddl.go`'s
`ALTER COLUMN TYPE` row-rewrite path via `o.ctx`; `operators_project_set.go`'s
`unnest(...)::type` element-cast path via `ctx`) and 2 did not
(`operators_from_unnest.go`'s `coerceUnnestElem`, which by its own doc
comment only ever coerces the numeric/oid/bool family, never date/time;
`operators_storage.go`'s int2/int4/int8 INSERT-coercion switch, which never
reaches the `KindTime` branch). Added a `ctx *Context` parameter to both
`evalCast` and `evalCastTyped`, threading the real `ctx`/`o.ctx` at the 4
reachable sites and `nil` at the 2 unreachable ones (nil falls back to
ISO/MDY, matching the pre-existing hardcoded behavior there — zero
observable change for those 2 sites).

New `dateStyleFromCtx(ctx *Context) (style, order string)` helper
(`internal/executor/expr.go`) mirrors the same `ctx.GetSetting("datestyle")`
+ `config.ParseDateStyleValue` resolution `dispatch.go`'s
`appendTypedCellText` already performs, defaulting ISO/MDY when `ctx` is nil
or the GUC is unset — kept identical across all three call sites
([[pattern_sibling_paths_must_agree]]). New `formatTimeDatumForCast(d Datum,
style, order string) string` mirrors `Datum.Format()`'s `KindTime` branching
(±infinity sentinels, then `flagDate` to distinguish DATE from TIMESTAMP/
TIMESTAMPTZ) but dispatches through `config.FormatDate`/`config.FormatTimestamp`
with the resolved style/order instead of `Format()`'s hardcoded layouts —
this incidentally also fixes the CAST path's DATE branch, which inherited
`Format()`'s Postgres-style-MDY-only hardcoding even under ISO/SQL/German.
The pre-existing `isTimeOnlyValue` short-circuit (TIME cast to text) is
untouched — TIME/TIMETZ have no DateStyle dependency.

Tests: `internal/executor/cast_datestyle_test.go`
`TestEvalCastTimeToTextHonorsDateStyle` (DATE × 4 styles, TIMESTAMP × 3
styles, table-driven against a `ctx.GetSetting`-stubbed `*Context`);
`TestEvalCastTimeToTextNilCtxDefaultsISO` (nil-ctx fallback, pinning the
2 unreachable call sites' unchanged behavior). Updated the two existing
direct `evalCast`/`evalCastTyped` test callers
(`char_oid18_truncation_test.go`, `tid_cast_test.go`) to pass `nil` for the
new parameter — pre-existing non-time assertions unchanged.

Live `psql` verification against a real `cmd/goopg` binary (isolated data
dir, port 5536, cleaned up after): a `dsx(id int, d date, ts timestamp,
tz timestamptz)` table with one populated row and one all-NULL row;
`SELECT d::text, ts::text, tz::text` under `ISO, MDY` → `2026-07-14` /
`2026-07-14 09:05:03` / `2026-07-14 09:05:03`; `SQL, DMY` → `14/07/2026` /
`14/07/2026 09:05:03` / same; `Postgres, DMY` → `14-07-2026` / `Tue 14 Jul
09:05:03 2026` / same; `German` → `14.07.2026` / `14.07.2026 09:05:03`;
`CAST(d AS text)`/`CAST(ts AS text)` and `text(d)`/`text(ts)` (function-call
cast form) both agree with the `::text` form under `German`; the all-NULL
row's `d::text, ts::text` correctly render empty (NULL passthrough,
untouched by this fix — `evalCast` returns `NullDatum` before reaching the
`KindTime` branch).

Still entirely unimplemented: `Datum.Format()`/`AppendValueText()` remain
DateStyle-unaware at their ~20 direct call sites (`to_char`'s fallback
formatting, plpgsql `RAISE`/string concatenation, `EXPLAIN`, error messages,
`operators_fk.go`/`operators_analyze.go`'s bound-rendering, array-element
`Format()` in `expr.go`'s `array_to_string`/`||`, etc.) — those still emit
fixed-ISO-style TIMESTAMP / hardcoded-Postgres-MDY DATE text regardless of
`SET datestyle`, now diverging from the CAST/SELECT/COPY paths on the
DateStyle-style axis. TIMESTAMPTZ's missing timezone-aware conversion/offset
and `pgoutput.go`'s gap remain open, as recorded in the prior follow-ups.

Gates: `go build ./...` clean; `go vet ./...` clean; `go test
./internal/executor/...` PASS (full package, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
bash scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads).

## Follow-up (2026-07-15): FK violation `DETAIL` line DateStyle-awareness

Picked up the "audit the ~20 `Format()`/`AppendValueText()` call sites for
`ctx` reachability" resume point, targeting `operators_fk.go`'s
`fkValsForDetail(vals []Datum) string` — the helper that renders the
`Key (col)=(val) is not present in table "X".` /
`Key (col)=(val) is still referenced from table "Y".` DETAIL line PostgreSQL
attaches to every `23503` foreign-key-violation error. Before this fix it
called `v.Format()` unconditionally, which hardcodes ISO for TIMESTAMP and
Postgres-style MDY for DATE regardless of `SET datestyle`.

Renamed the CAST follow-up's `formatTimeDatumForCast` helper to
`formatTimeDatumDateStyle` (it was never actually CAST-specific — just the
name implied it) and reused it directly rather than growing a parallel
helper, per [[pattern_sibling_paths_must_agree]]. `fkValsForDetail` gained a
`ctx *Context` parameter; all 4 production call sites
(`assertParentExists` ×2 via `checkFKInsert`/`enforceFKOnDelete`'s reverse
check, `assertNoChildRows`, `detachPartitionFKRefCheck`) already had `ctx`
in scope, so no call site is left passing `nil`. New
`TestFKValsForDetailHonorsDateStyle` (`operators_fk_test.go`) pins German
style DATE rendering plus the nil-ctx ISO/MDY fallback.

**Live-verification finding — a real, deeper, separate gap discovered by
this probe (not fixed here):** live `psql` testing against a real
`cmd/goopg` binary showed the fix works correctly for the DELETE/UPDATE
parent-side check (`assertNoChildRows`) and the partition-detach check
(`detachPartitionFKRefCheck`) — both operate on `Datum`s decoded from an
already-stored heap row, which carry a proper `KindTime`/`flagDate`-tagged
value (confirmed via a temporary debug probe: `Kind=5 Flags=2`), so
`SET datestyle='German'; DELETE FROM parent WHERE d = '2026-07-14'` now
correctly renders `DETAIL: Key (d)=(14.07.2026) is still referenced from
table "child".`

But the INSERT-side check (`assertParentExists` via `checkFKInsert`) does
**not** benefit: the same live probe showed `vals[0].Kind=3` (`KindString`)
there, i.e. the row value is still the raw VALUES-clause literal text, not
yet coerced to `KindTime`. `operators_storage.go`'s `insertOp.Next` only
explicitly coerces `int2`/`int4`/`int8` columns before the FK/CHECK/domain
constraint checks run (the "Integer range enforcement" block, ~line 1900);
DATE/TIMESTAMP/TIMESTAMPTZ (and NUMERIC) columns are left as whatever `Kind`
the source expression produced, and only become properly typed later, at
storage encode. So `INSERT INTO child VALUES ('2026-01-02')` under
`SET datestyle='German'` still renders `DETAIL: Key (d)=(2026-01-02) is not
present...` — the raw literal, unreformatted — because `fkValsForDetail`'s
`v.Kind == KindTime` branch never fires; it silently falls through to
`v.Format()` on the KindString, which happens to just echo the literal back
verbatim. Since the literal a user types is usually already close to
DateStyle-consistent this is easy to miss in casual testing, but it means
CHECK constraints, domain CHECK/NOT NULL, and FK checks all evaluate against
un-coerced DATE/TIMESTAMP/NUMERIC literals during INSERT — a correctness gap
wider than DateStyle alone (e.g. a CHECK constraint comparing a date column
against another date could behave differently pre- vs post-coercion for
edge cases like `'today'`/`'now'` special values). Deferral-ledger row
appended with the resume point: extend `insertOp.Next`'s existing
int2/int4/int8 coercion loop to also cover `date`/`timestamp`/`timestamptz`
(and audit `numeric`) via the same `evalCast(row[i], typeName, pos, ctx)`
pattern already used for the integer types — this is INSERT-path-only;
UPDATE's coercion (if any) and the `updateOp`/`upsertOp` siblings need the
same audit per [[pattern_sibling_paths_must_agree]].

Gates: `go build ./...` clean; `go vet ./...` clean; `go test -count=1
./internal/executor/...` PASS (full package, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
bash scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads).

## Follow-up (2026-07-15): INSERT-side literal coercion (`insertOp.Next`)

Picked up the previous section's resume point directly: extended
`operators_storage.go`'s `insertOp.Next` "Integer range enforcement" switch
(renamed in-place to "Type coercion", ~line 1903) to also coerce `date`,
`timestamp`, `timestamptz`, and `numeric`/`decimal` columns via
`evalCast(row[i], typeName, pos, ctx)` — the exact pattern already used for
`int2`/`int4`/`int8`. This runs before BEFORE-INSERT triggers, NOT NULL, and
CHECK/domain/FK constraint checks, so all of them now see a properly typed
value instead of the raw VALUES-clause literal text for these 4 additional
type families. Because this coercion loop is shared with the `INSERT ...
ON CONFLICT` upsert candidate-row path (`autoGenerateSerialValues`'s
neighboring comment already documents this sharing — root-0020), the same
fix also covers upsert's INSERT-branch candidate row for free; no separate
`upsertOp` change was needed for that half.

**Sibling bug found and fixed by the same live-verification pass
([[pattern_sibling_paths_must_agree]]):** live `psql` testing showed the new
INSERT-side FK DETAIL line correctly picked up German DateStyle ordering
(`15.07.2026`) but with a spurious `00:00:00` time-of-day suffix — i.e. it
was rendering as a TIMESTAMP, not a DATE. Root cause: `evalCast`'s `"date"`
case (`expr.go` ~3440-3463) built its result via `NewTimeDatum(...)`
instead of `NewDateDatum(...)` in both branches (string-source and
`KindTime`-source), leaving `flagDate` unset. `datum.go`'s `NewDateDatum`
doc comment is explicit that this flag must be set "at every date-producing
site" so type-agnostic renderers (`Datum.Format()`, and now
`fkValsForDetail`'s `formatTimeDatumDateStyle`) can distinguish a DATE from
a TIMESTAMP sharing the same `KindTime` carrier — `codec.go`'s
`DecodeValuePG` `"date"` case already follows this convention on the
storage-decode side. Ordinary `SELECT '...'::date` output was unaffected
by this bug because the SELECT-output dispatch path derives DATE-vs-
TIMESTAMP formatting from the column/expression's declared type, not from
the Datum's own flag — `fkValsForDetail` is one of the few call sites that
*only* has the bare `[]Datum` slice (no column-type context) and must rely
on the flag, which is exactly why the bug was invisible until this loop's
`evalCast`-driven INSERT-path fix started feeding it fresh, evalCast-
produced Datums. Fixed by switching both branches to `NewDateDatum`.

New tests: `TestEvalCastToDateSetsFlagDate` (`cast_datestyle_test.go`,
string- and `KindTime`-source), `TestInsertCoercesDateLiteralBeforeFKCheck`
and `TestInsertCoercesNumericLiteralBeforeCheckConstraint`
(`insert_fk_datestyle_coerce_test.go`, full parse→plan→exec integration via
the `newVMFixture`/`runDDL` harness, asserting both the German-DateStyle
DETAIL rendering and the absence of the spurious time suffix).

**Still open (deferred, not fixed this loop):** `UPDATE ... SET`'s new-row
construction has the same un-coerced-literal gap as INSERT did, and it is
*not* shared with this fix — `updateOp` builds `newRow[i]` straight from
`evalExpr(o.plan.Set[i], row, o.ctx)` (see `updateViaIndex`'s per-row
callback ~operators_storage.go:3833, the seq-scan path in `updateOp.Next`
~4448/4579, and `updateWithFrom` ~4808/6062-ish) with no column-type-aware
coercion step at all, for *any* type (not just date/timestamp/numeric — the
integer range-check gap from the original DateStyle bullet is present in
UPDATE too, pre-existing and wider than DateStyle). Confirmed live: `SET
datestyle='German'; UPDATE parent SET d = d WHERE ...` was not probed this
loop, but by the same code-reading logic a `CHECK`/FK-violating `UPDATE ...
SET d = '<literal>'` would hit the identical un-coerced-Datum problem
`assertParentExists` had, since `recheckChildFKs`/the CHECK-constraint call
in `updateViaIndex` (~operators_storage.go:3904) run directly against
`pu.newRow`. Resume point: audit whether to (a) factor the INSERT coercion
switch into a shared `coerceRowForConstraintChecks(cols, row, ctx, pos)`
helper and call it from both `insertOp.Next` and every `updateOp` new-row
construction site listed above, or (b) special-case just the SET-expression
evaluation to wrap bare literal SET targets in an implicit CAST at plan
time (`applyUpdateAssign`, `planner.go` ~8352-8361) — the latter would also
fix the pre-existing (non-DateStyle) integer range-check gap in one shot.
Deferral-ledger row appended below.

Gates: `go build ./...` clean; `go vet ./internal/executor/...` clean; `go
test -count=1 ./internal/executor/...` PASS (full package, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33, re-run after the flagDate
fix); `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`
PASS (0 failed, all 3 workloads, re-run after the flagDate fix). Live
`psql` verification: DATE and TIMESTAMP INSERT-side FK violations under
`SET datestyle='German'` both render correctly
(`Key (d)=(15.07.2026) is not present...`,
`Key (t)=(15.07.2026 11:00:00) is not present...`); a CHECK-constraint
violation and an invalid numeric literal on a `numeric` column both still
raise the correct `23514`/`22P02` codes; an `int4[]` array column round-
trips unaffected (confirms the pre-existing `col.Type.IsArray` guard still
protects array columns from this widened coercion switch).

## Follow-up (2026-07-15): UPDATE-side literal coercion (resume-point option (a))

Picked up the previous section's resume point, choosing option (a): a shared
`coerceRowForConstraintChecks(cols []catalog.Column, row Row, include func(i
int) bool, ctx *Context, pos int) error` helper (`operators_storage.go`,
placed just above `insertOp.Next`) now holds the exact coercion switch
`insertOp.Next` used to inline (int2/int4/int8 range-check +
date/timestamp/timestamptz/numeric literal typing), parameterized by an
`include(i)` predicate so callers control *which* columns get re-coerced.
`insertOp.Next` calls it with `include = !insertMissing[i]` (unchanged
behavior — a byte-for-byte refactor, not a behavior change, verified by the
existing INSERT tests still passing unmodified).

Every UPDATE new-row construction site now calls the same helper immediately
after its per-row SET-evaluation loop (before `computeGeneratedColumns`),
with `include = o.plan.Set[i] != nil` — i.e. only columns this statement's
own `SET` clause actually assigns get re-coerced; an untouched column's
value came straight from storage and is already correctly typed, so
re-running it through `evalCast` would be redundant work (and, for a
domain/enum type without a well-defined self-cast, could even be *wrong*).
Each of `updateViaIndex`, `updateOp.Next` (its SeqScan main loop — both the
non-inheritance-child and the inheritance-child-remap branches — plus its
*two* separate EPQ-retry rebinds, one in the Phase-1 collect loop and one in
the Phase-2 write loop discovered while auditing this function; the resume
point's own line-number guess only counted one), and `updateWithFrom` (main
path + EPQ retry) picked up its own `setCol := func(i int) bool { ... }`
closure defined once per operator invocation (not re-allocated per row) and
now calls `coerceRowForConstraintChecks` right after building
`newRow`/`parentNewRow`/`pu.newRow`, in whichever column-ordinal space that
site's own row is in (`cols` for the inheritance-child branch's
parent-space row, `captureCols`/`puCols` for the base-table/partition-child
row, `tgtCols` for `updateWithFrom`) — matching the same ordinal space the
`o.plan.Set` slice is already keyed against at that call site, so no new
remapping logic was needed. This closes both halves of the deferral-ledger
gap in one pass: the DateStyle-rendering gap (an UPDATE-triggered FK/CHECK
violation on a DATE/TIMESTAMP/NUMERIC column now renders its DETAIL line the
same way INSERT does) and the pre-existing, non-DateStyle int2/int4/int8
range-check gap UPDATE never had at all.

New tests (`update_fk_datestyle_coerce_test.go`):
`TestUpdateCoercesDateLiteralBeforeFKCheck` (mirrors
`TestInsertCoercesDateLiteralBeforeFKCheck`, but via an indexed-PK `UPDATE`
so it exercises `updateViaIndex` specifically — confirmed non-vacuous via
`git stash`, fails with the un-reformatted literal on the pre-fix tree),
`TestUpdateCoercesNumericLiteralBeforeCheckConstraint` (same non-vacuousness
check), and `TestUpdateCoercesInt4RangeOverflow` — the last one turned out
to *already* pass on the pre-fix tree (the heap encoder independently
range-checks a fixed-width `int4` column at write time, so the "silent
overflow" the ledger worried about doesn't reproduce that way), kept anyway
as a regression guard that both layers keep agreeing after this change.

Gates: `go build ./...` clean; `go vet ./...` clean (repo-wide, not just
`internal/executor`); `go test -count=1 ./internal/executor/...` PASS (full
package, no regressions); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
failed transactions, all 3 pgbench workloads).

Deferral-ledger row for this gap flipped to `resolved`; no new gap opened by
this loop (both resume-point options from the prior row were considered —
plan-time CAST-wrapping (b) was passed over in favor of the runtime
coercion (a) specifically to keep byte-for-byte behavioral parity with the
already-shipped, already-tested INSERT path rather than introduce a second,
differently-shaped fix for the same underlying problem).
