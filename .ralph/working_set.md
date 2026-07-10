(idle — nothing in flight)

## Loop summary (2026-07-11, loop #30)

**Nightly triage:** batch 20260711-011536 already fully dispositioned by loop #29
(all 3 AI items checked off, no new batch). Proceeded to feature work.

**Task — `unimplemented_feat #5(d-iii-rest)` year-month/time glued unit-word
absorption — DONE (committed, pushed).** Closed the prior ledger row's deferred
"bare unit word glued to a year-month field" item + spaced/time siblings.
`interval '1-2h'`/`'1-2 h'`/`'1-2 days'`/`'1-2mon3d'`/`'1-2h30m'` → 1 year 2 mons(+…);
`interval '12:00 h'`/`'12:00h'`/`'05:00 mon'` → the bare time. Absorbed unit is a
no-op (PG right-to-left DecodeInterval: year-month/time field to the LEFT resets
the pending unit type without consuming a magnitude).

Files:
- internal/parser/interval.go — `decodeIntervalFields` gains `prevAbsorbs`;
  `splitYearMonthFraction`→`splitYearMonthTrailer` (peel trailing letter run,
  only with ≥1 month digit); `expandIntervalFields` `:` branch peels a trailing
  letter run off time fields.
- internal/parser/select.go — `splitEmbeddedInterval` guarded to require a plain
  ParseIntervalMagnitude first field (sibling-path fix: `1-2 days` was raising
  "invalid interval count" before reaching ParseIntervalBody).
- internal/executor/interval_subday_test.go — new `TestYearMonthTimeGluedUnitAbsorb`
  (32 accepts + 13 rejects, byte-for-byte vs live PG 18.3 on port 5599).
- docs/design/0003-0006-date-interval-arithmetic.md — Follow-up section.
- .ralph/deferral_ledger.md — signed `-1-2h` deferred (needs DTK_TZ-shaped splitter).
- .ralph/fix_plan.md — item checked off.

Next feature step (deferral ledger 2026-07-11): SIGNED year-month glued form
`interval '-1-2h'` → PG -1 years -2 mons. PG lexes a sign-prefixed field as one
DTK_TZ token (collects digits/`:`/`.`/`-`, STOPS at a letter), so `-1-2h` splits
but `-1-2.5` stays whole & errors. Needs a separate DTK_TZ-shaped splitter in
expandIntervalFields for the signed branch; do NOT reuse the unsigned DTK_DATE
splitter (its `.`-vs-letter boundary differs). Then typmod grammar + ±infinity.

Gates: go vet parser+executor clean; parser/analyzer/planner/executor suites PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook; ralph-state-guard OK.

In-flight: none
