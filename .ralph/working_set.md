(idle — nothing in flight)

## Loop summary (2026-07-11, loop #45)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes). No new work.

**Task — `unimplemented_feat #5(d-iv)` cast-form interval typmod — DONE,
committed this loop.** Closed deferred item (2) of the EXTRACT(EPOCH) row.
`CAST(x AS interval hour to minute)`, `x::interval second(2)`, precision-only
`x::interval(2)` now apply a typmod (was a parse error inside CAST / ignored on
::). Key insight: PG's `interval_in` uses the typmod LOW field as the bare-
magnitude DEFAULT UNIT *before* truncation (`'90'::interval minute`=01:30:00, not
90s), so it can't be post-hoc truncation.
Parser: shared `parseIntervalCastQualifier` wired into `parseCastTail` +
`parseCastFuncExpr`; packs PG `INTERVAL_TYPMOD` (`packIntervalCastTypmod`) into
`CastExpr.Typmods[0]`; `DecodeIntervalCastTypmod` unpacks (internal/parser/
select.go). Executor: `applyIntervalCastTypmod` intercepted in CastExpr branch
(gated `TargetType=="interval" && Typmod!=0`), reuses `truncIntervalToUnit`/
`roundIntervalMicrosToPrec` (internal/executor/expr.go).
Files: internal/parser/select.go, internal/executor/expr.go,
internal/executor/interval_subday_test.go (new TestIntervalCastTypmod, 18 cases),
docs/design/0003-0006-date-interval-arithmetic.md (Follow-up),
.ralph/deferral_ledger.md (row), .ralph/fix_plan.md (item).

**Next feature step (deferral ledger 2026-07-11, remaining #5(d-iv)):**
(1) `timestamp/timestamptz ± interval 'infinity'` — needs a NEW infinite-
timestamp Datum carrier (TIMESTAMP_NOT_FINITE) + `timestamp_pl_interval`/`_mi`
short-circuit in `addTimeInterval` (expr.go). (2) `interval(p) '<lit>'`
leading-precision typed literal — in parsePrimaryExpr's interval case
(select.go ~L2999) detect `interval ( p ) '<str>'` (paren BEFORE the string),
reuse `tryConsumeIntervalPrecParen`, carry onto IntervalLit.HasPrec/Prec.

Gates: build/vet clean; parser + full executor/planner suites PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
