# 0102-0003 — Heterogeneous Failover E2E Harness

**Status:** accepted (implemented 2026-05-20)
**Date:** 2026-05-13
**Milestone:** M0102-0006, M0102-0007, M0102-0008
**Upstream reference:** `postgres/src/interfaces/libpq/fe-connect.c:1990` (`target_session_attrs` validation), `postgres/src/bin/pg_ctl/pg_ctl.c:1186` (`do_promote`), `postgres/src/bin/pg_basebackup/pg_basebackup.c:2356` (client main flow).

## Problem

goopg has the goopg↔goopg `replcluster.ReplCluster` harness (M0094-0001), but
no infrastructure for tests that mix a PostgreSQL primary with a goopg
standby (Scenario A) or a goopg primary with a PostgreSQL standby
(Scenario B). Both scenarios need:

- A way to start/stop the upstream PG binaries (`pg_ctl`, `psql`,
  `pg_basebackup`, `pgbench`) as test peers.
- A way to wire a goopg cluster and a PG cluster together via
  `primary_conninfo` + a replication slot.
- A SIGKILL injection helper (not graceful stop).
- A libpq multi-host client that reconnects across the failover boundary.
- Per-mode subtests (`async`, `sync_remote_apply`) with mode-specific DoD.

## Solution

### `internal/testutil/pgcluster/` (new package)

A thin wrapper around upstream `pg_ctl initdb / start / stop / promote`
modelled on the existing `internal/testutil/cluster/` API surface:

```go
type Cluster struct {
    DataDir string
    Port    int
    pgCtl   string  // path to ./postgres/local_install/bin/pg_ctl
    psql    string
    proc    *os.Process
}

func New(t *testing.T, name string) *Cluster
func (c *Cluster) InitDB(opts ...Option) error
func (c *Cluster) Start(ctx context.Context) error
func (c *Cluster) Stop(mode StopMode) error
func (c *Cluster) Kill() error               // SIGKILL via syscall.Kill on c.proc.Pid
func (c *Cluster) Promote() error            // ./pg_ctl promote -D <DataDir>
func (c *Cluster) DSN(user, db string) string
func (c *Cluster) ConfigureStandby(primaryDSN, slotName string) error
                                              // writes standby.signal +
                                              // postgresql.auto.conf line
                                              // `primary_conninfo = 'host=…'`
                                              // + `primary_slot_name = '…'`
```

Option helpers: `WithSyncCommit("remote_apply")`,
`WithSynchronousStandbyNames("standby1")`, `WithWALLevel("replica")`,
`WithMaxWalSenders(5)`, `WithApplicationName(name)`.

Port allocation: reuse the existing `testutil/cluster` port-allocator if
exposed; otherwise add a simple "bind to :0 then close" probe.

### `internal/testutil/replcluster/replcluster.go` extension

Add `NewMixed(t, name, primaryKind, standbyKind ClusterKind)` that returns a
generic `MixedReplCluster` with `Primary` and `Standby` interface fields
implementing a common `ReplPeer` interface:

```go
type ReplPeer interface {
    DataDir() string
    DSN(user, db string) string
    Kill() error
    Promote() error
    PID() int
    StreamingLSN() (uint64, error)  // pg_current_wal_lsn / pg_last_wal_replay_lsn
}
```

Both `*cluster.Cluster` and `*pgcluster.Cluster` implement `ReplPeer`.

### Workload driver

Two implementations:

- **PG-side**: spawn `pgbench -c 2 -T 180` against the PG primary; track
  committed-transaction count via pgbench's stdout summary OR via a polling
  `SELECT count(*) FROM pgbench_history` (the cleanest signal).
- **goopg-side**: a custom in-test loop (since pgbench-on-goopg is out of
  scope):

```go
func runINSERTUPDATELoop(ctx context.Context, db *sql.DB, table string) (
    committed atomic.Int64) {
    for ctx.Err() == nil {
        _, err := db.Exec(`INSERT INTO ` + table + ` (v) VALUES ($1)`, time.Now())
        if err != nil { continue }
        committed.Add(1)
    }
}
```

Both drivers expose a final `Committed()` count read at SIGKILL time. For the
**sync subtest**, the count is the strict equality bound for post-promotion
`count(*)`.

### Libpq multi-host client

Use `lib/pq` or `pgx` (whichever is already in `go.mod`):

```go
dsn := fmt.Sprintf(
    "postgres://%s:%s@%s:%d,%s:%d/%s?target_session_attrs=read-write&sslmode=disable",
    user, pass,
    primaryHost, primaryPort,
    standbyHost, standbyPort,
    db,
)
client, _ := sql.Open("postgres", dsn)
```

After SIGKILL on the primary, the next query auto-fails and libpq tries the
second host. With `target_session_attrs=read-write`, the standby is skipped
until it has been promoted; this is exactly the failover semantics we want
to verify.

### SIGKILL injection

```go
func killPID(t *testing.T, pid int) {
    proc, err := os.FindProcess(pid)
    require.NoError(t, err)
    require.NoError(t, proc.Signal(syscall.SIGKILL))
    // Linux: process is gone immediately; no wait needed
}
```

Tests record the kill timestamp and committed-count value atomically.

### Test scaffold

`internal/testport/e2e_failover_pg_to_goopg_test.go`:

