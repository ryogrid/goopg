(idle — nothing in flight)

Last loop (#91): M0119-0006 26th slice — **date/time/timestamp/timestamptz/
timetz/bytea ARRAY ELEMENT images**. Design
`docs/design/0119-0006-array-element-datetime-images.md`, index row `0119-0006t`,
3 ledger rows.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), ALL filed by loop #87 — nothing new to add.
Eleven remain PARKED per banner; re-run their repros at HEAD before
investigating.

What landed + what it FOUND: `pgarray.ElemTypeInfo` had no arm for any date-time
type or `bytea`, so those six array columns took the *unknown element* fallback —
`elemtype 25` (text) over the characters the user typed, while
`pg_attribute.atttypid` said `_date`/`_time`/`_timestamp`/`_timestamptz`/
`_timetz`/`_bytea` (confirmed at HEAD). Invisible inside goopg, wrong for any
descriptor-trusting reader. Five user-visible answers also moved onto PG (the
text path echoed the INPUT spelling: `{2020-1-2}`, `{1:2:3}`,
`{04:05:06.100000}`, a `+02` timestamptz, `{01:02:03+05:00}`). Encode now
DELEGATES to `encodeValuePG` with the scalar element type; decode renders through
new leaf `pgdatetime.Format{Date,Time,Timestamp,TimestampTZUTC,TimeTZ}` (+
`pgarray.ByteaOutHex`, which `executor.byteaOutHex` now delegates to). Separate
find: goopg never applied upstream's TRAILING element alignment
(`construct_md_array` re-aligns after the last element too), so a 1-element
`timetz[]` was 36 bytes where PG writes 40.

Banner state: M-NIGHTLY filing done; M0130 fully checked; banner falls through to
M0119 (M0119-0005 blocked on missing hash/gin/gist/spgist/brin AMs, so
M0119-0006 is the actionable head), then M0122.

Next loop: per banner, M0119-0006 again. Named remaining: `box`/`int4range` key
encodings (both types unsupported in goopg entirely) and the whole-database
unscoped pg_amcheck run (blocked on verify_heapam over system-catalog relkinds).
Cheapest actionable successors from this loop's ledger rows: lift
`decodeArrayKeyElemText`'s refusal for the five types that now HAVE a heap
element image (re-arms the IOS fast path), or the `parseCopyTimestamp` BC /
`HH:MM` input gap. Also still open: array SLICES `a[1:2]` (rejected by the
LEXER), TOASTed / multi-dim / NULL-element arrays in logical decoding, a
subscriber round-trip E2E over a publication on an array column.

Gates: build + vet clean; units (`RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`) PASS; `TestPort_RegressSuite` PASS (codec
change, Hard-won Rule #5 — needs `-timeout 45m`, it takes ~12 min and the go
default 10 m kills it); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
pgbench smoke via the commit hook. Three new gates mutation-checked.

In-flight: none
