(idle — nothing in flight)

## Loop summary (2026-07-11, loop #36)

**Nightly triage:** batch 20260711-011536's 3 AI items all already [x] in
fix_plan.md (loops #29–31 + d212f7ea). No new batch. Proceeded to feature work.

**Task — `unimplemented_feat #5(d-iv)` complex interval body under a range —
DONE (committing this loop).**
Closed the TRAILING-BARE-NUMBER half of the prior loop's deferred
complex-body-under-range item. A multi-field Form-1 body whose FINAL field is
unitless now resolves that number via the range's low field: `interval '1 day 5'
hour to minute`=1 day 00:05:00, `'2 hour 5' hour to minute`=02:05:00, `'1 day
1.5' hour to minute`=1 day 00:01:00, `'-1 day 5' hour to minute`=-1 days
+00:05:00 (11 cases, byte-for-byte vs live PG 18.3 port 5601).
Mechanism: new `parser.ParseIntervalBodyWithDefault(body, defaultUnit)` threads
PG's `DecodeInterval switch(range)` default field (= range low field) into
`decodeIntervalFields`'s trailing-unitless branch; `ParseIntervalBody` delegates
with `"second"` (siblings byte-identical). `evalIntervalLit` (executor expr.go)
routes a Qualified non-magnitude body through it, then the existing
`truncIntervalToUnit`/`roundIntervalMicrosToPrec` truncation runs unchanged.
Files: internal/parser/interval.go, internal/executor/expr.go,
internal/executor/interval_subday_test.go.

Next feature step (deferral ledger 2026-07-11): the LEFTWARD-CARRY half —
`interval '1 2:03:04' day to second`=1 day 02:03:04 (a bare number to the LEFT of
a time/year-month word takes DTK_DAY via PG's right-to-left carry, datetime.c
~L3549). goopg's left-to-right `decodeIntervalFields` rejects a non-final bare
magnitude — pre-exists at full range too (`interval '1 2:03:04'` errors), so NOT
range-specific. Needs a lookahead (bare mag before a `:`-time/year-month field
inherits that field's type) or a right-to-left rewrite; then move the case from
the reject list to accept. Then interval `±infinity` (cross-cutting: no Datum
infinity for date/timestamp/interval; carve `infinity` out of the
sign-must-precede-digit guard) and cast-form typmod `CAST(... AS interval hour to
minute)` / `interval(p) '...'`.

Gates: build/vet clean; parser/planner/executor suites PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
