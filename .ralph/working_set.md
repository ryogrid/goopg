(idle — nothing in flight)

Last loop (#22, M0118-0008): promoted `reindex-schema` (4th M0118-0008
promotion, design 0118-0030). Committed + pushed.

## What landed
- `reindexOp.Next` (operators_reindex.go) gained a `SCHEMA` case: enumerate the
  schema's non-virtual user tables via `Catalog.TablesInSchema`, sort by OID
  (creation order) in new helper `schemaRelsByOID`, then per relation take a
  `ShareLock` (plain) via `acquireRelLockMaybeTransient`, or — for CONCURRENTLY —
  `waitForRelationLockers` (reused 0118-0029 primitive).
- `context.go`: extracted `acquireSequenceLockTxn` body into mode-parameterised
  `(*Context).acquireRelLockMaybeTransient(rel, mode)` (held-to-commit in explicit
  txn; transient acquire+release in autocommit so the wait still happens).
  `acquireSequenceLockTxn` now delegates with `RowExclusiveLock` (no behaviour
  change).
- `TestPort_IsolationReindexSchema` strict PASS (2 perms).
GATES: build PASS; reindex-schema strict PASS; lock-sibling regression
(reindex-concurrently/create-trigger/sequence-ddl/drop-index-concurrently-1/
timeouts-table-level) PASS; -race lockmgr PASS; executor units PASS; pgbench
smoke 0-failed (pre-commit). CSV field count verified 7; coverage/inventory/
port-status regenerated.

KEY METHODOLOGY: throwaway zz_probe_test.go (RunAndCompare → log status+diff)
ranked candidates. multiple-cic was the alternative but needs immutable-function
constant-folding in a partial-index WHERE predicate during CREATE INDEX
CONCURRENTLY build (PG evaluates `lck_shr(281457)` via eval_const_expressions →
blocks on advisory lock) — harder, deferred.

NEXT loop candidates (remaining M0118-0008 tail — probe-first to confirm
divergence):
- `acquireRelLockMaybeTransient` + `waitForRelationLockers` are REUSABLE.
- `multiple-cic`: CREATE INDEX CONCURRENTLY must constant-fold an IMMUTABLE
  partial-index predicate fn during build (advisory-lock block). Probe showed
  s1i does not wait — predicate fn never called.
- `reindex-concurrently-toast`: needs `allow_system_table_mods` GUC (no-op bool
  accept) + pg_toast.<name> reindex + ALTER TABLE/INDEX RENAME of toast rels.
- biggest leverage = ROLE/ACL infra (CREATE ROLE/GRANT/SET ROLE/permission-denied)
  unblocks truncate/vacuum/cluster-conflict {,-partition}.
- `alter-table-1/2`: ADD CONSTRAINT … NOT VALID + VALIDATE CONSTRAINT + FK
  validation + lock semantics.
- partition specs: ATTACH/DETACH PARTITION (+ CONCURRENTLY, pg_backend_pid+cancel).

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). D-002 CSV is one
giant single-line row #13 (field 6 rationale COMMA-FREE; append before
`,M0060-0004`; verify `awk -F, 'NR==13{print NF}'` == 7). regen: gen-oracle-
port-status + gen-isolation-coverage --repo-root . + gen-oracle-inventory
--repo-root . NEVER `cd` into postgres/. never gofmt -w. Untracked postgres/ +
weekly_loc.* + requirements.txt are stray — leave. .ralph/progress.json is
driver-managed — don't commit it.
