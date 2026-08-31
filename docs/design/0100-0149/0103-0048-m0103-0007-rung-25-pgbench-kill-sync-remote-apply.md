# M0103-0007 rung 25 — apply-worker `application_name` plumbing for sync rep + Scenario A `sync_remote_apply` skeleton

Status: accepted (partial — sync-rep priority puzzle deferred)
Milestone: M0103-0007 (Scenario A E2E: PG primary + goopg subscriber)
Target: the second of two Scenario A DoD subtests — the **zero-loss**
`sync_remote_apply` half. (Rung 23 closed the bounded-loss `async`
half.)

## Goal

Land the load-bearing infrastructure for the Scenario A
`sync_remote_apply` DoD invariant

```
subscriber count(*) == killCommitted + 1
```

so that the live E2E can close in the next rung once the publisher-side
sync-rep priority puzzle is understood. This rung delivers:

1. The goopg-side wiring needed for PG's
   `synchronous_standby_names = '<sub>'` rule to recognise the apply
   worker by name (the M0103-0005 acceptance doc explicitly listed this
   as a follow-up).
2. The harness `SyncModeRemoteApply` change to inject only
   `synchronous_standby_names` + `synchronous_commit = local` at the
   cluster level, leaving session-level remote_apply opt-in to the test
   (cluster-level `remote_apply` deadlocked the harness on the first
   DDL commit because no standby existed yet).
3. A live E2E test scaffold for the sync subtest, currently `t.Skip`-ed
   with the verbatim PG18 diagnosis quoted in its docstring so the next
   rung can pick up the surface exactly.

## Why zero-loss holds (when the publisher cooperates)

For a logical-replication standby in sync mode:

1. PG's walsender registers an entry in `pg_stat_replication` for the
   apply worker keyed by `application_name`.
2. With `synchronous_standby_names = '<sub>'` active, every commit that
   runs under session-level `synchronous_commit = remote_apply` blocks
   inside `SyncRepWaitForLSN` until the named standby reports
   `apply_lsn ≥ commit_lsn` via a Standby Status Update (`'r'` frame).
3. goopg's logical receiver (`internal/server/logicalreceiver.go`,
   M0103-0003) reports `write_lsn = flush_lsn = apply_lsn = applyLSN`
   on a 10 s ticker AND eagerly in reply to `'k'` keepalives whose
   `ReplyRequested = true` (PG sets that bit as soon as a sync commit
   notices the standby is behind, so the round-trip is sub-second).
4. By the time `db.Exec("INSERT …")` returns, the row's heap mutation
   is durable on goopg. `committed.Add(1)` is bumped AFTER `db.Exec`
   returns and the workload's top-of-loop `ctx.Err()` check exits
   BEFORE a new INSERT round-trip starts, so `committed` is a strict
   lower bound on rows the subscriber holds. The upper bound is
   `killCommitted + 1` (the post-failover multi-host INSERT).

## What rung 25 landed

### Goopg-side wiring

**`LogicalReceiverConfig.ApplicationName`** (new field, non-empty →
sent as the `application_name` startup parameter):

```go
type LogicalReceiverConfig struct {
    ...
    // ApplicationName, when non-empty, is sent as the
    // `application_name` startup parameter so the publisher's
    // pg_stat_replication row and any matching
    // synchronous_standby_names rule see this apply worker under
    // its subscription-configured name.
    ApplicationName string
    ...
}
```

The handshake in `LogicalReceiver.handshake` adds the parameter when
non-empty; empty value preserves pre-M0103-0005 behaviour so older
callers that don't care about sync rep keep working.

**`DefaultLaunchApplyWorker`** (`internal/server/applylauncher.go`)
populates the field. The previous `_ = appName // SyncRep wiring lands
in M0103-0005` placeholder is gone. Resolution order:

1. Explicit `application_name=<value>` from the subscription's
   `Conninfo` wins (`parseSubscriptionConninfo` already extracted it
   M0103-0002 ago — it was just being thrown away).
2. Fall back to the subscription name itself
   (`resolveApplyWorkerApplicationName(parsedAppName, subName)`).
   Mirrors upstream libpqrcv's `walrcv_application_name` semantics —
   so a subscription created without an explicit `application_name=`
   in its conninfo still keys pg_stat_replication on a stable,
   predictable identifier.

### Harness `SyncModeRemoteApply` reshape

`internal/testutil/pubsubcluster/cluster.go` previously injected BOTH
`synchronous_standby_names = '<app>'` AND
`synchronous_commit = remote_apply` into the publisher's
`postgresql.conf` at cluster init time. That deadlocks the harness:
PG's default `synchronous_commit = on` is effectively `remote_flush`
whenever `synchronous_standby_names` is non-empty, so the very first
DDL commit (CREATE TABLE / CREATE PUBLICATION) waits for an apply
confirmation from a standby that has not yet been created — and the
harness's CREATE SUBSCRIPTION on the subscriber side runs AFTER the
publisher's DDL.

Rung 25 splits the injection: `SyncModeRemoteApply` now writes

```
synchronous_standby_names = '<app>'
synchronous_commit = local
```

with `local` short-circuiting the sync wait at the cluster level. Tests
that want sync semantics opt every commit into the wait per-session via
`SET synchronous_commit = remote_apply` AFTER the apply worker is
connected. The `synchronous_standby_names` rule alone is harmless
without a remote_apply session, so this is a strictly-stronger default
than `SyncModeAsync`.

