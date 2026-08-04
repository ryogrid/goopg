(idle — nothing in flight)

M0127-P5.6 is DECOMPOSED into -a…-e (fix_plan + IMPLEMENTATION-TODO), and
**P5.6-a is DONE**: `internal/planner/joinselectivity.go` (new) +
`catalog.ColumnStats.StaDistinct` (new, its two open-coded copies switched).
`examine_variable` / `get_variable_numdistinct` / `eqjoinsel`'s no-MCV arm /
the clause dispatcher, over `restrictInfo` operands. Inert.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects `M0127-P5.6-b` —
`calcJoinrelSize` + the concrete `joinRelBuilder`: 04 §3.1's FK/unique-superkey
generalisation over clause SUBSETS (the `isunique` arm of
`getVariableNumDistinct`, replacing `uniqueNoFanoutRawCount`'s edge-list form)
driving 04 §2's rows-once discipline — `sizeJoinRel` at find-or-create time,
BEFORE any path is generated. Bar: UNITS.**

Carry-over facts a next loop should not re-derive:

- `sizeJoinRel` still has NO production implementor (only
  `joinsearchlevel_test.go`'s `recordingBuilder`). P5.6-b writes both it and
  the concrete `joinRelBuilder` that binds it to `addPathsToJoinrel`.
- Base-rel resolution inside the search: relid bit i → `s.relInfos[i]`
  (FROM order); `.table` is nil for a subquery/CTE/VALUES leaf; `.baseRows` is
  the RAW (pre-filter) count = PG's `rel->tuples`.
- Column stats resolve by **NAME** (`columnStatsByName`, pathparamindex.go).
  `ColumnRef.Index` is a GLOBAL offset in the pre-search concatenation — a
  positional read of `Stats.Columns` reads the wrong column.
- goopg splits PG's signed `stadistinct` into `NDistinct` + `NDistinctFrac`;
  the fraction wins. ONE reduction now: `catalog.ColumnStats.StaDistinct()`,
  read by the pg_statistic heap row, the pg_stats view and the estimator.
- Dispatch on the OPERATOR, not `isEquijoin`: `a.x = b.y + c.z` is priced by
  eqjoinsel (0.005) not by the 0.5 unhandled default. `isEquijoin` governs only
  which operands are examined (leftKey pairs with leftRelids, never bo.Left).
- 3 new ledger rows: eqjoinsel MCV arm; `vardata->isunique` (= P5.6-b);
  `examine_variable`'s subquery/expression arms.
- Still open from earlier: P4.1 ledger row #3 (`mergeJoinStream.bufferGroup`
  twin); `pushOneConjunct` not taught the searched tag; `walkPlanExprs` misses
  `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc`.
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh` (~30 s + build). DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: build+vet clean; 17 new tests PASS; UNITS PASS (exit 0,
0 FAILs, `/tmp/units_p56a.log`); SPOT PASS (`/tmp/spot_p56a.log`, Q12=2 Q13=35
canonical, 25.7 s); pgbench SMOKE via the commit hook. DS05 + PLAN not
applicable — the planner half has no production caller, and the catalog half is
an identity refactor of one two-field reduction.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects from run
20260804-005028, all already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
