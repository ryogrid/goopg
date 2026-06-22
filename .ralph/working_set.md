Loop #51: M0118-0008 enabler (design 0118-0053) — PL/pgSQL record FOR-loop
`record::text` framing. NOT a promotion.

Done: closed the `r::text` output gap for plpgsql-toast assign4/5/6.
- assign4 (`r test2; select * into r`) was ALREADY correct after 0118-0052
  (named composite flows through the same bindSelectIntoRow multi-col branch) —
  verified `length(r::text)=6004` by probe. The working-set note predicting
  `<NULL>`/`6002` was STALE.
- assign5/6 fixed: a single-column record FOR-loop var held the RAW column value
  (6000) not the composite framing `(…)` (6002). `ForSelectStmt`'s scalar
  shortcut (`frame.values[idx]=row[0]`) was taken for any 1-col query — correct
  for scalar loop vars, wrong for `record` vars.

Fix (internal/executor/plpgsql_runtime.go):
- `(*plpgsqlFrame).isRecordVar` — declared type `record` OR registered
  compositeVarFields (named composite).
- `bindRecordRowComposite` helper — extracted from bindSelectIntoRow (sub-fields
  + composite Datum + field schema; no-row⇒NULL). bindSelectIntoRow now calls it
  (sibling-paths sync).
- ForSelectStmt per-row binding branches on isRecordVar FIRST → composite bind
  regardless of column count; scalar shortcut + legacy sub-field branch unchanged
  for non-record vars.

Files: internal/executor/plpgsql_runtime.go, internal/executor/
plpgsql_record_forloop_test.go (new TestPlpgSQLRecordForLoopAndText),
docs/design/0118-0053-* + README index.

Gates: TestPlpgSQLRecordForLoopAndText PASS (a4=6004/a5=6002/a6=`6002 9002
12002`); TestPlpgSQLRecordFieldAndText+SelectInto+ScalarSubquery+Composite* PASS;
full internal/executor PASS; go vet + go build clean; ralph-state-guard OK
(auto-repaired prev-loop completed marker). pgbench smoke = pre-commit hook.
COMMITTING this loop.

Next step (M0118-0008 hard tail — all Effort-L, one slice per loop). plpgsql-toast
still NOT portable; remaining blockers:
- assign6 `COMMIT` inside the FOR-loop body (hold cursor across COMMIT via
  0118-0049 PLpgSQLCommitChain) + detoast-across-COMMIT: free external TOAST
  pointers at the assignment boundary so a concurrent VACUUM can't orphan chunks.
- runner `<waiting ...>` advisory-lock/VACUUM timing marker (spec's LAST
  structural blocker; runner decides blocking purely by 300ms timeout — see
  iso_runner_blocking_is_timing_only memory).
- record_out quote/escape framing (current comma-join is unquoted).
Other tail specs (each a new subsystem): alter-table-4 (INHERITS + txn catalog
visibility), partition ATTACH/DETACH concurrent visibility,
partition-drop-index-locking (real pg_locks view), reindex-concurrently-toast
(allow_system_table_mods + TOAST relations as catalog objects).
