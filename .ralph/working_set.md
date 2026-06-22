Loop #49: M0118-0008 enabler (design 0118-0051) — PL/pgSQL scalar subquery
`x := (SELECT …)`. NOT a promotion.

Done: a top-level `(SELECT …)` in a PL/pgSQL expression (assignment RHS or
`RETURN` operand) is now planned+executed → first col of first row (NULL when no
row; SQLSTATE 21000 when >1 row), instead of the blanket `0A000 subqueries are
not supported`. Intercepted in `evalPLpgSQLExpr` BEFORE lowering (a subquery
can't lower to a planner.Expr) via new `evalScalarSubquery`: planner.Plan(
sq.Inner, ctxPlanCatalog) → Build/Open/first-Next; struct-copy row[0]; a second
row ⇒ 21000. Mirrors SelectIntoStmt/ForSelectStmt machinery (0118-0050).

Files: internal/executor/plpgsql_runtime.go (evalPLpgSQLExpr intercept +
evalScalarSubquery helper), internal/executor/plpgsql_scalar_subquery_test.go
(TestPlpgSQLScalarSubquery + runQueryExpectErr helper), docs/design/0118-0051-* +
README index, fix_plan.md, deferral_ledger.md.

Gates run: TestPlpgSQLScalarSubquery PASS (scalar / no-row→NULL / RETURN(SELECT)
/ >1 row⇒21000); TestPlpgSQLSelectInto PASS; go build ./internal/executor clean.
Probe (zz_probe_test.go, removed): plpgsql-toast first divergence advanced
assign2 → assign3 (assign1/assign2 now byte-match PG, both `length(x)=6000`).
pgbench smoke = pre-commit hook (no executor hot-path change).

Next step (M0118-0008 hard tail — all Effort-L, one slice per loop). plpgsql-toast
next blocker = assign3/assign4 EXPANDED RECORD:
- assign3: `r record; select * into r from test1; r.b := (select test1.b from
  test1); ... length(r::text)` → goopg emits `length(r) = <NULL>` vs PG `6004`.
  Needs expanded-record FIELD assignment (`r.b := …` on a record var) + record-
  to-text (`r::text`) reassembly. The `select * into r` already binds record
  sub-fields (_r_a/_r_b via bindSelectIntoRow), but `r.b := …` field-assign and
  `r::text` composite framing are missing.
- assign4: `r test2; select * into r` (composite type target) + `length(r::text)`
  → `<NULL>` vs `6002`.
- assign5/6: `FOR r IN SELECT … LOOP` detoast (`length(r::text)` = 6000 vs 6002 —
  composite framing bytes) + COMMIT-in-loop.
- detoast-across-COMMIT: free external TOAST pointers at the assignment boundary
  so a concurrent VACUUM can't orphan chunks; + the runner `<waiting ...>`
  advisory-lock/VACUUM timing marker.
- Other tail specs (each a new subsystem): alter-table-4 (INHERITS + transactional
  catalog visibility), partition ATTACH/DETACH concurrent visibility,
  partition-drop-index-locking (real pg_locks view), reindex-concurrently-toast
  (TOAST relations as catalog objects).
