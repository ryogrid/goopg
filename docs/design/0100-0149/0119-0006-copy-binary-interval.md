# Binary `COPY` of `interval` (M0119-0006, 55th slice)

Status: accepted
Date: 2026-08-13
Predecessors: `0119-0006-copy-binary-oid-family.md` (54th),
`0119-0006-copy-binary-float.md` (53rd), `0119-0006-copy-binary-int2.md` (52nd),
`0119-0006-copy-binary-time.md` (51st)

## The gap

`internal/executor/copy_binary.go` had no `interval` arm in either direction, so
both halves fell through to the raw-bytes default:

- **encode.** A stored interval is a `KindInterval` Datum, which matches none of
  the default's `Kind` cases, so `datumToCopyBinary` reached
  `[]byte(d.Format())` and shipped the interval's **text** — `"02:00:00"`,
  `"1 mon"` — under a format declared binary. Upstream `interval_send`
  (`postgres/src/backend/utils/adt/timestamp.c:1022`) is

  ```c
  pq_sendint64(&buf, interval->time);
  pq_sendint32(&buf, interval->day);
  pq_sendint32(&buf, interval->month);
  ```

  a fixed **16 bytes**. A field of any other length is `"incorrect binary data
  format"` to a real client, because `CopyReadBinaryAttribute` runs
  `pq_getmsgend` after every attribute (`copyfromparse.c`) — so the stream was
  unreadable, not merely wrong. Measured at HEAD: 21 bytes for `'1 mon 2 days
  03:00:00'`, and 44 for the sentinel.

- **decode.** `copyBinaryToDatum` handed the 16 bytes back as
  `NewStringDatum(string(payload))`. That is the worse half, and it is worse than
  the analogous `oid` case: an interval column holding a **string** Datum is
  right back in the lexicographic world the heap codec's own `interval` arm was
  written to escape — `'2 hours'` sorting after `'10 days'`,
  `i = interval '30 days'` missing the `'1 mon'` PG calls equal, the value
  echoing back as raw bytes where PG prints `02:00:00`.

## The fix

Both arms, plus one extraction.

PG's `Interval` is `{TimeOffset time; int32 day; int32 month}`
(`postgres/src/include/datatype/timestamp.h`), `pg_type` OID 1186,
`typlen 16`, `typalign 'd'`. goopg's heap has stored exactly that image, at
exactly those offsets, since the `interval` arm of `encodeValuePG` — the wire
image differs **only in byte order** (heap little-endian, COPY big-endian) and
in nothing else, including the field order, which is `time, day, month` and not
goopg's usual months-first spelling.

Because the two are that close, the fields are no longer derived twice. The
inline `switch d.Kind` inside `encodeValuePG`'s `interval` arm moved out to a
shared `pgIntervalFieldsFromDatum(d) (months, days int32, micros int64, err
error)` in `codec.go`, called by the heap encoder and the new COPY arm alike.
This is the third repetition of the same structural move — `pgFloatFromDatum`
(53rd), `pgUnsignedIDFromDatum` (54th) — and it is what makes Hard-won Rule #2
hold by construction rather than by review. It carries the `KindString` entry
point with it (a bare quoted literal is `unknown` upstream and reaches the column
through `interval_in`; `parser.ParseIntervalBody` is the same tokenizer
`'…'::interval` uses) and its 22007, so the COPY encode arm accepts and rejects
exactly what the heap arm does.

The `±infinity` sentinels need no case in either direction:
`INTERVAL_NOEND` / `INTERVAL_NOBEGIN` **are** all-three-fields-at-their-extreme,
so field-wise transport round-trips them exactly — the same reason the heap arm
has no case for them.

## The finding: this time the third twin was already right

The 53rd and 54th slices each found an adjacent *heap* defect through the
`…AgreesWithHeapEncode` pin (the `float` spelling bug; `xid8` halved to 32 bits,
with `internal/wal/pgoutput.go` as a third wrong twin). The pin ran here too —
same shape, same `physicalPGTypeAlign` assertion — and `interval` came back
clean: the heap arm is 16 bytes at the right offsets, `physicalPGTypeAlign`
returns 8 for `typalign 'd'`, and `pgoutput.go`'s `interval` decode already reads
`{micros, days, months}` at 16 bytes with the matching alignment entry.

That is worth recording rather than passing over. The drill's value is not that
it always finds something; it is that a clean result is now *evidence* the three
twins agree, where before this slice it was an assumption. The reason `interval`
was clean and `xid8` was not is visible in the ledger: `interval` got its
fixed-width heap layout in a dedicated slice that fixed all three twins at once,
while `xid8` had been riding a sibling's arm.

## Verification

- `internal/executor/copy_binary_interval_test.go` — six guards, **all six
  verified failing at HEAD** by scripted revert: `send` shape (16 bytes and the
  `time, day, month` field order specifically, which is the obvious way to be
  silently wrong), round-trip against `decodePhysicalPGValueMctx`'s Datum,
  `AgreesWithHeapEncode` field-by-field plus `physicalPGTypeAlign`, `recv`
  length rule, the `KindString`/22007 entry point agreeing with the
  `KindInterval` one, and a full row through
  `AppendCopyBinaryRow`/`ParseCopyBinaryRows` to pin the framing.
- Oracle E2E on a capped throwaway server (5533) against PG 18.3 (65432), nine
  values incl. `NULL`, `'2000 years'`, a mixed `'1 year 2 mons 3 days
  04:05:06.789'`, an all-negative interval and a sub-millisecond one:
  `\copy … TO … (FORMAT binary)` **byte-identical** (203 bytes); PG's own file
  loaded into goopg and re-exported **byte-identical** again; and the text
  rendering of the PG-authored binary values after ingest identical to PG's.
- `go build ./...` + `go vet` clean; `internal/executor`, `internal/wal`,
  `internal/catalog`, `internal/initdb` suites PASS;
  `RALPH_PRECOMMIT_SCOPE=units`; `scripts/tpch-spotcheck.sh`; pgbench smoke via
  the commit hook.

## Deferred (ledger rows filed)

- `AdjustIntervalForTypmod` is unported and this decode arm is its first
  consumer — upstream `interval_recv` applies the declared `interval(N)` /
  `INTERVAL YEAR TO MONTH` typmod at INPUT, rounding the sub-second field and
  zeroing out-of-span range fields. Same blocker and same signature widening
  (`copyBinaryToDatum` takes only `catalog.Type`) as the `time`/`timetz` rows the
  49th and 51st slices filed; it must land once for all three.
- Recorded as a **non**-defect so a later loop does not "fix" it into a
  divergence: `interval_recv` has no range check of its own, unlike
  `time_recv`/`timetz_recv`'s 22008/22009 — upstream accepts every 16-byte
  triple, so goopg does too.
- Still armless in binary `COPY`: `jsonb` (leading version byte) and `bpchar`.
