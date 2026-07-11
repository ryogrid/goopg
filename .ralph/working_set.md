(idle — nothing in flight)

## Loop summary (2026-07-11, loop #44)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(TestPort_IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes). No new work.

**Task — `unimplemented_feat #5(d-iv)` EXTRACT(EPOCH FROM interval) int64 overflow
— DONE, committed this loop.** Closed deferred item (3) of the prior EXTRACT(EPOCH)
row. `interval_part_common`'s EXTRACT/numeric epoch arm computes
`secs_from_day_month*10^6 + time` in int64; the `·10^6` product overflows int64
around 10^9 days (whole-day boundary `106751991`→`106751992`). goopg wrapped
silently; now mirrors PG's `pg_mul/pg_add_s64_overflow` guard + numeric fallback
`numericAdd(int64DivFastToNumeric(micros,6), numericFromInt(secsFromDayMonth))`.
float8 `date_part` spelling was already safe (double).
Files: internal/executor/expr.go (evalExtractInterval epoch case),
internal/executor/interval_subday_test.go (new TestExtractEpochIntervalOverflow,
9 cases), docs/design/0003-0006-date-interval-arithmetic.md (Follow-up),
.ralph/deferral_ledger.md (row), .ralph/fix_plan.md (item). All `want` from live
PG 18.3 (socket /tmp:5599).

**Next feature step (deferral ledger 2026-07-11, remaining #5(d-iv)):**
(1) `timestamp/timestamptz ± interval 'infinity'` — needs a NEW infinite-timestamp
Datum carrier (TIMESTAMP_NOT_FINITE) + `timestamp_pl_interval`/`_mi` short-circuit
in `addTimeInterval` (expr.go). (2) cast-form interval typmod
`CAST(... AS interval hour to minute)` / `interval(p) '...'` — parse the field
qualifier in the CAST/`::` type-name path (reuse intervalTypmodField/
intervalRangeLowField in parser/select.go), encode packed typmod into
CastExpr.Typmods, apply truncIntervalToUnit/roundIntervalMicrosToPrec in evalCast
interval case.

Gates: build/vet clean; interval/numeric/extract executor tests PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
