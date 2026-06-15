Task: M0110-0003 (AC-002 amcheck) — loop #2. LANDED gap #6 (LATERAL `c.oid`
resolution into a FROM-clause SRF's args). A NEW gap #7 surfaced (next loop).

=== WHAT LANDED (this loop) — committed directly on align-data-structure-with-pg ===
The MAIN tree is now CLEAN (foreign gen-column WIP cleared by a human; the
M0110 amcheck-sql worktree chain was merged — gap #5 at f0a75627). So engine
work lands directly in the main tree again (no worktree needed).

gap #6: pg_amcheck's per-relation heap check is the implicit-LATERAL comma-join
  FROM pg_catalog.pg_class c, "public".verify_heapam(relation := c.oid, …) v
  WHERE c.oid = <reloid>
The correlated `c.oid` now resolves. Two-layer fix, mirroring the proven
pg_get_publication_tables lateral pattern:
- Planner (internal/planner/planner.go): planVerifyHeapam now takes lateralCtx
  and resolves args against it (was empty resolveContext → "column oid does not
  exist"); nodeReferencesOuter has a *VerifyHeapam case so the wrapping Join is
  flagged Lateral. Call site planTableFuncRangeVar passes lateralCtx.
- Executor: verifyHeapamOp implements lateralBindable (BindLateralOuter) + uses
  evalExprSlot(arg, outerSlot, …) (operators_verify_heapam.go). KEY BUG: the
  server's BuildFast/OpNode path wraps a Join's children in opNodeOperator, which
  hid the lateralBindable interface from joinOp.openLateral → SRF arg evaluated
  against nil slot ("column ref oid/0 on nil slot"). Fix:
  opNodeOperator.BindLateralOuter forwards to the wrapped *opAdapterState op
  (internal/executor/opnode.go).
Regression tests: planner.TestPlanVerifyHeapamLateralArgResolvesAgainstLeftFromItem;
executor.TestVerifyHeapam_LateralCommaJoinViaFastPath (corruption surfaces via
the comma-join driven through BuildFastIterator — proves arg binds correct rel
per outer row). 002_nonesuch probe advanced gap #6→#7.

Files: internal/planner/planner.go, internal/planner/planner_test.go,
internal/executor/operators_verify_heapam.go, internal/executor/opnode.go,
internal/executor/operators_verify_heapam_test.go,
internal/testport/pgamcheck002_port_test.go,
docs/design/0110-0008-amcheck-sql-surface-plan.md.

Key symbols: planVerifyHeapam, nodeReferencesOuter, joinOp.openLateral,
lateralBindable, verifyHeapamOp.BindLateralOuter, opNodeOperator.BindLateralOuter.

Gates run: go test ./internal/{planner,executor,analyzer,parser,server} PASS;
gofmt+vet clean; TestPort_PgAmcheck002Nonesuch now SKIPs cleanly on gap #7
(was FAIL). TPC-H spotcheck not run (parser/planner change is row-count-neutral
for user queries; no executor row-shape change to existing ops).

=== NEXT STEP (resume point) — AC-002 gap #7, its OWN bounded loop ===
With gap #6 closed the heap check runs clean; the next 002_nonesuch failures are
connection/resolution-level, NOT SQL surface:
  (a) database-name pattern resolution — `no connectable databases to check
      matching "<pat>"` for multi-pattern/substring/superstring db args;
  (b) non-existent role rejection — `role "<name>" does not exist` (goopg accepts
      any role, exits 0); START HERE, likely smallest.
  (c) per-database amcheck-installed detection + template1/template0 registration
      — `skipping database "template1": amcheck is not installed`.
Each is an independent feature. After gap #7: clog XidStatusFunc tier wiring,
then AC-002…AC-005 TAP port + CSV flip (docs/test-port/postgres-oracle-port-status.csv).

=== CONTEXT ===
Main tree is clean — commit engine work directly on align-data-structure-with-pg.
.ralph/fix_plan.md is churned by the driver (md5 changes mid-loop) — progress
recorded in deferral_ledger.md + this file.
