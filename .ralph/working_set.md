Task: M0110-0003 (AC-003 pg_amcheck) — loop #8. LANDED the lateral outer-qual
pushdown that unblocks relation-scoped pg_amcheck heap checks. Committed on
align-data-structure-with-pg.

=== WHAT LANDED (this loop) ===
Live-wire diagnosis (temp GOOPG_DIAG_TRACE in server query.go, now removed)
showed pg_amcheck's heap command is an implicit-LATERAL comma-join:
  FROM pg_catalog.pg_class c, "<schema>".verify_heapam(relation := c.oid, …) v
  WHERE c.oid = N
goopg planned the outer-only `WHERE c.oid = N` ABOVE the lateral nested-loop, so
verify_heapam was opened for EVERY pg_class row and raised
"could not open relation: relation does not exist" on the first non-heap sibling
(an index/sequence OID) → exit 2 on a HEALTHY target table whenever the DB held
>1 relation. (Explains why 004's single-table case worked but anything with an
index/2nd table failed.)

Fix: internal/planner/pushdown.go
- pushOuterQualsIntoLaterals(node): for a residual Filter whose direct child is a
  Lateral Join, move each outer-only conjunct (sideLeft by index range AND name)
  onto Join.Left as a Filter. Indices already align (left child = leading cols).
- collectScanOutputNames: extracted shared name-walk; added *Values case (virtual
  catalog relations like pg_class plan as *Values).
- name guard lenient when leftNames empty (direct-child sideLeft is conclusive).
- Wired in planner.go right after pushPredicatesIntoCrossJoins (else branch).
No-op unless residual Filter's direct child is a Lateral join → ZERO TPC-H impact.

Files: internal/planner/pushdown.go, internal/planner/planner.go (call site),
internal/planner/planner_test.go (NEW TestPlanOuterQualPushedBelowLateralJoin +
findLateralJoin/exprMentionsColumnName helpers), docs/design/0110-0003 (2 new
sections), .ralph/fix_plan.md, deferral_ledger.md.

Gates: internal/planner + internal/executor suites PASS; go vet planner/server
clean; 001/002/004 amcheck port tests PASS; build ./... + gofmt clean. TPC-H
spotcheck SKIPPED (no data dir loaded in this tree; safe by lateral-only guard).
e2e verified manually: pg_amcheck --table public.t (heap-only) and
--table … --no-dependent-indexes return exit 0 over a multi-table/indexed DB.

=== NEXT STEP (resume) — AC-003 remainder, blocker #2 (small) ===
bt_index_check schema-qualified dispatch: evalFuncCall (internal/executor/expr.go
~L5286) strips only "pg_catalog." before matching, so pg_amcheck's
`"public".bt_index_check(...)` → 42883 "function public.bt_index_check does not
exist". Any table with a dependent index still fails. Fix = strip the amcheck
install-schema qualifier for the amcheck builtins (bt_index_check /
bt_index_parent_check / verify_heapam scalar paths). Then blocker #3 (system-
catalog heap resolution in verifyHeapamResolveTable/LookupTableByOID) for the
003_check whole-db pre-corruption clean run. 005_opclass_damage = CREATE OPERATOR
CLASS + pg_amproc parity (large).
