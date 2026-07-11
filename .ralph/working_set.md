(idle — nothing in flight)

## Loop summary (2026-07-11, loop #49)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes). No new work.

**Task — closed deferred item (2) of the timestamp-infinity carrier row:
`isfinite(timestamp/date/interval)` over the ±infinity sentinels.** `evalIsFinite`
(`internal/executor/expr.go`) was a v0 stub returning TRUE for any non-NULL arg;
after prior loops added the KindTime (INT64 extremes) + KindInterval (NOBEGIN/NOEND)
sentinels it mis-reported `isfinite(interval 'infinity')` / `isfinite(ts + interval
'infinity')` as finite. Now line-ports PG's date_finite/timestamp_finite/
interval_finite: FALSE when `d.IsTimestampNotFinite() || d.IsIntervalNotFinite()`,
else TRUE; NULL still propagates (strict). Single funnel, no sibling twin.
Verified byte-for-byte vs live PG 18.3 (socket /tmp:5599): f|f|f|f|f|t.
Test `TestIsFiniteInfinity` (8 cases) in `isfinite_test.go`; stale
`TestIsFiniteNullPropagates` comment updated. `unimplemented_feat.json`
m0097-0004 entry flipped to resolved (surgical Edit). Design doc 0003-0006
new Follow-up. Ledger row appended.

Gates: build clean; full executor suite PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook; ralph-state-guard OK.

**Remaining #5(d-iv)-adjacent gaps (deferred, pre-existing):** (1) `timestamp
'infinity'` LITERAL-INPUT parsing + `::timestamp` cast + wire codec; (2)
`timestamp − timestamp` with an infinite operand (PG → infinite interval;
`subTimeTime` would call TimeValue() on the sentinel). Next loop: pick from
these or a new milestone.

In-flight: none
