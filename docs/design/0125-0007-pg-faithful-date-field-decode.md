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

## 7. Follow-up 2026-08-12 (M0119-0006) — the ISO 8601 `T` separator and the `Z` zone

The 29th M0119-0006 slice, and the third consecutive one to find goopg's two
timestamp layout tables disagreeing with each other. This one closes that
recurrence structurally rather than case by case.

### The defect

`'2020-01-01T10:00:00'` — plain ISO 8601, the spelling every JSON encoder,
`date -Is` and ORM emits — raised `22007` on goopg. So did `2020-01-01t10:00:00`
(PG's field splitter is case-blind), `2020-01-01 10:00:00Z`, `…z`, `… Z`, and
every `T`-separated form carrying an offset. PG 18.3 accepts all of them: they
are the same instant.

Two further gaps fell out of the same root cause and are fixed here: an offset
wider than two digits (`+0530`, `+05:30`) was rejected on *both* separators,
because the only offset layout anywhere was `-07`.

### Why

PostgreSQL never matches a layout. `ParseDateTime()`
(`postgres/src/backend/utils/adt/datetime.c`) splits the input into fields —
treating the ISO 8601 `T` as an ordinary field break, in either case — and
`DecodeDateTime()` resolves the zone token against `datetbl`, where `Z` is a DTZ
entry for UTC, found case-insensitively and whether or not it arrives as its own
whitespace-delimited field.

goopg had two hand-written Go layout tables standing in for that machinery:
`parseCopyTimestamp` (`copy_text.go` — the COPY TEXT reader, the codec's value
encoder, the array element encoder, and the cross-kind comparison coercion) and
the list inside `evalTypedStringLit` (`expr.go` — typed literals). Between them
they covered the space separator with an optional `-07`, plus Go's `RFC3339` /
`RFC3339Nano` constants. Those two constants are the whole trap: they demand the
`T` **and** a zone, so `2020-01-01T10:00:00` matched neither the space layouts
(wrong separator) nor RFC3339 (no zone), and fell through to `invalid timestamp`.

### What landed

Two changes, deliberately split by which model each belongs to:

1. **One shared layout table.** `pgTimestampLayouts` (`copy_text.go`) is now the
   single table both paths iterate. It enumerates the separator × offset-width
   grid using Go's `Z07` / `Z0700` / `Z07:00` elements — each of which matches a
   literal `Z` as well as its numeric offset — with the zone-bearing layouts
   first so an explicit offset is honoured before the zone-less fallbacks treat
   the wall clock as UTC. No fractional-seconds layout is needed: Go's parser
   accepts a fraction after the seconds field even when the layout omits it.
   The RFC3339 constants are gone; they were never the right shape.
2. **Case and spacing folded upstream**, in `pgdatetime.NormalizeInput`, next to
   the field padding and for the same reason — a layout per spelling is exactly
   how the tables drifted. The separator scan now also breaks on lowercase `t`
   and emits canonical `T`; a space separator stays a space, since other call
   sites still carry space-only layouts. `canonicalZulu` folds a trailing
   `Z`/`z`, with any whitespace before it, onto an attached uppercase `Z`.

`canonicalZulu` is narrow on purpose: the letter must be last, and what remains
after dropping it and any trailing space must end in a **digit**. That keeps it
off timezone abbreviations that merely end in the same letter — `'10:00:00 NZ'`
is New Zealand time to PG, and folding it to UTC would be a silent 12-hour
error, the exact failure mode this document exists for.

### Still unimplemented (ledger rows, 2026-08-12)

* `'2020-01-01Z'` — PG accepts a zone on a bare **date** (`2020-01-01 00:00:00`).
  goopg's date token scan does not treat `Z` as a field break, so this stays
  22007.
* Timezone **abbreviations** (`NZ`, `EST`, `PDT`) — PG resolves them through
  `datetbl`/`pg_timezone_abbrevs`; goopg has no such table, so only numeric
  offsets and `Z` are understood.

`'2020-01-01T'` (dangling separator) and `'…+05:3'` (truncated offset) are
rejected by PG too and must stay rejected.

### Verification

`internal/executor/timestamp_iso8601_tz_input_test.go`: the accepted-forms table
(20 spellings, every `want` read from a PG 18.3 cluster on socket `/tmp` port
5599 under `set timezone='UTC'`), the `timestamp[]` element round-trip, and a
mutation guard pinning the four refusals above.
`TestTimestampLiteralAndCopyPathsAgree` is the structural one: it asserts the
literal path and the COPY path **agree** — same instant, or both erroring — for
every form, so widening only one table fails the build. `pgdatetime` gains
`TestNormalizeInputFoldsSeparatorAndZuluCase` and
`TestNormalizeInputZuluFoldIsNarrow`.

Mutation-checked in both halves: reverting `normalize.go` alone and removing the
`T` layouts alone each reproduce the failures. Gates: units, `TestPort_RegressSuite`
(249 s), `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35), pgbench smoke via the hook.

## 8. Follow-up 2026-08-12 (M0119-0006) — the BC era, and the nanosecond carrier underneath it

### The defect probed

PostgreSQL's date/time input carries an **era**: `'2020-01-01 BC'` is an
ordinary date, spelled with the `ADBC` token `DecodeDateTime` resolves out of
`datetbl` (`postgres/src/backend/utils/adt/datetime.c`). goopg raised `22007`
for every spelling of it — `'0001-01-01 BC'`, `'2020-01-01bc'` (PG's field
splitter does not need the space), `'2020-01-01 AD'` — which is the item the
25th slice's ledger row left open.

### What the probe found underneath

Teaching the input path to read an era exposed a far larger defect, and it was
producing **wrong answers rather than errors**. goopg's `KindTime` Datum stores
an int64 count of **nanoseconds** since 1970 (`datum.go`: `NewTimeDatum` /
`TimeValue`) — Go's `UnixNano` domain, which spans only 1677-09-21 ..
2262-04-11. PostgreSQL's `timestamp` is an int64 count of **microseconds** since
2000-01-01 (4713 BC .. 294276 AD) and its `date` an int32 day count over the
same span. Outside the narrower domain `UnixNano` overflows int64 and Go leaves
the wrapped value in place, so goopg answered ordinary historical input with a
plausible-looking different date and no diagnostic (measured at HEAD before this
slice, against PG 18.3 on socket `/tmp` port 5599):

| input | goopg | PG 18.3 |
|---|---|---|
| `'1000-01-01'::date` | `2169-02-08` | `1000-01-01` |
| `'1600-01-01'::date` | `2184-07-20` | `1600-01-01` |
| `'2300-01-01'::date` | `1715-06-13` | `2300-01-01` |
| `'0000-01-01'::date` | `1753-08-29` | `22008` (there is no year zero) |
| `'1000-01-01 12:00:00'::timestamp` | `2169-02-09 11:09:07.419103` | `1000-01-01 12:00:00` |

This is the M0125-0007 failure mode exactly: a silently wrong answer from input
PostgreSQL accepts, with nothing in the log.

### What landed

**The era model** (`internal/pgdatetime/era.go`, a leaf package so the pgoutput
and array decoders can share it):

- `SplitEra` strips the trailing `AD`/`BC` token, case-insensitively and with
  the whitespace optional, before any layout sees the string. It is as narrow as
  `canonicalZulu`: what precedes the token must end in a **digit**, so a doubled
  era (`'2020-01-01 BC BC'`), a bare `'BC'` and `'B.C.'` (PG reads that as a
  timezone name) are left for the parser to reject, as upstream does. Only a
  trailing token is recognised — PG rejects a leading `'BC 2020-01-01'` too.
- `ApplyEra` converts the era-relative year to the astronomical year goopg
  stores (`1 BC` is year 0, `2 BC` is year -1 — `date2j`/`j2date`'s own
  convention) and enforces the one range rule the era model owns: **there is no
  year zero** in either era, which upstream raises from `ValidateDate()` as
  `22008`, not as a syntax error.
- `EraYear` is the output-side inverse, tested as such so the two cannot drift.

**One entry point per domain** (`internal/executor/copy_text.go`). The previous
three slices each found goopg's two timestamp tables disagreeing and shared one
more thing between them; sharing the *table* was still not enough, because each
caller ran its own pre- and post-processing around it. `parsePGTimestampText`
(era split → `NormalizeInput` → the shared layouts → `ApplyEra` → range check)
and its calendar-only sibling `parsePGDateText` are now the entry points behind
the typed-literal path, the cast path, the COPY reader and `pg_input_is_valid`.

**Era-aware output** (`internal/config/datestyle.go`). `eraDisplay` rewrites
only the YEAR field and returns the trailing marker, so every DateStyle prints
what `EncodeDateOnly`/`EncodeDateTime` print (`-(tm_year - 1)` digits plus a
trailing `" BC"` after the time of day). The `Postgres` style keeps formatting
its **weekday** off the original instant: 15 June 1 BC is a Thursday and 15 June
AD 1 is not, so formatting the whole value off a year-substituted copy would
print the wrong day name there and nowhere else.

**The wrap is now loud.** Until the carrier moves to microseconds, every text
-input entry point refuses what it cannot represent with `22008` naming the
supported range, instead of storing a wrapped date. `dateTimeInputError` also
separates the two SQLSTATEs upstream distinguishes and goopg had merged: `22008`
(field/range) vs `22007` (syntax).

### Deliberately deferred (ledger row, same date)

The carrier itself. The honest consequence of the guard is an **acceptance
regression**: `'1000-01-01'` and `'2300-01-01'` are valid PG input that goopg
now rejects rather than answering wrongly, and a BC date can be *read* but still
not *stored*. Two in-tree tests asserted against out-of-range literals
(`date_infinity_literal_test.go`, `interval_subday_test.go` compared the
±infinity sentinels to `'9999-12-31'` / `'0001-01-01'`) and were moved onto the
representable extremes with the reason recorded inline. The fix is to move
`Datum.Int` for `KindTime` from nanoseconds to **microseconds** — PG's own unit,
whose int64 range (±292,471 years) covers the whole PG span — which is a
carrier-wide change (storage codec, wire binary, sort/hash, spill) and needs its
own loop and the full gate battery, not a tail-end edit here.

### Verification

`internal/pgdatetime/era_test.go` (`SplitEra` table incl. the four non-era
spellings, `ApplyEra` year math + both year-zero refusals, and `EraYear` proven
to be `ApplyEra`'s inverse); `internal/config/datestyle_era_test.go` (BC
rendering in all four DateStyles for date and timestamp, the `Postgres`-style
weekday, and the AD boundary at year 1); `internal/executor/
date_era_and_range_input_test.go` (the wrap table above now refused, year zero,
the `22008`/`22007` split, the era-split mutation guard, the representable range
untouched, and `TestDateEraLiteralAndCopyPathsAgree` — the sibling guard, which
asserts agreement so the carrier fix must widen both paths together). Every
`want` read from PG 18.3 on socket `/tmp` port 5599.

Gates: units, `TestPort_RegressSuite` (632 s), `scripts/tpch-spotcheck.sh`,
pgbench smoke via the commit hook.

## 9. Follow-up 2026-08-11 (M0119-0006) — the time-of-day FIELD ROLES

The eight sections above taught the date half of the input path to decode
fields instead of matching layouts. The time half was never converted: it still
ran a list of Go layouts (`"15:04:05"`, `"15:04"`, six fractional variants) in
`parseTimeString`. PostgreSQL has no such list — `DecodeTimeCommon` and the time
arms of `DecodeNumberField` (`postgres/src/backend/utils/adt/datetime.c`) assign
a ROLE to each numeric run after the fact, so the meaning of a field depends on
how many fields there are, whether one carries a fraction, and whether a
meridiem follows.

### 9.1 What PG accepts and goopg did not

Verified against PG 18.3 (local cluster, socket `/tmp`, port 5599):

| input | PG | goopg before | rule |
|---|---|---|---|
| `'10:00.5'` | `00:10:00.5` | 22007 | a fraction on TWO fields is MINUTE TO SECOND — the fields **shift right** |
| `'10::00'`, `'10:'` | `10:00:00` | 22007 | an empty subfield decodes as 0 (`strtoint` consumes nothing) |
| `':10:00'` | `10:00:00` | 22007 | leading punctuation is only a field delimiter |
| `'040506'`, `'0405'` | `04:05:06`, `04:05:00` | 22007 | `DecodeNumberField`: a 6-digit run is `hhmmss`, a 4-digit one `hhmm` |
| `'040506.5'`, `'0405.5'` | `04:05:06.5`, `04:05:00.5` | 22007 | the fraction is split off **first**, so the rest still picks its role |
| `'T040506'` | `04:05:06` | 22007 | the ISO 8601 separator in front of a bare time |
| `'10:00AM'` | `10:00:00` | 22007 | the tokenizer ends a numeric field at the first non-digit, so no space is needed |
| `'allballs'` | `00:00:00` | 22007 | the `datetktbl` zero-time token |
| **`'12:00 AM'`** | **`00:00:00`** | **`12:00:00`** | `DecodeDateTime`'s `DTK_AM` arm maps hour 12 to 0 |

The last row is the one that matters: a **silently wrong answer twelve hours
off**, not an error. `'13:00 PM'` was equally wrong in the other direction —
PG raises 22008 (a meridiem past hour 12 is out of range), goopg answered
`13:00:00`.

Every spelling above is also legal after a date, and there the failure had a
second cause: `parseTimeString` truncated its input at the first space to drop a
zone, so `'2020-01-01 12:00 AM'` lost its meridiem before decoding.

### 9.2 What landed

`internal/pgdatetime/timeofday.go` — a leaf sibling of `era.go`:

- `ParseTimeOfDay(s) (TimeOfDay, error)` reimplements `DecodeTimeCommon` (the
  colon forms, including the MINUTE TO SECOND shift and the empty-subfield rule)
  and the time arms of `DecodeNumberField` (the run-together forms), plus the
  `am`/`pm` and `allballs` tokens. It returns raw fields — hour may be 24 and
  second 60, exactly as PG leaves them for `time_in` to validate — so the
  caller keeps ownership of the range check.
- `ErrTimeBadFormat` / `ErrTimeFieldOverflow` mirror PG's `DTERR_BAD_FORMAT` vs
  `DTERR_FIELD_OVERFLOW` split, which is 22007 vs 22008 at the call sites. goopg
  reported 22007 for both.
- `CanonicalizeTimeToken(s)` rewrites just the time token of a timestamp into
  the padded `HH:MM:SS[.ffffff]` spelling, leaving the date and zone parts
  byte-identical, so `parsePGTimestampText` reuses the decoder without
  re-implementing date or zone decoding. It is tried only **after** the plain
  layout pass fails, so the common path costs nothing.

In the executor, `stripTimeZoneSuffix` replaces the truncate-at-first-space
logic and explicitly refuses to treat `AM`/`PM` as a zone token; `parseTimeString`
now delegates field decoding entirely, which also deletes its hour-24 rewrite
and its `:60` leap-second string surgery (both were only there to make a Go
layout match). `parseTimeTZString` re-attaches the meridiem it detaches for zone
extraction — it used to re-attach only `PM`.

### 9.3 Verification

`TestParseTimeOfDayPGAcceptedForms` / `TestParseTimeOfDayRejects` /
`TestCanonicalizeTimeToken` (unit), and `TestParseTimeStringPGFieldRoles`,
`TestParseTimeTZStringPGFieldRoles`, `TestParsePGTimestampTextPGFieldRoles`
(executor) pin every row of the table above to the PG 18.3 answer, plus the
unchanged behaviour the rewrite could have dropped (hour 24, the leap second, a
zone a time has no date to apply). Two mutation guards from §6/§7 named
`'10:00.5'` and `'10::00'` as forms that must keep raising **until a real
`DecodeTime` field walk lands**; that walk is this section, so both moved to
their PG readings.

A 36-case `::time` battery and a 16-case `::timestamp` battery were diffed
goopg-vs-PG through `psql` before and after: 0 divergences remain in the time
battery, and the timestamp battery's residue is the four ledger rows below.

### 9.4 Still unimplemented (deferral ledger, 2026-08-11)

1. The zone suffix of a TIME is stripped without validation, so `'10:00 A.M.'`
   is accepted where PG raises `time zone "a.m." not recognized`.
2. A TIMESTAMP still rejects hour 24 and the leap second (`'2020-01-01 24:00:00'`,
   `'2020-01-01 23:59:60'`; PG rolls both to `2020-01-02 00:00:00`) — the
   canonical token cannot carry them, so `CanonicalizeTimeToken` declines rather
   than invent an instant.
3. A bare `timetz` takes `+00` instead of the session `TimeZone`.
4. `timestamp` WITHOUT time zone **applies** a decoded zone offset
   (`'2020-01-01 10:00:00+05:30'` → `04:30:00`; PG ignores the zone). Pre-existing
   — the `Z07` layouts are shared by both types — but this slice let more
   spellings reach it.

## 10. Follow-up 2026-08-11 (M0119-0006) — the zone a `timestamp` and a `date` must THROW AWAY

Closes item (4) of §9.4, and the probe found the same root cause producing a
**whole-day** error one type over.

### 10.1 The defect

`timestamp` without time zone and `date` decode a time zone field out of their
input and then ignore it; only `timestamptz` keeps it. goopg applied the zone to
all three, so the answers were silently wrong — never errors:

| input | goopg (before) | PG 18.3 |
|---|---|---|
| `'2020-01-01 10:00:00+05:30'::timestamp` | `2020-01-01 04:30:00` | `2020-01-01 10:00:00` |
| `'2020-01-02 02:00:00+05:30'::date` | `2020-01-01` | `2020-01-02` |
| `'2020-01-01 22:00:00-08'::date` | `2020-01-02` | `2020-01-01` |
| `'2020-01-01 10:00:00+05:30'::timestamptz` | `2020-01-01 04:30:00` | `2020-01-01 04:30:00` |

The `date` rows are the expensive shape: an offset that crosses midnight moves
the stored day, so a `WHERE d = DATE '2020-01-02'` silently misses the row that
was inserted as `'2020-01-02 02:00:00+05:30'`.

### 10.2 Why

Upstream runs all three input functions through the SAME `DecodeDateTime()`
(`postgres/src/backend/utils/adt/datetime.c`), which fills `tzp` whenever the
text carries an offset. The types differ only in the call after it:

- `timestamptz_in` → `tm2timestamp(tm, fsec, &tz, &result)` — the offset moves
  the wall clock onto the UTC line;
- `timestamp_in` → `tm2timestamp(tm, fsec, NULL, &result)` — the decoded zone is
  discarded (`timestamp.c`);
- `date_in` → `date2j(tm->tm_year, tm->tm_mon, tm->tm_mday)` — the zone is never
  consulted at all (`date.c`).

goopg had exactly one shared layout table (§7) whose zone-bearing entries use
Go's `Z07*` elements, and converted every match with `.UTC()`. That single
`.UTC()` **is** the timestamptz rule, applied to all three types.

### 10.3 What landed

`internal/executor/copy_text.go` gained the type→rule mapping the shared table
cannot express:

- `tsZoneMode` (`tsApplyZone` / `tsDiscardZone`) with the upstream call sites
  documented on it;
- `tsZoneModeForType(typeName)` — only `timestamptz` / `timestamp with time zone`
  keep the offset;
- `applyTSZoneMode(ts, zone)` — `tsDiscardZone` re-reads the parsed wall-clock
  fields as UTC, which is upstream's `tm2timestamp(..., NULL, ...)`;
- `parsePGTimestampTextZone` / `parseCopyTimestampZone`, the mode-taking forms of
  the two shared entry points. The old no-mode names remain as the timestamptz
  reading for the paths that do not know their target type (§10.5).

Every input path that DOES know its target type now passes the mode: the typed
literal (`expr.go` `evalTypedStringLit`, from `x.Type`), the `::timestamp` /
`::timestamptz` and `::date` casts (`evalCast`), `pg_input_is_valid`, the COPY
TEXT reader (`copy_text.go` `copyTextToDatum`, from the column type) and the
value encoder (`codec.go` `encodeValuePG`).

### 10.4 Verification

`internal/executor/timestamp_zone_discard_input_test.go` pins the parser against
PG-18.3-captured answers for both modes. End-to-end, a throwaway goopg was
diffed against a throwaway PG 18.3 cluster over the literal, cast, `INSERT` and
`COPY FROM STDIN` paths for `timestamp`, `timestamptz` and `date`: every cell now
agrees.

### 10.5 Still unimplemented (deferral ledger, 2026-08-11)

1. Four paths still read a string as a timestamp with NO target type in hand and
   so keep the timestamptz rule: the cross-kind comparison coercion
   (`tryParseStringAs`), `EXTRACT`, `date_trunc` and the `pg_authid` `validuntil`
   sync. PG never has this ambiguity — `transformExpr` coerces the unknown
   literal to the target type before evaluation.
2. goopg prints a `timestamptz` with no zone suffix (`2020-01-01 04:30:00` where
   PG prints `2020-01-01 04:30:00+00`), because `KindTime` cannot tell the two
   types apart on output either — the same missing distinction, on the other
   side of the wire.

## 11. Follow-up 2026-08-11 (M0119-0006) — hour 24, the leap second, and the day a `date` must NOT carry

### 11.1 The defect

Six ordinary PG spellings were flat 22007 syntax errors on goopg's timestamp
path, and one class of genuinely-invalid input got the wrong SQLSTATE:

| input | PG 18.3 | goopg before |
|---|---|---|
| `'2020-01-01 24:00:00'::timestamp` | `2020-01-02 00:00:00` | 22007 |
| `'2020-01-01 23:59:60'::timestamp` | `2020-01-02 00:00:00` | 22007 |
| `'2020-01-01 10:00:60'::timestamp` | `2020-01-01 10:01:00` | 22007 |
| `'2020-12-31 24:00:00'::timestamp` | `2021-01-01 00:00:00` | 22007 |
| `'2020-01-01 240000'::timestamp` | `2020-01-02 00:00:00` | 22007 |
| `'2020-01-01 24:00:00'::date` | `2020-01-01` | 22007 |
| `'2020-01-01 25:00:00'::timestamp` | 22008 field out of range | 22007 |

The time-only path already answered every one of these correctly
(`'24:00:00'::time` → `24:00:00`, `'23:59:60'::time` → `24:00:00`,
`'10:00:60'::time` → `10:01:00`) — a textbook instance of the sibling-path rule:
one twin had the behaviour and the other did not, and each carried its own copy
of the arithmetic.

### 11.2 Why

`DecodeTimeCommon`'s sanity check (`postgres/src/backend/utils/adt/datetime.c`)
ends with the comment *"but caller must check the range of tm_hour"* — it
deliberately admits `tm_hour == 24` and `tm_sec == 60`. The caller's check is
`time_overflows()` (`postgres/src/backend/utils/adt/date.c`), called from both
`DecodeDateTime` and `DecodeTimeOnly`: the fields are range-checked
individually, and then, *"because we allow, eg, hour = 24 or sec = 60"*, the
TOTAL is separately required not to exceed 24:00:00. That total check is the
whole reason `'24:00:00'` and `'23:59:60'` are legal while `'24:00:00.5'`,
`'23:59:60.5'` and `'24:00:01'` are not.

The fold into the next day is then **not part of decoding at all**. It falls out
of `tm2timestamp()`, which composes `date2j(y, m, d) * USECS_PER_DAY` with the
time-of-day microseconds — 86 400 000 000 of them being exactly one more day.

That is why `date_in()` does not roll over: it never calls `tm2timestamp`, it
hands `tm_year`/`tm_mon`/`tm_mday` straight to `date2j`. So one string has two
correct readings, and they differ by a day:

```
'2020-01-01 24:00:00'::timestamp  ->  2020-01-02 00:00:00
'2020-01-01 24:00:00'::date       ->  2020-01-01
```

goopg's shape hid all of this. `CanonicalizeTimeToken` (§9) rewrites the time
token into the padded `HH:MM:SS` spelling the Go layout table can read, and it
*declined* any token that spelling cannot hold — which is precisely hour 24 and
second 60. The timestamp path therefore never saw them.

### 11.3 What landed

The decode/compose split is now goopg's shape too.

- `pgdatetime.TimeOfDay.Overflows()` reimplements `time_overflows()`, total
  check included, and `TimeOfDay.Normalize()` reimplements the fold: it returns
  the normalised time plus a **`dayCarry`**, 1 exactly when the time of day is
  the whole day. A second 60 below the boundary just rolls the minute
  (`10:00:60` → `10:01:00`), which the same code path gives for free.
- `CanonicalizeTimeToken` returns `(canon, dayCarry, error)`. The error is now
  typed: `ErrTimeBadFormat` means "not a time, leave the string alone",
  `ErrTimeFieldOverflow` means "decoded and out of range" — the caller maps the
  second to `pgdatetime.ErrFieldOutOfRange`, i.e. 22008, instead of letting the
  layout table have a second go and report a spelling error.
- `parsePGTimestampTextParts` / `parseCopyTimestampZoneParts` return the carry
  **unapplied**; the ordinary `…Zone` wrappers add it. That is the
  `tm2timestamp` seam, and it is where the range check against the `KindTime`
  carrier now runs (post-carry, so `'9999-12-31 24:00:00'` is judged on the
  value it actually becomes).
- `parseDateInputText` is the new `date_in` reading, used by the `::date` cast
  and the date arm of `encodeValuePG`: same parse, carry dropped. Its doc names
  both things `date_in` discards — the zone (§10) and now the carry — because
  each of them, taken alone, was a whole-day error.
- `parseTimeString` deletes its private leap-second fold and its private
  hour-24 range check and calls `Normalize` like everyone else.

### 11.4 Verification

`internal/executor/timestamp_hour24_leap_second_input_test.go` (four subtests:
timestamp composes the carry, date drops it, past-the-day is 22008, and the
time-only twin agrees) plus the extended `TestCanonicalizeTimeToken`. All wants
were captured from a throwaway PG 18.3 cluster and re-diffed end to end against
a throwaway goopg over the literal, cast and `::date`/`::time`/`::timetz` paths:
every cell agrees except the three already-open ledger rows below.

### 11.5 Still unimplemented (deferral ledger, 2026-08-11)

1. `'0001-01-01 24:00:00 BC'::timestamp` is a loud 22008 rather than
   `0001-01-02 00:00:00 BC` — the `KindTime` nanosecond carrier again (§8), not
   the carry, which composes correctly before the guard rejects the result.
2. `'23:59:60'::timetz` answers `24:00:00+00` where a `TimeZone = Asia/Tokyo`
   session answers `24:00:00+09` — the bare-`timetz` session-zone default, open
   since §9.
3. The three time-of-day items §9 left open are untouched by this slice: the
   unvalidated TIME zone suffix (`'10:00 A.M.'`), the run-together DATE half of
   `DecodeNumberField` (`'20200101T040506'`), and `timestamptz` output's missing
   zone suffix (§10).

## 12. Follow-up 2026-08-11 (M0119-0006) — the RUN-TOGETHER numeric date

The 34th M0119-0006 slice closes the last spelling this doc's §5 listed as
deferred and that a *numeric* decoder can reach: the separator-less date,
`'20200101'`. It is the form ISO 8601 calls "basic", the one `date +%Y%m%d`
prints, and the one every fixed-width extract file in existence carries — and
goopg answered `22007` for all of it.

### 12.1 What PG does

`ParseDateTime` splits on separators, so `20200101` arrives as one `DTK_NUMBER`
field and `DecodeNumberField` (`postgres/src/backend/utils/adt/datetime.c`)
interprets it *by length*, with the comment doing the specifying:

```c
/* No decimal point and no complete date yet? */
else if ((fmask & DTK_DATE_M) != DTK_DATE_M)
{
    if (len >= 6)
    {
        /* Start from end and consider first 2 as Day,
           next 2 as Month, and the rest as Year. */
        tm->tm_mday = atoi(str + (len - 2));  ...
        tm->tm_mon  = atoi(str + (len - 4));  ...
        tm->tm_year = atoi(str);
        if ((len - 4) == 2) *is2digits = true;
        return DTK_DATE;
    }
}
```

Three consequences that a "yyyymmdd" reading would get wrong, all verified on a
PG 18.3 oracle:

| input | PG 18.3 `::date` | why |
|---|---|---|
| `20200101` | `2020-01-01` | the year is "the rest", here four digits |
| `2020101` | `0202-01-01` | seven digits: the year is *three* digits |
| `202001011` | `20200-10-11` | nine digits: the year is *five* |
| `200101` | `2020-01-01` | six digits ⇒ 2-digit year ⇒ windowed |
| `700101` | `1970-01-01` | the window is 1970..2069, not 2000..2099 |
| `200101 BC` | `0020-01-01 BC` | BC **suppresses** the windowing |

The last row is the subtle one. The windowing does not live in
`DecodeNumberField` at all — it lives in `ValidateDate()`, as an `else if` that
runs only when the BC branch did not:

```c
if (bc) { ... tm->tm_year = -(tm->tm_year - 1); }
else if (is2digits) { if (tm_year < 70) tm_year += 2000; else if (tm_year < 100) tm_year += 1900; }
```

### 12.2 The fmask fork — why this is a second entry point

`DecodeNumberField`'s date arm is guarded by `(fmask & DTK_DATE_M) != DTK_DATE_M`
and its *time* arm by `(fmask & DTK_TIME_M) != DTK_TIME_M`. `DecodeTimeOnly`
(`time_in`, `timetz_in`) starts with the date fields already set, so it never
reaches the date arm; `DecodeDateTime` (`date_in`, `timestamp_in`,
`timestamptz_in`) does. The same six digits therefore mean two different things,
and PG 18.3 agrees with itself:

```
'040506'::time  ->  04:05:06        '040506'::date  ->  2004-05-06
```

That is why the slice adds `pgdatetime.NormalizeDateTimeInput(s, bc)` beside the
existing `NormalizeInput(s)` rather than widening the latter. `NormalizeInput`
*is* the DecodeTimeOnly context and keeps its old behaviour verbatim
(`parseTimeString`, `parseTimeTZString`); the new function is the DecodeDateTime
context and is what `parsePGTimestampTextParts`, `parsePGDateText` and the
verbose-format fallback now call. The `bc` argument exists purely for
`ValidateDate`'s ordering above — callers already run `SplitEra` first, so they
have the answer in hand.

The rewrite itself stays where every other spelling in this doc is handled: it
produces the zero-padded `YYYY-MM-DD` token the existing Go layout tables read,
so the time-of-day, zone and era machinery around it is untouched. A trailing
time token survives verbatim and is still canonicalised by
`CanonicalizeTimeToken`, which is why `'20200101 040506'` and `'20200101T040506'`
both work: the date half is §12's, the time half is §9's.

### 12.3 The one place goopg still has to guess

`tryParseStringAs` (`internal/executor/expr.go`) coerces an unknown literal to
match a `KindTime` datum with **no target type in hand** — the standing gap this
doc's §10 also records, since `KindTime` does not distinguish
`date`/`time`/`timestamp`/`timestamptz`. PG never guesses here: `transformExpr`
coerces the literal to the column's type *before* the input function runs, so the
fmask already says which arm applies.

The slice therefore resolves only the widths where there is nothing to guess.
`pgdatetime.RunTogetherDateIsTimeAmbiguous` reports whether the leading digit run
is 4 or 6 long — the two widths `DecodeTimeOnly` also accepts (`hhmm`, `hhmmss`).
For everything else the date reading is the only reading, and the coercion takes
it:

| comparison | before | after | PG 18.3 |
|---|---|---|---|
| `time_col = '040506'` | 1 row | 1 row | 1 row |
| `date_col = '20040506'` | **0 rows** | 1 row | 1 row |
| `ts_col = '20040506 040506'` | **0 rows** | 1 row | 1 row |
| `date_col = '040506'` | 0 rows | 0 rows | **1 row** |

The last row stays wrong, and stays wrong *silently* — a failed coercion is
"not equal", not an error, which is the exact shape of the original M0125-0007
defect. It cannot be fixed by guessing better; it needs the `KindTime` type
distinction, and it is on the ledger with the rest of that work.

### 12.4 Verification

`TestNormalizeDateTimeInputRunTogetherDate`,
`TestNormalizeInputKeepsTimeOnlyReading` and `TestRunTogetherDateIsTimeAmbiguous`
(`internal/pgdatetime`) pin the normalizer and the sibling-path guard;
`TestRunTogetherDateInput` and `TestRunTogetherTimeStaysTimeOnly`
(`internal/executor`) pin the executor entry points. All expectations were
captured from a throwaway PG 18.3 cluster and diffed cast-by-cast
(`date`/`timestamp`/`time`/`timestamptz`) against a throwaway goopg server: of
the 80 (input × cast) cells probed, every `date`, `timestamp` and `time` cell now
matches. The residual `timestamptz` cells differ only by the missing `+HH` output
suffix (§10's ledger row), and three inputs land outside the `KindTime`
nanosecond carrier (`'2020101'` = year 202, both BC forms) where goopg answers a
loud `22008` instead of PG's value — the carrier row, again.

One labelling gap the probe surfaced and this slice does **not** fix: a
run-together date whose fields decode but cannot exist (`'20201301'`) is `22007`
on goopg where PG answers `22008`. It is not new — `'2020-13-01'` behaves
identically at HEAD — because goopg has no `ValidateDate()` step: the Go layout
simply refuses month 13, which reads as a spelling error rather than a range one.

## 13. Follow-up 2026-08-11 (M0119-0006) — `ValidateDate()`'s month/day range check

### 13.1 The defect

§12.4's closing paragraph named the gap: `'20201301'`, `'2020-13-01'`,
`'2020-01-32'` all decode a month or day field the Go layout tables cannot hold
(month 13, day 32), and every entry in `pgTimestampLayouts` simply fails to
match — the failure that reaches the caller is the generic "no layout matched"
one, which maps to SQLSTATE `22007` ("invalid input syntax"). PostgreSQL's
`DecodeDateTime` DOES recognise the shape (three numeric fields in date order);
it is `ValidateDate()`, a step goopg has never had, that rejects the *values*
and raises `22008` ("date/time field value out of range") instead
(`postgres/src/backend/utils/adt/datetime.c`, `ValidateDate()`'s "check for
valid month" / "minimal check for valid day of month" arms — both
`DTERR_MD_FIELD_OVERFLOW` and `DTERR_FIELD_OVERFLOW` map to the same
`ERRCODE_DATETIME_FIELD_OVERFLOW` in `DateTimeParseError`, so the two need not
be distinguished on the goopg side either).

### 13.2 What landed

Two new functions in `internal/pgdatetime/normalize.go`:

- `ValidateMonthDay(month, day int) error` — the two range checks themselves
  (month 1..12, day 1..31), returning `ErrFieldOutOfRange` (the same sentinel
  §10-§12 already use for the era/hour-24/leap-second range failures).
- `ValidateDateToken(dateToken string) error` — extracts month/day from a
  normalized `"...-MM-DD"` token and calls `ValidateMonthDay`. It locates the
  fields from the TRAILING `-MM-DD` (last five characters before the string
  end) rather than a fixed offset, so it works whether the year field is the
  usual 4 digits or the wider verbatim year `expandRunTogetherDate` emits past
  6 input digits (`"20200-10-11"` for `'202001011'`). A token that is not that
  shape (a bare time, an empty string, anything with a following space) returns
  `nil` — ValidateDateToken defers to the caller's own parser rather than
  guess.

Wired into the two normalized-string entry points ahead of the Go `time.Parse`
attempt(s):

- `parsePGDateText` (`internal/executor/copy_text.go`) — one call, right after
  `NormalizeDateTimeInput`, before `time.Parse("2006-01-02", norm)`.
- `parsePGTimestampTextParts` — one call on `dateTokenPrefix(body)` (a new
  helper that splits the date token off at the first space/`T`/`t`, mirroring
  `normalizeInput`'s own split), placed ahead of the `cands`/layout loop since
  the date token is identical for both the `body` and `canon` candidates that
  loop tries.

### 13.3 What did NOT land (this slice) — closed by §13.5 below

`ValidateDate()`'s THIRD check — "check for valid day of month, now that we
know for sure the month and year" (`tm_mday > day_tab[isleap(tm_year)][tm_mon -
1]`) — needs the ASTRONOMICAL (era-adjusted) year, and is not ported. A day
that is `≤31` but impossible for its actual month (`'2020-02-30'`,
`'2021-04-31'`) is still silently accepted, unchanged from before this slice —
not a regression, just not yet closed. The blocker is ordering, not
difficulty: `ApplyEra` (era.go) — which turns the era-relative year the string
spelled into the astronomical year goopg stores — runs AFTER `time.Parse`
succeeds in both call sites, while `ValidateDateToken` runs BEFORE it. Adding
the day-in-month arm means either threading the astronomical year into
`ValidateDateToken` (which means computing it earlier) or moving the day-count
check to run after `ApplyEra` instead.

### 13.4 Verification

`TestValidateDateToken` (`internal/pgdatetime`) pins `ValidateMonthDay`/
`ValidateDateToken`'s bound checks and the not-our-shape no-op cases (bare
time, empty string, a token with a space). `TestDateTimeInputErrorSeparatesRangeFromSyntax`
(`internal/executor`) is updated: `'2020-13-99'` and the new `'2020-01-32'`
case now assert `22008`, not `22007`. Full `internal/pgdatetime` and
`internal/executor` suites re-verified green (no regressions — in particular
`TestRepresentableDatesStillParse`, `TestDateEraLiteralAndCopyPathsAgree` and
the run-together/hour-24 suites from §9-§12 are unaffected).
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35).

## 14. Follow-up 2026-08-11 (M0119-0006, 36th slice) — the day-in-month arm

### 14.1 The ordering question resolved differently than proposed

§13.3 framed the choice as "thread the astronomical year into
`ValidateDateToken` (compute it earlier)" vs. "move the day-count check to run
after `ApplyEra`". The second option turns out not to work: Go's `time.Parse`
(unlike `time.Date`/`time.Time.AddDate`, which normalize an out-of-range field
by rolling it into the next unit) already validates the calendar day itself —
`time.Parse("2006-01-02", "2020-02-30")` fails with `"day out of range"`
*before* `ApplyEra` ever runs, so waiting for `ApplyEra`'s output to check
day-in-month never reaches live code; `time.Parse`'s own bare error wins the
race and reports as goopg's generic 22007. The only viable ordering is the
first option: compute the astronomical year BEFORE `time.Parse`, not after.

Doing that does not actually require running `ApplyEra` early (which does need
a `time.Time` to carry the era-adjusted fields, an unwanted layering
inversion). The astronomical year is a pure function of the year DIGITS the
token spelled plus the `bc` flag `SplitEra` already extracted — `ApplyEra`'s
own transform, minus the `time.Time` plumbing. New `AstronomicalYear(year,
bc) (int, bool)` (`era.go`) computes exactly that, and `DateTokenYear`
(`normalize.go`) reads the year digits straight off the normalized token
(same trailing-anchored technique `DateTokenMonthDay` uses for month/day —
works for both the padded 4-digit case and the widened run-together year).

### 14.2 What landed

- `pgdatetime.ValidateDayOfMonth(year, month, day int) error` — `ValidateDate()`'s
  third check itself: a `daysInMonthTab` port of `day_tab[0]` plus an
  `isLeapYear(year)` port of the `isleap()` macro, applied to the
  ASTRONOMICAL year the caller supplies.
- `pgdatetime.DateTokenMonthDay` — `ValidateDateToken`'s month/day extraction,
  now exposed as its own function (`ValidateDateToken` is a one-line wrapper
  over it) so a caller can also read `DateTokenYear` and run
  `ValidateDayOfMonth`.
- `pgdatetime.DateTokenYear(dateToken string) (year int, ok bool)` — reads the
  digit run before the trailing `"-MM-DD"`.
- `pgdatetime.AstronomicalYear(year int, bc bool) (int, bool)` — §14.1's pure
  conversion; `ok` is false only for `bc && year <= 0` (the "no year zero"
  refusal — `AstronomicalYear` defers that error to `ApplyEra`'s own check
  downstream rather than duplicating it).
- `internal/executor/copy_text.go`'s new `validateDateTokenFull(dateToken
  string, bc bool) error` composes all three ValidateDate() checks (month,
  day, day-in-month) and replaces the separate `ValidateMonthDay`/
  `ValidateDateToken` calls the 35th slice added at both call sites
  (`parsePGDateText`, `parsePGTimestampTextParts`) — still run BEFORE
  `time.Parse`, per §14.1.

`'2020-02-30'`, `'2021-02-29'` (2021 is not a leap year), `'2021-04-31'` are
now `22008` where PG 18.3 agrees (verified live against a throwaway PG 18.3
and a throwaway goopg on the same inputs — see the psql session cited in the
deferral ledger row); `'2020-02-29'` and `'2021-04-30'` are unaffected
(accepted). Run-together and unpadded-month spellings inherit the fix for
free, since `DateTokenMonthDay`/`DateTokenYear` read the same normalized token
`ValidateDateToken` always has (`'20200230'`, `'2020-2-30'` both now `22008`).

### 14.3 What did NOT land — a BC-era leap-year edge case

A BC February-29 date where the LITERAL year's leap-ness disagrees with the
ASTRONOMICAL year's is still wrong: `'0001-02-29 BC'` is astronomical year 0
(1 BC), which the Gregorian `isleap()` formula counts as leap (`0 % 400 ==
0`), and PG 18.3 accepts it. goopg's `validateDateTokenFull` gets this RIGHT —
`AstronomicalYear(1, true) = 0`, `isLeapYear(0) = true`, so
`ValidateDayOfMonth` passes — but the code downstream still calls
`time.Parse("2006-01-02", "0001-02-29")` on the LITERAL token (the BC suffix
was already stripped by `SplitEra`, and nothing hands `time.Parse` the
astronomical year), and Go's own internal leap check uses literal year 1
(NOT a leap year), so `time.Parse` itself rejects the day before `ApplyEra`
ever runs — the same race condition §14.1 diagnosed, recurring one layer
down. Not a regression: this is identical to the pre-existing (30th-slice-era)
behaviour, since `time.Parse`'s leap check fires the same way with or without
this slice's pre-checks. Fixing it needs bypassing `time.Parse`'s built-in day
validation for a BC date specifically — e.g. constructing the `time.Time` via
`time.Date(year, month, day, ...)` directly once
`validateDateTokenFull` has already confirmed month/day/day-in-month are
valid, rather than relying on `time.Parse` to both extract AND validate the
fields. That touches the time-of-day handling at both call sites too (the
timestamp path composes date and time in one `time.Parse` call per layout) and
is its own slice — ledger row, M0119-0006.

### 14.4 Verification

`TestValidateDayOfMonth`, `TestDateTokenYear`, `TestAstronomicalYear`
(`internal/pgdatetime`) pin the new functions, including the century/400-year
leap-year rules (1900 not leap, 2000 leap) and negative (BC) astronomical
years. `TestDateTimeInputErrorSeparatesRangeFromSyntax` (`internal/executor`)
gains the day-in-month cases (`'2020-02-30'`, `'2021-02-29'`, `'2021-04-31'`
→ `22008`; `'2020-02-29'`, `'2021-04-30'` → accepted). Full
`internal/pgdatetime` and `internal/executor` suites green; no change to
`TestRepresentableDatesStillParse`, the era suites (§8), or the run-together
suites (§12) — this slice only tightens an existing acceptance, it does not
widen one. `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35). Also verified
live against throwaway goopg + PG 18.3 servers on the exact repro strings
above, including the run-together and BC forms.

