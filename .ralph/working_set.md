(idle — nothing in flight)

## Loop summary (2026-07-11, loop #17)

**Outcome: landed the PARSER half of sub-day interval units
(unimplemented_feat #5 RESOLVED). Sub-day interval *literals* now parse
end-to-end. Real feature, gated, committed + pushed.**

- Nightly triage: `ci/logs/action-items.md` AI-20260710-011513-001 already
  resolved (`[x]` fix_plan L6123/6204); HEAD builds clean. No new work.
- Task: unimplemented_feat #5 (sub-day interval units — parser half).
- Change: added `hour/minute/second/millisecond` (+plural) cases to all four
  unit switches:
  - `internal/parser/select.go` Form 1 trailing-unit (`interval '2' hour`)
    + `splitEmbeddedInterval` Form 2 (`interval '2 hours'`).
  - `internal/executor/expr.go` `evalIntervalLit` (typed literal) +
    `parseIntervalCastString` (`::interval`/`CAST` cast).
  - Convert magnitude → micros via `NewIntervalDatumFull(0,0,µs)` using new
    `usecsPerHour/Minute/Second/Milli` consts (siblings of `usecsPerDay`).
  - `parseIntervalCastString` signature grew `micros int64`; sole caller now
    builds via `NewIntervalDatumFull`.
- Rendering reuses the loop-#16 PG-verified `formatInterval`; analyzer untouched
  (already types every `IntervalLit` as `interval`).
- Tests: `internal/executor/interval_subday_test.go` `TestSubDayIntervalLiterals`
  (Form 1/Form 2/cast/arithmetic/timestamp+interval).
- Bookkeeping: unimplemented_feat.json #5 → resolved (JSON valid, surgical);
  deferral ledger row appended; design 0003-0006 new Follow-up section (README
  already indexes 0003-0006). Did NOT edit fix_plan.md (driver churn).
- Gates (all PASS): build/vet clean; executor/analyzer/planner + parser suites;
  tpch-spotcheck (Q12=2/Q13=33); pgbench smoke (pre-commit hook).

**Still deferred** (ledger): fractional magnitudes (`interval '1.5 seconds'`,
ParseInt rejects decimals), multi-field literals (`'1 day 05:00:00'`, needs a
DecodeInterval tokenizer), week/decade/microsecond units, and `date−date→integer`
(flagDate blast radius). Next natural: fractional-second literals OR continue
unimplemented_feat.json survey (~180 open).

In-flight: none
