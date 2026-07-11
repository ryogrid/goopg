(idle — nothing in flight)

## Loop summary (2026-07-11, loop #42)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items already
`[x]` in fix_plan.md (same batch as loops #37–#41, no new batch). Triage complete.

**Task — `unimplemented_feat #5(d-iv)` EXTRACT numeric display scale — DONE
(committing this loop).** Closed the deferred "EXTRACT trailing-zero scale gap"
(`6.5` vs PG `6.500000`). PG's `EXTRACT` is the numeric-returning spelling
(`retnumeric=true`); fractional-second fields use `int64_div_fast_to_numeric(val,
log10)` whose display scale = `log10` (second=6, millisecond=3, epoch=6, trailing
zeros kept). `date_part(text,…)` is the float8 spelling (`retnumeric=false`) and
MUST stay zero-stripped.
- New `int64DivFastToNumeric` helper (`internal/executor/expr.go`) →
  `Datum{KindNumeric, Int:val, Scale:log10}`; `formatNumeric` renders the zeros.
- Threaded `retnumeric bool` through `evalExtractInterval`: `evalExtract`→true,
  `evalDatePart`→false. Timestamp/time second/millisecond migrated to the helper;
  interval `epoch` numeric line-ported to PG's exact ×4/÷4 integer arithmetic
  (`(1461·(mon/12)+120·(mon%12)+4·day)·21600`, then `(secs·1e6+micros)/1e6`).
- Single funnel (fast-path `evalFastExpr` delegates to `evalExtract`).
Files: internal/executor/expr.go, internal/executor/interval_subday_test.go
(`TestExtractFromInterval` rows), docs/design/0003-0006-date-interval-arithmetic.md
(new Follow-up), .ralph/deferral_ledger.md (row), .ralph/fix_plan.md (checked).
All `want` from live PG 18.3 (socket /tmp:5599).

**Next feature step (deferral ledger 2026-07-11):** NEWLY-DISCOVERED bug —
`EXTRACT(EPOCH FROM timestamp)` returns SECONDS-OF-DAY not full Unix epoch
(`evalExtract` `case "epoch"`, PG `982355920.500000` vs goopg `74320.5`). Fix:
return `int64_div_fast_to_numeric(micros_since_1970, 6)` for timestamp sources,
keep seconds-of-day only for TIME/TIMETZ, add a `retnumeric` split there too.
Also still open: `timestamp ± interval 'infinity'` (needs infinite-timestamp
carrier); cast-form interval typmod (`CAST(... AS interval hour to minute)`).

Gates: build/vet clean; full executor suite PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
