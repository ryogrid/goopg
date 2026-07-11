(idle — nothing in flight)

## Loop summary (2026-07-11, loop #50)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes). No new work.

**Task — closed the infinite-timestamp carrier-row's deferred item:
`timestamp − timestamp` with a ±infinity operand.** `subTimeTime`
(`internal/executor/expr.go`) always took the finite path
(`left.TimeValue().Sub(right.TimeValue())`), so an INT64-extreme timestamp
sentinel was read as an ordinary ~year-2262/far-past timestamp → nonsense finite
interval. Now line-ports PG `timestamp_mi`'s TIMESTAMP_NOT_FINITE block: a single
infinite operand yields the correspondingly-signed infinite INTERVAL via
`NewIntervalInfinity` (`-inf−x=-inf`, `+inf−x=+inf`, `x−(-inf)=+inf`,
`x−(+inf)=-inf`); any same-signed `infinity − infinity` raises 22008 "interval
out of range". Signature became `(Datum,error)` + `pos` arg; single `evalBinary`
caller propagates. Reuses carrier `IsTimestamp{Pos,Neg}Inf`/`IsTimestampNotFinite`
+ `NewIntervalInfinity(bool)`; single arithmetic funnel, no sibling twin.
Verified byte-for-byte vs live PG 18.3 (socket /tmp:5599): infinity/-infinity/
-infinity/infinity/infinity/-infinity/777 days 20:38:40 + 2 ERROR rows.
Test `TestTimestampSubInfinity` (7 accepts + 2 rejects) in new
`internal/executor/timestamp_sub_infinity_test.go`. Design doc 0003-0006 new
Follow-up. Ledger row appended.

Gates: build clean; full executor suite PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook; ralph-state-guard OK.

**Remaining #5(d-iv) gap (deferred, pre-existing):** ONLY `timestamp 'infinity'`
LITERAL-INPUT parsing + `::timestamp` cast + wire codec — goopg produces an
infinite timestamp only via arithmetic short-circuits, never from a typed literal.
Next loop: pick that or a new milestone.

In-flight: none
