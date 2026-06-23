Loop #24: M0118-0008 — PL/pgSQL single-column `SELECT … INTO record` field-access
enabler (design 0118-0066). COMMITTED + pushed at loop end.

## What landed (enabler, NOT a promotion)
reindex-concurrently-toast.spec setup `DO` block: `SELECT INTO r reltoastrelid::regclass::text
AS table_name …` then `EXECUTE 'ALTER TABLE ' || r.table_name || …`. After the 0118-0065
GUC enabler the first divergence was `qualified names are not supported in PL/pgSQL
expressions (0A000)` on `r.table_name`. Root cause = a real PL/pgSQL gap:
`bindSelectIntoRow` (plpgsql_runtime.go) scalar-shortcut a SINGLE-COLUMN `SELECT … INTO`
straight onto the target even when the target is a `record` var, so the `_<var>_<col>`
sub-field + `compositeVarFields` entry the qualified-name expr path reads were never
registered. Fix: guard the scalar shortcut with `!frame.isRecordVar(name)` so a record
target routes to `bindRecordRowComposite` (the multi-column `SELECT * INTO r` path,
0118-0054). Plain scalar targets unaffected.

Files: internal/executor/plpgsql_runtime.go (bindSelectIntoRow guard + comment),
internal/executor/plpgsql_select_into_test.go (new sel_rec_field case),
docs/design/0118-0066-plpgsql-select-into-record-field.md + README index,
.ralph/deferral_ledger.md + fix_plan note.

Key symbols: bindSelectIntoRow, frame.isRecordVar, bindRecordRowComposite,
lowerPLpgSQLExpr (qualified-ColumnRef handler, plpgsql_runtime.go ~L2061).

Gates: new TestPlpgSQLSelectInto/sel_rec_field PASS; go test ./internal/executor/ PASS;
TestPlpgSQLRecordFieldAndText/ScalarSubquery/ForLoopMaterializeAndRecordFieldSubst/
DoCommitChain* PASS; TestPort_IsolationPlpgsqlToast strict PASS (no regression);
go build ./... clean; stash A/B probe confirms reind-con-toast divergence advanced
0A000 → routine_column_usage 42P01. pgbench smoke = pre-commit hook.

## M0118-0008 hard tail (all Effort-L, deferred — ledger has full blocker maps)
- reindex-concurrently-toast: FUNDAMENTAL — needs real TOAST relations as catalog
  objects (reltoastrelid=0; text/bytea stored inline). New post-enabler wall:
  routine_column_usage (42P01) during the toast-rename EXECUTE. Multi-loop.
- partition-concurrent-attach + alter-table-4: both need transactional DDL cross-session
  catalog visibility (goopg applies DDL to shared in-mem catalog non-transactionally).
  Milestone-sized MVCC catalog subsystem.
- partition-drop-index-locking: pg_locks today reads ONLY the explicit-LOCK-TABLE
  registry (globalRelLockMgr, pid hardcoded "0"), NOT the real heavyweight lockmgr
  holders/waiters; needs a real-lockmgr pg_locks bridge + DROP INDEX recursive
  partition-tree locking + partitioned-index CREATE propagation + pg_stat_activity
  pid join. Probe: s3getlocks returns 0 rows; s2drop does not wait. Multi-loop.
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) but no
  executor site; needs per-row CTID capture in cursor + CTID-restricted rewrite.

Next step: pick another bounded enabler from the hard tail next loop. partition-drop-
index-locking's pg_locks→real-lockmgr bridge is the most broadly reusable, but it must
land WITH DROP INDEX recursive locking to advance the spec (pid-only wiring is marginal).
