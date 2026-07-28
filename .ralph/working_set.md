(idle — nothing in flight)

Last loop (#17, 2026-07-29): **M0125-0010 CLOSED** — the FROM-subquery
`Project` remap. `remapSubqueryColumnRefs` (`internal/planner/planner.go`) is
now **verify-then-repair**: a bare-`ColumnRef` target whose index is in range
AND names the column the ref asks for is left alone; only an out-of-range index
or one naming a different column (the M0097-0058 leakage signature) is
re-derived by name. A plan dump with the pass disabled proved the pre-remap
indices were ALREADY correct — the pass caused the damage. A positional remap
(what its own doc comment claimed) was rejected: breaks `select b, a from t`.
Gate = 3 tests in new `internal/planner/subquery_remap_test.go`; 4 of 6
control-matrix rows fail against the old code, `GROUP BY` as `[0 1 1]`.

Measured SF=1: **all six carriers now match PG** — reproducer + Q21
byte-identical; Q28 Q46 Q66 Q68 Q79 identical mod `char(n)` padding. Q21/Q66
needed BOTH -0009 and -0010. Artifacts `analysis/m0125-0010-acceptance/`.

NEXT LOOP — re-read the `## Current Priority` banner (M-NIGHTLY still PARKED:
keep FILING `## AI-` items, do not select; `ci/logs/action-items.md` unchanged
since 2026-07-25, all 26 filed as ID RANGES `-008..-026`, so a per-ID grep
FALSE-NEGATIVES — grep loosely, e.g. `grep 20260725 .ralph/fix_plan.md`).

**Recommended: M0125-0011** — FULL OUTER JOIN drops all but the FIRST ON
conjunct. Probe matrix is in the fix_plan item; acceptance Q97 =
`541140|286927|161`. Unlike -0009/-0010 it CHANGES row counts, so the SF0.5
gate can see it. Design doc: §6 of
`docs/design/0125-0009-parser-expr-key-structural.md` has the isolation.

Gate notes for next loop (both cost time if rediscovered):
- The SF0.5 sweep has **no query-range option** and one full run EXCEEDS the
  60 min headless Bash ceiling (it reached Q53 in 3400 s). Run it in two parts:
  the script for Q1-Q53, then a manual row-count loop vs
  `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt` for the tail,
  restricted to baseline-PASS queries (a baseline TIMEOUT carries no signal).
  See `analysis/m0125-0010-acceptance/README.md` for the exact loop.
- Killing that sweep leaves an orphaned `psql` AND a 21 GB goopg on :65437 —
  reap both (`server.sh stop sf05`) before any timing work.
- **Q75 = `ERROR: division by zero` is PRE-EXISTING**, verified by reverting the
  planner hunk and re-running. Do not attribute it to a planner change.
- The 2026-07-27 SF0.5 pipeline log is a STALE baseline (10+ engine commits);
  Q4/Q39/Q49/Q50/Q51 have since recovered to PASS on their own.

Gates run: units precommit PASS; planner/analyzer/parser/executor PASS;
tpch-spotcheck PASS (Q12=2, Q13=35); SF0.5 full coverage, zero regressions;
pgbench smoke via commit hook.
In-flight: none.
