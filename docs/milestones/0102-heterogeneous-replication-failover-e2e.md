# Milestone 0102 — Heterogeneous Streaming-Replication + SIGKILL-Failover E2E (PG↔goopg, sync + async)

**Status:** accepted
**Filed:** 2026-05-13
**Depends on:** M0005 (streaming replication), M0094 (replication E2E foundation), M0101 (PG-compatible WAL on-disk format), **M0105 (goopg→PG data-file format parity — required for Scenario B)**
**Reference plan:** `.ralph/fix_plan.md` (M0102 section)

## Operational policy (2026-05-13)

- **Within this milestone, marking any sub-task as DEFERRED is, as a rule, not permitted.** The two E2E tests are the milestone's reason for existing; leaving any required runtime gap (BASE_BACKUP, TIMELINE_HISTORY, sync replication wait, promote signal) unimplemented means the tests cannot pass and the Definition of Done is unreachable. Escape hatches such as "push to a later milestone" or "skip the sync variant" must not be used.
- DEFERRED is permitted only when **all three** of the following hold simultaneously: (a) it is clearly demonstrated that the item is impossible to implement in this release due to goopg's Go-implementation constraints or explicit design constraints; (b) the reason is documented in the body of the affected sub-milestone; and (c) within the same milestone, an alternative path is presented that lets the corresponding test subtest reach `pass` (not `excluded`).
- Blocker existence does not justify partial completion: blocker resolution is itself in scope when the blocker is internal to goopg.

## Goal

Deliver two end-to-end tests that verify goopg can act as both a PostgreSQL-compatible
streaming-replication primary and a standby, and that a SIGKILL on the primary
followed by promotion of the standby preserves the client workload:

- **Scenario A — PG primary + goopg standby**
- **Scenario B — goopg primary + PG standby**

Each scenario runs in **two modes**:

- **async** (default `synchronous_commit`) — bounded data loss tolerated
- **sync** (`synchronous_commit = remote_apply`) — zero loss of committed rows

The user's intent is operational confidence: a running cluster experiencing an
unclean primary failure can be rescued by promoting a streaming standby — across
the goopg/PostgreSQL interop boundary — without losing what the client was
told had been committed.

## In Scope

1. **BASE_BACKUP wire-protocol command** on the goopg primary so
   `pg_basebackup -h <goopg> -D <out>` produces a clone-able data directory.
2. **TIMELINE_HISTORY wire-protocol command** + timeline-history file writer so
   walreceivers can fetch history across timeline switches.
3. **`promote.signal` file watcher** in the standby loop so
   `pg_ctl promote -D <goopg-dir>` triggers promotion (parity with PG operator
   tooling; goopg's existing control-socket PROMOTE remains supported).
4. **`synchronous_standby_names` GUC and commit-wait machinery** on the goopg
   primary; **per-progress feedback messages** on the goopg standby. Cover
   `remote_write` / `on` (= remote_flush) / `remote_apply` modes; the test uses
   the most demanding (`remote_apply`).
5. **`TestE2E_FailoverPGtoGoopg`** with `async` + `sync_remote_apply` subtests.
6. **`TestE2E_FailoverGoopgToPG`** with `async` + `sync_remote_apply` subtests
   (requires a new `internal/testutil/pgcluster/` package wrapping `pg_ctl`).

## Out of Scope

- Logical replication failover (covered by M0008 / M0094-0002).
- Cascading standbys, archive-restore failover, point-in-time recovery.
- `hot_standby_feedback` round-trip and predicate-lock conflict resolution.
- Cross-major PG version compatibility — target is PostgreSQL 18, the version in `./postgres/`.
- pgbench-on-goopg correctness (Scenario B uses a custom psql INSERT/UPDATE loop instead).

## Definition of Done

1. `go test -v -run TestE2E_FailoverPGtoGoopg -timeout 15m ./internal/testport/`
   passes with both subtests (`async`, `sync_remote_apply`).
2. `go test -v -run TestE2E_FailoverGoopgToPG -timeout 15m ./internal/testport/`
   passes with both subtests.
3. **sync subtest invariant:** post-promotion `count(*)` on the new primary
   equals the workload-side committed-INSERT counter at SIGKILL time (zero loss).
4. **async subtest invariant:** post-promotion row count ≥
   `commits_at_kill_time − bounded_loss_N` and no silent corruption; the
   bound is documented in `0102-0003`.
5. **sync rep unit test** at `internal/wal/syncrep_test.go` passes with
   `-race`: COMMIT blocks when no standby is connected with the configured
   name; unblocks when the standby reattaches and acks past the commit's LSN.
6. **Regression bar:** `TestE2E_PhysicalReplication`, `TestE2E_LogicalReplication`,
   all 6 `TestPort_Recovery*` and all 3 `TestPort_Subscription*` tests continue to pass.
7. Style + state gates: `gofmt -l .` empty; `go vet ./...` clean;
   `make ralph-state-guard` passes.
8. All 5 design docs (`0102-0001..-0005`) at status `accepted`.

## Required Design Docs

Under `docs/design/`:

- `0102-0001-base-backup-wire-protocol.md` — `BASE_BACKUP` on goopg primary.
- `0102-0002-timeline-history-and-promotion-tli-switch.md` — `TIMELINE_HISTORY` command + TLI history file write on promotion.
- `0102-0003-heterogeneous-failover-e2e-harness.md` — test architecture (dual-binary harness, libpq multi-host reconnection, SIGKILL injection, async vs sync DoD per subtest).
- `0102-0004-promotion-trigger-pg-ctl-parity.md` — `promote.signal` file watcher.
- `0102-0005-synchronous-replication.md` — `synchronous_standby_names`, commit-wait, standby feedback.

Each design doc must cite the upstream PostgreSQL implementation under
`postgres/src/` with concrete file:line references.
