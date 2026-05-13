# 0103-0005 — Heterogeneous Logical-Failover E2E Harness

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0103-0006, M0103-0007, M0103-0008
**Upstream reference:** `postgres/src/interfaces/libpq/fe-connect.c:1990` (`target_session_attrs` validation), `postgres/src/bin/pg_ctl/pg_ctl.c:1186` (`do_promote` — for reference; logical replication doesn't use it), `postgres/src/backend/replication/logical/launcher.c` (subscriber-side process lifecycle reference).

## Problem

goopg's existing logical-replication tests run in-process
(`TestE2E_LogicalReplication`, `TestPort_Subscription*`) — no harness spawns
a publisher and a subscriber as **separate cluster processes**, and no
harness mixes goopg and PostgreSQL binaries.

The M0103 E2E tests need:

- Two cluster lifecycles running concurrently (publisher + subscriber).
- Either side may be goopg or PG (4-test matrix: 2 directions × 2 modes).
- A workload driver running against the publisher.
- A SIGKILL injection helper.
- A libpq multi-host client that auto-redirects after the kill.
- A per-mode (`async` / `sync_remote_apply`) DoD with mode-specific
  invariants.

## Solution

### `internal/testutil/pubsubcluster/` (new package)

Analog of `replcluster` (physical) but for logical replication. Mirrors the
M0102-introduced `ReplPeer` interface so the same abstraction covers
goopg's `*cluster.Cluster` and PG's `*pgcluster.Cluster`.

```go
type PubSubCluster struct {
    Publisher  ReplPeer  // ← reuses M0102's interface
    Subscriber ReplPeer
}

type Options struct {
    PublisherKind  ClusterKind // ClusterKindGoopg | ClusterKindPG
    SubscriberKind ClusterKind
    SyncMode       SyncMode    // SyncModeAsync | SyncModeRemoteApply
    ApplicationName string     // e.g. "goopg_sub"
    PublicationName string     // e.g. "p"
    SubscriptionName string    // e.g. "goopg_sub"
}

func NewMixed(t *testing.T, name string, opts Options) *PubSubCluster
```

Methods on `*PubSubCluster`:

- `Start(ctx)` — InitDB + start both peers; configure publisher with
  `synchronous_standby_names = opts.ApplicationName` +
  `synchronous_commit = remote_apply` when `SyncMode == RemoteApply`.
- `CreatePublication(t, tables ...string)` — `CREATE PUBLICATION
  opts.PublicationName FOR TABLE …` on the publisher.
- `CreateSubscription(t)` — on the subscriber, `CREATE SUBSCRIPTION
  opts.SubscriptionName CONNECTION '<publisher conninfo>
  application_name=<opts.ApplicationName>' PUBLICATION
  <opts.PublicationName> WITH (enabled = true, copy_data = false)`.
- `WaitForApply(t, lsn)` — poll the subscriber until `apply_lsn ≥ lsn`.
- `SubscriberApplyLSN(t) uint64`.
- `Close()` — defer-friendly cleanup.

### Workload driver

Per scenario:

- **PG publisher**: `pgbench -i -s 1` to seed; `pgbench -c 2 -T 180` to drive.
  pgbench's stdout is parsed for the final commit count; alternatively,
  `SELECT count(*) FROM pgbench_history` polled periodically gives a
  monotonic counter.
- **goopg publisher**: custom Go loop using `database/sql`:

```go
func runINSERTUPDATELoop(ctx context.Context, db *sql.DB, table string,
                         committed *atomic.Int64) {
    for ctx.Err() == nil {
        _, err := db.Exec(`INSERT INTO ` + table + ` (v) VALUES (NOW())`)
        if err == nil { committed.Add(1) }
    }
}
```

The workload driver runs in a goroutine and is cancelled at SIGKILL time.
The recorded `committed` count is the "zero-loss" target for the sync subtest.

### Libpq multi-host client

Same as M0102:

```go
dsn := fmt.Sprintf(
    "postgres://%s:%s@%s:%d,%s:%d/%s?target_session_attrs=read-write&sslmode=disable",
    user, pass, pubHost, pubPort, subHost, subPort, db,
)
client, _ := sql.Open("postgres", dsn)
```

Note: `target_session_attrs=read-write` is correct here — both pub and sub
accept writes (the subscriber is always writable in logical replication),
so the post-kill connection lands on whichever host is alive. If the kill
order ever inverted, `target_session_attrs=any` would also work, but
`read-write` matches the M0102 convention.

### SIGKILL injection

```go
func sigKillPeer(t *testing.T, peer ReplPeer) {
    pid := peer.PID()
    proc, _ := os.FindProcess(pid)
    require.NoError(t, proc.Signal(syscall.SIGKILL))
}
```

The kill is recorded with `time.Now()` and the `committed.Load()` value
atomically:

```go
killCommitted := committed.Load()
killAt := time.Now()
sigKillPeer(t, psc.Publisher)
```

### Scenario A test scaffold

`internal/testport/e2e_logical_failover_pg_to_goopg_test.go`:

```go
func TestE2E_LogicalFailoverPGtoGoopg(t *testing.T) {
    for _, mode := range []pubsubcluster.SyncMode{
        pubsubcluster.SyncModeAsync,
        pubsubcluster.SyncModeRemoteApply,
    } {
        t.Run(modeName(mode), func(t *testing.T) {
            psc := pubsubcluster.NewMixed(t, "lfA_"+modeName(mode), pubsubcluster.Options{
                PublisherKind:    pubsubcluster.ClusterKindPG,
                SubscriberKind:   pubsubcluster.ClusterKindGoopg,
                SyncMode:         mode,
                ApplicationName:  "goopg_sub",
                PublicationName:  "p",
                SubscriptionName: "goopg_sub",
            })
            defer psc.Close()

            ctx, cancel := context.WithCancel(context.Background())
            defer cancel()
            require.NoError(t, psc.Start(ctx))
            psc.CreatePublication(t, "t")
            psc.CreateSubscription(t)

            committed := &atomic.Int64{}
            go runPgbenchOnPG(ctx, psc.Publisher, committed)

            // Let the workload run for 60s and observe replication lag
            require.Eventually(t, func() bool {
                return psc.SubscriberApplyLSN(t) > 0
            }, 30*time.Second, 200*time.Millisecond)
            time.Sleep(60 * time.Second)

            killCommitted := committed.Load()
            cancel() // stop workload
            sigKillPeer(t, psc.Publisher)

            // Libpq multi-host reconnect
            client := openMultiHost(t, psc.Publisher, psc.Subscriber)
            defer client.Close()
            _, err := client.ExecContext(context.Background(),
                `INSERT INTO t (v) VALUES ('after-failover')`)
            require.NoError(t, err)

            // DoD per mode
            var count int64
            require.NoError(t, client.QueryRow(`SELECT count(*) FROM t`).Scan(&count))
            switch mode {
            case pubsubcluster.SyncModeRemoteApply:
                // +1 for the after-failover insert
                require.Equal(t, killCommitted+1, count,
                    "sync_remote_apply: expected zero loss")
            case pubsubcluster.SyncModeAsync:
                // Bounded loss: allow ≤ asyncLossBound
                require.GreaterOrEqual(t, count, killCommitted-asyncLossBound+1,
                    "async: expected loss ≤ bound")
                require.LessOrEqual(t, count, killCommitted+1)
            }
        })
    }
}
```

### Scenario B test scaffold

Symmetric, in `internal/testport/e2e_logical_failover_goopg_to_pg_test.go`:

- `PublisherKind: ClusterKindGoopg`, `SubscriberKind: ClusterKindPG`.
- Workload driver: custom psql/database-sql INSERT loop (pgbench-on-goopg
  out of scope).
- Same `t.Run("async"…)` and `t.Run("sync_remote_apply"…)` split.

### Async-loss bound

`asyncLossBound = 50` rows for a 2-client workload. The bound is empirical
and documented in this design doc; the actual loss is determined by the
gap between the last commit on the primary and the last apply on the
subscriber at SIGKILL time. The async subtest tests the **bound**, not a
fixed number — silent corruption (rows beyond the workload's writes, gaps
in middle of sequence) fails the test.

## Files to create / modify

| File | Action |
|---|---|
| `internal/testutil/pubsubcluster/cluster.go` (new) | `PubSubCluster` + `NewMixed` + helpers |
| `internal/testutil/pubsubcluster/cluster_test.go` (new) | Smoke test of the harness |
| `internal/testport/e2e_logical_failover_pg_to_goopg_test.go` (new) | Scenario A |
| `internal/testport/e2e_logical_failover_goopg_to_pg_test.go` (new) | Scenario B |
| `docs/test-port/postgres-oracle-port-status.csv` | 4 new rows (CSV+regen md) |

## Verification

```bash
go test -v -run TestE2E_LogicalFailoverPGtoGoopg -timeout 15m ./internal/testport/
go test -v -run TestE2E_LogicalFailoverGoopgToPG -timeout 15m ./internal/testport/

# No regression
go test -v -run TestE2E_LogicalReplication ./internal/testport/
go test -v -run TestPort_Subscription ./internal/testport/
```

## Risks

- **PG binary version drift**: tests use `./postgres/local_install/bin/pg_ctl`
  and `pgbench`; pin to PG 18.3.
- **Subscriber receives partial transaction on SIGKILL**: PG's logical
  replication guarantees per-transaction atomicity on the subscriber; goopg
  must too. M0103-0002 / M0103-0003 land the apply-worker; verify the
  abort-on-disconnect path in the receiver rolls back the in-flight
  transaction so partial inserts don't show up.
- **Replication lag at kill time** in async mode determines the loss bound.
  If CI is slow, the lag can balloon. Mitigation: monitor the lag during
  the workload; bail out (`t.Skip`) if the lag exceeds, say, `asyncLossBound`
  before kill — that indicates the harness can't reliably observe the bound.
- **Two-cluster port allocation**. Reuse M0102's bind-:0 probe; gate with
  a process-local mutex to avoid races between the publisher and subscriber
  port assignments.
