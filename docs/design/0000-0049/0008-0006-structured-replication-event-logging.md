# 0008-0006 — Structured replication-event logging (M0008)

Status: accepted

## Goal

Operators triaging a stuck logical-replication subscription
should be able to grep / filter / alert on a stable structured
vocabulary instead of free-text log lines. Mirrors the existing
physical-replication event vocabulary
(`0005-0007-replication-event-logging.md`) so dashboards built
against the M0005 events extend naturally to logical workers.

## Approach

Logical replication reuses the existing infrastructure:

- `log/slog` is the universal logger seam — every component
  takes a `*slog.Logger` (or falls back to `slog.Default()`).
- `internal/wal/repllog.go` carries the canonical
  `event=<name>` constant vocabulary. Producers pass
  `"event", wal.EventXxx` plus structured key/value pairs
  (`sub`, `rel_oid`, `from`, `to`, `lsn`, `err`, ...).

This slice extends `repllog.go`'s constant set with five new
M0008 events, plumbs `*slog.Logger` through the apply worker /
tablesync transport / tablesync manager, and wires the call
sites that fire those events.

### Event vocabulary (added in this slice)

| Event constant | Slog key | Fired from | When |
|---|---|---|---|
| `EventApplyCommit` | `apply_commit` | `ApplyWorker.applyCommit` | After every successful subscriber commit |
| `EventApplyError` | `apply_error` | `ApplyWorker.ApplyMessage` | Any per-message error path |
| `EventTablesyncStarted` | `tablesync_started` | `RunTableSync` | After seeding the `subscription_rel` row |
| `EventTablesyncStateChange` | `tablesync_state_change` | `RunTableSync`, `ApplyWorker.promoteSyncedRels` | Every successful `srsubstate` transition |
| `EventTablesyncCompleted` | `tablesync_completed` | `RunTableSync` | After `d → s` and trailer drain |

### Wiring

```go
// internal/executor/applyworker.go
func (w *ApplyWorker) SetLogger(*slog.Logger)
   // applyCommit success → Info: event=apply_commit, sub, xid, lsn
   // ApplyMessage error  → Error: event=apply_error, sub, kind, rel_oid, lsn, err
   // promoteSyncedRels   → Info: event=tablesync_state_change, sub, rel_oid, from=s, to=r, lsn

// internal/server/tablesync.go
type TableSyncConfig struct { ...; Logger *slog.Logger }
   // entry             → Info: event=tablesync_started, sub, rel_oid, rel
   // i→d advance       → Info: event=tablesync_state_change, sub, rel_oid, from=i, to=d
   //                     (only when previous state actually was 'i' — d→d is silent)
   // d→s advance       → Info: event=tablesync_state_change, sub, rel_oid, from=d, to=s
   // exit              → Info: event=tablesync_completed, sub, rel_oid, rel, rows

// internal/server/tablesync_manager.go
type TableSyncManagerConfig struct { ...; Logger *slog.Logger }
   // forwarded into every per-rel RunTableSync invocation
```

### Why `slog`, not a custom interface

The first draft of this slice introduced a `wal.ReplicationLogger`
interface with `LogEvent(ev ReplicationEvent)`. That doubled the
logging surface (two ways to emit a structured event) and didn't
match the existing M0005 retention / walreceiver wiring, which
already uses `slog`. The accepted approach extends the
established pattern. New event kinds are a one-line addition to
`repllog.go`; no interface churn.

### Error handling

Logging never fails an apply. `slog` swallows write errors
internally; the structured-log layer is best-effort
observability, not a durability primitive. A transient log
write failure does not abort the apply path or the tablesync
exchange.

## Verification

- **`internal/executor/applyworker_test.go::TestApplyWorkerLogsCommitAndPromotion`**:
  drives a B/R/I/C through an apply worker whose subscription
  has a rel at state `s` with end-LSN 0xCAFE; the commit at
  0xC0FFEE should produce both an `event=apply_commit` line
  with `lsn:12648430` and a `tablesync_state_change` line with
  `from=s to=r`. Both lines flow through a JSON slog handler so
  the test introspects the structured fields directly.
- **`internal/server/tablesync_test.go::TestRunTableSyncLogsLifecycle`**:
  drives a happy-path sync of two rows; asserts the JSON output
  contains `tablesync_started`, both state-change lines
  (`from=i to=d` and `from=d to=s`), and `tablesync_completed`
  with `rows=2`.

## What this slice doesn't deliver

- **Walsender / walreceiver lifecycle events for logical
  replication.** Logical walsenders run on the same goroutine
  as their physical counterparts (the `walsender_disconnect`
  event already covers exit). Per-handshake start/connect
  events for logical-only flows are a follow-up, blocked on
  the per-flow plumbing landing in the production composition.
- **Per-conflict logging.** Conflict resolution beyond
  stop-on-error is out of scope for M0008; once it lands, an
  `apply_conflict` event will join this vocabulary.
- **Production composition.** Server / cmd-level wiring that
  attaches a real logger to every apply / tablesync worker is
  a separate slice (it composes naturally once the production
  LogicalReceiver hookup lands — see
  `0008-0004-apply-worker-and-tablesync.md`).

## Cross-references

- Physical replication events: `0005-0007-replication-event-logging.md`.
- Apply worker / tablesync mechanics:
  `0008-0004-apply-worker-and-tablesync.md`.
- Subscriber-side observability registry:
  `0008-0005-logical-replication-observability.md`.
