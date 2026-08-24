Task just completed: M0134-0126 (gin.sql) — sized live, PARKED, engine-wide
fix shipped.

`scripts/pg-regress-runner.sh gin`: 0% parity, diff 218 lines (183-line
source). Four independent root causes: (1) no GIN physical-index plan
integration (EXPLAIN shows Seq Scan not Bitmap Index Scan — REFACTOR-tier,
own subsystem, parallels the documented GiST catalog-only-index gap); (2)
`gin_clean_pending_list()` unimplemented (REFACTOR-tier — needs the GIN
fastupdate pending-list mechanism); (3) `gin_fuzzy_search_limit` GUC
unregistered (small, unclaimed); (4) plpgsql `RETURN QUERY EXECUTE '...' ||
arg` dynamic-SQL parsing gap, newly EXPOSED once the shipped fix unblocked
that test block's LATERAL correlation.

Shipped fix (genuinely engine-wide, not gin-specific — the case just exposed
it via `lateral explain_query_json(... || query)`): `planTableFuncRangeVar`'s
user-defined-routine FROM-clause branch (`internal/optimizer/planner.go`,
~line 4653) resolved args against an EMPTY `*resolveContext{}`, ignoring
`lateralCtx` entirely — so ANY user-defined function in the FROM clause with
a correlated arg to an earlier FROM item (`FROM t, LATERAL f(t.col)`,
implicit or explicit LATERAL, qualified/unqualified, base-table or
VALUES-source) raised "column ... does not exist" at plan time, for both
SETOF and non-SETOF routines. Builtin SRFs (pg_get_publication_tables,
verify_heapam, pg_get_sequence_data, pg_options_to_table) already chained
lateralCtx+planParent individually via their own planX helpers; the
user-defined fallback never did. Fixed the context construction to match;
added `*ScalarFuncScan`/`*UserSrfScan` cases to `nodeReferencesOuter` (so the
wrapping Join gets flagged Lateral); added the missing runtime half —
`scalarFuncScanOp`/`userSrfScanOp` had no `BindLateralOuter` at all (a
plan-only fix reproduces at runtime as "column ref query/0 on nil slot" —
verified live before adding this). New tests:
`TestPlanUserFuncLateralArgResolvesAgainstLeftFromItem`
(internal/optimizer/planner_test.go, setof+scalar),
`TestScalarFuncScanFrom_LateralArgResolvesAgainstLeftFromItem`
(internal/executor/scalar_func_scan_from_test.go, setof+scalar, end-to-end
row-value assertions).

`gin.sql`'s own diff line count is UNCHANGED (218→218) — the fix just moves
the failure point within the `explain_query_json`/LATERAL test block from
the masking symptom (bucket 4's real cause, "RETURN QUERY: syntax error").
Confirmed via live probes (/tmp/gin_probe4..7.sql style repros) that the
LATERAL correlation bug itself is genuinely fixed for both scalar and SETOF
shapes, independent of gin.sql's own score.

Design `docs/design/m0134-0126-gin-lateral-srf-arg.md` (full root-cause
breakdown, resume points), indexed in README.md. Ledger row:
`.ralph/deferral_ledger.md` 2026-08-24 M0134-0126. CSV flipped `not-tried` →
`failed` via `make regen-testport`. fix_plan.md M0134-0126 marked [x] with
full summary.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0127 (gist.sql)**. Size it live first. Note:
gin.sql/gist.sql are both GIN/GiST index-shaped files — worth checking early
in the sizing whether gist.sql shares the same catalog-only-index gap (bucket
1 above) before assuming a fresh root cause; if so it's the SAME
already-tracked REFACTOR-tier item, not a new one.

Standing recommendation, carried across several loops (unchanged this loop):
1. LANGUAGE C dynamic-extension loading gap — recurs across M0134-0106,
   -0116, -0120, create_operator/create_type adjacent files.
2. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
3. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting.
4. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18 call sites in plpgsql_runtime.go).
5. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port) —
   M0134-0120, unfixed.
6. pg_shdepend-shaped object-enumeration/CASCADE engine — CONFIRMED A
   THIRD TIME (M0134-0124), after M0134-0117/-0118. Single most-recurring
   blocker across M0134; strong candidate for its own milestone. Resume:
   `catalog.InMemory.RoleDropDependencyDescriptions`
   (`internal/catalog/catalog.go`).
7. `CREATE CONVERSION`-registered procs never consulted by convert_from/
   convert_to (M0122-0008, M0134-0121).
8. DDL-event-trigger firing engine + `session_replication_role` GUC
   (M0134-0122/-0123) — second-most-recurring blocker.
9. `NonSuperuserRole != ""` "is superuser" convention is wrong for any
   non-"postgres"-named `CREATE ROLE ... SUPERUSER` role — worth a
   dedicated sweep.
10. Geometric type-system gap (point/lseg/line/path/polygon typed-literal
    parsing + operator lexer family) — box.sql/circle.sql/geometry.sql
    shared blocker, full resume points in
    `docs/design/m0134-0125-geometry-sizing.md`.
11. **NEW this loop**: GIN physical-index plan integration + fastupdate
    pending-list mechanism — a `USING gin` index is catalog-only, same shape
    as the documented GiST catalog-only-index gap; likely shared by
    gist.sql, the very next task. Candidate for a dedicated GIN/GiST-index
    milestone once both are sized.

Gates run this loop: scripts/pg-regress-runner.sh gin (sizing run, 0/1);
go build ./... PASS; go test ./internal/optimizer/... ./internal/executor/...
(targeted + full package) PASS; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all packages); scripts/tpch-spotcheck.sh
PASS (Q12=2 rows, Q13=35 rows, 35.3s wall); make check-testport-inventory
PASS; make regen-testport PASS; make ralph-state-guard: TODO — run before
final commit.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/*, ci/logs/launch.log,
ci/logs/scheduler.log, docs/wiki/*,
internal/executor/operators_recursive_cte.go, postgres (untracked convenience
symlink), third-party/tpcds-postgres) and was deliberately left
untouched/uncommitted — only this loop's own files were staged and committed
by explicit pathspec.

M-NIGHTLY: checked ci/logs/action-items.md this loop — same run 20260824-013441
(2 items) as prior loops. Item -001 (recursive-CTE nil-deref) already
FIXED+[x] in fix_plan.md (a prior loop's work; the corresponding source edit
is the peer's in-flight WIP noted above — not re-verified this loop since it
wasn't staged/committed by this loop). Item -002 is a duplicate of the
already-open AI-20260822-001356-003 row. Filing obligation satisfied, nothing
new to file this loop.
