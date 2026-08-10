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
