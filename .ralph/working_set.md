Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 17
(pg_options_to_table FROM-clause SRF) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 18 (correlated SRF-arg resolution).

=== DONE (loop #40) — DU-002 slice 17 ===
pg_dump's getForeignDataWrappers ARRAY subquery expands fdwoptions via
pg_options_to_table(fdwoptions); planning aborted at `column "option_name"
does not exist`. Implemented pg_options_to_table as a FROM-clause SRF
(text[] of "name=value" → rows of (option_name text, option_value text);
split at FIRST '=', bare name → NULL value; mirrors untransformRelOptions
src/backend/foreign/foreign.c). Files:
- internal/parser/select.go: added to FROM known-builtin SRF name switch.
- internal/planner/plan.go: PgOptionsToTable node (Arg, schema) + Pos/Output.
- internal/planner/planner.go: planPgOptionsToTable (two text cols, AS-alias
  overridable, arg via lateralCtx); dispatch before unnest branch.
- internal/planner/foldconst.go + unnest.go: FoldConstants + walkPlanExprs cases.
- internal/executor/operators_pg_options_to_table.go: pgOptionsToTableOp
  (eval arg vs outer lateral row, expandArrayDatum, strings.Cut at first '=').
- internal/executor/executor.go: Build() dispatch case.
- internal/analyzer/analyzer.go tableFuncColumns: SIBLING PATH — analyzer
  derives FROM-SRF cols independently & runs BEFORE planner; without this case
  bare `option_name` failed analysis before FROM planning (SELECT * worked but
  named cols didn't — that was the non-obvious bug). Added the case.
- 4 unit tests operators_pg_options_to_table_test.go (all PASS).
- design doc 0110-0001 slice-17 block; pgdump_connsetup_test.go header (landed
  list + next-blocker comment); fix_plan loop #40 entry.
Gates: build/gofmt/vet clean; parser/planner/analyzer/executor/catalog suites
PASS; TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive new
plan-node + SRF; zero existing query-path/row-count risk).

=== NEXT STEP — DU-002 slice 18 (correlated FROM-SRF argument) ===
After slice 17 the subquery resolves but the query advances to a NEW
empirically-confirmed blocker: `column "fdwoptions" does not exist`.
pg_options_to_table(fdwoptions) — fdwoptions is a CORRELATED ref to the OUTER
pg_foreign_data_wrapper row, inside a scalar ARRAY(...) subquery. goopg cannot
resolve a FROM-clause SRF argument that reaches up into an OUTER query level.
VERIFIED minimal repros (CREATE TABLE fdw(id int, opts text[])):
  FAIL: SELECT id, ARRAY(SELECT option_name FROM pg_options_to_table(opts)
        ORDER BY option_name) FROM fdw   → 42703 column "opts" does not exist
  FAIL: SELECT id, (SELECT count(*) FROM pg_options_to_table(opts)) FROM fdw
  PASS: SELECT id, x.option_name FROM fdw, LATERAL pg_options_to_table(opts) x
So same-level explicit LATERAL works (lateralCtx threads sibling bindings); the
gap is cross-query-level correlation flowing into the SRF arg. Slice 18 = thread
the outer scope into BOTH the analyzer's FROM-SRF arg resolution (analyzer.go
tableFuncColumns caller / synthesizeSubqueryTable scope chain) AND the planner's
planTableFuncRangeVar (lateralCtx must include the enclosing subquery's outer
scope). Look at how scalar/ARRAY subquery planning builds its resolveContext and
whether the outer scope reaches FROM-clause SRF arg resolution. RUN
TestPort_PgDumpConnectionSetup after to find the REAL next blocker (predicted:
getForeignServers/pg_foreign_server, getUserMappings/pg_user_mappings — VERIFY).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
