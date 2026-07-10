(idle — nothing in flight)

## Loop summary (2026-07-11, loop #31)

**Nightly triage:** batch 20260711-011536's 3 AI items (IsolationTimeouts,
IsolationTuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip) all already
[x] in fix_plan (loops #29/#30). No new batch. Proceeded to feature work.

**Task — `unimplemented_feat #5(d-iii-rest)` SIGNED year-month glued unit-word —
DONE (committed, pushed).** `interval '-1-2h'` → -1 years -2 mons; `'+1-2h'` →
1 year 2 mons; `'-1-2mon3d'` → -1 years -2 mons +3 days; `'-1-2h30m'` →
-1 years -2 mons +00:30:00; `'-1-h'`/`'-1-day'` → -1 years (empty-month asymmetry
vs unsigned `1-h` which errors). PG lexes a sign-prefixed field as ONE DTK_TZ
token (collects digit|:|.|-, stops at a letter); its DTK_TZ→DTK_NUMBER
years-months branch flows the sign into BOTH year and month.

Files:
- internal/parser/interval.go — new `splitSignedTZTrailer` (DTK_TZ collection set:
  peels a trailing letter run, leaves `.` inside, collects year-month `-` even on
  empty month); `expandIntervalFields` `-`-branch dispatches signed→it,
  unsigned→`splitYearMonthTrailer`.
- internal/executor/interval_subday_test.go — `TestYearMonthTimeGluedUnitAbsorb`
  now 52 accepts + 17 rejects (20 new signed cases, byte-for-byte vs live PG 18.3
  port 5599); removed the old `-1-2h` reject.
- docs/design/0003-0006-date-interval-arithmetic.md — new Follow-up section;
  docs/design/README.md row updated.
- .ralph/deferral_ledger.md + fix_plan.md — signed form checked off; `-1-2+3`
  `+`-continuation deferred.

Next feature step (deferral ledger 2026-07-11): the `+`-separated numeric
continuation `interval '-1-2+3'` → PG -1 years -2 mons +00:00:03. PG's DTK_TZ
stops at `+`, which begins a FRESH DTK_TZ field (default seconds). Needs
`expandIntervalFields` made re-entrant on the remainder after a DTK_TZ token (not
just peeling a letter run in `splitSignedTZTrailer`). Then full interval typmod
grammar (`HOUR TO MINUTE`, `SECOND(p)`) + interval ±infinity.

Gates: go build+vet parser/executor clean; parser/analyzer/planner/executor
suites PASS; tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit
hook; ralph-state-guard OK (auto-repaired stale completed marker).

In-flight: none
