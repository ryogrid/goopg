Loop #52: M0118-0008 — `plpgsql-toast` PROMOTED (design 0118-0054). All 7
permutations byte-for-byte. This is a PROMOTION (16th of the M0118-0008 group).

Done: closed the last two plpgsql-toast divergences on top of 0118-0049..0053.
Probe-first found assign1-5 already PASS; only assign6 + fetch-after-commit left.
- assign6: FOR-loop over a query whose body does `delete; commit;` only ran ONCE
  (live operator hit EOF after the delete). Fix: `ForSelectStmt` now materializes
  ALL rows up front (deep-copied; operator closed) BEFORE running the body —
  mirrors PG holding the implicit cursor snapshot across COMMIT (holdable).
- fetch-after-commit: `select b into t … where a = r.a` failed `42P01 missing
  FROM-clause entry for table "r"`. Two fixes: (a) `SelectIntoStmt` now calls
  `substitutePlpgsqlFrameVarsInSQL` (was missing on this path); (b) that function
  now substitutes `r.field` when `r` isRecordVar (literal from `_<var>_<field>`),
  guarded so plain `table.column` is untouched.

Files: internal/executor/plpgsql_runtime.go (ForSelectStmt materialize +
SelectIntoStmt substitution + record-field branch in substitutePlpgsqlFrameVarsInSQL),
internal/executor/plpgsql_record_forloop_test.go (new
TestPlpgSQLForLoopMaterializeAndRecordFieldSubst),
internal/testport/isolation_port_test.go (new strict TestPort_IsolationPlpgsqlToast),
docs/test-port/postgres-oracle-port-status.{csv,md} (D-002 rationale append),
docs/design/0118-0054-* + README index.

Gates: TestPort_IsolationPlpgsqlToast strict PASS (7 perms);
TestPlpgSQLForLoopMaterializeAndRecordFieldSubst PASS; full internal/executor
PASS; TestPort_IsolationSubxidOverflow+FreezeTheDead PASS (no regression);
go vet + go build ./... clean; pgbench smoke = pre-commit hook. COMMITTING.

Next step (M0118-0008 hard tail — all Effort-L, one new subsystem each, one slice
per loop). plpgsql-toast is DONE. Remaining:
- `reindex-concurrently-toast`: TOAST relations as catalog objects +
  `allow_system_table_mods` GUC (probe to find first divergence).
- `alter-table-4`: INHERITS + transactional-DDL cross-session catalog visibility
  (COUPLED to visibility per memory — can't split).
- `detach-partition-concurrently-{1,2,3,4}` + `partition-concurrent-attach`:
  transactional partition visibility (0118-0048 fixed the parser; the wait/
  two-phase concurrent-detach visibility remains).
- `partition-drop-index-locking`: real pg_locks view parity.
Suggested: probe each with a throwaway zz_probe_test.go (copy the loop #52 probe:
IsolationRunner.RunAndCompare → t.Logf STATUS+Diff) and rank by first-divergence
cost before committing to one.
