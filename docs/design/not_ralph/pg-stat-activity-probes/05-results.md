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

## Full wait-event distribution (recomputed from the raw `samples.csv`)

Provenance: `tmp/psa-sample-20260825-200107/samples.csv` (baseline) and
`tmp/psa-contended/samples.csv`; `(none)` = empty wait columns in the view
(backend not inside any probe window at sample time). Percentages are shares
of all client-backend samples in the run.

### Baseline — `-N -c 50 -j 8 -T 60 -s 10` (5,851 backend-samples / 118 sweeps)

| samples | % of total | state | wait_event_type | wait_event |
|---:|---:|---|---|---|
| 4511 | 77.1% | active | (none) | (none) |
| 508 | 8.7% | idle in transaction | Client | ClientRead |
| 230 | 3.9% | idle in transaction | (none) | (none) |
| 187 | 3.2% | idle | Client | ClientRead |
| 160 | 2.7% | idle in transaction | Client | ClientWrite |
| 107 | 1.8% | active | Lock | relation |
| 81 | 1.4% | idle | (none) | (none) |
| 42 | 0.7% | active | Client | ClientWrite |
| 25 | 0.4% | idle | Client | ClientWrite |

### Contended — `-N -c 50 -T 45 -s 2` (4,401 backend-samples / 89 sweeps)

| samples | % of total | state | wait_event_type | wait_event |
|---:|---:|---|---|---|
| 3283 | 74.6% | active | (none) | (none) |
| 377 | 8.6% | idle in transaction | Client | ClientRead |
| 214 | 4.9% | idle in transaction | (none) | (none) |
| 211 | 4.8% | idle | Client | ClientRead |
| 132 | 3.0% | idle in transaction | Client | ClientWrite |
| 76 | 1.7% | active | Lock | relation |
| 71 | 1.6% | idle | (none) | (none) |
| 29 | 0.7% | active | Client | ClientWrite |
| 8 | 0.2% | idle | Client | ClientWrite |

### Reading: what a transaction spends time on besides CPU

On-CPU share is 77.1% / 74.6%; normalizing the remainder to 100% shows what
the other ~23–25% of backend lifetime goes to:

| bucket | baseline (share of non-on-CPU) | contended (share of non-on-CPU) |
|---|---:|---:|
| Client round-trips (ClientRead + ClientWrite, any idle state) | 68.8% | 66.8% |
| Lock : relation (acquired mid-statement, `active`) | 8.0% | 6.8% |
| idle-state sampling gaps ((none)-wait idle slices) | 23.1% | 26.3% |

Interpretation:

1. Excluding CPU, the single largest consumer is **client communication** —
   protocol round-trips (read next command / write results), which is the
   expected profile for a pgbench driver doing 3 statements per transaction
   over loopback TCP. The server-side engine itself is CPU-bound here, not
   blocked.
2. Genuine intra-server blocking visible to clients is **Lock:relation**
   (catalog/table lock acquisition under 50 concurrent sessions): ~8% of
   non-CPU time. No IO, BufferPin or transactionid rows appear — consistent
   with the MVCC finding above (no first-updater waits) and a warm,
   memory-resident dataset.
3. The `(none)`-wait idle slices are sampling-window artifacts, not hidden
   work: they capture the microseconds between `UpdateState(idle...)` and
   the next `WaitEventStart(ClientRead)` park (dispatch.go:1313 →
   postmaster/server.go:1140). They shrink toward zero with finer probe
   placement (setting the idle state inside the read-park window) but are
   harmless at this granularity.
