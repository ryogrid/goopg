(idle — nothing in flight)

## Loop summary (2026-07-11, loop #18)

**Outcome: landed fractional interval magnitudes (`interval '1.5 hours'`)
end-to-end AND discovered/implemented Form-1 SQL typmod truncation. Real PG
feature, byte-for-byte verified vs PG 18.3, committed + pushed.**

- Nightly triage: `ci/logs/action-items.md` AI-20260710-011513-001 already
  resolved (`[x]` fix_plan L6123); `make build` clean. No new nightly work.
- Task: interval deferred item (a) fractional magnitudes (continuation of
  loop #17's sub-day parser half).
- Two semantics implemented:
  1. **Fractional spill** (typmod-free forms — Form 2 `interval '1.5 hours'`,
     `::`/CAST): `parseIntervalMagnitude` splits body→(val int64, fval float64);
     `intervalUnitToParts` (now the SINGLE shared helper for evalIntervalLit +
     parseIntervalCastString) spills per PG DecodeInterval (years→months
     rint*12, months→days int*30+rem micros, days→micros, h/m/s/ms→(val+fval)*
     scale); `intervalFractMicros` = AdjustFractMicroseconds (trunc + strict
     >0.5 round). → 1.5 hours=01:30:00, 1.5 months=1 mon 15 days.
  2. **Form-1 typmod truncation** (`interval '1.5' hour`=01:00:00, NOT 01:30):
     trailing unit is an SQL typmod field → truncate below granularity toward
     zero (AdjustIntervalForTypmod). New `Qualified` flag on parser+planner
     IntervalLit (set only in parser Form-1, threaded thru 2 planner.go
     conversions + plpgsql_runtime.go) gates new `truncIntervalToUnit`.
- Cache widened `CachedN int32`→`CachedMonths/Days/Micros`.
- Files: internal/executor/expr.go (helpers+evalIntervalLit+parseIntervalCast),
  internal/parser/{expr.go,select.go}, internal/planner/{plan.go,planner.go},
  internal/executor/plpgsql_runtime.go, interval_subday_test.go,
  docs/design/0003-0006-*.md (new Follow-up), deferral_ledger.md.
- Tests: `TestFractionalIntervalLiterals` (29 cases). Verified end-to-end vs a
  live cmd/goopg + psql AND vs real PG 18.3 (identical output).
- Gates (all PASS): build/vet clean; executor/parser/planner/analyzer suites;
  tpch-spotcheck Q12=2/Q13=33; pgbench smoke (pre-commit hook).

**Still deferred** (ledger): (b) multi-field literals `'1 day 05:00:00'` (needs
DecodeInterval tokenizer); (c) week/decade/century/microsecond unit names;
(d) full interval typmod (ranges `HOUR TO MINUTE`, `SECOND(p)`, non-standard
trailing word→column alias over bare `interval '1.5'`=seconds; goopg still
treats `millisecond` as a unit in Form 1 — pre-existing loop-#17 divergence).
Next natural: (b) multi-field tokenizer OR continue unimplemented_feat survey.

In-flight: none
