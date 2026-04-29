# 0008-0005 — Logical-replication observability (M0008)

Status: accepted (first slice)

## Goal

Operators monitoring a goopg subscriber should see the same
shape of state they see on upstream PostgreSQL:

- **`pg_stat_subscription`** — one row per active subscriber
  worker, with `received_lsn` / `latest_end_lsn` /
  `last_msg_*_time` columns that move forward as the apply loop
  consumes the publisher's stream.
- **`pg_stat_replication`** rows on the publisher already cover
  logical-replication connections (existing infrastructure from
  `0005-0003-replication-observability.md` — walsenders register
  themselves regardless of whether they're physical or logical).
  No new work needed there for this slice.
- **Structured replication-event logging** — deferred to a
  follow-up slice. Scope this slice to the catalog-visible
  observability and leave structured logging for when there are
  enough event types to warrant a dedicated logger interface.

## Substrate

`internal/wal/subscriber_mon.go` ships the in-process
`Subscribers` registry plus a per-worker `Subscriber` handle.
Mirrors the publisher-side `Senders` / `Receivers` pattern from
`replmon.go` — same lock-free Snapshot, same atomic-backed
counters, same monotonic-clamp helpers (`advanceLSN`).

```
type SubscriberWorkerType string
const (
    SubscriberWorkerLeader    = "leader"     // one per active sub
    SubscriberWorkerTablesync = "tablesync"  // one per non-r rel
    // SubscriberWorkerParallel reserved for a future loop
)

type SubscriberState struct {
    SubID       uint32       // pg_subscription.oid
    SubName     string
    WorkerType  SubscriberWorkerType
    PID         uint32
    LeaderPID   uint32       // !=0 only for tablesync rows
    RelOID      uint32       // !=0 only for tablesync rows
    ReceivedLSN uint64
    LatestEndLSN uint64
    LastMsgSendTime    time.Time
    LastMsgReceiptTime time.Time
    LatestEndTime      time.Time   // zero until first non-zero endLSN
}

type Subscribers struct { ... }
func NewSubscribers() *Subscribers
func (s *Subscribers) Register(SubscriberState) *Subscriber
func (s *Subscribers) Unregister(*Subscriber)
func (s *Subscribers) Snapshot() []SubscriberState

type Subscriber struct { ... }
func (s *Subscriber) AdvanceReceivedLSN(uint64)
func (s *Subscriber) MarkMessage(now time.Time, endLSN uint64)
```

### Snapshot ordering

`Snapshot` returns rows in `(subname, worker_type, relid, pid)`
order. This is the stable contract `pg_stat_subscription`
readers depend on so a repeated SELECT against a quiet
subscription returns identical bytes.

### Why atomic + mutex hybrid

`receivedLSN` / `latestEndLSN` are atomic so the high-frequency
update path (every commit) doesn't take a lock. Identity fields
(subID/subname/workerType/pid/leaderPID/relOID) are set once at
`Register` and read without a lock — they don't change for the
worker's lifetime. The registry-level mutex is only acquired on
`Register` / `Unregister` / iteration order during `Snapshot`.

### `LatestEndTime` zero-vs-epoch handling

Storing `time.Time` atomically is awkward, so the implementation
caches `UnixNano` in an `atomic.Int64`. `time.Unix(0, 0)` is
*not* Go's zero `time.Time` — it's the Unix epoch. To preserve
the "never marked" sentinel without polluting the view with a
1970 timestamp, the snapshot leaves `LatestEndTime` as Go's zero
when the cached nanos are 0, and the view renders that as a
blank string.

## View

`internal/initdb/replication_views.go::registerStatSubscriptionView`
installs `pg_catalog.pg_stat_subscription` with the upstream
PG 18.x columns:

```
subid, subname, worker_type, pid, leader_pid, relid,
received_lsn, last_msg_send_time, last_msg_receipt_time,
latest_end_lsn, latest_end_time
```

`leader_pid` and `relid` render as the empty string (blank) on
leader rows where they don't apply; both render as the
formatted integer on tablesync rows. `latest_end_time` stays
blank until the first `MarkMessage` with a non-zero endLSN
arrives, mirroring upstream's "never reported" rendering. All
LSN columns use `formatLSN` so the operator's existing
`\watch pg_stat_subscription` muscle memory transfers verbatim
(`X/Y` hex pairs). All timestamps use `formatTime` (the same
helper that drives `pg_stat_replication.backend_start`).

`pg_stat_subscription` is registered in `initdb.Open` next to
the existing replication views; `Runtime.WalSubscribers` exposes
the registry to apply / tablesync workers so the integration
slice below has a public seam to register against.

## Verification

- **`internal/wal/subscriber_mon_test.go`**: registry-level
  contract — register/unregister round-trip, default
  `WorkerType=leader`, LSN monotonic-clamp on
  `AdvanceReceivedLSN` and on the LSN half of `MarkMessage`,
  Snapshot sort key `(subname, worker_type, relid, pid)`,
  `MarkMessage` timestamp/LSN updates including the
  zero-`LatestEndTime` sentinel for never-marked rows.
- **`internal/initdb/replication_views_test.go::TestStatSubscriptionRendersRegisteredSubscribers`**:
  view-rendering contract — empty registry yields zero rows;
  registering a leader + a tablesync worker yields two rows in
  the right column order; `leader_pid` / `relid` are blank on
  the leader row; `received_lsn` reflects the post-`Advance`
  state; `latest_end_*` stay blank until marked; rows vanish on
  Unregister.

## What this slice doesn't deliver

- **Apply-worker / tablesync-manager hookup.** `ApplyWorker`
  and `RunTableSyncManager` don't yet `Register` into the
  registry. The plumbing is straightforward (call `Register`
  on entry, `defer Unregister`, `AdvanceReceivedLSN` after
  every applyCommit, `MarkMessage` after every received frame)
  but is split into the next slice so the observability
  surface lands as one reviewable change.
- **Structured replication-event logging.** A `wal.ReplicationLogger`
  interface with hooks for slot-create/drop, walsender
  start/stop, walreceiver start/stop, applyCommit,
  tablesync-state-transition events is a separate slice.
  Until then, ad-hoc `log.Printf` lines in the live workers
  remain the sole structured-log source.
- **Multi-process / shared-memory backing.** Upstream PG backs
  `pg_stat_subscription` from `LogicalRepWorker` shared-memory
  entries. v0 has no shared memory yet — every subscriber lives
  in the same process. The registry is the single source of
  truth.

## Cross-references

- Publisher-side observability (already present):
  `0005-0003-replication-observability.md`.
- Catalog substrate that tablesync rows attach to:
  `0008-0003-publication-subscription-ddl.md`.
- Apply worker / tablesync state machine that the observability
  layer reflects: `0008-0004-apply-worker-and-tablesync.md`.
- Upstream:
  - `postgres/src/backend/replication/logical/launcher.c` —
    `LogicalRepWorker` allocation and registration.
  - `postgres/src/backend/utils/adt/pgstatfuncs.c` —
    `pg_stat_get_subscription` (the view's backing function).
