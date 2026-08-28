# 01 — Upstream PostgreSQL probe survey (PG 18.3)

All paths relative to `./postgres/` (read-only oracle). Line numbers refer to
the checked-out 18.3 sources.

## 1. Data plane

- Shared-memory state: one `PgBackendStatus` entry per backend in the backend
  status array (`src/backend/utils/activity/backend_status.c`, shmem setup in
  `pgstat_shmem.c`). Writers are the owning backends themselves (single-writer
  per slot, lock-free reads via sequential scans in
  `pgstat_get_activity()`).
- SQL surface: `src/backend/catalog/system_views.sql:878`
  `CREATE VIEW pg_stat_activity AS ...` (paraphrase: the view enumerates its
  columns and selects `FROM pg_stat_get_activity(NULL)` at :902).

## 2. State machine (`pgstat_report_activity`)

Call sites in `src/backend/tcop/postgres.c` define the lifecycle:

| state | where |
|---|---|
| `STATE_RUNNING` + query text | query entry points: :1027 (simple), :1409 (extended), :1679 (extended bind), :2171 (portal exec) |
| `STATE_IDLE` | :4637, top of main loop when waiting for a new message outside a transaction block |
| `STATE_IDLEINTRANSACTION` | :4584, waiting for a new message with an open transaction |
| `STATE_IDLEINTRANSACTION_ABORTED` | :4570, same but txn is aborted |
| `STATE_FASTPATH` | :4846, fast-path function calls |

Key property: the *query text stays attached* while a backend is idle /
idle-in-transaction ("last query"), matching goopg's M0118-0073 behavior.

## 3. Wait events (`pgstat_report_wait_start/end`, `wait_event.c`)

Instrumentation policy observed upstream:

1. **Report only genuine blocking.** There is no "CPU" wait event; an active
   backend not blocked shows `wait_event_type = NULL`. Probes wrap operations
   that can actually park the process.
2. **Choke points, not call sites.** Whole classes are reported from one place:
   - LWLock waits: inside `lwlock.c` acquisition paths only — no caller
     instruments itself.
   - Heavyweight-lock waits: `storage/lmgr/proc.c` `ProcSleep()` (defined at
     proc.c:1309, reports `PG_WAIT_LOCK | locktag_type` at :1454; `lock.c`
     itself contains no wait reports) emits class `Lock` with a per-locktype
     event (`relation`, `tuple`, `transactionid`, `advisory`,
     `virtualxid`, ...). `XactLockTableWait` therefore surfaces as
     `Lock:transactionid` without its own probe.
3. **IO boundaries.** Every storage primitive that can block on the OS wraps
   its syscall: counts of `pgstat_report_wait_*` occurrences per file
   (top of list): `access/transam/xlog.c` ×30, `storage/file/fd.c` ×18,
   `utils/init/miscinit.c` ×15, `access/transam/timeline.c` ×14,
   `replication/slot.c` ×12, `access/transam/slru.c` ×11,
   `access/heap/rewriteheap.c` ×8, `utils/cache/relmapper.c` ×6,
   `storage/file/copydir.c` ×6, `replication/logical/snapbuild.c` ×6,
   `access/transam/twophase.c` ×6, `storage/aio/{aio_io,method_io_uring}.c`
   ×4 each (the ×4 tier is a wider tie: `dsm_impl.c`, `reorderbuffer.c`,
   `dbcommands.c`, `xlogarchive.c` also have ×4).
4. **Client communication.** `ClientRead` / `ClientWrite` around
   secure_read/secure_write in `libpq/be-secure.c`
   (`WAIT_EVENT_CLIENT_READ` at :219, `WAIT_EVENT_CLIENT_WRITE` at :344).
5. **Timeouts.** Class `Timeout` events (`PgSleep`, `VacuumDelay`,
   `CheckpointWriteDelay`, ...) wrap deliberate sleeps, e.g. `pg_sleep` →
   `PgSleep` in `src/backend/utils/adt/misc.c`.

Event names/classes are declared in
`src/backend/utils/activity/wait_event_names.txt` and compiled into enums by
`generate-wait_event_types.pl` — i.e., upstream also interns wait identities
as small integers, never as per-call strings.

## 4. Implications for goopg (the policy we adopt)

| # | Policy | Rationale |
|---|---|---|
| P1 | Probe only operations that can park the goroutine | Active+NULL = on-CPU, mirroring PG; avoids noise and cost on hot non-blocking paths |
| P2 | One choke point per blocking primitive | `WaitForXID` ≈ `ProcSleep`/`XactLockTableWait`: instrument once, all callers inherit |
| P3 | Wait identity = interned code, zero allocation | Upstream uses enum IDs from wait_event_names.txt; goopg packs two interned strings into one uint32 |
| P4 | State transitions at the same lifecycle positions as postgres.c | statement start (RUNNING+text), loop-top IDLE / IDLEINTRANSACTION |
