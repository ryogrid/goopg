# 0125-0007 — PG-faithful date/time field decode

Status: **landed 2026-07-30** (branch `tpcds-fix2`).
Milestone: M0125 (`docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`).
Measurement record: `analysis/m0125-0007/README.md`.

## 1. The defect

`d_date = '2002-5-01'` matched zero rows on goopg and raised nothing. PostgreSQL
matches the row.

Three TPC-DS queries were answering `0 / NULL / NULL` because of it — Q16, Q94
and Q95, whose date predicates are spelled `'2002-4-01'`, `'2002-5-01'` and
`'2001-4-01'` in the generated query set. The full 99-query SF0.5 gate
(`analysis/tpcds-sf05-full-gate-20260729/`) caught them only once M0124-0005 put
a value checksum on every cell: all three reported the *same* goopg checksum
`512b5fdab820c47b` against three *different* oracle checksums. One defect, three
queries — and before the checksum existed, the sweep had been recording Q16 as
`OK / 1 row` since chunk 2.

## 2. Why the two engines disagreed

PostgreSQL does not parse date/time text against a layout. `date_in` calls
`ParseDateTime()` to split the string into fields and then `DecodeDate()` →
`DecodeNumber()` (`postgres/src/backend/utils/adt/datetime.c`), which reads each
numeric run on its own with `strtoint`. Field *width* carries no meaning beyond
disambiguation, so `'2002-5-1'` and `'2002-05-01'` decode identically. Range
checking happens afterwards, in `ValidateDate()`.

goopg parsed with Go's `time.Parse` against fixed layouts. `"2006-01-02"` demands
exactly two digits for month and day; `"15:04:05"` demands two for minute and
second (the hour is the one lenient field). Every unpadded spelling was a parse
error.

That alone would have been a loud, ordinary compat gap. What made it a *wrong
answer* is where the parse sat. goopg types a bare string literal as `unknown`
and resolves the coercion at runtime — `compareDatum` → `promoteCrossKind` →
`tryParseStringAs` (`internal/executor/expr.go`), by design, see
`docs/design/root-0019-unknown-literal-coercion.md`. `tryParseStringAs` reports
a failed parse by returning the *string* unchanged, and the comparison of a
`KindTime` against a `KindString` is simply false. The cast path errored; the
comparison path returned the empty set.

Ten-odd call sites had the same fixed-layout assumption baked in, several of
them not as a layout but as a byte offset: `parseTimeString` and
`parseTimeTZString` detect a date prefix with `s[4] == '-' && s[7] == '-'`,
rewrite hour-24 via `s[:2]`, and probe for a leap second at `s[5]`.

## 3. What landed

### 3.1 One shared normaliser — `internal/pgdatetime`

`pgdatetime.NormalizeInput` rewrites the ISO-ordered numeric spellings
PostgreSQL accepts into the zero-padded form the Go layouts require, and returns
everything else byte-identical (modulo the surrounding-whitespace trim PG also
performs). It is idempotent, allocation-free on already-canonical input, and
performs **no range validation** — mirroring PG, which decodes first and
validates in `ValidateDate()`. `'2002-13-01'` normalises to itself and the
downstream parser still rejects it.

The one non-obvious rule is the width floor on the leading field: it must carry
at least three digits. That is `DecodeNumber`'s own test
(`if (flen >= 3 || DateOrder == DATEORDER_YMD)`) — the point at which PostgreSQL
stops guessing and commits to reading the field as a year. With one or two
digits the reading depends on the `DateStyle` GUC (`'02-5-1'` is 2001-02-05
under the default MDY order), which goopg does not model. Normalising those
would have converted a loud error into a silently-wrong date, which is the exact
failure this milestone item exists to remove. Month and day are capped at two
digits for the same reason: a three-digit second field is PostgreSQL's
day-of-year form, not a month.

Chosen as a normaliser in front of the existing layout tables rather than a
replacement decoder because the layout tables encode a large accumulated set of
accepted forms (named zones, AM/PM, verbose `February 22, 2022` output,
RFC 3339) whose wholesale rewrite is a much larger, riskier change than the
defect warrants. The normaliser is the shared PG-faithful *field* step; the
layouts remain the acceptance oracle.

### 3.2 Call sites routed through it

`internal/executor/expr.go`
: the `date` literal/cast case, the `timestamp`/`timestamptz` layout loop, and
  the `date` arm of `pg_input_is_valid` (which must agree with the cast path).

`internal/executor/copy_text.go`
: `parseTimeString`, `parseTimeTZString` and `parseCopyTimestamp`, normalising
  at function entry — before the fixed-offset probes described above. These three
  are the funnel for COPY TEXT input, the implicit date coercion in
  `codec.go`'s `date` encode arm, and `tryParseStringAs`, so the comparison path
  is fixed by the same edit that fixes COPY.

`internal/pgnodes` needed nothing: `parseDateFields` there already splits on
`-` and uses `strconv.Atoi`, so it was the *lenient* sibling all along. The two
siblings now agree.

### 3.3 A second silent wrong answer, found on the way

`tryParseStringAs` tried `parseTimeString` before `parseCopyTimestamp`.
`parseTimeString` strips a leading date and returns the bare time-of-day
anchored at 1970-01-01, and it succeeds on a full timestamp — so
`ts_col = '2002-05-01 03:04:05'` compared 2002-05-01T03:04:05 against
1970-01-01T03:04:05 and reported no match. Fully padded literal; nothing to do
with this item's title, same silent shape. Fixed by trying the timestamp decode
first when the (normalised) literal carries a date prefix, guarded by
`hasISODatePrefix` so an offset-bearing bare time still coerces to `timetz`
(the M0097-0004 ordering).

