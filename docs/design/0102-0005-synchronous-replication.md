# 0102-0005 — Synchronous Replication (`synchronous_standby_names` + commit-wait + standby feedback)

**Status:** accepted
**Date:** 2026-05-13 (drafted), 2026-05-14 (accepted)
**Milestone:** M0102-0005
**Upstream reference:** `postgres/src/backend/replication/syncrep.c` (`SyncRepWaitForLSN`, `SyncRepReleaseWaiters`), `postgres/src/backend/replication/walsender.c:2721` (sync standby tracking + `WalSndKeepaliveIfNecessary`), `postgres/src/backend/replication/walreceiver.c` (feedback message emission), `postgres/src/include/replication/syncrep.h` (mode constants), `postgres/src/backend/utils/misc/guc_tables.c` (`synchronous_standby_names`, `synchronous_commit` GUC defs).

## Problem

goopg has the `synchronous_commit` GUC stored (`internal/config/defaults.go:234`,
`BootVal: "on"`) but no wait semantics: a transaction that hits COMMIT writes
its commit record to local WAL and returns immediately, regardless of whether
any standby has received the WAL. The `WaitSyncRep` wait-event constant
(`internal/activity/activity.go:70`) is registered but never consumed.

For the M0102 E2E tests, the sync subtest requires the primary's COMMIT to
**block** until at least one configured standby has applied (or flushed, or
written) the commit's LSN, so that after a SIGKILL on the primary the
promoted standby provably has every acknowledged commit. Without this,
"zero-loss failover" is not testable on goopg-as-primary.

## Upstream contract

### `synchronous_standby_names`

From `postgres/src/backend/utils/misc/guc_tables.c` and `syncrep.c`:

Grammar (simplified):

```
synchronous_standby_names = ''                       # async (default)
                          | 'standby1'                # one standby named
                          | 'standby1, standby2'      # any of these (PG-pre-9.6 form, now == FIRST 1)
                          | 'FIRST 2 (a, b, c)'       # first 2 in list order
                          | 'ANY 2 (a, b, c)'         # any 2 of the 3
```

The semantics:

- **FIRST n (names…)**: wait for the first n names in the list to ack.
- **ANY n (names…)**: wait for any n of the listed names to ack.

A standby is identified by its `application_name` (set in
`primary_conninfo='… application_name=…'`).

### `synchronous_commit` levels

From `syncrep.h`:

| Level | Constant | Behaviour |
|---|---|---|
| `off`              | `SYNCHRONOUS_COMMIT_OFF`              | No wait, no local fsync (async to disk too) |
| `local`            | `SYNCHRONOUS_COMMIT_LOCAL_FLUSH`      | Wait for local fsync only |
| `remote_write`     | `SYNCHRONOUS_COMMIT_REMOTE_WRITE`     | Wait for standby to receive (write to OS) |
| `on` (= remote_flush) | `SYNCHRONOUS_COMMIT_REMOTE_FLUSH`     | Wait for standby fsync |
| `remote_apply`     | `SYNCHRONOUS_COMMIT_REMOTE_APPLY`     | Wait for standby to apply (visible to readers) |

Default `on`. M0102 targets `remote_apply` for the sync subtest because it
is the strongest invariant (and a passing `remote_apply` implementation
subsumes the weaker levels with a one-line dispatch).

### `SyncRepWaitForLSN`

The commit path (after writing the COMMIT WAL record + local flush) calls
`SyncRepWaitForLSN(XactLastCommitEnd)`:

1. Resolve current sync standby set via `SyncRepGetSyncStandbys`.
2. If empty (no eligible standby) and `synchronous_commit ≥ remote_*`,
   log a warning and proceed without waiting (PG-parity: don't hang a
   primary just because the standby is down — `wal_sender_timeout` handles
   that policy).
3. Otherwise enqueue self in the syncrep wait queue keyed by LSN, sleep on
   a CV until awakened by `SyncRepReleaseWaiters`.

### `SyncRepReleaseWaiters`

Called from the walsender's main loop whenever the standby's reported
write/flush/apply LSN advances. Walks the queue and releases every waiter
whose target LSN ≤ the new reported LSN.

### Standby feedback (walreceiver)

The standby walreceiver periodically (every `wal_receiver_status_interval`,
default 10 s, or immediately upon catching up) sends a Standby Status
Update message:

```
'r' (1 byte) | write_lsn (8) | flush_lsn (8) | apply_lsn (8) | clock_time (8) | reply_requested (1)
```

The primary's walsender consumes this and calls `SyncRepReleaseWaiters`.

## Solution

### 1. GUC: `synchronous_standby_names`

In `internal/config/defaults.go`, add:

```go
{Name: "synchronous_standby_names", Type: TypeString, BootVal: "", Context: SighupContext},
```

