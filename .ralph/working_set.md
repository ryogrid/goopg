(idle — nothing in flight)

Last completed: M-NIGHTLY DateStyle follow-up — UPDATE-side date/timestamp/
timestamptz/numeric + int-range literal coercion. Factored insertOp.Next's
coercion switch into a shared `coerceRowForConstraintChecks(cols, row,
include, ctx, pos)` helper (`internal/executor/operators_storage.go`) and
wired it into all 7 UPDATE new-row construction sites (updateViaIndex
main+EPQ; updateOp.Next's SeqScan inherit/non-inherit branches + its
Phase-1 EPQ rebind + a second, previously-undiscovered Phase-2-write-loop
EPQ rebind; updateWithFrom main+EPQ), gated on `o.plan.Set[i] != nil` so
only freshly-SET columns get re-coerced. New tests:
TestUpdateCoercesDateLiteralBeforeFKCheck,
TestUpdateCoercesNumericLiteralBeforeCheckConstraint,
TestUpdateCoercesInt4RangeOverflow
(internal/executor/update_fk_datestyle_coerce_test.go); non-vacuousness
confirmed via git stash. Design doc
docs/design/0097-0151-datestyle-partial-set-merge.md "Follow-up
(2026-07-15): UPDATE-side literal coercion" section + README index updated.
Deferral ledger row flipped resolved + new row appended. fix_plan.md
updated. Gates: go build/go vet (repo-wide) clean; go test -count=1
./internal/executor/... PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33);
RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh PASS (0 failed, all 3
workloads). make ralph-state-guard: auto-repaired a stale
running/completed mismatch, then OK.

This closes both halves of the M-NIGHTLY DateStyle-literal-coercion gap
(INSERT done in the prior loop, UPDATE done this loop).

Next natural DateStyle slice (still open per the ledger tail, not started):
the `Datum.Format()`/`AppendValueText()` ~20-call-site audit (to_char
fallback, plpgsql RAISE, EXPLAIN, error messages, operators_analyze.go
bound-rendering, array_to_string/||), TIMESTAMPTZ timezone-aware
conversion, and pgoutput.go's DateStyle gap.

In-flight: none. Not yet committed — pending git commit with pathspec
(internal/executor/operators_storage.go,
internal/executor/update_fk_datestyle_coerce_test.go,
.ralph/deferral_ledger.md, .ralph/fix_plan.md,
docs/design/0097-0151-datestyle-partial-set-merge.md, docs/design/README.md,
.ralph/progress.json, .ralph/working_set.md) — several unrelated stray
files (analysis/perf-optimize3/runs/*, weekly_loc.csv/png, ci/logs/*.log,
analysis/tpch-explain-baseline.md, untracked postgres/ content) are present
in the tree from other processes and were deliberately excluded from this
commit.
