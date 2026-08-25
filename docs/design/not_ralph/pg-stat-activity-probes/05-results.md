# Results — live validation (2026-08-25)

Server: goopg @ branch `waitevent-impl` (9c46be33f + script fixes), throwaway
cluster on :5533 under a systemd transient scope
(MemoryHigh=20G/MemoryMax=24G/SwapMax=0), pgbench from
`postgres/local_install/bin`.

## Baseline: `pgbench -N -c 50 -j 8 -T 60 -s 10`

550,365 txns, 0 failed, 9,175 tps; 118 samples @500 ms, 5,851 backend-samples:

| state | share | dominant waits |
|---|---|---|
| active | 79.6% | NULL wait (on-CPU); Lock:relation 107; ClientWrite 42 |
| idle in transaction | 15.3% | ClientRead / ClientWrite |
| idle | 5.0% | ClientRead |

Matches the §5 pass criteria in 03-design: idle time surfaces as ClientRead,
active time mostly has a NULL wait event, IO/Lock rows rare at low
contention.

## Contended: `pgbench -N -c 50 -T 45 -s 2` (20 tellers vs 50 clients)

414,491 txns, 0 failed, **9,210 tps — identical to -s 10**; distribution same
shape (77% active / 16.4% idle-in-txn). No `Lock:transactionid` rows.

**Finding (deferral):** with two explicit sessions (`BEGIN; UPDATE
pgbench_tellers ... WHERE tid=1` twice), session B's UPDATE returned
immediately instead of blocking on A's uncommitted tuple — goopg's MVCC does
not implement PG's first-updater-waits semantics
(`XactLockTableWait` / heap_update tuple-lock wait, `postgres/src/backend/access/heap/heapupdate.c`).
Consequently row-conflict waits cannot be exercised by pgbench simple-update;
the new `Lock:transactionid` choke-point probe in `Manager.WaitForXID` is
covered by the unit test (`waitforxid_activity_test.go`) and remains correct
for the speculative-upsert/FK paths that do call it. Closing the MVCC
semantics gap itself is out of scope here.

## Probe demonstrations over SQL

- `SELECT pg_sleep(5)` → `active | Timeout | PgSleep`
- advisory: session B blocked on `pg_advisory_lock(4242)` held by A →
  `active | Lock | advisory`; cleared after unlock
- parked inside `BEGIN` → `idle in transaction | Client | ClientRead`

## pprof correlation (60 s CPU profile, concurrent with baseline run)

Total 392.75 s samples across 50 goroutine-clients (654%):
top leaves are `Syscall6` 22% (client socket I/O), runtime futex/allocation,
executor work (dispatchSimpleQueryViaExecutor cum 78%). Probe frames:
`WaitEventStart` flat 0.31 s (**0.079%**), `packWaitStrings` flat 0.03 s —
recording overhead is invisible, confirming the zero-allocation analysis in
02-goopg-current-state.md.

## Harness notes

`scripts/pgbench-wait-sample.sh` aggregation initially mangled multi-word
states ("idle in transaction") by re-splitting on whitespace; fixed to pipe-
delimited `column -t`. Raw CSVs kept under `tmp/psa-{sample,contended}/`.