## 15. Follow-up 2026-08-11 (M0119-0006, 37th slice) — the DATE half of the BC leap-day race

### 15.1 What landed

§14.3's diagnosis, fixed for `parsePGDateText` only: new `bcLeapDateFallback`
(`internal/executor/copy_text.go`) is tried when `time.Parse("2006-01-02",
norm)` fails AFTER `validateDateTokenFull` has already confirmed the token is
valid. It re-derives month/day (`pgdatetime.DateTokenMonthDay`) and the
ASTRONOMICAL year (`pgdatetime.DateTokenYear` + `pgdatetime.AstronomicalYear`)
straight from the token's digits — the same values `validateDateTokenFull`
itself just validated — and builds the `time.Time` directly via
`time.Date(astroYear, month, day, 0, 0, 0, 0, time.UTC)`. This sidesteps two
things at once: `time.Parse`'s own day-out-of-range check (which uses the
LITERAL year, not the astronomical one) and `pgdatetime.ApplyEra`'s
literal→astronomical shift (already done by hand, so `ApplyEra` is skipped
for this path — its only other job, the no-year-zero refusal, is preserved
by `bcLeapDateFallback` returning `ok=false` when `AstronomicalYear` does,
which leaves the original `time.Parse` error — itself now dead for every
input this fallback actually fires on, since `!bc` is the only other `ok=
false` case — to propagate unchanged).

