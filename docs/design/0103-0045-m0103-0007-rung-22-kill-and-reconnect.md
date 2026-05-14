# M0103-0007 rung 22 — SIGKILL + libpq multi-host reconnect (PG → goopg)

Status: accepted (2026-05-14)

## Goal

Bring the M0103-0007 Scenario A DoD a step closer by closing the
SIGKILL-of-publisher + libpq multi-host fall-through pipeline that the
DoD's two subtests both pivot on. Once the publisher is dead and clients
are redirected via a multi-host conninfo, the (always-writable) goopg
subscriber must accept the post-failover write.

Rungs 1–21 covered the apply-worker correctness story under sustained,
concurrent, UPDATE-heavy load. The remaining Scenario A surface is the
client-redirection plumbing itself — exercised end-to-end here against
the in-tree `local_install/bin/psql` (libpq).

## Non-goals

- Full Scenario A subtests (`pgbench -c 2 -T 180` with kill mid-flight
  and zero-loss / bounded-loss invariants). Deferred to later rungs.
- `sync_remote_apply` mode. Multi-host fall-through is orthogonal to
  SyncRep; the sync subtest will lift on top of rung 22 + a separate
  rung that exercises `application_name`-matched SyncRep wait.
- `target_session_attrs=read-write` semantics on the goopg side. libpq's
  default `target_session_attrs=any` already does the right thing for
  the kill case (the dead host is connect-refused, libpq retries the
  next), and goopg accepts writes by default since logical-replication
  subscribers are always writable.

## Design

### `pubsubcluster.ReplPeer.Kill()`

The `ReplPeer` interface gains a `Kill() error` method. Both
implementations already have a SIGKILL equivalent on their underlying
cluster handle:

- `pgPeer.Kill()` delegates to `pgcluster.Cluster.Kill()` —
  `pg_ctl -m immediate -w stop`, the upstream documented equivalent of
  a postmaster SIGKILL.
- `goopgPeer.Kill()` delegates to `cluster.Cluster.Kill()`, which
  sends SIGKILL to the goopg server process directly
  (`c.cmd.Process.Kill()`).

Both leave the peer in the "not running" state so a follow-up `Stop()`
in `Close()` is a safe no-op (matches the existing crash-recovery
pattern from `internal/testutil/cluster/crash_recovery_test.go`).

### `pubsubcluster.PubSubCluster.MultiHostConninfo`

New helper:

```go
func (p *PubSubCluster) MultiHostConninfo(applicationName string) string
```

Returns the libpq-style conninfo `host=<pub>,<sub> port=<pp>,<gp>
user=<u> dbname=<db>` (with optional `application_name=<an>` suffix).
The publisher is listed first; libpq walks the host list in order, so
after `Publisher.Kill()` the dead host is connect-refused and libpq
falls through to the (surviving) subscriber.

Mirrors PostgreSQL 18's documented multi-host conninfo shape from
§32.1.1 of libpq-connect — same form upstream's regression suite uses
for failover redirection.

## Tests

Pinned by `TestPort_PgoutputInteropPGToGoopgKillAndReconnect` in
`internal/testport/pgoutput_interop_test.go`. The flow:

1. Spin up `pubsubcluster.NewMixed` (PG pub, goopg sub, async).
2. `CREATE TABLE public.failover_log (id int PRIMARY KEY, src text NOT NULL)`
   on both sides; `CreatePublication`; `pg_create_logical_replication_slot`
   on PG; `CREATE SUBSCRIPTION ... WITH (copy_data = false, slot_name =
   <slot>, create_slot = false)` on goopg.
3. INSERT three pre-failover rows (`src='pre'`, ids 1–3) on PG.
4. `psc.WaitForRow(t, "public.failover_log", "src = 'pre'", 3, 60s)` —
   bounded wait for the apply worker to drain the tail.
5. `psc.Publisher.Kill()` — SIGKILL the PG postmaster.
6. Build `psc.MultiHostConninfo("failover_client")`; run
   `psql -d <multi> -c "INSERT INTO public.failover_log VALUES (4, 'post')"`
   with `LD_LIBRARY_PATH` pointing at the in-tree
   `postgres/local_install/lib` so the in-tree libpq resolves.
7. Assert `count(*) = 4` and `SELECT src WHERE id = 4` returns `'post'`
   on the subscriber via the harness's Go-driver QueryScalar.

Two orthogonal pins:

- `count(*) = 4` would drop to 3 if either (a) replication regressed,
  or (b) the multi-host fall-through silently failed (psql connecting
  to the dead PG would error out and the test would fail at the psql
  invocation, not at the count check).
- `id=4 src='post'` catches the case where libpq landed the INSERT on
  the original publisher (wrapping back somehow) or where some
  alternate write path appeared.

baseDir kept short (`pg2g-kill`) for the 108-byte Linux Unix-sockaddr
limit, same constraint as rungs 20–21.

## Out of scope (deferred within M0103-0007)

- `pgbench`-driven workload mid-kill with bounded-loss / zero-loss DoD
  invariants. Needs an injected `Kill()` mid-workload + a workload-side
  committed-row counter; deferred to a follow-up rung that lifts onto
  rung 22's plumbing.
- `sync_remote_apply` mode subtest. Logical SyncRep wait integration
  landed in M0103-0004; lifting it into rung 22's test shape is a
  separate rung that needs to assert the `count(*) == killCommitted`
  invariant.
- `pgcluster.Cluster.Kill()` was already present from before this
  rung; no plumbing change there. The rung was scoped to lifting
  `Kill()` onto the `ReplPeer` interface + adding the multi-host
  conninfo helper + pinning the end-to-end shape.

## Upstream references

- `postgres/src/interfaces/libpq/fe-connect.c` — libpq's host-list
  walk in `PQconnectPoll` (the loop that retries each host on
  ECONNREFUSED).
- `postgres/doc/src/sgml/libpq.sgml` §32.1.1 — multi-host conninfo
  format spec.
- `postgres/src/bin/pg_ctl/pg_ctl.c::do_stop` — the `pg_ctl -m
  immediate` SIGKILL-equivalent path that `pgcluster.Cluster.Kill()`
  invokes.
