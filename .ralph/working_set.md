(idle — nothing in flight)

## Loop summary (2026-07-11, loop #43)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(TestPort_IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes / resolved). Triage complete,
no new work.

**Task — `unimplemented_feat #5(d-iv)` EXTRACT(EPOCH) full Unix epoch — DONE,
committing this loop.** Closed the prior loop's newly-discovered VALUE bug:
`EXTRACT(EPOCH FROM timestamp)` / `date_part('epoch', …)` returned only
SECONDS-OF-DAY not the full Unix epoch (PG `982355920.5` vs goopg `74320.5`).
Rewrote the `epoch` case in BOTH sibling paths (`evalExtract` numeric,
`evalDatePart` float8) source-type dependent, line-porting PG
timestamp_part/timetz_part/extract_date DTK_EPOCH:
  timestamp/timestamptz → full Unix epoch µs scale 6; time → seconds-of-day
  scale 6; timetz → local seconds-of-day − offset scale 6; date → int seconds
  scale 0.
New `timeOfDayMicros` helper. `evalExtract` picks arm from `x.SourceTypeName`
(+flagDate); `evalDatePart` distinguishes only timetz via `Scale!=0`, else full
Unix epoch uniformly (correct for `time` since it's stored on 1970-01-01).
Files: internal/executor/expr.go, internal/executor/interval_subday_test.go
(new `TestExtractEpochFromTimestamp`, 11 cases), docs/design/0003-0006-date-interval-arithmetic.md
(Follow-up table), .ralph/deferral_ledger.md (row), .ralph/fix_plan.md (item).
All `want` from live PG 18.3 (socket /tmp:5599).

**Next feature step (deferral ledger 2026-07-11):** remaining #5(d-iv) items —
(1) `timestamp ± interval 'infinity'` (needs a NEW infinite-timestamp Datum
carrier + `timestamp_pl_interval` TIMESTAMP_NOT_FINITE short-circuit in
`addTimeInterval`); (2) cast-form interval typmod `CAST(... AS interval hour to
minute)` / `interval(p) '...'` (real type-name typmod path + AdjustIntervalForTypmod);
(3) interval-epoch numeric int64 overflow fallback in `evalExtractInterval`.

Gates: build/vet clean; full executor suite PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
