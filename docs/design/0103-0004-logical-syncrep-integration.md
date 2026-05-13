# 0103-0004 — Logical Walsender → SyncRep Wait Queue Integration

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0103-0005
**Depends on:** M0102-0005 (`internal/wal/syncrep.go` SyncRep wait primitive).
**Upstream reference:** `postgres/src/backend/replication/walsender.c::ProcessStandbyReplyMessage` + `ProcessStandbyMessage` (consumes 'r' feedback), `postgres/src/backend/replication/syncrep.c::SyncRepGetSyncStandbys` (matches `application_name` against `synchronous_standby_names`), `postgres/src/backend/replication/syncrep.c::SyncRepWakeQueue` (releases waiters).

## Problem

M0102-0005 lands `internal/wal/syncrep.go` with `SyncRep.WaitForLSN(ctx, lsn,
mode)` and `SyncRep.UpdateStandbyProgress(appName, write, flush, apply)`,
hooked into the **physical** walsender's feedback path
(`internal/server/replication.go`'s START_REPLICATION PHYSICAL handler).

For the M0103 sync subtests, the publisher waits on the **logical** standby's
feedback — but goopg's logical walsender (`internal/server/logicalwalsender.go`)
does not currently feed the SyncRep wait queue. As a result, `COMMIT` on a
publisher with `synchronous_standby_names = '<subscription_application_name>'`
would hang indefinitely (the wait queue never releases) even when the
subscription is fully caught up.

The upstream contract is that SyncRep treats physical and logical standbys
uniformly: both report progress via the same Standby Status Update message,
both are matched against `synchronous_standby_names` by application_name.

## Solution

### Dispatch feedback in the logical walsender

In `internal/server/logicalwalsender.go`, where the walsender reads Standby
Status Update messages from the subscriber (the 'r' message, sent every
`wal_receiver_status_interval` and on close-to-real-time when caught up),
add a call to `cfg.SyncRep.UpdateStandbyProgress`:

```go
// On 'r' message receipt (Standby Status Update):
if s.cfg.SyncRep != nil {
    s.cfg.SyncRep.UpdateStandbyProgress(
        s.appName,    // application_name from the replication connection
        writeLSN,
        flushLSN,
        applyLSN,
    )
}
```

The application_name is set by the subscriber's connection string:
`CREATE SUBSCRIPTION s CONNECTION '… application_name=<name>'`. In PG, the
default is the subscription name itself; goopg should match.

### `application_name` propagation

The logical walsender must learn the application_name during the
START_REPLICATION handshake. In the upstream protocol, the client (apply
worker) sets it in the libpq connection startup packet. goopg's server
already parses startup parameters; expose `application_name` on the session
struct and let the walsender read it.

If goopg's existing session parsing already stores `application_name` (it
should, for `pg_stat_activity`), this is a one-line plumbing change in
`logicalwalsender.go`.

### No new wait primitive

`SyncRep.WaitForLSN(ctx, lsn, mode)` from M0102-0005 already handles the
publisher-side blocking; the logical walsender side only needs to feed
progress in. M0102-0005's commit-path hook
(`internal/executor/operators_tx.go`) already calls `WaitForLSN` after the
local flush in the COMMIT path, regardless of whether the waiting standby
is physical or logical.

### Subscription-name vs application_name

In PG, the subscriber's connection sends `application_name = <subscription
name>` by default; that's what `synchronous_standby_names = 'sub1'` matches
against. The M0103 tests must follow this convention:

```sql
CREATE SUBSCRIPTION goopg_sub
    CONNECTION 'host=<pub> … application_name=goopg_sub'
    PUBLICATION p WITH (enabled = true, copy_data = false);
```

And on the publisher:

```sql
ALTER SYSTEM SET synchronous_standby_names = 'goopg_sub';
ALTER SYSTEM SET synchronous_commit = remote_apply;
SELECT pg_reload_conf();
```

### `synchronous_commit = remote_apply` semantics for logical

`remote_apply` waits for the standby's apply_lsn (not just flush) to pass
the commit LSN. For physical replication, "apply" means the redo loop has
replayed up to that LSN; for logical replication, "apply" means the apply
worker has committed the upstream transaction locally. goopg's
`LogicalReceiver` updates `applyLSN` (M0103-0003's atomic LSN) on each
local commit; that value flows back as `apply_lsn` in the Standby Status
Update; the publisher's `SyncRep.WaitForLSN(commitLSN, RemoteApply)`
releases when `applyLSN ≥ commitLSN`.

## Files to create / modify

| File | Change |
|---|---|
| `internal/server/logicalwalsender.go` | Dispatch 'r' message to `SyncRep.UpdateStandbyProgress`; plumb `application_name` from session |
| `internal/server/session.go` | Confirm `application_name` is parsed at startup and reachable from the walsender |
| `internal/wal/syncrep_test.go` | New: logical-walsender variant — fake `LogicalReceiver` reports lagging apply_lsn; publisher COMMIT blocks; advance apply_lsn; COMMIT unblocks |

No changes to `internal/wal/syncrep.go` (M0102-0005 covers the primitive).

## Verification

```bash
# Unit
go test -race -run TestLogicalSyncRep ./internal/wal/

# E2E (final form): the M0103-0007 sync subtest uses this in production
```

## Risks

- **Multiple subscriptions with the same application_name**. PG documents
  that names must be unique; goopg should reject CREATE SUBSCRIPTION with a
  duplicate application_name. Out of M0103 scope (tests use unique names).
- **Feedback interval too long for the test**. PG default is 10 s; that
  makes the M0103 sync subtest sluggish. Mitigation: the subscriber sets
  the GUC `wal_receiver_status_interval = '200ms'` for the test's
  subscription connection.
- **Catch-up race**. If the subscription was paused and is replaying old
  WAL, the publisher's commit on new WAL might be released before the new
  WAL has been applied. The release condition is `apply_lsn ≥ commit_lsn`,
  which by construction means the new commit IS applied — no race.
- **Disconnected subscriber blocks publisher commits indefinitely.** Same as
  M0102: this is PG-parity behaviour; document for operators. The M0103
  sync subtest only kills the **publisher**, so the subscriber-disappear
  case isn't exercised.
