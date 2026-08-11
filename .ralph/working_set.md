(idle — nothing in flight)

Last loop (#104): M0119-0006 **39th slice — `timestamptz` OUTPUT renders its
zone and leaves UTC**, closing the 2026-08-11 "no zone suffix" ledger row.
Design `docs/design/0119-0006-timestamptz-output-zone-rendering.md` (new) +
README row `0119-0006v`. Ledger: 1 row resolved, 2 new rows appended.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` still run
`20260811-014635` (12 items, unchanged since loop #100). All 12 already filed
under M-NIGHTLY. Nothing new to file. M0130 stays CLOSED, so M0119 is head.

What landed: new `internal/config/timestamptz_out.go`
(`FormatTimestampTZ`, `encodeTimezone`, `sessionLocation` with a sync.Map
tzdata cache) porting `EncodeDateTime(print_tz=true)` + `EncodeTimezone`;
`datestyle.go` split into `formatTimestampBody` (body + era returned
separately) so the zone can be spliced BEFORE the " BC" marker.
Both output paths that know the declared column type were split off the
shared `case "timestamp", "timestamptz"`: `dispatch.go` `appendTypedCellText`
and `copy_text.go` `datumToCopyText` (with `timeZone` threaded through
`EncodeCopyTextRow`/`EncodeCopyCsvRow`/`RunCopyTo`).

Bonus bug found while wiring COPY: standalone `COPY … TO STDOUT`
(`dispatchCopyViaExecutor`, internal/server/copy.go) hand-built its
executor.Context and never attached GetSetting/GetSettingDisplay, so it read
NO session GUCs — `SET datestyle` was ignored there too. Now wired like the
inline path. Ledger row asks for a GUC-parity audit of that context.

Key discovery: `Datum.TimeSub` was NOT the prerequisite the old ledger row
claimed — the wire and COPY paths both have the catalog type in hand. Only
the `::text` cast path (`formatTimeDatumDateStyle`) still needs a TimeSub
producer.

Next loop: per banner (M-NIGHTLY filing unconditional, M0130 CLOSED, then
M0119 top-to-bottom, then M0122). Remaining M0119-0006 datetime candidates:
the `Datum.Int` KindTime carrier move ns→MICROSECONDS (282 refs, multi-loop,
then delete `checkTimeCarrierRange`); populate `TimeSubTimestampTZ` producers
(unlocks the `::text` cast zone); POSIX `TimeZone` spellings; the four
target-type-less parse paths (`tryParseStringAs`, `EXTRACT`, `date_trunc`,
`pg_authid` validuntil); `timetz`'s bare-time zone = session TimeZone;
textual month names and DateStyle MDY/DMY input orders.

Gates: build clean; `internal/config` + `internal/executor` + `internal/server`
PASS; `TestPort_RegressSuite` PASS (159s); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (one load-flake in `internal/wal` on the
first run — passed standalone and on re-run); `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35). pgbench smoke via the commit hook.

In-flight: none
