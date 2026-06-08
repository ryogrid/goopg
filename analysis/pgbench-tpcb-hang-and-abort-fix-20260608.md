# pgbench TPC-B hang + "current transaction is aborted" fix (2026-06-08)

Base commit: `4285554f` (branch `align-data-structure-with-pg`)

Follow-up to `analysis/pgbench-tellers-duplicate-key-fix-20260608.md`. After the
duplicate-key fix landed, CI (`pgbench -T 30 -c 2 -j 2 -P 5`) progressed ~15 s
then:

```
pgbench: error: client 0 script 0 aborted in command 4 query 0:
ERROR:  current transaction is aborted, commands ignored until end of transaction block
```

and the run **hung** (never completed). The CI machine is ~3× faster than the
local box (~500 vs ~170 tps), so its `-c 2` reaches a contention level the local
box only reaches at `-c 8`; the bug reproduces locally with
`pgbench -T 30 -c 8 -j 8`.

Two distinct, compounding bugs were found.

## Bug A — spurious 40001 under READ COMMITTED (the client abort)

### Symptom
Some `UPDATE pgbench_tellers` / `pgbench_branches` returned SQLSTATE 40001
("could not serialize access due to concurrent update") mid-transaction. pgbench
then sent the next statement, got 25P02 ("current transaction is aborted"), and
aborted the client → `Run was aborted` → non-zero exit.

### Root cause
The EvalPlanQual retry loop capped re-checks at `maxEPQRetries = 3` for **all**
isolation levels. Under high contention many clients "lap" the same hot row
(teller / the single branch); a backend that is re-stamped more than 3 times
escalates to 40001. Instrumentation confirmed **every** spurious 40001 came from
the cap (`iso=0` = READ COMMITTED), none from the wait-for-graph deadlock
detector. PostgreSQL never surfaces a serialization failure for plain
UPDATE/DELETE row contention under READ COMMITTED — it blocks
(`XactLockTableWait`) and re-evaluates against the latest version until it can
apply the change.

### Fix (`internal/executor/operators_storage.go`)
`epqRetryLimit(iso)` returns a high backstop (`maxEPQRetriesRC = 100000`) under
READ COMMITTED and the prompt `maxEPQRetries = 3` under REPEATABLE READ /
SERIALIZABLE. The three EPQ cap sites (`updateOp.updateViaIndex`,
`updateOp.Next` seqscan, `deleteOp.Next`) now use it. Each re-check is paced by
`epqWait` blocking on the current `xmax` holder, and the wait-for-graph still
breaks genuine deadlock cycles, so the high RC backstop is "retry until it
applies" without busy-spinning, while RR/SERIALIZABLE keep first-update-wins
semantics.

## Bug B — leaked XID on disconnect (the hang)

### Symptom
Once one client aborted (Bug A) and dropped its connection, the remaining
clients hung forever; the run never finished.

### Root cause
A `SIGQUIT` runtime traceback (the Go-1.26 pprof goroutine profiler did **not**
report blocked backend goroutines — it showed only 16, hiding the real ones)
showed every surviving backend blocked in
`mvcc.WaitForXID(..., 13) ← epqWait ← tryApplyHOTUpdate ← updateViaIndex`,
all waiting on the **same** XID 13. XID 13 belonged to the aborted client, which
had disconnected with an explicit transaction still open. `runPostStartupLoop`
`return`s on disconnect/EOF and `serveConn`'s defers close the socket and
unregister the backend — but **nothing rolled back the open transaction**, so
XID 13 was never cleared from the ProcArray. `xidInProgress(13)` stayed true and
every `WaitForXID(13)` waiter blocked permanently.

### Fix (`internal/server/server.go`)
`rollbackOpenTxnOnTeardown` is deferred at the top of `runPostStartupLoop`: on
any exit, if an explicit transaction is still open it is rolled back (mirroring
the dispatch `planner.TxRollback` path). `TxnMgr.Rollback` clears the XID and
broadcasts `commitCond`, waking every blocked `WaitForXID` waiter. A no-op in
auto-commit mode (per-statement transactions finish inline).

## Verification

| config | before | after |
|--------|--------|-------|
| `-c 2 -j 2 -T 30` ×3 | clean locally (CI hung) | exit 0, 0 failed, 0 aborts |
| `-c 4 -j 4 -T 20`    | aborts + hang | exit 0, 0 failed, 0 aborts |
| `-c 8 -j 8 -T 30`    | hang (7 backends on WaitForXID(13)) | exit 0, 0 failed, 0 aborts, 9849 tx, 328 tps |
| `-c 16 -j 16 -T 20`  | hang | exit 0, 0 failed, 0 aborts |

TPS at `-c 8` nearly doubled (171 → 328) because clients no longer waste work on
spurious aborts/retries. Unit tests: `TestEPQRetryLimitByIsolation` (executor),
`TestRollbackOpenTxnOnTeardownReleasesXID` (server, asserts the ProcArray slot
is freed on teardown). `go build ./...` clean; executor/mvcc/storage/btree
suites pass; server package has one pre-existing unrelated failure
(`TestPGHeapEncodingPreservesTextLikeInsertCoercions`, also red at HEAD).

Verification was performed in an isolated git worktree because a concurrent
Ralph loop was mutating the executor/server packages in the main checkout at the
same time (see [[concurrent_ralph_loops_corrupt_tree]]).

## Notes / possible follow-ups

- A genuine deadlock under READ COMMITTED is still reported by the wait-for-graph
  as 40001; upstream uses 40P01 (deadlock_detected). pgbench retries both, so
  this is cosmetic, but aligning the SQLSTATE would be more PG-faithful.
- The RC backstop (100000) bounds a pathological livelock rather than enforcing
  fairness; goopg lacks the tuple-lock FIFO ordering PG uses to guarantee
  progress. Not reachable under any realistic workload.
