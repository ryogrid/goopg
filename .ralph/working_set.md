(idle — nothing in flight)

Last landed (loop #5): regression fix (design 0118-0134) — two pass-required
isolation specs `vacuum-concurrent-drop` + `vacuum-skip-locked` had been silently
RED since commit d1f40e28 (async-notify, design 0118-0090). Root cause found by
git-bisecting `TestPort_IsolationVacuumConcurrentDrop`.

The specs' lock step is a single 2-statement step `{ BEGIN; LOCK part1 IN SHARE
MODE; }`. The async-notify loop made the isolation runner send a multi-statement
step as ONE simple-query message (`execMultiStatement`), exposing a latent server
bug: `dispatchSimpleQueryViaExecutor` seeded `ectx.TxnLockBackendID` ONCE before
the per-statement loop from the message-entry txn state (autocommit for
`BEGIN; LOCK …`), so the later `LOCK` saw `TxnLockBackendID==0` →
`acquireRelLockTxn` no-op (display-only) → no real ShareLock → concurrent
ANALYZE/VACUUM never `<waiting>`-blocked → drop re-check WARNING never fired.

Fix (internal/server/dispatch.go): refresh `ectx.TxnLockBackendID` at the top of
the statement loop from the LIVE `connTx.InExplicit()` state, so a transaction-
scoped lock following an in-message `BEGIN` is held to commit, matching upstream
`exec_simple_query` (one PQexec = one transaction command list).

Gates: strict `TestPort_IsolationVacuumConcurrentDrop`/`VacuumSkipLocked` PASS
(were red at HEAD); FULL `TestPort_Isolation*` suite PASS (657s, exit 0, no
regression); gofmt/vet clean; pgbench smoke = pre-commit hook.

Note: M0118-0009's own specs are all resolved (stats was the final one, loop #4);
the two specs fixed this loop belong to M0118-0008. No fix_plan item left open by
this loop. Candidate next: confirm remaining open items (M0118-0002 predicate-gin/
gist need real GIN/GiST AMs; M0118-0004 deadlock-parallel needs lock groups).
