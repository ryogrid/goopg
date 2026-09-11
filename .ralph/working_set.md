(R65 landed `cdfe9ed32`, pushed — see result below; nothing in flight)

## Plan-parity take2 R65 result (2026-09-11) — Q11→MATCH, 6/14/0/2

**Round:** renderer-only two-arm cut (`operators_explain.go` +151,
`walk_export.go` +39, `flaglabels_test.go` 1-line, 4 new pins):
Arm A Sort-key OUTER_VAR expansion through child agg targetlist
(Aggs-only, fail-closed); Arm B `(InitPlan N).col1` value deparse
(InitPlan-branch-only). P0–P4 all green: values 24/24 MATCH pre/post,
explain A/B text-only, pp 6/14/0/2 vs fixtures AND live PG,
DS SF0.25 PASS=96 SKIP=3, plan-gate opt-out (pre/post identical 20/22
verdict sets vs stale Sep-05 baseline). Review APPROVE-WITH-NOTES.
Commits: `fdb1a6774` (SCOPE) + `cdfe9ed32` (code+pins+REPORT+TODO),
both pushed.

**Nightly triage:** run `20260905-011015` (9 items) filed — 6 new tasks
(race/executor, IntraGrantInplace, IsolationStats,
LockRowsSortOverJoin, PgDumpConnectionSetup, RegressSuite) + AI-ids
appended to 3 open tasks (PGColdStart, PgStatActivity ×2).

**NEXT:** re-triage queue per R63 ordering — (a) Materialize (R65 was
the query-closing precondition), then #6, R61 #4, (b) Q4, R63-#1/#2,
watches, P0-04 suffix remainder.

**Hygiene notes:** first spotcheck attempt exit 137 transient (rerun
PASS); `:65433` serves foreign `tmp/goopg-bench-bin` (started 19:00 by
another lane — untouched); private clone `:5533` DOWN kept for A/B;
pre-cut source worktree `/tmp/pp2/r65-pre-src`; evidence `/tmp/pp2/r65/`.

**In-flight:** none. Scopes reaped, private ports quiet.
