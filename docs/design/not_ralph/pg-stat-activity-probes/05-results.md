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

## Addendum — WAL persistence waits were invisible, then fixed (2026-08-26)

Review question: "why doesn't WAL flush time show up in the tables?" Answer:
fdatasync WAS being issued — goopg uses backend-driven group commit
(`Writer.FlushUpTo` runs write+fdatasync synchronously in the COMMITTING
backend's goroutine; there is no dedicated writer goroutine,
xlog/writer.go:267-270) — and probe hooks existed (`OnWALSync` /
`OnWALSyncDone`), but initdb/open.go wired them to the fixed background
walwriter procNum. Every committer's wait therefore collapsed into one
shared background row and vanished from client-backend sampling.

Fix: resolve identity per call via `activity.LookupCurrentGoroutine()` in
both callbacks (fallback: the fixed walwriter slot for unregistered callers)
— upstream parity, where XLogFlush surfaces `IO:WALSync` on the COMMITTER.
Per-connection state itself was never in question: the registry keeps a slot
per connection (procNum) and the view returns one row per client backend.

Re-run after the fix — `-N -c 50 -j 8 -T 45 -s 10`, 421,388 txns / 9,370 tps,
4,401 backend-samples / 89 sweeps:

| samples | % of total | state | wait_event_type | wait_event |
|---:|---:|---|---|---|
| 2530 | 57.5% | active | IO | WALSync |
| 1076 | 24.5% | active | (none) | (none) |
| 251 | 5.7% | idle in transaction | Client | ClientRead |
| 154 | 3.5% | idle in transaction | (none) | (none) |
| 122 | 2.8% | idle | Client | ClientRead |
| 95 | 2.2% | idle in transaction | Client | ClientWrite |
| 84 | 1.9% | active | Lock | relation |
| 57 | 1.3% | idle | (none) | (none) |
| 18 | 0.4% | active | Client | ClientWrite |
| 14 | 0.3% | idle | Client | ClientWrite |

Reading: with correct attribution, **WAL durability is the single largest
non-CPU consumer (~75% of non-on-CPU time)** — committers either hold
writeMu doing the fdatasync or park in `acquireOrWait` as group-commit
losers, all inside the WALSync window. Throughput is unchanged (9,175 →
9,370 tps): the wait was always being paid, it just was not attributable.
The earlier tables' "on-CPU 77%" was inflated by exactly this attribution
gap; treat the pre-fix distributions as client-side-only views.

## Addendum 2 — probe audit: track_io_timing was suppressing IO waits entirely (2026-08-26)

Follow-up review asked whether the WAL attribution bug had siblings. Audit of
every WaitEventStart/End site found one more systemic defect and cleared the
rest:

- **Correct attribution already**: relation locks (`executor/context.go`,
  ExecContext procNum), hash spill (`spill.go`, registry captured at
  construction from the running goroutine), client read/write
  (`postmaster/server.go`, per-connection closure), plus all choke-point
  probes added in this bundle.
- **Defect (fixed)**: every pool/storage/AIO hook (`OnPinWait/OnFlushWait/
  OnExtendWait/OnBackendWriteback*`, `OnRead|Write|Extend|SyncWait/Done`,
  aioEngine `OnWaitStart/End`) gated its whole body on
  `LookupTrackedGoroutine()`, whose fast-path flag is seeded by
  track_io_timing. With the GUC at its default OFF, **no DataFile*/AIO/
  BufferPin wait event could ever be emitted** — upstream reports these
  waits unconditionally and uses the GUC only for pg_stat_io *_time.
  Fix: wait-event windows now resolve identity per firing via
  `LookupCurrentGoroutine()` and always emit; only the *_time accumulation
  remains gated on the backend's track_io_timing.

Deterministic proof: during concurrent pgbench + repeated `CHECKPOINT`,
client backends now show `active | IO | DataFileSync` (12 samples) — a path
that could not report before. Full re-run `-N -c 50 -j 8 -T 45 -s 10`
(429,691 txns / 9,548 tps, 4,401 backend-samples / 89 sweeps):

| samples | % of total | state | wait_event_type | wait_event |
|---:|---:|---|---|---|
| 2413 | 54.8% | active | IO | WALSync |
| 1182 | 26.9% | active | (none) | (none) |
| 276 | 6.3% | idle in transaction | Client | ClientRead |
| 146 | 3.3% | idle in transaction | (none) | (none) |
| 102 | 2.3% | idle | Client | ClientRead |
| 101 | 2.3% | idle in transaction | Client | ClientWrite |
| 75 | 1.7% | active | Lock | relation |
| 60 | 1.4% | idle | (none) | (none) |
| 27 | 0.6% | active | Client | ClientWrite |
| 18 | 0.4% | idle | Client | ClientWrite |
| 1 | 0.02% | active | IO | DataFileRead |

The lone DataFileRead row is itself evidence: a cold cache-miss read
surfacing as a client-backend wait was impossible before the fix. tps moved
9,370 → 9,548 (noise range); the added per-I/O goroutine lookup costs tens
of ns against real syscalls and is invisible in pprof.

## Addendum 3 — WALWrite/WALSync split, then corrected labeling (2026-08-26)

Review challenged the post-split distribution ("WALWrite cannot exceed
WALSync: pwrite only copies into page cache, fdatasync waits for the device").
Correct — the 57% labeled WALWrite after the mechanical split was NOT pwrite
time: it was committers PARKED inside `acquireOrWait` queueing for the WAL
write lock, which I had mislabeled as IO:WALWrite to keep them visible.

Relabeled per upstream semantics: those parks are `LWLock:WALWriteLock`
(writeMu IS the named WAL write lock, direct analog of the upstream tranche;
likewise stripe-wait = `LWLock:WALInsert`), leaving IO:WALWrite for the
actual drain/pwrite phase and IO:WALSync for the fdatasync barrier. These
are the bundle's first LWLock-class emitters, added at named choke points —
consistent with §4 as amended.

Final run `-N -c 50 -j 8 -T 45 -s 10` (417,277 txns / 9,277 tps, 4,401
backend-samples / 89 sweeps):

| samples | % of total | state | wait_event_type | wait_event |
|---:|---:|---|---|---|
| 2342 | 53.2% | active | LWLock | WALWriteLock |
| 1172 | 26.6% | active | (none) | (none) |
| 314 | 7.1% | idle in transaction | Client | ClientRead |
| 146 | 3.3% | idle in transaction | (none) | (none) |
| 122 | 2.8% | idle | Client | ClientRead |
| 91 | 2.1% | idle in transaction | Client | ClientWrite |
| 83 | 1.9% | active | IO | WALSync |
| 57 | 1.3% | active | Lock | relation |
| 32 | 0.7% | idle | (none) | (none) |
| 21 | 0.5% | active | Client | ClientWrite |
| 19 | 0.4% | idle | Client | ClientWrite |
| 2 | 0.05% | active | IO | AIO |

The shape now matches both physics and vanilla-PG field behavior: queueing
on the WAL write lock dominates committer wait time; true IO:WALWrite does
not even reach sampling resolution (pwrite into page cache); IO:WALSync is
real but small here because group-commit batches amortize one fdatasync
across many committers on this WSL2 host. tps stayed in the 9.3–9.5k band.
