(idle — nothing in flight)

Last loop (#105): M0119-0006 **40th slice — textual month names on
date/timestamp INPUT**. Design `docs/design/0119-0006-textual-month-name-date-input.md`
(new) + README row `0119-0006w`. Ledger: 3 new rows appended.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` still run `20260811-014635`
(12 items, unchanged since loop #100). All 12 already filed; the 11 open ones
are PARKED per the banner (not gates the priority milestones depend on).
M0130 is fully checked (CLOSED), so M0119 is head.

What landed: new `internal/pgdatetime/monthname.go`
(`normalizeTextualMonthDate`, `joinNormalizedDate`, `monthNameValue` = the 21
`MONTH` rows of `datetktbl`), hooked into `normalizeInput` as a THIRD arm after
`padDateFields` / `expandRunTogetherDate`, gated on the `runTogetherDate` flag
so only `NormalizeDateTimeInput` (DecodeDateTime's context) reaches it.

Key discovery: a textual month needs NO DateStyle modelling. `DecodeDate` runs
a text-first pass ("look first for text fields, since that will be unambiguous
month"), so the month is in `fmask` before any numeric run and MON-DD-YYYY /
DD-MON-YYYY / YYYY-MON-DD all take one path. Only `DecodeNumber`'s two
`DATEORDER_YMD`-gated branches were skipped. Under MDY, `'02-May-1'` is
2001-05-02 (oracle-confirmed).

Two oracle-checked refusals (NOT omissions): `'2002-May-1T10:20:30'` is an
ERROR on PG 18.3 though the numeric-month spelling parses; `:` is barred from
the date token so `'10:00 May'` cannot build a date.

Next loop: per banner (M-NIGHTLY filing unconditional, M0130 CLOSED, then M0119
top-to-bottom, then M0122). Remaining M0119-0006 datetime candidates: the
`Datum.Int` KindTime carrier move ns→MICROSECONDS (282 refs, multi-loop —
also the reason NO BC date round-trips end-to-end today); the variable-width
day field that would give `'2002-May-100'` / `'2002-5-100'` PG's 22008 (one fix
closes both, needs `DateTokenMonthDay` widened); routing
`pgnodes.parseDateFields` through `pgdatetime` (planner/executor divergence);
`TimeSubTimestampTZ` producers (unlocks the `::text` cast zone); POSIX
`TimeZone` spellings; the target-type-less parse paths; `timetz`'s bare-time
zone; DateStyle MDY/DMY *numeric* input orders.

Gates: build clean; `internal/pgdatetime` + `internal/executor` + `internal/pgnodes`
PASS; `TestPort_RegressSuite` PASS (251s); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35). pgbench smoke via the commit hook.

In-flight: none