```go
func TestE2E_FailoverPGtoGoopg(t *testing.T) {
    for _, mode := range []string{"async", "sync_remote_apply"} {
        t.Run(mode, func(t *testing.T) {
            primary := pgcluster.New(t, "pg_primary")
            standby := cluster.NewCluster(t, "goopg_standby")
            // primary.InitDB with sync settings dependent on mode
            // pg_basebackup -h primary -D standby.DataDir() -X stream -S goopg_standby
            // standby.ConfigureStandby + Start
            // workload start, ~3 min
            // kill primary, record committed
            // standby.Promote()
            // libpq client reconnects, verifies row count per mode invariant
        })
    }
}
```

Symmetric for `TestE2E_FailoverGoopgToPG`. The two tests share helpers in a
local `_test.go` helper file or in `replcluster`.

## Per-subtest DoD (matches milestone DoD)

| Subtest | Steps | Invariant |
|---|---|---|
| `async` | start primary → start workload → clone via pg_basebackup → start standby → wait for replay catch-up → kill primary → promote standby → client reconnect → INSERT row | `count(*) >= committed - bounded_loss_N` and the new INSERT succeeds |
| `sync_remote_apply` | same, with `synchronous_commit=remote_apply` + `synchronous_standby_names=<standby>` | `count(*) == committed` (strict equality) and the new INSERT succeeds |

Bounded loss for async is computed from the workload's commit rate × the
network/streaming latency at kill time; for the M0102 tests, the bound is
set empirically (e.g., `N = 50` rows for a 2-client pgbench workload). The
real DoD is "no silent corruption" — every committed row that survived is
correctly visible, and no aborted row leaks in.

## Files to create / modify

| File | Change |
|---|---|
| `internal/testutil/pgcluster/cluster.go` (new) | `Cluster` struct + `pg_ctl` wrapper |
| `internal/testutil/replcluster/replcluster.go` | `ReplPeer` interface + `NewMixed` |
| `internal/testport/e2e_failover_pg_to_goopg_test.go` (new) | `TestE2E_FailoverPGtoGoopg` |
| `internal/testport/e2e_failover_goopg_to_pg_test.go` (new) | `TestE2E_FailoverGoopgToPG` |
| `docs/test-port/postgres-oracle-port-status.csv` | Add 4 rows (async/sync × pg-to-goopg/goopg-to-pg) |

## Verification

```bash
go test -v -run TestE2E_FailoverPGtoGoopg -timeout 15m ./internal/testport/
go test -v -run TestE2E_FailoverGoopgToPG -timeout 15m ./internal/testport/

# No regression in goopg↔goopg
go test -v -run TestE2E_PhysicalReplication ./internal/testport/
```

## Risks

- **PG binary version drift.** Tests link to `./postgres/local_install/bin/pg_ctl`;
  upgrading the bundled PG version may change pg_ctl semantics. Mitigation:
  pin to PG 18.3 (the version checked in `./postgres/`).
- **Port-allocation races** between goopg and pgcluster. Mitigation: both
  use the bind-to-:0 probe with a process-local mutex.
- **pg_basebackup -X stream** opens a second connection that the goopg
  primary must handle in parallel with the BASE_BACKUP CopyData stream.
  Verify the walsender + BASE_BACKUP handler don't share unguarded state.
- **Time-based test bound (`-T 180`)** is flaky in slow CI. Mitigation:
  switch to a transaction-count-based stop condition (e.g., stop after 10k
  commits or 3 minutes, whichever first).
- **Standby auto-recovery on missing WAL.** When the primary is killed
  mid-stream, the standby may have partial WAL records. The standby must
  recover gracefully (skip the torn tail per M0088 if landed; else
  document the workaround).

## Status update (2026-06-13, M0102-0009 closure)

The PG↔goopg physical failover repros (`TestE2E_FailoverPGtoGoopg`,
`TestE2E_FailoverGoopgToPG`) were previously gated behind the
`GOOPG_RUN_BLOCKED_M0102_E2E` opt-in env var because `sync_remote_apply` mode
could not reach streaming state — the primary's
`pg_stat_replication.sync_state` never became `'sync'`
("physical replication did not reach streaming state within 45s
(requireSync=true)"). That gap was closed by the `sync_state` wiring tracked in
`0105-0008-sync-state-pg-stat-replication.md` (real FIRST/ANY rule evaluation in
`registerStatReplicationView` instead of a hard-coded `"async"`).

Both directions now pass all modes (`async`, `sync_remote_apply`, `sync_on`):

```
--- PASS: TestE2E_FailoverPGtoGoopg (29.25s)
    --- PASS: TestE2E_FailoverPGtoGoopg/async
    --- PASS: TestE2E_FailoverPGtoGoopg/sync_remote_apply
    --- PASS: TestE2E_FailoverPGtoGoopg/sync_on
--- PASS: TestE2E_FailoverGoopgToPG (5.97s)
    --- PASS: TestE2E_FailoverGoopgToPG/async
    --- PASS: TestE2E_FailoverGoopgToPG/sync_remote_apply
```

The "blocked" env gate has been removed; the tests now follow the standard
heterogeneous-E2E convention (run in the non-short suite when the PG binaries
are present, skipped under `-short` or `GOOPG_SKIP_M0102_E2E=1`), matching
`e2e_replication_test.go`. M0102-0009 is resolved.
