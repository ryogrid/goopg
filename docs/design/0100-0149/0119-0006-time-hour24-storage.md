# M0119-0006 — `'24:00:00'` survives storage in a `time`/`timetz` column

Status: accepted
Date: 2026-08-13
Slice: M0119-0006, 50th
Closes: the deferral-ledger row filed by the 49th slice
(`'24:00:00'` stored in a `time` column collapses to `00:00:00`)

## The gap

`24:00:00` is a real PostgreSQL `TimeADT` value, not a rejected out-of-range
input. `time_in` decodes it and `AdjustTimeForTypmod`'s range check admits
exactly `USECS_PER_DAY`
(`postgres/src/backend/utils/adt/date.c`), so PG stores, sorts and prints it as
a value strictly above `23:59:59.999999`.

goopg's parsers already accepted it. `parseTimeString("24:00:00")` returns a
`time.Time` of **1970-01-02 00:00:00 UTC** — `time.Date` normalises an hour
field of 24 into the next day, which is the only representation available to a
`time.Time` carrier. The extraction that turns that carrier back into PG's
microseconds-since-midnight, `pgTimeMicros` (`internal/executor/codec.go`), read
only `Hour`/`Minute`/`Second`/`Nanosecond` and therefore reported **0** for it.

Everything downstream of that single function inherited the collapse:

| consumer | symptom before this slice |
|---|---|
| heap encode, `encodeValuePG` `"time"`/`"timetz"` arms | `INSERT … '24:00:00'` stored `0`; the value read back as `00:00:00`. A **silent data rewrite**, not a display bug. |
| `encodeScalarBTreeKey` (`btree_scalar_keys.go`) | the index key sorted `24:00:00` **below** `00:00:01`, i.e. an index disagreeing with its own heap. |
| `timeTzKeyParts` (the timetz GMT-equivalent key) | same collapse, shifted by the zone. |
| `btree_array_key.go`'s element renderer | a `time[]` element printed `00:00:00`. |

The one consumer that was already correct is `copyTimeOfDayMicros`
(`internal/executor/copy_text.go`), added by the 49th slice: it carried a
**private copy** of the next-day probe. That is precisely the sibling-divergence
shape Hard-won Rule #2 exists to prevent — the renderer and the storage encoder
had different ideas of what the same carrier meant, and the renderer's
correctness masked the encoder's bug for any value that never reached the heap.

## The change

The probe moves **into `pgTimeMicros`**, which is the single place every
consumer already routes through — `btree_scalar_keys.go`'s own comment states
that the key must derive from "the same microseconds the heap stores", so
sharing the extraction is the existing contract, not a new one:

```go
func pgTimeMicros(t time.Time) int64 {
	u := t.UTC()
	micros := /* Hour/Minute/Second/Nanosecond, as before */
	if micros == 0 && u.Year() == 1970 && u.Month() == time.January && u.Day() == 2 {
		return usecsPerDay
	}
	return micros
}
```

`copyTimeOfDayMicros`'s duplicate probe is **deleted**, so the two cannot drift
again; it now delegates and keeps only the typmod truncation, which is its own
concern.

The probe is deliberately narrow: it fires only for an exact zero clock reading
on 1970-01-02, so genuine midnight (1970-01-01 00:00:00) still yields 0. That
asymmetry is asserted directly rather than left implicit.

**No decode-side change was needed** — and this was checked, not assumed. The
`"time"`/`"timetz"` decoders in `decodePhysicalPGValueMctx` already admit
`micros == 24h` (their range check is `micros > maxTimeMicros`, inclusive of the
bound) and `pgTimeFromMicros(usecsPerDay)` reconstructs 1970-01-02 00:00:00
naturally by addition. The encode↔decode pair was therefore *already* agreed on
the wire format; only the encoder's input mapping was lossy.

## Verification

`internal/executor/codec_time_hour24_test.go` — four tests, all proven to FAIL
against the pre-fix extraction (`git stash` of the two source files, then
re-run), covering each consumer that must agree:

1. `pgTimeMicros` returns `USECS_PER_DAY`, *and* genuine midnight still returns
   0 (the too-broad-probe guard).
2. the heap encode→decode round trip for **both** `time` and `timetz`, asserting
   the carrier comes back as next-day midnight and the micros as `USECS_PER_DAY`.
3. the btree scalar key orders `00:00:00 < 00:00:01 < 24:00:00`.
4. `datumToCopyText` renders `24:00:00`, at both no typmod and `time(2)`.

Test 4 passed before the fix as well — it is the one covering the consumer that
already had the private probe — and is kept precisely to pin that the renderer
did not regress when its copy was deleted.

E2E on a capped throwaway server (port 5533) diffed against the PG 18.3 oracle
(65432): a 4-row table over `time` + `timetz`, read back plainly, ordered by the
time column, ordered through a forced index scan (`enable_seqscan = off`),
dumped via `COPY … TO STDOUT` in TEXT and CSV, and aggregated with
`max()`/`min()`. **Byte-identical output on every statement.**

## Still deferred (ledger rows, not closed here)

- **`AdjustTimeForTypmod` is still unported.** PG rounds at INPUT; goopg
  truncates at DISPLAY. `'23:59:59.999999'` into a `time(2)` column is
  `24:00:00` on PG and `23:59:59.99` here. This slice makes the *destination*
  value of that rounding representable and storable for the first time — it is
  a precondition for the port, not the port. When it lands, the output-side
  truncation must be deleted from BOTH `copyTimeOfDayMicros` and
  `internal/server`'s `appendTimeText`.
- **`internal/server`'s `appendTimeText` keeps a third copy of the probe.** It
  is in a different package that cannot reach `internal/executor`, which is the
  same package-boundary constraint the 49th slice recorded for `time_out`
  itself. Unifying it needs the shared-port extraction that boundary implies.
- **`copy_binary.go` has no `time`/`timetz` arms at all** (both directions fall
  through to the raw-bytes default), so binary COPY of these types was never
  exercised by this or the preceding slice.
