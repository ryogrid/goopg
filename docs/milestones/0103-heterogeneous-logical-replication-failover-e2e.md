# Milestone 0103 — Heterogeneous Logical-Replication + SIGKILL-Failover E2E (PG↔goopg, sync + async)

**Status:** accepted
**Filed:** 2026-05-13
**Accepted:** 2026-05-14
**Depends on:** M0008 (logical replication, complete), M0094-0002 (UPDATE apply via key-tuple, complete), M0101 (PG-compatible WAL on-disk format), M0102-0005 (synchronous_standby_names + SyncRep wait primitive)
**Companion to:** M0102 (physical-replication failover E2E)
**Reference plan:** `.ralph/fix_plan.md` (M0103 section)

## Operational policy (2026-05-13)

- **Within this milestone, marking any sub-task as DEFERRED is, as a rule, not permitted.** The two E2E tests are the milestone's reason for existing; leaving any required runtime gap (apply-worker launcher, reconnect loop, pgoutput interop, logical SyncRep wiring) unimplemented means the tests cannot pass and the Definition of Done is unreachable. Escape hatches such as "push to a later milestone" or "skip the sync variant" must not be used.
- DEFERRED is permitted only when **all three** of the following hold simultaneously: (a) it is clearly demonstrated that the item is impossible to implement in this release due to goopg's Go-implementation constraints or explicit design constraints; (b) the reason is documented in the body of the affected sub-milestone; and (c) within the same milestone, an alternative path is presented that lets the corresponding test subtest reach `pass` (not `excluded`).
- Blocker existence does not justify partial completion: blocker resolution is itself in scope when the blocker is internal to goopg.

## Goal

Deliver two E2E tests that verify goopg can act as either a PostgreSQL-
compatible **logical-replication** publisher or subscriber across the
PG/goopg interop boundary, and that a SIGKILL on the primary followed by
client redirection to the subscriber preserves the workload:

- **Scenario A — PG primary + goopg subscriber**
- **Scenario B — goopg primary + PG subscriber**

Each scenario runs in **two modes**:

- **async** (default `synchronous_commit`) — bounded loss tolerated
- **sync** (`synchronous_commit = remote_apply` +
  `synchronous_standby_names = '<subscription_application_name>'`) —
  zero loss of committed rows

The user's intent is operational confidence: a cluster running unidirectional
logical replication can survive an unclean primary failure by redirecting
clients to the subscriber (which is always writable in logical replication),
without losing committed writes.

## How this differs from M0102 (physical)

| Property | M0102 (physical) | M0103 (logical) |
|---|---|---|
| Standby state before failover | read-only | **read-write** (subscriber accepts client writes independently) |
| Failover mechanism | promote standby (TLI bump + clear standby.signal) | **client redirect only** — no state flip |
| Setup tool | `pg_basebackup` | `CREATE PUBLICATION` + `CREATE SUBSCRIPTION` |
| WAL format | physical WAL bytes | pgoutput logical messages |
| TLI history | written on promote | not used |
| Recovery file | `standby.signal` / `promote.signal` | not used |
| Sync rep wait queue | physical walsender registers | **logical walsender** registers |

This is why M0103 has no analog of M0102's BASE_BACKUP or TIMELINE_HISTORY
sub-milestones, but adds an apply-worker launcher + reconnect loop +
pgoutput wire-interop verification.

## In Scope

1. **Subscriber apply-worker auto-launcher** — server scans `pg_subscription`
   on start and on DDL; spawns a `LogicalReceiver` goroutine per enabled
   subscription. Equivalent of PG's `logical/launcher.c::ApplyLauncherMain`.
2. **Apply-worker reconnect loop with bounded backoff** — `LogicalReceiver.Run`
   no longer exits on publisher disconnect; loops with exponential backoff
   (1 s → 30 s cap) and resumes from `confirmed_flush_lsn`.
3. **pgoutput wire-byte interop verification** — explicit tests that PG can
   decode goopg's pgoutput output and vice versa; fix any field-encoding
   divergences (type OIDs, BEGIN/COMMIT LSN, replica identity marker).
4. **Logical walsender SyncRep integration** — extend M0102-0005's
   `internal/wal/syncrep.go` so the logical walsender also calls
   `UpdateStandbyProgress` and matches subscription `application_name`
   against `synchronous_standby_names`.
5. **`internal/testutil/pubsubcluster/`** — dual-binary publisher/subscriber
   test harness (logical analog of `replcluster`, reuses `pgcluster` from M0102).
6. **`TestE2E_LogicalFailoverPGtoGoopg`** with `async` + `sync_remote_apply` subtests.
7. **`TestE2E_LogicalFailoverGoopgToPG`** with `async` + `sync_remote_apply` subtests.

## Out of Scope

- `ALTER TABLE … REPLICA IDENTITY FULL / USING INDEX` — v0 keeps DEFAULT identity
  (per M0008); tests use primary-key tables so DEFAULT suffices.
- Two-phase commit logical replication (`streaming = parallel`, `two_phase = true`
  subscription options).
- Logical decoding of DDL (not in PG18 either).
- Conflict resolution. M0103 tests are unidirectional: the client writes to
  the primary until SIGKILL, then writes only to the subscriber.
- Cross-major PG version compatibility — target = PostgreSQL 18.

## Definition of Done

1. `go test -v -run TestE2E_LogicalFailoverPGtoGoopg -timeout 15m ./internal/testport/`
   passes with both subtests (`async`, `sync_remote_apply`).
2. `go test -v -run TestE2E_LogicalFailoverGoopgToPG -timeout 15m ./internal/testport/`
   passes with both subtests.
3. **sync subtest invariant:** post-SIGKILL `count(*)` on the subscriber
   equals the workload-side committed-INSERT counter at kill time (zero loss).
4. **async subtest invariant:** post-SIGKILL row count is within the
   documented bounded-loss range; no silent corruption.
5. **Interop wire-byte test** `TestPort_PgoutputInterop` (both directions)
   passes.
6. **Regression bar:** `TestE2E_LogicalReplication` and all three
   `TestPort_Subscription*` tests continue to pass.
7. Style + state gates: `gofmt -l .` empty; `go vet ./...` clean;
   `make ralph-state-guard` passes.
8. All 5 design docs (`0103-0001..-0005`) at status `accepted`.

## Required Design Docs

Under `docs/design/`:

- `0103-0001-apply-worker-launcher.md` — server-side apply-worker spawn from `pg_subscription`.
- `0103-0002-apply-worker-reconnect.md` — reconnect-with-backoff loop on publisher disconnect.
- `0103-0003-pgoutput-wire-interop.md` — byte-for-byte pgoutput parity, both directions.
- `0103-0004-logical-syncrep-integration.md` — wire logical walsender into M0102-0005's SyncRep wait queue.
- `0103-0005-heterogeneous-logical-failover-e2e-harness.md` — `pubsubcluster` test framework, SIGKILL injection, libpq multi-host redirection, per-mode DoD.

Each design doc cites the upstream PostgreSQL implementation under
`postgres/src/` with concrete file:line references.
