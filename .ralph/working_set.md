(idle — nothing in flight)

Last loop (#96): M0119-0006 31st slice — **the time-of-day FIELD ROLES, and a
second silent twelve-hour error**. Committed + pushed. Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §9 (+ README row edit),
2 ledger rows.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), all already filed under M-NIGHTLY (loop #87).
Nothing new to file; they stay PARKED per the banner.

What landed: `internal/pgdatetime/timeofday.go` — `ParseTimeOfDay` reimplements
PG's `DecodeTimeCommon` + the time arms of `DecodeNumberField`, and
`CanonicalizeTimeToken` rewrites just the time token of a timestamp so the date
/zone layouts still decode the rest. All three text paths (`parseTimeString`,
`parseTimeTZString`, `parsePGTimestampText`) share it. `stripTimeZoneSuffix`
replaces the truncate-at-first-space zone strip that ate the meridiem.

What it FIXED beyond acceptance: `'12:00 AM'` decoded as `12:00:00` (PG:
`00:00:00`) — twelve hours wrong, no diagnostic; `'13:00 PM'` answered
`13:00:00` where PG raises 22008.

Next loop: per banner (M0130 fully checked → M0119-0006 is the actionable head).
Strongest candidate is still the CARRIER: move `Datum.Int` for `KindTime` from
nanoseconds to MICROSECONDS (282 `KindTime` references — multi-loop; audit every
`.Int` consumer: storage codec, wire binary in server/dispatch.go, sort/hash/
spill, interval arithmetic — then delete `checkTimeCarrierRange`). Cheaper, all
from this loop's ledger rows: `timestamp` WITHOUT tz must DISCARD a decoded zone
offset (`'2020-01-01 10:00:00+05:30'` → goopg `04:30:00`, PG `10:00:00` — split
`pgTimestampLayouts` by target type); `timetz`'s bare-time zone should be the
session `TimeZone` GUC not `+00`; hour-24 / leap-second next-day rollover for
TIMESTAMP (`CanonicalizeTimeToken` needs a day-carry out-param); unvalidated
TIME zone suffix (`'10:00 A.M.'` accepted, PG errors).

Gates: build + vet clean; units PASS; `TestPort_RegressSuite` PASS (672 s — one
run failed only on the flaky `TempDir RemoveAll` cleanup, rerun clean);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench smoke via the commit
hook. Wants captured from a throwaway PG 18.3 cluster (socket /tmp, port 5599,
datadir /tmp/pgoracle-loop94) diffed against a throwaway goopg on 5544 — both
stopped.

In-flight: none