## 4. Verification

Unit: `internal/pgdatetime/normalize_test.go` (accepted forms, foreign spellings
left alone, no-validation, idempotence — every "want" checked against PG 18.3
output) and `internal/executor/date_unpadded_input_test.go` (the coercion,
timestamp-literal ordering, timetz preservation, and that foreign spellings
still fail loudly).

Gates: units suite PASS; `tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35);
regress-port quick set + the six datetime suites diffed against a HEAD-built
baseline binary — `1/52 PASS` on both and every per-test diff byte-identical
except a clock-dependent `uuidv7` test (Hard-won Rule #5).

TPC-DS acceptance at SF0.5: the predicted signature landed — the single goopg
checksum `512b5fdab820c47b` became three distinct ones, and `0 / NULL / NULL`
became real numbers on all three queries. It did **not** become the oracle's
three checksums, because a second defect sits behind each, and the probe says
which: Q16 and Q94 now *over*-count (63 vs 23, 7 vs 2), the
`EXISTS` + `NOT EXISTS` conjunction-grows-the-result shape already filed as
M0125-0008 — Q16 was not previously named there and now is. Q95 *under*-counts
(5 vs 23) and contains no `EXISTS`; it gates on two `ws_order_number IN
(subquery)` over a CTE, a different mechanism, filed separately as M0125-0023.
Full table in `analysis/m0125-0007/README.md`.

## 5. Still unimplemented (deferral ledger, 2026-07-30)

PostgreSQL date/time input forms goopg continues to reject, each unchanged by
this work and each recorded as its own ledger row:

* textual month names — `'2002-May-1'`, `'May 1, 2002'` (`DecodeSpecial`)
* `DateStyle`-dependent field orders — `'5-1-2002'`, `'02-5-1'`
* the run-together spelling `'20020501'` (`DecodeNumberField`)
* `/` separators — `'2002/5/1'`
* the 3-digit day-of-year field — `'2002-005-01'`
* 2-digit years, and the `BC` era suffix

And two behaviours that are wrong rather than merely absent:

* **A failed coercion is still silent.** `d_date = 'garbage'` returns no rows
  where PG raises 22007. The normaliser removed the *common* trigger; the
  mechanism is intact, and it covers `int_col = 'abc'` too. Making
  `promoteCrossKind` raise is a cross-cutting executor change that needs its own
  loop and its own regress-port pass.
* **The error-code split is wrong.** PG raises 22008 (`date/time field value out
  of range`) for `'2002-5-32'` / `'2002-13-1'` and 22007 only for a malformed
  string. goopg raises 22007 for both, because the range check is Go's layout
  parser rather than an equivalent of `ValidateDate()`.

## 6. Follow-up 2026-08-12 (M0119-0006) — the absent seconds field

`INSERT INTO t(ts) VALUES ('2020-01-01 10:00')` raised

```
22007: invalid input syntax for type timestamp: "2020-01-01 10:00"
```

while `timestamp '2020-01-01 10:00'` — the same text, one code path over —
parsed fine. PostgreSQL has no such split: `DecodeTime`
(`postgres/src/backend/utils/adt/datetime.c`) requires hour and minute and only
reads a seconds field `if (*cp == ':')`, leaving `tm_sec = 0` otherwise, so
`10:00` **is** `10:00:00` for `time`, `timetz`, `timestamp` and `timestamptz`
alike.

goopg's two tables disagreed about it: `evalTypedStringLit` (`expr.go`) lists a
`"2006-01-02 15:04"` layout, `parseCopyTimestamp` (`copy_text.go`) does not — and
`parseCopyTimestamp` is what the COPY TEXT reader, `encodeValuePG` and the array
element encoder all funnel through. The array-element slice
(`0119-0006-array-element-datetime-images.md`) made that visible in a second
place when it stopped storing element text verbatim: `'{2020-01-01 10:00}'::timestamp[]`
inherited the scalar column's rejection.

The fix supplies the missing field once, in `padTimeFields`
(`internal/pgdatetime/normalize.go`), for exactly the same reason the padding
lives there: adding a layout per table is how the two tables drifted apart.
`"10:00"` → `"10:00:00"`, `"10:00+05"` → `"10:00:00+05"`, `"10:00 PM"` →
`"10:00:00 PM"`, and the empty trailing field `"10:00:"` (which PG also accepts)
→ `"10:00:00"`.

Two spellings PG accepts are deliberately **not** rewritten, because a plausible
guess would be a wrong time rather than a loud error — the failure mode this
whole document exists for:

* `'10:00.5'` — PG answers `00:10:00.5`: a fractional field after the *second*
  numeric run makes `DecodeNumberField` read the pair as `MM:SS.f`, not as
  `HH:MM` plus fractional minutes. A seconds default would silently produce
  `10:00:00.5`.
* `'10::00'` — an empty **minute** field, not an empty seconds one (PG:
  `10:00:00`).

Both stay 22007 and are ledger rows for the day the real `DecodeTime` field walk
is ported. `'2020-01-01 10'` (a lone hour) is rejected by PG too and must stay
rejected.

Tests: `TestNormalizeInputPGAcceptedForms` gains the rewrite cases and
`TestNormalizeInputLeavesForeignSpellingsAlone` the two refusals;
`internal/executor/timestamp_secondless_input_test.go` pins the executor-visible
half (`parseCopyTimestamp`, `parseTimeString`, `parseTimeTZString`, and the
`timestamp[]` / `time[]` element round-trip), including a guard that the default
does not fire for the ambiguous forms. Every expected value was read from a PG
18.3 cluster (socket `/tmp`, port 5599).