Parser: a small new `internal/wal/syncrep_parse.go` reading the FIRST/ANY/named-list grammar.

### 2. Primary-side wait: `internal/wal/syncrep.go` (new)

Modelled directly on `postgres/src/backend/replication/syncrep.c`:

```go
type SyncRepMode int
const (
    SyncRepOff SyncRepMode = iota
    SyncRepRemoteWrite
    SyncRepRemoteFlush
    SyncRepRemoteApply
)

type SyncRep struct {
    mu       sync.Mutex
    cond     *sync.Cond
    waiters  []*syncRepWaiter   // sorted by LSN ascending
    standbys map[string]*standbyProgress  // by application_name
    rule     syncRepRule        // parsed synchronous_standby_names
}

func (s *SyncRep) WaitForLSN(ctx context.Context, lsn uint64, mode SyncRepMode) error
func (s *SyncRep) UpdateStandbyProgress(appName string, write, flush, apply uint64)
func (s *SyncRep) ReleaseWaiters() // walks waiters; releases those whose LSN ≤ enough_standbys_min_lsn
```

`WaitForLSN`:

- Compute `enoughLSN` per the rule: for `FIRST 1 (a)`, it's a.<mode>_lsn;
  for `ANY 2 (a,b,c)`, it's the 2nd-largest of {a.lsn, b.lsn, c.lsn} at the
  chosen mode level.
- If `lsn ≤ enoughLSN` immediately, return.
- Else enqueue + `cond.Wait` until woken by `ReleaseWaiters` or ctx
  cancellation.

`UpdateStandbyProgress`: called by the walsender when it receives a Standby
Status Update. Updates `standbys[appName]`, then calls `ReleaseWaiters`.

### 3. Commit-path hook

In `internal/executor/operators_tx.go` (or whichever site emits the COMMIT
WAL record), after the local fsync:

```go
if cfg.SyncRep != nil && cfg.SyncRep.NeedsWait() {
    mode := mapSyncCommitToMode(session.SyncCommitLevel)
    _ = cfg.SyncRep.WaitForLSN(ctx, commitLSN, mode)
    // ctx.Done() may fire on cancel; we still return success to the client —
    // upstream PG-parity: a cancelled WAIT means the commit will be replayed
    // on crash anyway. Document this matches PG.
}
```

### 4. Walsender feedback consumption

`internal/server/replication.go` walsender loop: on Standby Status Update
message receipt, call `cfg.SyncRep.UpdateStandbyProgress(appName, w, f, a)`.

### 5. Standby-side feedback emission

