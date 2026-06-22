Loop #47: M0118-0008 enabler (design 0118-0049) — PL/pgSQL transaction control
(`COMMIT;`/`ROLLBACK;` in a non-atomic DO block). NOT a promotion.

Done: `COMMIT`/`ROLLBACK` in a top-level DO (or procedure outside an explicit
txn) now commit/roll back the current tx and chain into a fresh one; atomic
context (DO inside BEGIN…COMMIT) ⇒ SQLSTATE 2D000. New `Context.PLpgSQLCommitChain`
callback installed by dispatch only in auto-commit mode bridges the server-owned
txn lifecycle to the executor. New `TxControlStmt` AST/parser/runtime.

Files: internal/plpgsql/ast.go (+TxControlStmt), internal/plpgsql/parser.go
(parseStmt COMMIT/ROLLBACK case), internal/executor/context.go
(+PLpgSQLCommitChain field), internal/executor/plpgsql_runtime.go (+runtime
case), internal/server/dispatch.go (install closure when autoCommit),
internal/plpgsql/parser_test.go (+TestParseTransactionControl),
internal/testport/plpgsql_txctl_test.go (3 behavioral tests),
docs/design/0118-0049-* + README index, fix_plan.md, deferral_ledger.md.

Gates run: TestParseTransactionControl PASS; behavioral commit-chain/rollback-
chain/atomic-reject PASS; TestPort_IsolationSubxidOverflow + FreezeTheDead strict
PASS (no dispatch regression); internal/executor + internal/server units PASS;
go vet clean; pgbench smoke = pre-commit hook.

Next step (M0118-0008 hard tail — all Effort-L, one slice per loop):
- plpgsql-toast NEXT blocker = PL/pgSQL `SELECT … INTO var` (scalar+record):
  goopg captures `select test1.b into x from test1` as raw embedded SQL and
  mis-parses it as SQL `SELECT … INTO <table>`. Resume = add a PL/pgSQL
  `SELECT … INTO` statement form (strip INTO target before re-parsing the query,
  bind first row to the named var(s)) + record/composite (`r record`, `r test2`,
  `r.b := …`) + `FOR rec IN SELECT … LOOP` + post-COMMIT advisory-lock wait
  (7 perms). The COMMIT-in-loop machinery (assign6/fetch-after-commit) is now
  in place via this loop's PLpgSQLCommitChain.
- Other tail specs (all Effort-L, each a new subsystem): alter-table-4 +
  partition ATTACH/DETACH (transactional catalog/inheritance visibility — lock
  is COUPLED to visibility, can't split), partition-drop-index-locking (real
  pg_locks view: relation/mode/granted/pid join with pg_stat_activity),
  reindex-concurrently-toast (TOAST relations as catalog objects +
  allow_system_table_mods).
