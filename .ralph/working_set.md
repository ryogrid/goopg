Loop #48: M0118-0008 enabler (design 0118-0050) — PL/pgSQL `SELECT … INTO`
statement form. NOT a promotion.

Done: `select … into [strict] target[, …] from …` inside a PL/pgSQL body now
binds the first result row to the named variable(s). Was mis-parsed as SQL
CREATE-TABLE-AS (`SELECT … INTO <table>`). parseSQLStmt special-cases a leading
SELECT: detect top-level (depth==0) `INTO`, peel optional STRICT + comma target
list (dotted names ok), reconstruct query with INTO clause excised → new
`*SelectIntoStmt{SQL,Targets,Strict}` (plain SELECT still → verbatim *SQLStmt).
Runtime `bindSelectIntoRow`: single target+1col → scalar; single target+Ncols →
record `_<target>_<col>` sub-fields; multi-target → positional scalar; no row ⇒
NULL (schema from op.Schema() before first Next); STRICT ⇒ P0002/P0003.

Files: internal/plpgsql/ast.go (+SelectIntoStmt), internal/plpgsql/parser.go
(parseSQLStmt SELECT-INTO detection), internal/plpgsql/parser_test.go
(+TestParseSelectInto/+TestParseSelectNoIntoIsEmbeddedSQL),
internal/executor/plpgsql_runtime.go (+SelectIntoStmt case + bindSelectIntoRow),
internal/executor/plpgsql_select_into_test.go (TestPlpgSQLSelectInto),
docs/design/0118-0050-* + README index, fix_plan.md, deferral_ledger.md.

Gates run: TestParseSelectInto + TestParseSelectNoIntoIsEmbeddedSQL PASS;
TestPlpgSQLSelectInto PASS (scalar/multi-target/no-row→NULL); full
internal/executor + internal/plpgsql PASS; txctl (DoCommitChain/RollbackChain/
CommitInExplicit) + SubxidOverflow PASS (no DO-path regression); go vet clean;
pgbench smoke = pre-commit hook. Probe: plpgsql-toast assign1 now runs (emits
length(x)=6000); first divergence moved past SELECT INTO.

Next step (M0118-0008 hard tail — all Effort-L, one slice per loop). plpgsql-toast
next blockers (in order the probe surfaces them):
- assign2: subquery-valued assignment `x := (select test1.b from test1)` —
  PL/pgSQL scalar exprs reject subqueries (`subqueries are not supported in
  PL/pgSQL expressions in v0`). Resume = let evalPLpgSQLExpr handle a top-level
  scalar SubqueryExpr (plan+run, take first row first col).
- assign3/4: expanded record (`r record`, `r test2`) with `r.b := (select …)`
  and `length(r::text)` — needs record reassembly to text.
- assign5/6: `FOR r IN SELECT … LOOP` detoast + COMMIT-in-loop.
- detoast-across-COMMIT: free external TOAST pointers at the assignment boundary
  so a concurrent VACUUM can't orphan chunks; + the runner `<waiting ...>`
  advisory-lock/VACUUM timing marker.
- Other tail specs (each a new subsystem): alter-table-4 (INHERITS + transactional
  catalog visibility), partition ATTACH/DETACH concurrent visibility,
  partition-drop-index-locking (real pg_locks view), reindex-concurrently-toast
  (TOAST relations as catalog objects).
