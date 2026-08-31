# 0103-0002 — Apply-Worker Reconnect Loop with Bounded Backoff

**Status:** accepted
**Date:** 2026-05-13
**Milestone:** M0103-0003
**Upstream reference:** `postgres/src/backend/replication/logical/worker.c::ApplyWorkerMain` (line 4818+), `postgres/src/backend/replication/logical/launcher.c::ApplyLauncherWakeup`, `postgres/src/backend/replication/libpqwalreceiver/libpqwalreceiver.c::libpqrcv_connect` (retry semantics on failure).

## Problem

`internal/server/logicalreceiver.go::LogicalReceiver.Run` (lines 198–251)
currently exits cleanly on `io.EOF` (line 230) and with an error on any
other failure (line 232). When the publisher dies (SIGKILL or transient
network error) the apply worker terminates and never reconnects on its own.

For the M0103 E2E tests, after the primary is `kill -9`'d, the subscriber
side must remain functional and accept client writes — but the apply
worker's exit before kill is fine (no more changes coming). The reconnect
loop matters for two cases:

1. **Transient disconnect during the workload** (publisher restart,
   network hiccup) — without retry, the subscriber falls behind permanently
   until manual restart.
2. **Standby-side smoke tests** that re-introduce the publisher after kill
   to confirm replication resumes — the apply worker must reconnect.

## Upstream contract

From `postgres/src/backend/replication/logical/worker.c::ApplyWorkerMain`:

- Connection failures bubble up as `ereport(LOG, …)` (do not raise
  PANIC/FATAL); the worker exits cleanly and the launcher restarts it on
  the next cycle (default 10 s via `wal_retrieve_retry_interval`).
- On reconnect, the worker resumes streaming from the slot's
  `confirmed_flush_lsn` — set as the LSN argument to `START_REPLICATION
  SLOT … LOGICAL <lsn>`. Since the slot retains state on the publisher,
  resumption is exact.

In goopg's architecture (Go goroutines, not OS processes), we keep the
worker goroutine alive and loop internally rather than relying on a
parent-process supervisor. Same end behavior.

## Solution

### Reconnect loop in `LogicalReceiver.Run`

Refactor `Run(ctx context.Context) error` to:

```go
func (r *LogicalReceiver) Run(ctx context.Context) error {
    backoff := initialBackoff // 1 * time.Second
    for {
        err := r.runOnce(ctx)  // existing connect+stream+apply path
        switch {
        case ctx.Err() != nil:
            return ctx.Err()
        case err == nil:
            backoff = initialBackoff // clean EOF; reset
            continue
        case isPermanent(err):
            return err
        default:
            r.log.Warn("logical receiver disconnect; retrying",
                "err", err, "backoff", backoff)
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(backoff):
            }
            backoff = nextBackoff(backoff) // 1→2→4→8→16→30 (cap)
        }
    }
}
```

`runOnce` does what the current `Run` does (dial, START_REPLICATION,
stream until EOF/err, close).

### `isPermanent(err)` classification

- Permanent (return without retry): unrecognised protocol message, schema
  divergence the apply worker can't reconcile, authentication failure,
  slot doesn't exist on the publisher.
- Transient (retry): TCP reset/EOF, dial timeout, primary shutting down,
  `wal_sender_timeout` exceeded.

Implement as a switch on `*net.OpError`, `pgproto3` connection errors, and
sentinel errors from `LogicalReceiver`.

### Resume from `confirmed_flush_lsn`

On each `runOnce`, the receiver consults `r.applyLSN.Load()` (the last
applied LSN, updated by the apply worker on each commit) and issues
`START_REPLICATION SLOT <name> LOGICAL <applyLSN>`. The publisher's slot
ensures only WAL ≥ `applyLSN` is sent; no duplicates.

### Backoff parameters

```go
const (
    initialBackoff = 1 * time.Second
    maxBackoff     = 30 * time.Second
    backoffFactor  = 2
)
func nextBackoff(b time.Duration) time.Duration {
    n := b * backoffFactor
    if n > maxBackoff { return maxBackoff }
    return n
}
```

Matches `wal_retrieve_retry_interval` semantics (PG uses a fixed 10 s; the
exponential variant is a goopg ergonomic improvement that compounds to ≤30 s
in steady state).

### Apply worker LSN feedback

The apply worker, on each commit, updates `r.applyLSN.Store(commitLSN)`.
The receiver's standby-status frame (sent every
`wal_receiver_status_interval`, default 10 s — reduce to 200 ms for the M0103
sync subtests via GUC) reports `flush_lsn = applyLSN` so the publisher's
SyncRep wait queue can release waiters. M0103-0005 handles the publisher
side.

## Files to create / modify

| File | Change |
|---|---|
| `internal/server/logicalreceiver.go` | Refactor `Run` into `Run` + `runOnce`; backoff loop; `isPermanent` helper |
| `internal/server/logicalreceiver_test.go` | New test: kill publisher mid-stream; assert reconnect + resume within ~5 s |

## Verification

```bash
# Unit
go test -race -run TestLogicalReceiverReconnect ./internal/server/

# Manual
# Start goopg pub + goopg sub with CREATE SUBSCRIPTION (enabled).
# Kill -9 pub, restart pub.
# Sub's apply worker reconnects automatically; row count catches up.
```

## Risks

- **Tight retry loop on permanent error.** Mitigation: `isPermanent` classifier
  short-circuits; backoff caps at 30 s for transient errors.
- **Slot LSN drift.** If the slot's `confirmed_flush_lsn` advances before
  the apply worker has actually applied the local commit (e.g., due to a
  bug in the apply pipeline), reconnect resumes at the wrong LSN. Mitigation:
  feedback the apply LSN, not the receive LSN — i.e., advance the slot
  only after the local commit visible.
- **Reconnect storm.** Many subscriptions reconnecting in lockstep can
  overload a recovering publisher. Mitigation: add ±20% jitter to backoff
  (simple `time.Duration(rand.Int63n(int64(b/5)))` addition).
- **CHECK ON APPLY**: applying a row that conflicts with local state
  (e.g., primary-key conflict due to local writes during the disconnect)
  aborts the apply worker. PG's behaviour: subscription enters error state
  until `ALTER SUBSCRIPTION SKIP` or DROP/RECREATE. For M0103 the test
  workload is unidirectional (no local writes during replication), so this
  shouldn't trigger; document the limitation.
