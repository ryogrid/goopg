(idle — nothing in flight)

## Loop summary (2026-07-11, loop #40)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items already
`[x]` (2 co-load timing flakes + 1 resolved), same batch as loops #37–#39, no new
batch → triage complete. Proceeded to feature work.

**Task — `unimplemented_feat #5(d-iv)` EXTRACT/date_part FROM interval — DONE
(committing this loop).** Closed the "extract-from-interval unsupported for ANY
interval" blocker the prior #5(d-iv) infinity rows named.
- New `evalExtractInterval` (internal/executor/expr.go) line-ports PG
  `interval_part_common` (timestamp.c:6098): NO justification (`interval2itm` —
  hour may exceed 24, day verbatim); integer fields→int8, second/ms/epoch→numeric;
  `quarter` from `interval->month` (negative interval negates sign-reversed field);
  epoch weighting 365.25/30/86400. ±infinity sentinels → ±Infinity (monotonic
  units) / NULL (oscillating) / error, per `NonFiniteIntervalPart`.
- `intervalUnitError`: 0A000 for DecodeUnits-known-but-unsupported
  (dow/isodow/doy/isoyear/julian/timezone*), 22023 for unknown.
- Sibling `date_part('field', interval)` (`evalDatePart`) routes KindInterval
  through the same helper.
- Wired the branch into `evalExtract` (before the KindTime coercion).
Files: internal/executor/expr.go (evalExtractInterval + intervalUnitError +
evalExtract/evalDatePart branches), internal/executor/interval_subday_test.go
(new TestExtractFromInterval: 24 accepts incl. 4 date_part + 3 NULL + 2 error),
docs/design/0003-0006-*.md (new Follow-up), docs/design/README.md (index note),
.ralph/deferral_ledger.md (new row), .ralph/fix_plan.md (checked item).
All `want` values captured from live PG 18.3 (local_install, unix socket).

**Next feature step (deferral ledger 2026-07-11):** remaining #5(d-iv) items —
(1) `timestamp ± interval 'infinity'` (needs a NEW infinite-timestamp carrier +
timestamp_pl_interval short-circuit in addTimeInterval); (2) unary
`- interval 'infinity'` (interval_um: NOBEGIN↔NOEND swap + overflow-guarded negate,
extend evalUnary `-` arm); (3) cast-form typmod `CAST(... AS interval hour to
minute)` / `interval(p) '...'` (type-name typmod path + AdjustIntervalForTypmod);
(4) EXTRACT numeric trailing-zero scale gap (`6.5` vs PG `6.500000`) — shared with
timestamp EXTRACT path, scope on its own with a full EXTRACT re-verify.

Gates: build/vet clean; executor suite PASS; values cross-checked vs PG 18.3;
tpch-spotcheck + pgbench smoke via pre-commit hook.

In-flight: none
