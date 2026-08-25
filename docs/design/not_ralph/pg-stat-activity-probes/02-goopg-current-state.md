# 02 — goopg current state (base 977a487e0)

All paths relative to the repo root; line numbers refer to this base commit.

## 1. Architecture

`internal/utils/activity/` implements a goroutine-model analog of PG's backend
status array:

- `ActivityRegistry` (`registry.go`) holds one preallocated `activitySlot`
  per MVCC proc number; background workers occupy reserved slots
  (`WalWriterIdx`, `CheckpointerIdx`, `BgwriterIdx`, `AutovacuumIdx`).
- Hot fields are atomics on the slot: packed wait info (`uint32`),
  state code, monotonic timestamps taken via `runtimeshim.Nanotime()`
  (~20 ns/op; wired in design 0107-0008e) and converted to wall clock only in
  `Snapshot()` via a cached `(monoEpoch, wallEpoch)` pair.
- Cold, rarely-written strings (user, datname, application_name, query text)
  live behind an atomic pointer to a `*coldActivity` struct.
- Wait identities are interned at package init into two read-only maps
  (`waitTypeStringCode`, `waitEventStringCode`, registry.go:695-762);
  `packWaitStrings` (registry.go:781) combines both codes with OR.

## 2. Hot-path cost / GC analysis

| operation | work | allocations |
|---|---|---|
| `WaitEventStart` (registry.go:278) | bounds check + 2 read-only map lookups + `atomic.Store(uint32)` + Nanotime store | **0** |
| `WaitEventEnd` | bounds check + load/clear + Nanotime | **0** |
| `UpdateState` (registry.go:317) | cold.Load + string switch + atomic stores; `c.Query.Store(&query)` boxes one `*string` per statement | 1 × 8-byte box per statement (negligible next to parser/executor traffic) |
| `Snapshot()` | read-side formatting (RFC3339Nano etc.), only when pg_stat_activity is queried | read-side only |

GC impact: slot storage is fixed-size and allocated once at server start;
the recording path creates no garbage that survives or churns across GC
cycles — sweep pressure from probes is effectively zero. **The foundation
meets the "low overhead / few extra GC sweeps" requirement and needs no
redesign.**

Concurrency notes: slots are single-writer per procNum (owning connection
goroutine), readers never block (atomic loads). Background-slot registration
uses `RegisterBackground` to avoid PID/procNum identity-space conflation
(fixed in completed_deferral_004).

## 3. Existing probe inventory

| class : event | site |
|---|---|
| Lock : relation | `internal/executor/context.go:1219,1416,1609,1709-1722` (relation-lock acquisition paths) |
| IO : BuffileRead/Write | `internal/executor/spill.go:124-264` (hash spill) |
| IO : DataFileRead/Write/Extend/Sync | `internal/initdb/open.go` storage-manager callbacks (:2362-2397) |
| BufferPin : BufferPin | `internal/initdb/open.go:797` (bufpool pin callback) |
| IO : AIO | `internal/initdb/open.go:2352` |
| IO : WALWrite/WALSync | `internal/initdb/open.go:448,2407` |
| Client : ClientRead/Write | `internal/postmaster/server.go:1139-1154` |

State transitions: `internal/postmaster/dispatch.go:698` sets
`active` + query text at statement start; `dispatch.go:1313` sets `idle`;
session end-of-transaction flows through `sess.EndTransaction`
(`dispatch.go:507`, `dispatch_extended.go:291`). The idle-in-transaction
surface is covered by tests
(`internal/testport/e2e_goopg_crashstart_on_pgdata_test.go:480`).

## 4. Gaps found

| id | gap | evidence |
|---|---|---|
| G1 | Row-conflict waits report no wait event. `Manager.WaitForXID` (`internal/access/transam/manager.go:846`) parks the goroutine on `commitCond` but emits nothing; 13+ executor call sites block through it (`operators_upsert.go:426,811`, `operators_fk.go:383,1465,1616`, `operators_vacuum.go:239`, `operators_storage.go:273,317,8730`, `operators_lockrows.go:1279,1323,1470,2308`). PG surfaces this exact wait as `Lock:transactionid`. | choke point exists, probe missing |
| G2 | `pg_sleep` sleeps without `Timeout:PgSleep`. `evalPgSleep` (`internal/executor/expr.go:18030`) selects on `time.After`/ctx.Done directly. | probe missing |
| G3 | Advisory-lock blocking path (`internal/executor/advisory.go`) contains no `WaitEventStart`; PG shows `Lock:advisory`. A real park exists (waiter `ready chan` at :47, waiter registration at :220), so this resolves to "wire it". | verify + wire during implementation |
| G4 | `LWLock` class has zero emitters (only the code mapping exists). Go mutex/cond parks (bufpool, WAL insert) are invisible to us. | policy decision needed → see 03-design §4 |
| G5 | `Lock:tuple` constant defined but unused; row-level conflicts currently all funnel through WaitForXID (transactionid). Acceptable for now; revisit if a separate tuple-lock queue appears. | documented deferral |
