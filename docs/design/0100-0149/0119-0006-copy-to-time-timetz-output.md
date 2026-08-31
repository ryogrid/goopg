# `COPY … TO` of a `time`/`timetz` column (M0119-0006, 49th slice)

- status: accepted
- date: 2026-08-13
- supersedes: nothing; closes the deferral-ledger row of 2026-08-13 filed by the
  48th slice ("the encode SIBLING does not exist")
- related: `0119-0006-timetz-session-zone.md` (the 47th/48th slices — the INPUT
  side of the same two types), `0119-0006-timestamptz-output-zone-rendering.md`
  (the same encoder's `timestamptz` arm), `0014-copy.md`

## The gap

`datumToCopyText` (`internal/executor/copy_text.go`) — the single renderer both
the TEXT writer and the CSV writer call — had arms for `int4`, `bool`, `date`,
`timestamp`, `timestamptz` and numeric, and nothing for `time` or `timetz`. Both
fell through to the default branch, whose inner switch is over `Datum.Kind` and
has no `KindTime` case, so:

```
=# COPY t TO STDOUT;
ERROR:  kind 5 cannot encode as time in COPY TEXT
```

Any table carrying a `time` or `timetz` column could not be dumped at all. The
FROM side had had both arms all along (`copyTextToDatum`, most recently widened
by the 48th slice), so the codec's two directions covered different type-sets —
the exact asymmetry Hard-won Rule #2 exists to catch — and `COPY … TO` →
`COPY … FROM` was not a round trip.

## The fix

Two arms delegating to the existing `internal/pgdatetime` ports of `time_out`
and `timetz_out` (`FormatTime`, `FormatTimeTZ`). The alternative — porting the
formatting inline — would have made a *third* copy of `time_out`: the first is
`pgdatetime`'s, the second is `internal/server`'s byte-appending
`appendTimeText` (`dispatch.go:3608`), which cannot be reused here because
`internal/executor` must not import `internal/server`. Package placement, not
new logic, was the whole decision.

Two details `pgdatetime`'s micros-based API needs that the `time.Time` carrier
does not supply, both in the new `copyTimeOfDayMicros` helper:

- **`24:00:00`** is carried as `1970-01-02 00:00:00` (next-day midnight), which
  a naive Hour/Minute/Second read reports as `00:00:00`. The explicit next-day
  probe is the same one `appendTimeText` carries.
- **the declared precision** is applied at OUTPUT, by truncation. goopg has no
  `AdjustTimeForTypmod` port, so a `time(2)` column really does hold full
  microseconds; truncating here and letting `FormatTime` strip trailing zeros
  reproduces `appendTimeText`'s trim-then-strip byte for byte. That is the
  point: the slice must not introduce a NEW `COPY`-vs-`SELECT` divergence.

`timetz`'s offset needs one conversion. `pgdatetime.FormatTimeTZ` takes PG's own
`TimeTzADT.zone` convention — seconds **west** of UTC — while the Datum stores
seconds **east** (in `Scale`, as minutes). The negation is the same one the heap
encoder already does (`codec.go`'s `timetz` arm), and keeping both spelled the
same way is deliberate.

`time_out` ignores `DateStyle` entirely — upstream calls `EncodeTimeOnly` with
`USE_ISO_DATES` unconditionally (`postgres/src/backend/utils/adt/date.c`) — so
unlike the `date`/`timestamp` arms above them, neither new arm takes a style
argument. The test drives all four DateStyles to pin that as behaviour rather
than an omission.

## Verification

E2E on a capped throwaway server (5533) against the PG 18.3 oracle (65432), same
DDL and same rows on both. COPY output now matches `SELECT` output on goopg
exactly, and matches PG on every value except the two pre-existing storage-layer
gaps below, which are identical on both goopg paths.

## Deferred (ledger rows filed the same day)

1. **`'24:00:00'` stored in a `time` column becomes `00:00:00`.** The literal
   still renders `24:00:00`; only the stored value loses it, because
   `pgTimeMicros` (`codec.go:858`) reads the carrier's clock fields. The new
   render-side probe is correct and simply never fires for a heap-decoded Datum.
   Fixing it is an encode↔decode storage change, not a rendering change.
2. **No typmod rounding at input.** Upstream applies the typmod at INPUT and
   *rounds*; goopg stores full microseconds and *truncates* at display.
   Measured: `'23:59:59.999999'` into `time(2)` is `24:00:00` on PG 18.3 and
   `23:59:59.99` on goopg — on both the COPY and the SELECT path. Porting
   `AdjustTimeForTypmod` moves the value on disk, so it cannot ride a
   COPY-rendering slice; when it lands, the output-side truncation in
   `copyTimeOfDayMicros` **and** in `appendTimeText` must be deleted, or the
   precision is applied twice.
