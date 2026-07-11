(idle — nothing in flight)

## Loop summary (2026-07-11, loop #35)

**Nightly triage:** batch 20260711-011536's 3 AI items all already [x] (loops
#29–31; item 003 fixed d212f7ea). No new batch. Proceeded to feature work.

**Task — `unimplemented_feat #5(d-iv)` interval typmod range/precision grammar —
DONE (committing this loop).**
Form-1 interval literals now accept `<hi> TO <lo>` ranges and `SECOND(p)`:
`interval '5' hour to minute`=00:05:00, `'5' day to hour`=05:00:00, `'5' year to
month`=5 mons, `'90' minute to second`=00:01:30, `'1.23456789' second(2)`=
00:00:01.23, `'1.999999' second(2)`=00:00:02. Key: PG's DecodeInterval
`switch(range)` + AdjustIntervalForTypmod both collapse a range to its LOW field
(interpret+truncate there, higher fields kept) → reused single-field `Qualified`
path with unit=lowField. New `tryIntervalTypmodQualifier`/`intervalRangeLowField`/
`intervalTypmodField` (parser/select.go, lookahead-only); `roundIntervalMicrosToPrec`/
`intervalPrecScales` (executor/expr.go). **Fixed fidelity bug in passing:** old
switch treated plural `days`/`hours`/`millisecond` as typmod fields; PG treats
them as column ALIASES on the bare interval (`interval '5' days`=00:00:05 AS
days). Singular-only rewrite; TestParseIntervalLiteral updated. HasPrec/Prec
added to parser+planner IntervalLit (2 conversion sites).

Next feature step (deferral ledger 2026-07-11): thread a DecodeInterval-style
`range` param into the shared `parser.ParseIntervalBody` (sibling-locked with the
::interval/CAST path) so a COMPLEX body under a range resolves its trailing
bare-number default field by qualifier (`interval '1 day 5' hour to minute`,
`interval '1 2:03:04' day to second`). Then interval ±infinity (carve `infinity`
out of the sign-must-precede-digit guard + a Datum infinity representation — note
goopg v0 stores NO infinity for date/timestamp/interval; evalIsFinite always
TRUE — so this is a larger cross-cutting change). Also cast-form typmod
`CAST(... AS interval hour to minute)` / `interval(p) '...'`.

Gates: build/vet clean; parser/analyzer/planner/executor suites PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