`'0001-02-29 BC'::date` no longer raises `22007` ("invalid input syntax",
read as a spelling mistake); PG accepts it and goopg now reaches it too,
modulo the carrier: the reconstructed astronomical year 0 is still outside
goopg's nanosecond-carrier domain (1677..2262 — see §9's carrier note and the
`Datum.Int`/`KindTime` deferral row), so `checkTimeCarrierRange` still
refuses it, but with the CORRECT SQLSTATE (`22008`, a range failure) instead
of the wrong one (`22007`, a syntax failure). The typed-literal and COPY
paths move together for free (`TestDateEraLiteralAndCopyPathsAgree`) — both
already share `parsePGDateText`.

### 15.2 What did NOT land — the TIMESTAMP half

`parsePGTimestampTextParts` has the identical bug (§14.3 named both call
sites) and is UNCHANGED: it composes date and time-of-day in one
`time.Parse` call per layout across two candidate strings (the plain body and
the hour-24/leap-second-canonicalized one), so a bypass there needs the
time-of-day fields threaded through the `time.Date` construction too, not
just month/day/year. `'0001-02-29 10:00:00 BC'::timestamp` still raises
`22007`. Deliberately deferred — see the M0119-0006 ledger row this slice
appends — to keep the diff scoped to one call site per Ralph's "ONE task per
loop" rule; the timestamp site additionally has to decide how the two
candidate layouts interact with the bypass, which is its own design
question.

