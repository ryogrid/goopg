(idle — nothing in flight)

## Loop summary (2026-07-12, loop #66)

**Nightly triage:** action-items batch `20260711-011536` (same as #58–#65) —
all 3 AI items already `[x]` in M-NIGHTLY. No new nightly work.

**Task — M0122-0004 follow-up: RANGE interval-offset sign matches PG.**
The working_set candidate "RANGE window value-offset — only interval-sign edge
deferred" (deferral-ledger row 716 item 2). `rangeOffsetNegative`
(internal/executor/operators_window.go) rejected an interval RANGE offset when
ANY component was negative (`months<0||days<0||micros<0`); PG's
`in_range_interval_interval` (timestamp.c) instead uses `interval_sign(offset)<0`
= sign of the linear span `time_micros + (months*30+days)*USECS_PER_DAY`
(interval_cmp_value). So `INTERVAL '1 mon -10 days'` (+20-day span) was wrongly
`22013`-rejected. Fixed via the same overflow-safe day/frac decomposition
`compareDatum` already uses for interval ordering (sign = sign(days) if days≠0
else sign(frac)); ±infinity sentinels handled (NOEND +, NOBEGIN −).

Landed:
- internal/executor/operators_window.go: rewrote `rangeOffsetNegative` KindInterval arm.
- internal/executor/window_compat_test.go: `TestRangeOffsetNegativeIntervalSign`
  (10 sign cases incl. the +20-day mixed-sign edge and span-exactly-0).
- docs/design/0122-0004-range-offset-window-frame.md: new Follow-up section
  (moved item out of Deferred). docs/design/README.md row note updated.
- .ralph/deferral_ledger.md: appended `resolved` row (row 716 item 2 closed;
  item 1 — per-type in_range parse-time 0A000 catalog — still open).
- .ralph/fix_plan.md: M0122-0004 bucket dated closure note.

Gates: go build ./... clean; executor Window/Interval/Range/Frame tests PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); ralph-state-guard consistent (auto-repaired
prev clean-exit marker). gofmt: my new lines clean; a pre-existing
compareSortDatums comment diff (go1.25 vs local 1.26.3) left untouched per rule.

Next-loop candidates (open, bounded-ish):
- M0122-0004 remaining: general frame-clause combining forms; per-type in_range
  parse-time 0A000 catalog (ledger row 716 item 1) — cross-cutting.
- pg_get_expr stub entry (if any) — already functional pass-through; bookkeeping.
- ALTER DOMAIN SET SCHEMA: LARGE — no Domain.Schema field.

In-flight: none