`internal/server/walreceiver.go`: ensure (or add) periodic Standby Status
Update emission. Verify the existing implementation; if absent, emit every
`wal_receiver_status_interval` and also immediately when caught up. apply_lsn
must reflect the actual replayed LSN (from the replayer's state), not the
write_lsn.

### 6. Wait-event registration

The `WaitSyncRep` constant at `internal/activity/activity.go:70` already
exists; wire `cfg.SyncRep.WaitForLSN` to call
`activity.WaitEventStart(... WaitSyncRep)` at sleep start and
`WaitEventEnd` on wake/return.

## Files to create / modify

| File | Change |
|---|---|
| `internal/config/defaults.go` | Add `synchronous_standby_names` GUC |
| `internal/wal/syncrep.go` (new) | SyncRep struct, WaitForLSN, ReleaseWaiters |
| `internal/wal/syncrep_parse.go` (new) | FIRST/ANY/list parser |
| `internal/wal/syncrep_test.go` (new) | Race-tested unit tests |
| `internal/executor/operators_tx.go` | Commit-path WaitForLSN call |
| `internal/server/replication.go` | Walsender feedback dispatch → UpdateStandbyProgress |
| `internal/server/walreceiver.go` | Confirm/extend Standby Status Update emission |

## Verification

```bash
# Unit test
go test -race -run TestSyncRep ./internal/wal/

# Focused E2E: kill standby with synchronous_commit=remote_apply; primary commits must block
# Covered by a new internal/testport/e2e_syncrep_block_test.go (added in M0102-0005)
```

The full E2E coverage comes from the `sync_remote_apply` subtests in
M0102-0006 (PG primary + goopg standby) and M0102-0007 (goopg primary + PG
standby).

## Risks

- **Deadlock between commit-path wait and the walsender goroutine.** If the
  walsender holds a lock that the SyncRep callback contends on, a slow ack
  can block both. Mitigation: SyncRep's mutex is fine-grained; never call
  back into the walsender from inside `WaitForLSN`.
- **Hung commits on standby disconnect.** Upstream PG hangs commits in
  `remote_*` mode when no eligible standby is configured (intentionally —
  operator policy). The M0102 sync subtest requires the standby to be
  present; for the SIGKILL scenario we kill the **primary**, not the
  standby, so this is not encountered. Document so future readers don't
  attempt the inverse.
- **`wal_receiver_status_interval` tunability.** Default 10 s is too slow
  for tight tests. Reduce to 200 ms in the M0102 sync subtest config. The
  GUC already exists in the catalog (verify); if not, add it.
- **Re-ordering of WaitForLSN's `mode` mapping.** Internal mode constants
  must match upstream `SyncRepGetSyncStandbysAccordingToConfig`'s mapping.
  Pin via the cross-reference table in the design doc and a parity test.
- **Application_name plumbing.** The standby's libpq connection string
  must carry `application_name=<name>` and walsender must propagate it to
  the SyncRep registration. Verify both sides; the goopg walreceiver in
  `cmd/goopg/standby.go` builds the conninfo — add `application_name` if
  missing.

## Implementation log (2026-05-14)

Landed:

- `internal/wal/syncrep.go` — `SyncRep` type with `WaitForLSN`,
  `UpdateStandbyProgress`, `ForgetStandby`, `SetStandbyNames`,
  `NeedsWait`. Mode mapping via `ParseSyncCommitLevel`.
- `internal/wal/syncrep_parse.go` — parser for FIRST/ANY/legacy
  bare-list grammar.
- `internal/wal/syncrep_test.go` — race-tested unit suite covering:
  rule parsing (15 cases incl. malformed); off/empty-rule fast paths;
  FIRST/ANY semantics; write-vs-flush-vs-apply mode distinction;
  immediate release; context cancellation; ForgetStandby; concurrent
  update/wait stress; monotonic-progress invariant; rule-relaxation
  release.
- `internal/config/defaults.go` — `synchronous_standby_names` GUC
  registered (`ContextSigHup`); `synchronous_commit` retyped from bool
  to string so `remote_apply` etc. parse without a GUC error. Default
  `on` preserved.
- `internal/initdb/open.go` — `Runtime.SyncRep` constructed
  unconditionally; empty-rule default means commits are async until
  the operator sets the GUC.
- `internal/server/server.go` (`Config.SyncRep`),
  `internal/server/replication.go` (walsender forwards each Standby
  Status Update into `SyncRep.UpdateStandbyProgress`, registers
  `ApplicationName` on the senderHandle, calls `ForgetStandby` on
  walsender disconnect), `internal/server/logicalwalsender.go`
  (logical walsender same dispatch path).
- `internal/server/walreceiver.go` — `WalReceiverConfig.ApplicationName`
  forwarded as the `application_name` startup parameter so the primary's
  SyncRep can match the standby; `ApplyLSNFunc` lets the standby report
  apply_lsn distinct from received-LSN so `remote_apply` waits see
  real replay progress instead of receive-only.
- `internal/executor/context.go` (`SyncRep`, `WAL`, `SyncCommitMode`
  fields), `internal/executor/operators_tx.go` (`execCommit` calls
  `SyncRep.WaitForLSN(ctx.Ctx, WrittenLSN, mode)` after a successful
  `TxnMgr.Commit`).
- `internal/server/dispatch.go` + `dispatch_extended.go` — populate
  `ectx.SyncRep`, `ectx.WAL`, and `ectx.SyncCommitMode` (parsed from
  session-effective `synchronous_commit`) on every dispatch.
- `cmd/goopg/main.go` — plumb `cfg.SyncRep = rt.SyncRep`; on startup
  read `synchronous_standby_names` from the GUC and call
  `SetStandbyNames`. Walreceiver gets `ApplicationName` from
  `primary_conninfo`'s `application_name=…` token via the new
  `parsePrimaryConninfoFull` helper.

Deferred (will land with the E2E harness in M0102-0006/0007):

- Per-statement wait-event registration (`activity.WaitSyncRep`) — the
  constant exists but the executor still binds a single wait window per
  commit rather than per WaitForLSN sleep cycle. The on-disk pin remains
  unchanged so a future loop can light up wait_event = "SyncRep" without
  GUC surface changes.
- `pg_reload_conf()` re-applying `synchronous_standby_names` at runtime.
  The reload pipeline already calls back into the registry on SIGHUP;
  routing the new value into `rt.SyncRep.SetStandbyNames` is a single
  hook addition once the reload path is exercised by a regression test.
- Apply-LSN feedback from the standby's StreamReplayer. The walreceiver
  carries a `ApplyLSNFunc` callback; standby start-up doesn't currently
  install one (it reuses the received-LSN). The M0102-0006 sync subtest
  is the first user of real apply-LSN feedback and will wire it.

## Verification

```bash
go test -race -count=1 -run TestSyncRep ./internal/wal/
go test -race -count=1 ./internal/wal/ ./internal/server/ ./internal/executor/ \
  ./internal/mvcc/ ./internal/initdb/ ./internal/config/ ./cmd/goopg/
```

All green as of 2026-05-14.
