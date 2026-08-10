(idle — nothing in flight)

Last loop (#103): M0119-0006 **38th slice — the TIMESTAMP half of the BC
leap-day race is fixed**, closing the 37th slice's deferral. Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §16 (+ README row),
1 ledger row resolved (37th slice's) + 1 new row appended.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` still run
`20260811-014635` (12 items, unchanged since loop #100). All 12 already
filed under M-NIGHTLY. Nothing new to file. M0130 re-verified CLOSED
(0 unchecked in lines 761-1962 of fix_plan.md), so M0119 stays the head.

What landed: new `bcLeapTimestampFallback` + `bcLeapProxyYear` in
`internal/executor/copy_text.go`, hooked INSIDE `parsePGTimestampTextParts`'s
candidate loop (after each candidate's layout loop fails, before the next
candidate) so the hour-24 / leap-second canonicalized candidate still takes
its turn. The 37th slice's open question — how to thread the time-of-day
fields through the `time.Date` rebuild — is answered by NOT hand-parsing
them: substitute a leap PROXY YEAR (2000) into the date token, re-parse
through the ordinary `pgTimestampLayouts` table (so time/fraction/`T`/zone
stay owned by the shared table), then rebuild the decoded wall clock at the
real astronomical year via `time.Date`. The zone rule is applied to the
PROXY value before the rebuild and its whole-day delta re-applied with
`AddDate`, because `tsApplyZone` can cross midnight.

Oracle (throwaway PG 18.3 on :5599): 6 cells captured, all matching —
incl. `'0001-02-29 00:30:00+05:30 BC'` as timestamp (00:30, offset dropped)
vs timestamptz (0001-02-28 19:00), `'0001-02-29 24:00:00 BC'` →
0001-03-01, `'0005-02-29 10:00:00 BC'` (astronomical -4, leap), and
`'0001-02-30 10:00:00 BC'` → 22008.

Still refused after the fix (deliberate, unchanged): the nanosecond
`KindTime` carrier (1677..2262) cannot STORE any BC value, so these are
22008 either way — what moved is the PATH and therefore the code (was
22007) plus the decoded fields underneath. That carrier move is the
standing 282-reference ledger row.

Next loop: per banner (M-NIGHTLY filing unconditional, then M0130 — CLOSED
— then M0119 top-to-bottom, then M0122). Within M0119-0006 remaining
candidates: the `Datum.Int` KindTime carrier move ns→MICROSECONDS (282
refs, multi-loop, then delete `checkTimeCarrierRange`); `timestamptz`
OUTPUT missing its `+00` suffix; the four target-type-less parse paths
(`tryParseStringAs`, `EXTRACT`, `date_trunc`, `pg_authid` validuntil);
`timetz`'s bare-time zone should be the session `TimeZone` GUC; textual
month names and DateStyle MDY/DMY orders.

Gates: build clean; `go test ./internal/pgdatetime/... ./internal/executor/...`
PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35). pgbench smoke via commit hook.

In-flight: none