### Pins

- `TestResolveApplyWorkerApplicationName` (unit, applylauncher_test.go)
  — four cases covering empty/empty, explicit/empty, fallback,
  override-when-both-set.
- `TestLogicalReceiverConfigCarriesApplicationName` (unit) — verifies
  the new field round-trips through `NewLogicalReceiver` so a future
  refactor that drops the wiring fails loudly.
- `TestParseSubscriptionConninfo` — already covered the parser side,
  unchanged.

### Live E2E scaffold

`TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply` in
`internal/testport/pgoutput_interop_test.go` carries the complete
sequence (publisher + subscriber bring-up, sync-state wait,
sustained workload with per-session remote_apply, mid-flight SIGKILL,
multi-host fall-through INSERT, count-stabilisation poll, strict
equality assertion). It is currently **t.Skip**-ed with the verbatim
diagnosis below so the next rung can resume from the exact failing
surface without re-running the diagnosis loop.

## Deferred-within-scope: PG18 sync-rep priority puzzle

Repeated runs of the live E2E (under multiple
`synchronous_standby_names` shapes: bare identifier, `FIRST 1 (name)`,
`*` wildcard) consistently produce:

```
pg_stat_replication.application_name = 'pg2g_ksync'
pg_stat_replication.state            = 'streaming'
pg_stat_replication.sync_priority    = 0
pg_stat_replication.sync_state       = 'async'
SHOW synchronous_standby_names       = 'pg2g_ksync' (or '*')
pg_is_in_recovery()                  = false
pg_stat_activity.backend_type        = 'walsender'
```

Per PG18's `SyncRepGetCandidateStandbys`
(`postgres/src/backend/replication/syncrep.c:798`), a walsender with
`sync_standby_priority == 0` is skipped from the sync-rep candidate
list — `SyncRepReleaseWaiters` returns without releasing any waiter,
and the next session-level `synchronous_commit = remote_apply` commit
on the publisher hangs indefinitely.

Conditions that should make `SyncRepGetStandbyPriority` (line 860)
return ≥ 1 all appear to hold:

- `!am_cascading_walsender` — confirmed via `pg_is_in_recovery()`.
- `SyncStandbysDefined() && SyncRepConfig != NULL` — `SHOW
  synchronous_standby_names` returns a non-empty value, and PG accepted
  the GUC value.
- `pg_strcasecmp(standby_name, application_name) == 0` — both sides
  read `pg2g_ksync` in `pg_stat_replication`.
- `strcmp(standby_name, "*") == 0` — tested with `'*'`, still
  priority 0.

Forcing a re-evaluation by `pg_reload_conf()` or by
`pg_terminate_backend()` on the existing walsender (so goopg's
M0103-0003 reconnect loop spins up a fresh walsender against the
already-active GUC) reproduces the same priority=0 state.

The most likely culprit is a per-process `SyncRepConfig` lifecycle
quirk for logical walsenders: `assign_synchronous_standby_names`
allocates the parsed config via `guc_malloc` and stores it in `*extra`,
which `SyncRepConfig = extra` then picks up. For a walsender forked
from the postmaster, the inherited `SyncRepConfig` may not be the
post-walsender-init re-parse result. A fully diagnostic loop would need
to attach with gdb to the walsender process and inspect
`SyncRepConfig->member_names` directly — out of rung 25's scope.

Closing this requires either:

(a) understanding PG18's logical-replication walsender `SyncRepConfig`
lifecycle well enough to drive `SyncRepInitConfig` into setting
`MyWalSnd->sync_standby_priority > 0` (could be a goopg-side connection
shape adjustment or a publisher conf tweak); or

(b) replacing the publisher-side `SyncRepReleaseWaiters` dependency
with a goopg-side polling invariant — explicitly require each writer
goroutine to poll the publisher's `pg_stat_replication.apply_lsn ≥
commit_lsn` AFTER `INSERT` returns, treating apply latency as bounded.
This loses the "every commit waits before returning" semantic but
preserves the "zero loss at SIGKILL" outcome if the polling window is
tight.

(b) is the natural fallback; (a) is the principled fix.

## Files modified

- `internal/server/logicalreceiver.go` — `LogicalReceiverConfig.ApplicationName`
  field + handshake plumbing.
- `internal/server/applylauncher.go` — `DefaultLaunchApplyWorker`
  populates `ApplicationName`; new `resolveApplyWorkerApplicationName`
  helper for the fallback.
- `internal/server/applylauncher_test.go` — unit pins.
- `internal/testutil/pubsubcluster/cluster.go` — `SyncModeRemoteApply`
  conf-injection reshaped.
- `internal/testport/pgoutput_interop_test.go` — live E2E scaffold
  (`t.Skip` with quoted diagnosis).
- `docs/design/0103-0048-...md` (this file).
- `docs/design/README.md` — index row.
- `.ralph/fix_plan.md` — rung 25 progress block under M0103-0007.

## Out of scope (deferred within M0103-0007, in order)

- Rung 26: close the PG18 sync-rep priority puzzle (a or b above) and
  flip the live E2E to a positive assertion.
- proto_version=2 streaming subxacts (apply-worker subxact tracking).
- column-ref-typed `nextval` args in DEFAULT expressions.
- Binary-format pgoutput (`(format 'binary')`).
- Scenario A milestone closure rung (rolls all of M0103-0007 to
  `[x]`).
