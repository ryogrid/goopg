(idle — nothing in flight)

Last loop (#99): M0119-0006 **34th slice — the RUN-TOGETHER numeric date is
ordinary date/timestamp input**. Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §12 (+ README row),
2 new ledger rows.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), all already filed under M-NIGHTLY (loop #87).
Nothing new to file; they stay PARKED per the banner. M0130 is fully checked
(0 unchecked), so M0119-0006 remains the actionable head.

What landed: `pgdatetime.NormalizeDateTimeInput(s, bc)` — a SECOND entry point
beside the unchanged `NormalizeInput` — reproducing `DecodeNumberField`'s date
arm (last 2 digits = day, 2 before = month, the rest = year at any width) plus
`ValidateDate()`'s 1970..2069 2-digit-year window, which BC suppresses. Also
`RunTogetherDateIsTimeAmbiguous` for goopg's one target-type-less path.
Callers switched: `parsePGTimestampTextParts`, `parsePGDateText`, the verbose
fallback, and `tryParseStringAs`'s KindTime probe (guarded).

What it FIXED: `'20200101'`, `'20200101 040506'`, `'20200101T040506'`,
`'200101'`, `'690101'`/`'700101'`, `'040506'::date`, … were ALL 22007;
`date_col = '20040506'` matched zero rows SILENTLY and now matches.

Next loop: per banner. Candidates, all ledger rows: the CARRIER move of
`Datum.Int` for `KindTime` ns→MICROSECONDS (282 refs, multi-loop, then delete
`checkTimeCarrierRange`); `timestamptz` OUTPUT missing its `+00` suffix; the
four target-type-less parse paths (`tryParseStringAs`, `EXTRACT`,
`date_trunc`, `pg_authid` validuntil) — which also closes
`date_col = '040506'`; a `ValidateDate()` port so `'20201301'` is 22008 not
22007; `timetz`'s bare-time zone should be the session `TimeZone` GUC;
textual month names (`'2002-May-1'`) and the DateStyle MDY/DMY orders.

Gates: build + vet clean; units PASS (`RALPH_PRECOMMIT_SCOPE=units`);
`TestPort_RegressSuite` PASS (673 s, needs `-timeout 40m`);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench smoke via the commit
hook. Wants captured from a throwaway PG 18.3 cluster (socket /tmp, port 5599,
datadir /tmp/pgoracle-l99) diffed cast-by-cast against a throwaway goopg on
5544 — 80 cells; both servers stopped and removed.

In-flight: none
