Loop #46: M0118-0008 enabler (design 0118-0048) — `DETACH PARTITION …
CONCURRENTLY` parser-position bug fix. NOT a promotion.

The parser consumed the optional CONCURRENTLY/FINALIZE trailer BEFORE the child
name, so `ALTER TABLE parent DETACH PARTITION child CONCURRENTLY` failed with
`syntax error … (got concurrently)` — aborting step `s2detach` of all four
detach-partition-concurrently-{1,2,3,4} specs at their first divergence. Fix:
moved trailer accept after parseObjectName; new AST field
`AlterTableAction.DetachConcurrently`; FINALIZE accepted+ignored. Executor
unchanged (synchronous detach via UnregisterPartitionChild + clear bounds).

Files: internal/parser/ddl.go (parse branch), internal/parser/ast.go (AST
field), internal/parser/alter_test.go (+TestParseAlterTableDetachPartition),
docs/design/0118-0048-* + README index, .ralph/deferral_ledger.md, fix_plan.md.

Gates: TestParseAlterTableDetachPartition PASS; full internal/parser PASS;
build+vet clean; throwaway probe confirmed detach-1 first-divergence moved
syntax-error → unmodelled `<waiting ...>` (post-detach SELECT rows now correct);
ralph-state-guard OK (auto-repaired prior clean-exit marker); pgbench smoke =
pre-commit hook.

Next step (M0118-0008 hard tail — all Effort-L, one slice per loop):
- detach-partition-concurrently-{1,2,3,4} + partition-concurrent-attach +
  alter-table-4: need transactional-DDL cross-session catalog visibility
  (shared in-memory catalog makes DETACH/ATTACH immediately visible; PG defers
  partition visibility per-snapshot + detacher waits out older snapshots;
  relpartbound→NULL at commit). This is the biggest lever (unblocks ~6 specs).
- partition-drop-index-locking: needs a real pg_locks view (relation/mode/
  granted/pid join with pg_stat_activity) + DROP INDEX partition locking.
- reindex-concurrently-toast: toast relations as first-class catalog objects
  (reltoastrelid currently hard-coded 0) + `allow_system_table_mods` GUC.
- plpgsql-toast: COMMIT/ROLLBACK in DO block (PL/pgSQL txn control) + detoast
  across commit + pg_advisory_lock blocking.
