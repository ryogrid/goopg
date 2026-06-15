Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 18 (correlated
FROM-clause SRF argument resolution) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 19 (empty pg_foreign_server virtual view).

=== DONE (loop #41) — DU-002 slice 18 ===
getForeignDataWrappers' ARRAY(SELECT … FROM pg_options_to_table(fdwoptions))
references fdwoptions from the OUTER pg_foreign_data_wrapper row; planning
aborted at 42703 column "fdwoptions" does not exist. Root cause: the planner
resolved the SRF arg against a context built only from same-level FROM siblings
(planFromClause), and the lexical-scope parent (planParent) was attached to the
SELECT ctx only AFTER FROM planning — so a correlated arg with no left-siblings
had no path to the outer scope. Fix (planner-only):
- internal/planner/planner.go planPgOptionsToTable: now takes `cat`; builds the
  arg-resolution ctx chaining to planParent, mirroring generate_series (no
  siblings → &resolveContext{cat,parent:planParent}; siblings-no-parent → copy
  + set parent). Dispatch call (line ~3078) passes cat.
- fdwoptions → OuterColumnRef; executor pgOptionsToTableOp.Open evaluates it
  per outer row (ctx.OuterRows top). OuterColumnRef resolution in expr.go uses
  ctx.OuterRows[len-Level], so it works regardless of the passed row.
- ANALYZER NEEDED NO CHANGE (refutes the prior working-set prediction):
  tableFuncColumns builds the SRF OUTPUT columns but never resolves the arg;
  analysis already passed — the 42703 came from the planner at the `opts` byte
  offset (verified empirically with a repro test).
- Tests: TestPlanPgOptionsToTableCorrelatedArg (planner — ARRAY/scalar/LATERAL),
  TestPgOptionsToTableCorrelatedArg (executor — per-outer-row eval). Both PASS.
- Design doc 0110-0001 slice-18 block; pgdump_connsetup_test.go header updated;
  fix_plan loop #41 entry.
Gates: build/gofmt/vet clean; planner/analyzer/executor/parser/catalog suites
PASS; TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive
correlation-scope fix on a new SRF path; zero existing query-path/row-count
risk — generate_series already used this exact pattern).

ORTHOGONAL PRE-EXISTING (do NOT conflate with slice 18): reading a text[]
column back from the heap yields the BINARY array encoding (KindString w/ raw
bytes), not the text repr expandArrayDatum parses; plain `SELECT opts FROM t`
reproduces it. Irrelevant to pg_dump (the FDW views are empty, SRF never
evaluates a non-empty options array). Track separately only if real
text[]-column expansion is ever needed.

=== NEXT STEP — DU-002 slice 19 (pg_foreign_server virtual view) ===
After slice 18 getForeignDataWrappers passes end-to-end; pg_dump advances to
getForeignServers → `relation "pg_foreign_server" does not exist`. Add the
empty pg_foreign_server virtual view in internal/catalog/catalog.go (beside
pg_foreign_data_wrapper, OID 1417 per pg_foreign_server.h): schema oid,
srvname name, srvowner oid, srvfdw oid, srvtype text, srvversion text,
srvacl aclitem[], srvoptions text[]. Empty by construction (goopg defines no
foreign servers). The dump query already expands srvoptions via the now-working
correlated pg_options_to_table(srvoptions) ARRAY subquery, so NO new SRF work.
RUN TestPort_PgDumpConnectionSetup after to find the REAL next blocker
(predicted: getUserMappings / pg_user_mappings — VERIFY).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
