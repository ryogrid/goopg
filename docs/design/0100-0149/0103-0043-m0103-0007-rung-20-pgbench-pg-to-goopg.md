# M0103-0007 rung 20 — pgbench-driven PG → goopg replication

Status: accepted (2026-05-14)

## Goal

Bring the M0103-0007 Scenario A DoD a concrete step closer by driving the
PG publisher's INSERT workload with the upstream `pgbench` binary instead
of hand-coded `psql -c "INSERT ..."` loops, and verifying every replicated
row lands on the goopg subscriber.

Three pieces of the deferred Scenario A wiring sit under the umbrella
phrase "pgbench against PG publisher with `pgbench_history` polling":

1. `pgcluster.Cluster` must be able to run `pgbench` against itself.
2. A live test must wire the publisher's pgbench workload through the
   PubSubCluster harness and assert subscriber convergence.
3. The "pgbench_history" idiom (use an INSERT-only workload table whose
   row count is the workload progress counter) must be demonstrably
   sound under pgoutput replication.

Rung 20 closes (1) and demonstrates (3) through a focused custom-script
workload; the full pgbench standard-schema replication (`-i -s 1` +
`tpcb-like`) plus `kill -9` failover plumbing remain future rungs.

## Non-goals

- Standard pgbench schema (`pgbench_accounts/branches/tellers/history`)
  replication. The standard schema's UPDATE-heavy workload would
  exercise the same apply paths already pinned by rungs 1–11; the new
  surface in rung 20 is the publisher-side workload driver, not the
  apply worker.
- `kill -9 <pg-pid>` + libpq multi-host reconnect. Still deferred —
  needs a publisher-side SIGKILL helper on `pgcluster.Cluster` and a
  client-side dialer that re-targets the secondary host.
- `sync_remote_apply` mode. Async mode is sufficient for the
  convergence-by-polling shape; sync-mode wait will join when the
  failover harness lands.

## Design

### Helper: `pgcluster.Cluster.Pgbench`

Mirrors the goopg-side `(*cluster.Cluster).PGbench` so a future rung can
swap publisher/subscriber roles without touching call sites. Standard
connection flags (`-h/-p/-U <database>`) are prepended to the variadic
args; combined stdout+stderr is returned and a non-zero exit fails the
test. `LD_LIBRARY_PATH` is inherited from `Cluster.env()`, so the
in-tree `local_install/lib` libpq is resolved without manual setup at
the call site.

### Workload shape

`pgbench` is given a custom `-f script` that performs one INSERT per
transaction against a small log table:

```sql
\set rid random(1, 1000000000)
INSERT INTO bench_log (id, client_id) VALUES (:rid, :client_id);
```

With `id int PRIMARY KEY` and a 10⁹-row random range, the probability of
a primary-key collision across 50 INSERTs (`-c 2 -t 25`) is negligible.
`:client_id` is pgbench's built-in 0..N-1 worker index; the assertion
asserts both clients made progress (`count(*) WHERE client_id = 0 > 0`,
`count(*) WHERE client_id = 1 > 0`) on top of the bulk count.

The table is INSERT-only — no UPDATE, no DELETE — so REPLICA IDENTITY
DEFAULT is sufficient. The shape mirrors `pgbench_history` (the
upstream `tpcb-like` workload's INSERT target) without requiring the
standard schema's denormalised accounts/tellers/branches setup.

### Pre-creation contract

The schema is created on both ends before the publication/subscription
pair is established — same contract every rung 2–11 test follows.
goopg's CREATE SUBSCRIPTION does not (yet) auto-create slots; the test
issues `pg_create_logical_replication_slot('rung20', 'pgoutput')` on PG
and then `CREATE SUBSCRIPTION ... WITH (create_slot = false)` on goopg.

### Convergence assertion

`PubSubCluster.WaitForRow(t, "public.bench_log", "1=1", 50,
60*time.Second)` polls the subscriber's `count(*)` until it hits 50.
The 60 s deadline matches every other rung's polling budget. Two
follow-up per-client assertions (`count(*) WHERE client_id = 0 > 0` and
`= 1 > 0`) catch a workload that fired but only ran on one client (e.g.,
pgbench startup error that pinned `:client_id` to 0).

### Fresh-session visibility

Every assertion goes through `Subscriber.QueryScalar` which opens a
fresh psql process. That path consults the dispatcher's MVCC view —
the same path the rung-1 (0103-0024) fix made apply-worker INSERTs
visible to.

## Verification

`go test -count=1 -timeout 180s
-run TestPort_PgoutputInteropPGToGoopgPgbenchInsert
./internal/testport/` runs the new test in isolation; the full
`TestPort_PgoutputInteropPGToGoopg*` family is run together to confirm
no rung-1–11 regression. Targeted unit suite on
`./internal/executor/ ./internal/wal/ ./internal/catalog/
./internal/testutil/pubsubcluster/` is race-tested.

## Next rungs (deferred within M0103-0007)

- pgbench standard schema replication (`-i -s 1` + tpcb-like).
- `kill -9 <pg-pid>` plumbing on `pgcluster.Cluster` + libpq
  multi-host reconnect on the client side.
- proto_version=2 streaming subxacts.
- column-ref-typed `nextval` args.
