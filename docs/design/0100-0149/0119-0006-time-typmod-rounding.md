# M0119-0006 (62nd slice) — a `time(N)`/`timetz(N)` column rounds at INPUT, not truncates at output

Closes the three `AdjustTimeForTypmod` deferral rows filed by the 49th, 51st
and 55th slices (and the cast-path half of the 49th). The time-half of the
`Adjust*ForTypmod` cluster the working-set named as a remaining small slice; the
interval half (`AdjustIntervalForTypmod`) stays open — its column typmod rides
the *range* fields, which the literal/cast paths already model separately.

## The defect

Upstream `time_in`/`timetz_in` (`postgres/src/backend/utils/adt/date.c`) call
`AdjustTimeForTypmod(&result, typmod)` **before** the value is stored: the
precision `N` of a `time(N)`/`timetz(N)` column rounds the fractional seconds
half-away-from-zero to `10^(6-N)` microseconds. `AdjustTimeForTypmod` operates on
`TimeADT` (microseconds since midnight), so the carry is expressed naturally —
`'23:59:59.999999'::time(2)` is `24:00:00` (USECS_PER_DAY), because
`59.999999` rounds up through the second into the next day.

goopg did the opposite of every one of those words: it stored full microseconds
and **truncated** the fractional seconds at three output boundaries, in three
separate hand-maintained copies:

| boundary | where | what it did |
|---|---|---|
| `::time(N)` / `::timetz(N)` cast | `expr.go` `evalExpr` CastExpr arm | `ns = (ns/(factor*1000))*(factor*1000)` — truncate |
| `COPY … TO` (text + CSV) | `copy_text.go` `copyTimeOfDayMicros` | `(micros/scale)*scale` — truncate |
| `SELECT` DataRow | `server/dispatch.go` `appendTimeText` | `frac = frac[:prec]` — truncate |

Consequence, measured side by side on the same literal: `'23:59:59.999999'` into
`time(2)` is `24:00:00` on PG 18.3 and `23:59:59.99` on goopg — the stored value
differs, not just the display. Worse, the three truncators were only consistent
by coincidence (three copies of "precision = 6 decimal digits" that could drift,
the same class the 50th slice recorded for the `24:00:00` probe).

## The fix

One rounding function, one Datum wrapper, applied at every input site that holds
the precision, and the three output truncators deleted.

- `internal/pgdatetime/adjust_typmod.go` — `AdjustTimeForTypmod(micros int64,
  typmod int32) int64`, a literal port of `date.c:1710` (the `TimeScales`/
  `TimeOffsets` tables, half-away-from-zero, no-op for `typmod ∉ [0,6]`). It is
  pure over microseconds, so it sits beside `FormatTime` in the leaf package the
  two cross-package readers already share.
- `internal/executor/codec.go` — `roundTimeDatumToPrecision(d Datum, prec int64)
  Datum`: `pgTimeMicros` → `AdjustTimeForTypmod` → `pgTimeFromMicros`, preserving
  the `timetz` subtype's offset (`TimeSubTimeTZ` ⇒ `NewTimeTZDatum`, else
  `NewTimeDatum`). The `pgTimeMicros`/`pgTimeFromMicros` pair already carries the
  hour-24 rule from the 50th slice, so a rounded `24:00:00` round-trips for free.
- Input sites (round, then the value is stored pre-rounded):
  - the CastExpr `time`/`timetz` arm (`expr.go`) — replace the truncate block.
  - `copyTextToDatum` `time` + `timetz` arms (`copy_text.go`).
  - `copyBinaryToDatum` `time` + `timetz` arms (`copy_binary.go`).
  - `coerceRowForConstraintChecks` (`operators_storage.go`) — the INSERT/UPDATE
    coercion gains a `time` case and the `timetz` case applies the typmod; this is
    the one site that had no `time` case at all (a `time` column's value stayed
    `KindString` until `encodeValuePG` parsed it, so the column precision never
    reached it).
- Output sites (render the stored value verbatim):
  - `copyTimeOfDayMicros` (`copy_text.go`) — becomes `pgTimeMicros(tv)`, the
    `t`/`Args` truncation block deleted.
  - `appendTimeText` (`server/dispatch.go`) — the `prec` trim deleted; the
    trailing-zero strip stays (a pre-rounded value already holds at most `N`
    non-zero fractional digits, so stripping is now a pure no-op for correctness).

The literal/cast **bare** `time`/`timetz` (no `(N)`) is unaffected everywhere: a
missing `Args`/`Typmod` is the same no-op in `roundTimeDatumToPrecision` that it
was in the three truncators.

## Sibling audit (Hard-won Rule #2)

| path | verdict |
|---|---|
| `encodeValuePG` `time`/`timetz` arm | unchanged — it stores `pgTimeMicros` of whatever datum arrives; the datum is now pre-rounded by the input sites. Rounding twice is idempotent, so no double-round is possible even on a path that misses coercion. |
| `btree_scalar_keys.go` time/timetz keys | unchanged — derive from `pgTimeMicros`, which reads the rounded stored value. |
| `datumToCopyBinary` / `datumToCopyText` | unchanged — render the rounded value; their `24:00:00` handling is `pgTimeMicros`-based. |
| `interval` (AdjustIntervalForTypmod) | **not** in this slice — the literal/cast typmod is already correct (`applyIntervalCastTypmod`), the column typmod is a separate range-field concern (ledger row 2026-08-13, 55th slice). |

## Gates

`go build ./...` clean; `internal/pgdatetime` + `internal/executor` targeted
tests PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh`
RESULT=PASS (Q12=2, Q13=35 — the harness's `lineitem`/`orders` carry date/time
columns, but no `time(N)` typmod, so this is a regression guard not a tripwire);
pgbench smoke via the commit hook.

New guards (all verified red on the pre-fix source):
- `internal/pgdatetime/adjust_typmod_test.go` — oracle cells for
  `AdjustTimeForTypmod`: half-away-from-zero (`0.5` → up, `0.499` → down),
  the `23:59:59.999999 → 24:00:00` carry, `time(0)` whole-second rounding,
  no-op for typmod −1 and > 6.
- `internal/executor/time_typmod_rounding_test.go` — E2E through the four input
  sites against PG 18.3 cells: `INSERT`/`COPY TEXT`/`COPY BINARY`/`::time(2)`
  all store the rounded value, and `SELECT`/`COPY TO` render it without
  re-truncating.

## Deferred

- `AdjustIntervalForTypmod` for the `interval(N)` / `INTERVAL YEAR TO MONTH`
  **column** typmod — the 55th slice's row; the literal/cast half is already
  ported (`applyIntervalCastTypmod`), the column half needs the range-field
  truncation wired into the same four input sites.
- The `appendTimeText`/`pgTimeMicros`/`copyTimeOfDayMicros` three-copies of the
  time-of-day probe remain (50th slice's row) — this slice removes the *truncate*
  copies, not the *extract* copies.
