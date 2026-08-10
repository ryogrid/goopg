(idle — nothing in flight)

Last loop (#102): M0119-0006 **37th slice — the DATE half of the BC
leap-day race is fixed.** Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §15 (+ README row),
1 ledger row resolved (36th slice's row) + 1 new ledger row appended.
Committed pending push to `make-db-cluster-compat`.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` still nightly run
`20260811-014635` (12 items, unchanged from loops #100/#101). Reconfirmed
all 12 already filed under M-NIGHTLY (lines 713, 740, 744, 750 of
fix_plan.md). Nothing new to file. M0130 confirmed fully checked ([x] on
every subtask through S11.6), so M0119-0006 remains the actionable head
(M0119-0005 precedes it in file order but needs hash/gin/gist/spgist/brin
index AMs — out of scope for a single-loop slice).

What landed: new `bcLeapDateFallback` in `internal/executor/copy_text.go`,
tried inside `parsePGDateText` when `time.Parse("2006-01-02", norm)` fails
AFTER `validateDateTokenFull` already confirmed the token valid. It
re-derives month/day (`pgdatetime.DateTokenMonthDay`) and the ASTRONOMICAL
year (`pgdatetime.DateTokenYear` + `pgdatetime.AstronomicalYear`) straight
from the token's digits and builds the `time.Time` directly via
`time.Date(astroYear, month, day, 0,0,0,0, UTC)` — bypassing BOTH
`time.Parse`'s literal-year day check and `ApplyEra`'s literal→astronomical
shift (done by hand instead). `'0001-02-29 BC'::date` no longer raises the
syntax-shaped 22007; it now reaches the carrier-range 22008 the
un-representable astronomical year 0 still deserves (goopg's nanosecond
`KindTime` carrier is 1677..2262, so year 0 is refused either way — just
with the CORRECT SQLSTATE now). `bcLeapDateFallback` returns `ok=false`
(falls through to the original time.Parse error) for `!bc` and for the
`AstronomicalYear` no-year-zero refusal, so behavior for every other input
is unchanged.

What did NOT land: `parsePGTimestampTextParts` (the TIMESTAMP-domain
sibling) has the IDENTICAL bug — deliberately deferred (ledger row
appended) since it composes date and time-of-day in one `time.Parse` call
per layout across two candidate strings, so the bypass needs the
time-of-day fields threaded through too, not just month/day/year. Scoped to
one call site to keep this slice self-contained (Ralph "ONE task per loop"
rule).

Next loop: per banner (M-NIGHTLY filing unconditional, then M0130 — CLOSED
— then M0119 top-to-bottom, then M0122). Within M0119-0006, candidates:
the `parsePGTimestampTextParts` BC leap-day fix just deferred (natural
next slice, same shape as this one but needs time-of-day field threading);
the CARRIER move of `Datum.Int` for `KindTime` ns→MICROSECONDS (282 refs,
multi-loop, then delete `checkTimeCarrierRange`); `timestamptz` OUTPUT
missing its `+00` suffix; the four target-type-less parse paths
(`tryParseStringAs`, `EXTRACT`, `date_trunc`, `pg_authid` validuntil);
`timetz`'s bare-time zone should be the session `TimeZone` GUC; textual
month names and DateStyle MDY/DMY orders.

Gates: build clean; `go test ./internal/pgdatetime/...
./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35). `make ralph-state-guard` PASS (auto-repaired the routine
running/completed loop-boundary marker). pgbench smoke will run via commit
hook.

In-flight: none
