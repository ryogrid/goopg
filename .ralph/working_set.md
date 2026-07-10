(idle — nothing in flight)

## Loop summary (2026-07-11, loop #19)

**Outcome: landed multi-field / HH:MM:SS interval literals (unimplemented_feat
#5(b)) end-to-end. goopg can now re-parse its own intervalout output. Real PG
feature, byte-for-byte verified vs PG 18.3, all gates green.**

- Nightly triage: `ci/logs/action-items.md` AI-20260710-011513-001 already
  resolved (`[x]` fix_plan L6123); `make build` clean. No new nightly work.
- Task: interval deferred item (b) — multi-field + time bodies.
- Design (the key part): hoisted the pure interval-body math into a NEW
  `internal/parser/interval.go` as the single source of truth:
  `ParseIntervalMagnitude`, `IntervalUnitToParts` (both MOVED from executor),
  and the new `ParseIntervalBody` tokenizer (PG DecodeInterval-lite: `<num>
  <unit>` pairs any order + `[+-]HH:MM[:SS[.ffffff]]` time words, per-field
  signs, abbreviations mon(s)/min(s)/sec(s)/hr(s)). Executor's two sibling
  paths now DELEGATE: `evalIntervalLit` (typed lit) uses parser helpers;
  `parseIntervalCastString` (`::interval`/CAST) is a one-line
  `parser.ParseIntervalBody` call. Multi-field bodies decode once at parse time
  into `IntervalLit.PreMonths/PreDays/PreMicros` (`PreComputed` flag, threaded
  through 2 planner.go conversions + plpgsql_runtime.go, same as `Qualified`).
- Files: internal/parser/interval.go (NEW), internal/parser/{expr.go,select.go},
  internal/planner/{plan.go,planner.go}, internal/executor/{expr.go,
  plpgsql_runtime.go}, interval_subday_test.go (+TestMultiFieldIntervalLiterals,
  +TestParseIntervalBodySingleFieldMatchesUnitToParts sibling guard),
  interval_cast_test.go (invalid-syntax cases updated), docs/design/0003-0006-*
  (new Follow-up) + README, deferral_ledger.md, fix_plan.md.
- Gates (all PASS): build/vet clean; executor/parser/planner/analyzer suites;
  tpch-spotcheck Q12=2/Q13=33; pgbench smoke (pre-commit hook).

**Still deferred** (ledger 2026-07-11): (d-i) bare-number default-unit
(`interval '5'`→seconds, PG typmod default) — goopg rejects it (22007); (c)
week/decade/century unit names; (d-ii) single-letter units (h/m/s/d/y/w +
m/s positional ambiguity); (d-iii) full interval typmod grammar (`HOUR TO
MINUTE` ranges, `SECOND(p)`). Next natural: (d-i)+(d-iii) default-unit +
typmod grammar together, OR continue unimplemented_feat survey.

In-flight: none
