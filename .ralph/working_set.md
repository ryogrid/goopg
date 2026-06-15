Task: M0110-0003 (pg_amcheck) — loop #17. FIXED residual #2: the
exclude-pattern anti-join backend PANIC. A GENERAL planner correctness bug.
COMMITTED-pending.

=== WHAT LANDED (this loop) ===
Root cause = the recurring sibling-path divergence (pattern_sibling_paths_must_agree)
between the two LEFT JOIN inner-only pushdown helpers:
- `classifyConjunctSide`→`walkColumnRefs` (internal/planner/pushdown.go) decides a
  conjunct is inner-only and pushable to a Filter on the inner plan.
- `shiftColumnRefsBy` (internal/planner/planner.go) rebases its ColumnRef.Index by
  -leftWidth once pushed.
Both hand-enumerate Expr kinds in a switch; several sub-expr-bearing kinds were
missing from one or both. In pg_amcheck's `toast` exclusion CTE the conjunct
`('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)` over a 5-col exclude_pat
VALUES build relation: the `~` ref classified it sideRight (pushed+shifted) but the
`*IsNullExpr` inner ref was left at combined index 43 → MaterializedSlot.Get(43) on
a width-5 row → panic via joinOp.Open→drainRowsCtx→filterOp.Next.
FIX: added `*IsNullExpr`/`*IsBoolExpr`/`*IsDistinctFromExpr`/`*CollateExpr`/`*RowExpr`
to shiftColumnRefsBy AND the same set + `*CastExpr` to walkColumnRefs (the CastExpr
add is the dangerous-direction twin: before it `cast(outer.col)=inner.col` hid the
outer ref so a mixed conjunct could be wrongly pushed below the LEFT JOIN).

Files:
- internal/planner/planner.go (shiftColumnRefsBy: +5 cases)
- internal/planner/pushdown.go (walkColumnRefs: +6 cases)
- internal/planner/shift_colrefs_in_test.go (+TestShiftColumnRefsByIsNullExpr)
- docs/design/0110-0011-leftjoin-inner-pushdown-shift-completion.md (+README row)
- .ralph/deferral_ledger.md (loop #17 entry)

Gates: build/vet/gofmt clean; planner suite PASS; executor suite PASS; state-guard
self-repaired OK. End-to-end on a live capped server: 5-col VALUES anti-join CTE +
real `pg_amcheck --exclude-relation 'pg_catalog.*'` and `--exclude-schema pg_catalog`
both exit 0, NO panic in server log. TPC-H spotcheck SKIPPED (bench data dir is an
unloaded husk — no `tpch` role; not introduced by this change). Generalizes 0097-0023.

=== NEXT STEP (resume) ===
The exclude anti-join no longer panics, so the previously-blocked sections are
portable. Options:
(a) Port the `--exclude-schema`/`--exclude-table` sections of 002_nonesuch.pl
    (M0110-0003 residual #2 test-porting; needs the pg_amcheck TAP harness, faithful
    transcription — a clean self-contained loop).
(b) Residual #1: datconnlimit=-2 invalid-database filter — BLOCKED on a runtime
    pg_database shared-catalog write goopg lacks (goopg_no_runtime_shared_catalog_inplace_update).
(c) Other open milestones: M0095-0003 recvlogical (030, logical decoding, large);
    M0110-0001 pg_dump 002; M0110-0002 pg_waldump 002; AC-003 remaining 003_check
    tiers + 005_opclass_damage; M0117-0006/7/8 (Effort-L, defer).
Note for any future executor/planner loop: reload the TPC-H husk
(bench/tpch/build_schema_goopg.sh) so the spotcheck gate runs instead of SKIPping.
