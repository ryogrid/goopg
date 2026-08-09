(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260810-011258-006 (pgbench/nightly silent client
aborts). Root cause found and the protocol half FIXED; the fix_plan item
STAYS UNCHECKED because the stage still fails — now honestly.

The bug: goopg answered EVERY `ReadyForQuery` with `'I'`. `'T'`/`'E'` were
declared in `internal/protocol` but no path could emit them. libpq exposes the
byte as `PQtransactionStatus`; pgbench's `CSTATE_ERROR` cleanup uses it to
decide whether a failed block still owes a ROLLBACK. A permanent `'I'` took the
`TSTATUS_IDLE` branch → no ROLLBACK → the NEXT iteration's BEGIN (command index
4 of both scripts IS the BEGIN, not the UPDATE) got 25P02 → non-retriable →
client aborted. The "missing originating error" was never missing: pgbench only
prints retriable (40001/40P01) errors under `--verbose-errors`.

Fix: `connTxState.wireStatus(afterError)` (internal/server/conn_tx.go, mirrors
upstream TransactionBlockStatusCode) installed as `FrameWriter.TxStatusFn` in
serveConn; the ~47 sites now call `w.ReadyForQuery()` /
`w.ReadyForQueryAfterError()`. The afterError variant exists because goopg calls
`connTxState.Fail()` AFTER writing the error, unlike upstream.
Design: follow-up section in `docs/design/root-0002-wire-protocol.md` + README row.

Measured: client aborts 80 → 0; workload summaries 2/3 → 3/3; reported failed
transactions 0 → 1488 (0.154%, TPC-B only; -N and -S clean). Those 1488 are the
originating errors the aborts had masked: `ERROR: could not serialize access due
to concurrent update (deadlock)` (40001) from `epqWait`
(internal/executor/operators_storage.go:3534/:4186). Upstream READ COMMITTED
never raises this for TPC-B — 100 clients on 50 branch rows is a waiter chain,
not a cycle. Likely tied to `goopg_dml_conflict_no_fifo_tuple_lock` / ledger
0021-0012.

Ledger: 2 rows (plan-time errors still don't abort the block — the choke-point
fix in handleQuery is deferred because `Fail()` releases table locks and the
pinned snapshot, which the isolation specs time against; and the
serialization-error discovery).

Gates run: `TestPort_ReadyForQueryTransactionStatus` PASS (new guard, 0.69 s);
`go test ./internal/server/` PASS (22.7 s); units precommit PASS (cached);
testport subset PASS 293 s (RegressSuite, Psql001/020, TwoPhaseCommit,
SetConstraints, SSI, 4 isolation specs incl. MergeUpdate, Scripts020/100);
nightly pgbench stage re-run (12 min, results above); `make ralph-state-guard` OK.

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M-NIGHTLY is top selectable (all M0130 S1–S10 are `[x]`). Highest-value
remaining: the 40001-from-epqWait false deadlock above (finishes AI-…-006), or
AI-…-003's pg_attrdef index 2656 blocker.

In-flight: none.
