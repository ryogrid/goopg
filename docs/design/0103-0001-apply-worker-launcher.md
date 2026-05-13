# 0103-0001 — Subscriber Apply-Worker Auto-Launcher

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0103-0002
**Upstream reference:** `postgres/src/backend/replication/logical/launcher.c` (`ApplyLauncherMain`, `logicalrep_worker_launch`, `ApplyLauncherWakeup`), `postgres/src/include/replication/worker_internal.h`, `postgres/src/backend/catalog/pg_subscription.c` (catalog access).

## Problem

`CREATE SUBSCRIPTION … WITH (enabled = true)` registers the subscription in
goopg's catalog, but **does not start an apply worker**. The existing
`LogicalReceiver` (`internal/server/logicalreceiver.go`) implements the
client side of the logical-replication protocol, but it must be dialed
manually — the goopg server never spawns it on its own.

This blocks the M0103 E2E tests: after the test issues `CREATE SUBSCRIPTION
…`, no apply happens until the test reaches into goopg's internals to start
the receiver, which is fragile and unrepresentative of how an operator would
deploy logical replication.

## Upstream contract

From `postgres/src/backend/replication/logical/launcher.c::ApplyLauncherMain`:

1. A long-lived `logical replication launcher` background worker scans
   `pg_subscription` on a periodic timer (default 10 s, controlled by
   `wal_retrieve_retry_interval` GUC) and on explicit wake-up via
   `ApplyLauncherWakeup` (called from `CREATE/ALTER/DROP SUBSCRIPTION` DDL).
2. For each enabled subscription without a running worker, the launcher
   spawns a per-subscription apply worker via `logicalrep_worker_launch`.
3. Each apply worker runs `ApplyWorkerMain` (in `worker.c`), which connects
   to the publisher via libpqwalreceiver, issues `START_REPLICATION LOGICAL`,
   and consumes pgoutput messages.
4. Worker process lifecycle: a worker exits on subscription DDL change,
   apply error, or shutdown signal. The launcher restarts it on the next
   poll cycle (or on explicit wakeup).

## Solution

### `internal/server/applylauncher.go` (new)

```go
type ApplyLauncher struct {
    server   *Server
    catalog  catalog.Catalog
    workers  map[string]*launchedWorker  // by subscription name
    mu       sync.Mutex
    wake     chan struct{}              // buffered(1); CREATE/DROP/ALTER signal
    poll     time.Duration              // default 10s; matches PG's wal_retrieve_retry_interval
}

type launchedWorker struct {
    cancel  context.CancelFunc
    done    chan struct{}
    recv    *LogicalReceiver
    appName string
}

func NewApplyLauncher(s *Server) *ApplyLauncher
func (l *ApplyLauncher) Start(ctx context.Context)              // launches goroutine
func (l *ApplyLauncher) Wake()                                   // signals re-scan
func (l *ApplyLauncher) launch(sub catalog.Subscription) error
func (l *ApplyLauncher) stop(name string)
```

The launcher goroutine:

```go
for {
    l.reconcile()
    select {
    case <-ctx.Done():
        l.stopAll()
        return
    case <-l.wake:
        // immediate re-scan
    case <-time.After(l.poll):
        // periodic re-scan
    }
}
```

`reconcile()`:

1. Read all subscriptions from `pg_subscription` (catalog package).
2. For each subscription where `enabled = true` and no worker is running:
   call `launch(sub)`.
3. For each running worker whose subscription is no longer enabled or has
   been dropped: call `stop(name)`.

### `launch(sub)`

Creates a child context, dials a `LogicalReceiver` against `sub.Conninfo`
with `START_REPLICATION SLOT <slot> LOGICAL <lsn>` (lsn = the slot's
`confirmed_flush_lsn`), wires the receiver to a freshly-constructed
`ApplyWorker` (`internal/executor/applyworker.go`). Stores both in
`workers[sub.Name]`. The receiver's runloop is M0103-0003's reconnecting
variant.

### DDL integration

In `internal/executor/operators_ddl.go`:

- `execCreateSubscription` (line 174): after the catalog write, call
  `ctx.Server.ApplyLauncher.Wake()`.
- `execDropSubscription` (line 189): same.
- New `execAlterSubscription` (for `ENABLE` / `DISABLE`): same.

The launcher's next `reconcile()` cycle (≤ 100 ms after Wake) picks up the
change.

### Catalog access

Use `internal/catalog/`'s existing `Catalog.Subscriptions()` accessor (verify
it exists; if not, add one returning `[]Subscription`). The Subscription
struct must expose: Name, Conninfo, SlotName, ApplicationName, Enabled,
Publications.

### Server wiring

In `internal/server/server.go`:

```go
func (s *Server) Start(ctx context.Context) error {
    // … existing wiring …
    s.applyLauncher = NewApplyLauncher(s)
    s.applyLauncher.Start(ctx)
    // …
}
```

Shutdown via `ctx.Done()` is already handled by the launcher's goroutine.

## Files to create / modify

| File | Change |
|---|---|
| `internal/server/applylauncher.go` (new) | `ApplyLauncher` struct + reconcile loop |
| `internal/server/applylauncher_test.go` (new) | Race-tested unit: create sub → worker starts; drop sub → worker stops |
| `internal/server/server.go` | Construct + start launcher in `Server.Start` |
| `internal/executor/operators_ddl.go` | `Wake()` call from CREATE/DROP/ALTER SUBSCRIPTION |
| `internal/catalog/catalog.go` (or similar) | Confirm/add `Subscriptions()` accessor |

## Verification

```bash
# Unit test
go test -race -run TestApplyLauncher ./internal/server/

# E2E smoke
./bin/goopg start -D /tmp/sub & SUB=$!
./postgres/local_install/bin/psql -h 127.0.0.1 -p <port> -c \
  "CREATE SUBSCRIPTION s1 CONNECTION 'host=<pub>' PUBLICATION p WITH (enabled=true);"
# After ≤1s, pg_stat_subscription shows s1 active without further wiring.
```

## Risks

- **Catalog reads during DDL**. `reconcile()` reads the catalog; CREATE
  SUBSCRIPTION writes to it. Use the catalog's existing read-lock model;
  if catalog access is racy, add a coarse `RLock` around `Subscriptions()`.
- **Worker leak on rapid CREATE/DROP cycles**. Defensive: `stopAll()` on
  launcher shutdown; per-worker contexts cancel cleanly; `done` channel
  drains.
- **Slot ownership across worker restarts**. The logical slot survives
  worker restart; the next dial passes the slot name and starts at
  `confirmed_flush_lsn`. M0103-0003 handles this.
- **Initial table sync**. PG's tablesync is a separate worker pair; M0103
  scope assumes the test uses `copy_data = false` to skip initial sync
  and tests with empty target tables, mirroring `TestPort_Subscription004Sync`'s
  approach. Document this in the test.
