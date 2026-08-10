(idle — nothing in flight)

Last loop (#97): M0119-0006 32nd slice — **the zone a `timestamp` and a `date`
must THROW AWAY**. Committed + pushed. Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §10 (+ README row
edit), 3 ledger rows (1 resolved, 2 new).

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), all already filed under M-NIGHTLY (loop #87).
Nothing new to file; they stay PARKED per the banner. No unchecked M0130 item
remains, so M0119-0006 is the actionable head.

What landed: `internal/executor/copy_text.go` gained `tsZoneMode`
(`tsApplyZone`/`tsDiscardZone`), `tsZoneModeForType`, `applyTSZoneMode` and the
mode-taking `parsePGTimestampTextZone`/`parseCopyTimestampZone`. Every input
path that knows its target type passes the rule: typed literal
(`evalTypedStringLit`), `::timestamp`/`::timestamptz`/`::date` casts
(`evalCast`), `pg_input_is_valid`, `copyTextToDatum`, `encodeValuePG`.

What it FIXED: `'…10:00:00+05:30'::timestamp` answered `04:30:00` (PG
`10:00:00`), and `'2020-01-02 02:00:00+05:30'::date` answered `2020-01-01` (PG
`2020-01-02`) — an offset crossing midnight moved the STORED DAY.

Next loop: per banner. Candidates, all from ledger rows: the CARRIER move of
`Datum.Int` for `KindTime` ns→MICROSECONDS (282 refs, multi-loop, then delete
`checkTimeCarrierRange`); `timestamptz` OUTPUT missing its `+00` suffix (needs
`TimeSub` populated at every timestamptz producer — the open half of the
2026-08-06 M0127-P5.9-u row); the four target-type-less parse paths
(`tryParseStringAs`, `EXTRACT`, `date_trunc`, `pg_authid` validuntil);
`timetz`'s bare-time zone should be the session `TimeZone` GUC not `+00`;
hour-24 / leap-second next-day rollover for TIMESTAMP; unvalidated TIME zone
suffix (`'10:00 A.M.'` accepted, PG errors).

Gates: build + vet clean; units PASS (2 stale expectations updated:
`TestEvalTypedStringLitTimestampForms` offset cases are now per-type,
`TestTimestampLiteralAndCopyPathsAgree` compares both paths under the SAME
type's rule); `TestPort_RegressSuite` PASS (487 s, needs `-timeout 40m` — the
default 600 s cut kills it); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
pgbench smoke via the commit hook. Wants captured from a throwaway PG 18.3
cluster (socket /tmp, port 5599, datadir /tmp/pgoracle-loop97) diffed against a
throwaway goopg on 5544 over literal/cast/INSERT/COPY — both stopped and
removed.

In-flight: none