### 15.3 Verification

`TestDateTimeInputErrorSeparatesRangeFromSyntax` gains `'0001-02-29 BC'` →
`22008`. `TestDateEraLiteralAndCopyPathsAgree` gains the same input. Full
`internal/pgdatetime` and `internal/executor` suites green;
`TestRepresentableDatesStillParse` unaffected (this slice only changes the
error CODE for an input that was already rejected, it does not widen
acceptance into the representable range).

## 16. Follow-up 2026-08-11 (M0119-0006, 38th slice) — the TIMESTAMP half of the BC leap-day race

### 16.1 What landed

§15.2's deferral, closed. `parsePGTimestampTextParts`
(`internal/executor/copy_text.go`) now falls back to a new
`bcLeapTimestampFallback` when a candidate matches no layout, and the answer to
§15.2's open design question — "how do the time-of-day fields get threaded
through the `time.Date` construction?" — is that they must NOT be re-parsed by
hand. `pgTimestampLayouts` exists precisely because three separate M0119-0006
slices found the timestamp input paths disagreeing about a spelling; a private
time-of-day parser inside the fallback would be a fourth such divergence,
invisible until some later slice widened the table and forgot this one.

So the fallback substitutes a **proxy year** into the date token
(`bcLeapProxyYear = 2000`, chosen only because it is leap — Feb 29 is the whole
point — and four digits wide, which is what the layouts' `2006` element wants)
and re-parses the candidate through the ordinary table. Everything but the year
is decoded by the same code as every other input: time of day, fractional
seconds, the `T` separator, and the zone spellings. The decoded wall clock is
then rebuilt at the real astronomical year via `time.Date`, which performs
`ApplyEra`'s literal→astronomical shift by hand exactly as the DATE fallback
does.

One subtlety the DATE half does not have: the zone rule must be applied to the
PROXY value, before the rebuild. `applyTSZoneMode(ts, tsApplyZone)` shifts the
wall clock onto the UTC line, and that shift can cross midnight —
`'0001-02-29 00:30:00+05:30 BC'::timestamptz` is the 28th in UTC. Rebuilding at
the token's own month/day after the shift would silently discard that day
movement. The fallback therefore measures the whole-day delta between the zoned
proxy value and the proxy date and re-applies it with `AddDate` after the
rebuild, so the fallback and the ordinary path land on the same day.

`ok=false` (caller keeps its existing `errNoTimestampLayout`) for: `!bc` —
where the literal and astronomical years agree, so `time.Parse` never disagreed
in the first place; a token whose fields do not read back; the no-year-zero
refusal (`AstronomicalYear` `ok=false`); and any candidate the proxy rebuild
still fails to parse. The hook sits INSIDE the candidate loop rather than after
it, so the hour-24 / leap-second canonicalized candidate still gets its own
turn when the plain body's rebuild fails — which is what makes
`'0001-02-29 24:00:00 BC'` compose to March 1st with `carry=1` rather than
falling out as a syntax miss.

Range behavior is unchanged and unchanged deliberately: every BC value is still
outside the nanosecond carrier (1677..2262), so `'0001-02-29 10:00:00 BC'` is
still refused — with `22008` (a field/range failure, as upstream) instead of
`22007` (a spelling mistake). What moved is the PATH and therefore the code,
plus the decoded fields underneath, which is what the tests pin.

### 16.2 PG 18.3 oracle

Captured from a throwaway 18.3 cluster; all five are accepted upstream and all
five now decode to the same fields in goopg:

| input | PG 18.3 |
|---|---|
| `'0001-02-29 10:00:00 BC'::timestamp` | `0001-02-29 10:00:00 BC` |
| `'0001-02-29 00:30:00+05:30 BC'::timestamp` | `0001-02-29 00:30:00 BC` (offset dropped) |
| `'0001-02-29 00:30:00+05:30 BC'::timestamptz` | `0001-02-28 19:00:00 BC` (offset applied) |
| `'0001-02-29 24:00:00 BC'::timestamp` | `0001-03-01 00:00:00 BC` (whole-day carry) |
| `'0005-02-29 10:00:00 BC'::timestamp` | `0005-02-29 10:00:00 BC` (astronomical −4, leap) |
| `'0001-02-30 10:00:00 BC'::timestamp` | ERROR 22008 |

### 16.3 Verification

Two new tests in `internal/executor/date_era_and_range_input_test.go`:
`TestBCLeapDayTimestampDecodesAtTheAstronomicalYear` asserts the DECODED
FIELDS through `parsePGTimestampTextParts` (both zone modes, plus the hour-24
and leap-second carries) — asserting only the SQLSTATE would pass with the
fields still wrong, since the carrier refuses every BC value regardless — and
`TestBCLeapDayTimestampFallbackKeepsTheErrorClasses` pins the error classes on
either side of the fallback (impossible day, non-leap astronomical year, year
zero, a real syntax miss, an out-of-range hour, and the untouched non-BC path).
Mutation-checked: short-circuiting `bcLeapTimestampFallback` to `ok=false`
turns all seven field assertions red and returns the `22008` case to `22007`.

## 17. Follow-up 2026-08-13 (M0119-0006, 46th slice) — the trailing zone field a `time` accepted without looking at it

### 17.1 The defect

The §9 ledger row (2026-08-11) recorded that `parseTimeString` strips the zone
suffix of a `time` input *without validating it*, so `'10:00 A.M.'::time` was
accepted as `10:00:00` where PG raises `time zone "a.m." not recognized`. The
probe for this slice found the gap is wider than that one spelling: the old
`stripTimeZoneSuffix` peeled **every** trailing space-separated token that was
not `AM`/`PM` and threw it away, so

    '10:00 GARBAGE'::time  →  10:00:00
    '10:00 Japan'::time    →  10:00:00
    '10:00 zzz'::time      →  10:00:00
    '10:00 pst pdt'::time  →  10:00:00

all landed a guessed value in the column with no diagnostic. On PG every one of
them is an error. This is the worse direction of the two failure modes: refusing
a spelling PG accepts is visible and reported; accepting nonsense silently
stores a value the user never wrote. The COPY TEXT reader shares the function,
so a corrupt zone field in a load file was absorbed rather than reported.

`timetz` was only half-affected — `parseTimeTZString` had its own scan that
*did* reject an unrecognised token — but that scan knew only the abbreviation
table and the numeric displacement, so it was wrong in three other ways
(below).

### 17.2 Where PG's three answers come from

The interesting part is that PG has **three** verdicts for a trailing field, and
the one you get is decided by the *tokenizer*, not the decoder
(`postgres/src/backend/utils/adt/datetime.c`):

`ParseDateTime()` lowercases the token and classifies it. A leading run of
letters is `DTK_STRING` — unless the next character is `.`, `/` or `-`, or is
`+`/a digit while the letters are **not** a `datetktbl` keyword; then the whole
token becomes `DTK_DATE`, the shape a zone *name* has.

`DecodeTimeOnly()` then routes by that type:

| type | lookup | failure |
|---|---|---|
| `DTK_STRING` | `DecodeTimezoneAbbrev()` (the `timezone_abbreviations` table), then `datetktbl` for `am`/`pm`, `bc`/`ad`, `mon`, `january`, `allballs`, `today`, … | `DTERR_BAD_FORMAT` → **22007** |
| `DTK_DATE` | `pg_tzset()` on the lowercased token, then `pg_get_timezone_offset()` | not a zone → `DTERR_BAD_TIMEZONE` → **22023** `time zone "%s" not recognized`; a DST zone with no date in the input → `DTERR_BAD_FORMAT` → **22007** |

Two consequences that no amount of reasoning about "is this a zone" would
produce, and both were measured against PG 18.3 before any code was written:

- **`datetktbl` (datetime.c:105) contains no timezone abbreviation at all.**
  They live in the separately GUC-selected abbreviation table. So the
  tokenizer's keyword probe fails for `utc`, and `'10:00 UTC+5'` is ONE
  `DTK_DATE` token read as a POSIX TZ spec — which is why it yields `-05`, the
  POSIX sign being the opposite of the SQL displacement, and not UTC plus five
  hours. `'10:00 UTC-5'::timetz` is `10:00:00+05`.
- **A bare word never reaches `pg_tzset()`.** `'10:00 Japan'` is 22007 even
  though `Japan` is a real zone name, because without punctuation the token
  stays `DTK_STRING`. Conversely `'10:00 Etc/GMT'::time` is *accepted* while
  `'10:00 America/New_York'::time` is 22007 — a fixed-offset zone resolves
  without a date, a DST zone does not.

Era and meridiem are ordinary fields, not zones, so they may follow a zone:
`'10:00:00 PST BC'` and `'10:00 AM BC'` both parse. Two *zone* fields do not
(`'10:00 pst pdt'` is 22007) — `DecodeTimeOnly`'s `fmask` rejects the repeat.

### 17.3 What landed

New leaf `internal/executor/time_zone_token.go`:

- `classifyZoneToken(tok)` reproduces the table above, returning one of
  meridiem / era / fixed-offset / DST-name-needs-date / not-recognised /
  bad-format plus the offset. `pgDateTimeKeywords` is `datetktbl`'s
  wholly-alphabetic token set, present for the tokenizer's `datebsearch` probe
  and nothing else.
- `parsePOSIXZoneOffset` reads the `±h[h][:mm[:ss]]` tail of a POSIX spec. It
  is deliberately *not* `parseTZOffset`: that one requires the two-digit hour of
  the SQL displacement spelling, while POSIX allows one digit and PG enforces no
  upper bound here (`'utc-25'::timetz` is `+25` on PG 18.3).
- `fixedZoneOffset(loc)` answers `pg_get_timezone_offset()`'s question — one
  offset for all time? — by sampling both solstice sides of 1901/1970/2000/
  2025/2100, since Go exposes no transition list.
- `stripValidatedZoneSuffix` replaces `stripTimeZoneSuffix` and is now shared by
  `parseTimeString` and `parseTimeTZString`, so the two cannot drift again. It
  peels era fields freely, at most one zone field, stops at the meridiem, and
  handles the attached `Z` that `pgdatetime.NormalizeInput` folds a spaced or
  lowercase zulu into — which is why `'10:00 Z'::time` used to fail as a
  malformed hour and now returns `10:00:00`.

`wrapTimeTZError` gained a 22023 pass-through: that error names the offending
*zone*, not the input, so re-wrapping it as 22007 would have swapped PG's error
for a different one.

Three fixes fell out of sharing the classifier with `timetz`: `'10:00 BC'` was
rejected outright, a fixed-offset zone *name* was rejected, and a name-shaped
token that names nothing got 22007 instead of 22023.

### 17.4 PG 18.3 answers this slice now reproduces

Captured on port 65432, session `TimeZone` `Asia/Tokyo`.

| input | PG 18.3 |
|---|---|
| `'10:00 PST'::time`, `'10:00 UTC'`, `'10:00 Z'`, `'10:00 +05'`, `'10:00+05'` | `10:00:00` |
| `'10:00 Etc/GMT'::time`, `'10:00 UTC-5'::time` | `10:00:00` |
| `'10:00 BC'::time`, `'10:00 AD'`, `'10:00:00 PST BC'`, `'10:00 AM BC'` | `10:00:00` |
| `'12:00 AM'::time` | `00:00:00` |
| `'10:00 UTC-5'::timetz` / `'10:00 UTC+5'` / `'10:00 GMT+3'` | `+05` / `-05` / `-03` (POSIX sign inverted) |
| `'10:00 A.M.'::time`, `'10:00 P.M.'`, `'10:00 ABC-DEF'`, `'10:00 Foo.Bar'` | ERROR 22023 `time zone "a.m." not recognized` (lowercased token) |
| `'10:00 GARBAGE'::time`, `'10:00 zzz'`, `'10:00 Japan'`, `'10:00 EST5EDT'` | ERROR 22007 |
| `'10:00 allballs'::time`, `'10:00 today'`, `'10:00 Mon'`, `'10:00 January'` | ERROR 22007 (a keyword, but not a zone one) |
| `'10:00 a_b'::time`, `'10:00 xy:zw'`, `'10:00 12'`, `'10:00 -'`, `'10:00 .'` | ERROR 22007 |
| `'10:00 pst pdt'::time` | ERROR 22007 (two zone fields) |
| `'10:00 America/New_York'::time` | ERROR 22007 (DST zone, no date) |
| `'2003-03-07 15:36:39 America/New_York'::time` | `15:36:39` (the date resolves it) |
| `'2020-01-01 10:00 A.M.'::time` | ERROR 22023 — the split survives a date prefix |

### 17.5 Verification

`internal/executor/time_zone_token_test.go` drives all 31 oracle-pinned inputs
through **both** `parseTimeString` and `parseTimeTZString` from one shared table
(the sibling-paths rule: a green test on one proves nothing about the other),
plus the offsets `timetz` keeps, the dated/bare DST-zone pair, and
`fixedZoneOffset`'s fixed-vs-DST separation. Mutation-checked twice: collapsing
the 22023 arm into bad-format turns 8 subtests red, and calling a DST zone
fixed turns 7 red.

End-to-end on a throwaway capped server (port 5533), all 14 probes byte-identical
to PG including the COPY TEXT reader, which now reports
`'10:00 GARBAGE'` in a `time` column instead of storing `10:00:00`.

Gates: `go test ./internal/executor/ ./internal/pgdatetime/ ./internal/config/
./internal/pgarray/ ./internal/pgnodes/` PASS, `TestPort_RegressSuite` PASS
(558 s), `RALPH_PRECOMMIT_SCOPE=units` PASS, `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35).

### 17.6 Deferred (ledger rows 2026-08-13)

- **`pg_tzset()`'s POSIX TZ parser is not reproduced.** `'10:00 EST.5'::timetz`
  is `-05` on PG; goopg raises 22023 because `.5` is not the `±hh` shape and
  `time.LoadLocation("est.5")` fails. Reaching full parity means porting
  upstream's `tzparse()`, which is a slice of its own.
- **`fixedZoneOffset` samples rather than reads the transition list.** A zone
  whose only transitions fall outside the ten probe instants would be called
  fixed and would then be accepted on a bare time with one arbitrary offset. No
  zone in tzdata 2025b behaves that way, but the check is a stand-in for
  `pg_get_timezone_offset()`, not a port of it.
- **The abbreviation table is still goopg's 40-entry `tzAbbrevOffsets`, not the
  GUC-selected file.** `timezone_abbreviations` is not implemented, so an
  abbreviation PG's `Default` file carries and goopg's map does not (e.g. `WITA`,
  `CHAST`) is 22007 here and accepted there.
