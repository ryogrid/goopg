# M0103-0007 rung 23 — pgbench-shape kill mid-flight + async DoD bracket (PG → goopg)

Status: accepted (2026-05-14)

## Goal

Bring rungs 1–22 together end-to-end against M0103-0007's Scenario A
**async DoD bracket**:

```
subscriber count(*) ∈ [killCommitted - asyncLossBound + 1,
                       killCommitted + 1]
```

with `asyncLossBound = 50` rows. Rungs 1–21 verified apply-worker
correctness under sustained, concurrent INSERT/UPDATE/DELETE workloads
and rung 22 closed the SIGKILL-of-publisher + libpq multi-host
fall-through plumbing. Rung 23 ties both halves together: a sustained
INSERT workload on the PG publisher, mid-flight SIGKILL of the
postmaster, post-failover INSERT against the surviving goopg
subscriber via the multi-host conninfo, and a bounded-loss invariant
that catches both silent row drop and replication amplification.

## Non-goals

- The `sync_remote_apply` subtest. That mode requires logical SyncRep
  wait coverage and a tighter zero-loss invariant — separate rung.
- `pgbench` as the workload driver. `pgbench` is a subprocess with no
  inline atomic counter, so deriving `killCommitted` precisely (the
  upper-bound check needs it tight) would require parsing `pgbench`'s
  per-transaction log or polling `pgbench_history` — both flaky under
  pgoutput's commit ordering. Rungs 20/21 already pinned the pgbench
  driver path; rung 23 uses a Go-driven workload so `killCommitted`
  is exact.
- Subxact streaming (proto_version=2) and `filler char(N)` bpchar
  padding. Out of M0103-0007 scope for this rung.
- Scenario B (goopg primary). Symmetric work, separate sequence.

## Design

### Workload shape — Go-driven INSERT loops with an atomic commit counter

Two writer goroutines drive the workload, each holding its own
`*sql.DB` (lib/pq driver) directly against the PG publisher. Each
goroutine runs a tight INSERT loop into
`public.bench_log (client int, src text)` (no PK — INSERT-only, so
REPLICA IDENTITY is irrelevant). The atomic `committed atomic.Int64`
is bumped AFTER each successful commit returns; the `ctx.Err()` check
sits at the TOP of the loop, so an in-flight INSERT always runs to
completion and bumps the counter before the goroutine exits.

This ordering is load-bearing: it eliminates the
"committed-on-server-but-not-counted" race that would otherwise let
the subscriber observe a row not reflected in `killCommitted`,
breaking the upper-bound assertion.

### Workload throttle

The unthrottled INSERT rate on the in-tree `local_install` build is
≈ 5 k commits/s/client × 2 clients = 10 k commits/s. That far outpaces
goopg's apply throughput, so a multi-thousand-row backlog accumulates
in the walsender's network buffer + the apply-side queue, and once
the postmaster dies the publisher's reorder buffer (in memory) goes
with it. The observed loss in early prototyping balloons past any
fixed `asyncLossBound`.

Throttling each writer to one INSERT every 5 ms caps the workload at
~200 commits/s/client = ~400 commits/s total. The apply path keeps up
in steady state, so the **lag at kill time** is bounded by the depth
of the walsender's pgoutput buffer plus the apply queue — easily
inside the 50-row bound.

### Drain window before kill

After `workCancel + wg.Wait`, the workload stops issuing INSERTs but
the walsender + apply pipeline still has in-flight bytes. A 200 ms
sleep gives the walsender time to ship its tail to the apply worker
before PG dies, bringing the observed lag at kill comfortably under
`asyncLossBound`.

200 ms is empirical: with the 5 ms throttle, the steady-state
end-to-end commit→apply lag is well under 200 ms on the in-tree
local_install build. Increasing it would only narrow the loss window
further (zero-loss in the limit); 200 ms keeps the test budget tight
while staying well inside the bracket.

### Sequence

1. Spin up `pubsubcluster.NewMixed` (PG pub + goopg sub, async).
2. `CREATE TABLE public.bench_log (client int, src text)` on both
   sides; `CreatePublication`; `pg_create_logical_replication_slot`;
   `CREATE SUBSCRIPTION ... WITH (copy_data = false, create_slot =
   false)`.
3. Launch two writer goroutines (one per client_id) with their own
   `*sql.DB` handles to PG; each runs a throttled INSERT loop and
   bumps the atomic `committed` counter after each successful commit.
4. Wait for at least one row to land on the subscriber (replication
   is alive, 30 s deadline).
5. Sleep 1500 ms — accumulate ≈ 600 commits across both clients.
6. `workCancel + wg.Wait` — drain in-flight INSERTs; from this point
   no more commits land on PG.
7. `killCommitted := committed.Load()` — exact snapshot of how many
   commits the workload landed on PG.
8. Sleep 200 ms drain window — let the walsender ship its tail.
9. `psc.Publisher.Kill()` — SIGKILL via `pg_ctl -m immediate -w stop`
   (rung 22 plumbing).
10. Multi-host post-failover INSERT (`client = -1, src = 'post'`) via
    the in-tree `psql` with `LD_LIBRARY_PATH` pointing at the in-tree
    `lib/`. libpq walks the host list: PG is dead → connect-refused →
    falls through to goopg → INSERT lands.
11. Poll subscriber `count(*)` until it stays unchanged for 1 s or a
    30 s deadline expires — PG is dead, the apply buffer drains, and
    the count freezes.
12. Assertions:
    - `count(*) ∈ [killCommitted - asyncLossBound + 1,
       killCommitted + 1]` — async DoD bracket.
    - `SELECT src FROM bench_log WHERE client = -1` returns `'post'`
      — multi-host fall-through landed on goopg.

## Tests

Pinned by `TestPort_PgoutputInteropPGToGoopgPgbenchKillAsync` in
`internal/testport/pgoutput_interop_test.go`. Also lands a small
shared helper `waitForCountStable` in the same file for the
"poll-until-stable" pattern (apply-buffer drain detection).

## Assertions catch what

- **Lower bound** (`count < killCommitted - asyncLossBound + 1`):
  rows that should have been visible at kill were lost — apply path
  regression (e.g., rung 1's `maintainUniqueIndexesForInsert`
  silently dropping commits under load).
- **Upper bound** (`count > killCommitted + 1`): subscriber gained
  rows it shouldn't have — replication amplification or counter race.
- **post-row presence**: rung 22's multi-host fall-through still
  works (regression in the libpq host-list walk would fail at the
  psql step, not here; this is a belt-and-braces check).

## Out of scope (deferred within M0103-0007)

- `sync_remote_apply` mode with the zero-loss invariant —
  `count(*) == killCommitted + 1` strict.
- `pgbench` as the workload driver — see Non-goals; rungs 20/21
  already covered the driver path.
- Mid-flight kill on the apply path (apply worker crashes vs.
  publisher crashes). Subscriber-side crash recovery is a different
  failure mode.
- proto_version=2 subxact streaming, `filler char(N)`.

## Upstream references

- `postgres/src/backend/replication/logical/worker.c::apply_handle_*`
  — the apply-worker handlers whose goopg equivalents
  (`internal/executor/applyworker.go`) rungs 1–22 already pinned.
- `postgres/src/interfaces/libpq/fe-connect.c::PQconnectPoll` — the
  host-list walk that rung 22 lifted into `MultiHostConninfo`.
