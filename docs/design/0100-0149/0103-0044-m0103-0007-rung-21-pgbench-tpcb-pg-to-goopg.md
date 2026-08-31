# M0103-0007 rung 21 — pgbench tpcb-like (UPDATE-heavy) PG → goopg

Status: accepted (2026-05-14)

## Goal

Bring the M0103-0007 Scenario A DoD a step closer by driving the PG
publisher's *UPDATE-heavy* workload with the upstream `pgbench` binary
running the standard tpcb-like script — the load-bearing shape the DoD
calls for (`pgbench -i -s 1 && pgbench -c 2 -T 180`). Every replicated
row and per-table balance aggregate must converge on the goopg
subscriber.

Rung 20 closed the pgbench *driver* path (`pgcluster.Cluster.Pgbench` +
`ReplPeer.Pgbench`) with an INSERT-only custom script. Rung 21 lifts
that driver onto the load-bearing tpcb-like shape, exercising the
apply-worker UPDATE / REPLICA-IDENTITY-DEFAULT-PK paths from rungs
1–2 under sustained, concurrent load.

## Non-goals

- Full upstream `pgbench -i -s 1` initialisation. With `-s 1`, pgbench
  writes 100 K accounts and the goopg CREATE SUBSCRIPTION does not
  copy_data — so the subscriber would need an independent
  `pgbench -i -s 1` run, costing ≈30 s per loop. The new surface in
  rung 21 is the apply worker's UPDATE path under tpcb-like, not
  pgbench's own initial-load path. Manual schema + scaled-down seed
  matches the *post-init* state exactly (balances at 0) without the
  cost.
- `kill -9 <pg-pid>` + libpq multi-host reconnect. Still deferred.
- `sync_remote_apply` mode. Async is sufficient for the
  aggregate-convergence-by-polling shape.
- `filler char(N)` columns. tpcb-like never references them; bpchar
  padding through pgoutput is its own surface (out of scope for
  rung 21's UPDATE-apply focus).

## Design

### Manual scaled-down standard schema

Four tables matching pgbench's standard shape, sans the unused
`filler` columns:

```sql
CREATE TABLE pgbench_branches (bid int PRIMARY KEY, bbalance int NOT NULL);
CREATE TABLE pgbench_tellers  (tid int PRIMARY KEY, bid int, tbalance int NOT NULL);
CREATE TABLE pgbench_accounts (aid int PRIMARY KEY, bid int, abalance int NOT NULL);
CREATE TABLE pgbench_history  (tid int, bid int, aid int, delta int, mtime timestamp);
```

Seeded on both ends to `1 branch / 10 tellers / 100 accounts`, all
balances 0. `pgbench_history` starts empty.

The scale-1 ratios are 1 branch : 10 tellers : 100 000 accounts;
rung 21 shrinks the accounts to 100 (×1000 reduction) which keeps
both ends' seed under 0.5 s while still exercising every apply-side
code path the full scale does.

### Workload: scaled-down tpcb-like custom script

```sql
\set aid random(1, 100)
\set tid random(1, 10)
\set bid 1
\set delta random(-5000, 5000)
BEGIN;
UPDATE pgbench_accounts SET abalance = abalance + :delta WHERE aid = :aid;
SELECT abalance FROM pgbench_accounts WHERE aid = :aid;
UPDATE pgbench_tellers  SET tbalance = tbalance + :delta WHERE tid = :tid;
UPDATE pgbench_branches SET bbalance = bbalance + :delta WHERE bid = :bid;
INSERT INTO pgbench_history (tid, bid, aid, delta, mtime)
       VALUES (:tid, :bid, :aid, :delta, CURRENT_TIMESTAMP);
END;
```

Identical to upstream pgbench's built-in tpcb-like script (see
`pgbench.c::tpcb_like_script`) with `:scale = 1` substituted in for
`:bid` and the id ranges scaled down. `pgbench -c 2 -j 2 -t 20`
produces 40 transactions (= 40 history rows).

### Convergence and invariants

Three orthogonal assertions:

1. **`count(*) = 40` on `pgbench_history` within 90 s.** Catches a
   lost INSERT — i.e., rung 1's index maintenance or rung 2's
   `primaryKeyOnlyRow` regressing under sustained load.

2. **Cross-side aggregate equality** for `sum(delta)` / `sum(abalance)`
   / `sum(tbalance)` / `sum(bbalance)`. Polled until equal or 60 s
   deadline. Catches *wrong-row* UPDATE apply: pgoutput emits
   `'U' relOid 'N' newTuple` (OldTuple omitted) for non-key-touched
   UPDATEs because the PK didn't change; the apply path depends on
   `primaryKeyOnlyRow` synthesising the row-locator key from the new
   tuple's PK columns. A regression there lands `:delta` on the wrong
   row and one or more aggregate drifts.

3. **Publisher-side tpcb-like invariant**
   (`sum(abalance) == sum(tbalance) == sum(bbalance) == sum(delta)`).
   Each transaction adds the same `:delta` to one row in each balance
   table plus an INSERT into history, so the invariant must hold
   independently of replication. Pins the *workload* itself — a
   regression where pgbench dropped one of the three UPDATEs would
   surface here before any replication question is asked, keeping the
   replication assertion well-conditioned.

### Pre-creation contract

Same as every rung 2–20 test: schema and seed go up on both ends, then
`pg_create_logical_replication_slot('rung21', 'pgoutput')` on PG,
then `CREATE SUBSCRIPTION ... WITH (create_slot = false, copy_data =
false)` on goopg.

### baseDir / slot name

Kept short (`pg2g-pgb-tpc` / `pg2g_pgb_tpc`) so the cluster's Unix
control-socket path stays under the 108-byte Linux sockaddr limit —
same constraint as rung 20.

## Verification

`go test -count=1 -timeout 180s
-run TestPort_PgoutputInteropPGToGoopgPgbenchTpcb
./internal/testport/` runs the new test in isolation (≈2.0 s on the
local machine).

`go test -count=1 -timeout 300s
-run TestPort_PgoutputInteropPGToGoopg ./internal/testport/` runs all
17 PG → goopg interop tests together (rungs 1–21) to confirm no
regression (≈26.5 s).

## Next rungs (deferred within M0103-0007)

- `kill -9 <pg-pid>` plumbing on `pgcluster.Cluster` + libpq
  multi-host reconnect on the client side. With the pgbench-driven
  UPDATE-heavy workload now in place, the failover harness can layer a
  mid-workload SIGKILL on top.
- proto_version=2 streaming subxacts.
- column-ref-typed `nextval` args.
- `filler char(N)` bpchar padding through pgoutput (deferred from the
  scope cut here).
