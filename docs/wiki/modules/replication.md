# Module: `internal/replication`

Streaming and logical replication, split publisher/subscriber like upstream PG's
`src/backend/replication/`. The primary side (walsender) dispatches replication
commands (`IDENTIFY_SYSTEM`, slot DDL, `START_REPLICATION`, `TIMELINE_HISTORY`,
`BASE_BACKUP`) and runs a physical WAL streaming loop plus a logical `pgoutput`
arm. The standby side (walreceiver, logicalreceiver) are pure-Go v3 wire clients
with reconnect loops. Logical replication adds subscriber-side initial COPY table
sync and a process-global apply launcher.

## Responsibilities

- Physical (streaming) WAL replication — primary sender + standby receiver.
- Logical replication (`pgoutput`) — publisher decode/encode + subscriber apply.
- Replication slots, `IDENTIFY_SYSTEM`, `TIMELINE_HISTORY`, `BASE_BACKUP`.
- Table sync (`pg_subscription_rel` state machine `i→d→s→r`) and the apply
  launcher (one apply worker per enabled subscription).

## Key Files

- `walsender.go` — primary-side command dispatcher + physical streaming loop
  (mirrors `walsender.c::exec_replication_command`).
- `logicalwalsender.go` — logical arm of `START_REPLICATION`: `SlotDecoder` +
  `PgOutput` → `'w'` CopyData frames.
- `walreceiver.go` — standby physical client: connect, `START_REPLICATION`,
  append raw WAL; carries the reconnect launcher `StartWalReceiver`.
- `logicalreceiver.go` — subscriber logical client: reconnect loop, pgoutput
  decode → `executor.ApplyWorker`, LSN acking.
- `tablesync.go` / `tablesync_manager.go` — one publisher `COPY … TO STDOUT`
  exchange per relation; per-subscription sweep of unsynced rels.
- `applylauncher.go` — background reconcile loop spawning apply workers.
- `replication_util.go` — `parseLSN`/`formatLSN`, error summaries.

## Public API

Physical walsender (`walsender.go`):

```go
type Config struct{ Logger; Catalog catalog.Catalog; PubSub *catalog.PubSub;
    Slots *xlog.Slots; SyncRep *xlog.SyncRep; WalSenders *xlog.Senders;
    WAL *xlog.Writer; WALDirPath string; WALSegmentSize int64;
    SystemID string; Timeline uint32 }
type WriteQueryErrorFunc func(w *libpq.FrameWriter, code errcodes.Code, msg string,
    extra ...libpq.ErrorField) error
func NewHandler(cfg Config, werr WriteQueryErrorFunc, bb *backup.Handler) *Handler
func (s *Handler) HandleCommand(ctx, r, w, payload, appName, dbName) (bool, error)
```

Walreceiver (`walreceiver.go`):

```go
type WalReceiverConfig struct{ PrimaryAddr, User, SlotName string; StartLSN uint64;
    WAL *xlog.Writer; StatusInterval, DialTimeout time.Duration; /* … */ }
func DialWalReceiver(ctx, cfg) (*WalReceiver, error)
func (r *WalReceiver) Run(ctx) error
func (r *WalReceiver) ApplyLSN() uint64
func StartWalReceiver(ctx, done chan struct{}, cfg) error   // reconnect-with-backoff launcher
```

Logical (`logicalreceiver.go`, `applylauncher.go`, `tablesync*.go`):

```go
func NewLogicalReceiver(cfg LogicalReceiverConfig) *LogicalReceiver
func (r *LogicalReceiver) Run(ctx) error
func (r *LogicalReceiver) ApplyLSN() uint64
func NewApplyLauncher(cfg ApplyLauncherConfig) *ApplyLauncher
func (l *ApplyLauncher) Run(ctx) / Wake()
func RunTableSync(cfg TableSyncConfig) (int64, error)
func RunTableSyncManager(ctx, TableSyncManagerConfig) ([]TableSyncResult, error)
```

## Internal structure

Central split is **physical vs logical** at every layer. The walsender `Handler`
dispatches both: `replyStartReplication` branches on `args.Mode` into the
physical iterator loop (`xlog.NewRecordIterator`) or `runLogicalWalsender`. The
receivers are twins: `WalReceiver` appends raw WAL into `*xlog.Writer`;
`LogicalReceiver` decodes pgoutput into `*executor.ApplyWorker`. Both reuse
`libpq.DecodeReplicationMessage`/`EncodeWALData`/`EncodeStandbyStatusUpdate`.

Logical subscriber pipeline: `ApplyLauncher` (reconcile) →
`DefaultLaunchApplyWorker` → `NewLogicalReceiver` → `ApplyWorker`. Table sync is
a parallel path: `RunTableSyncManager` walks `pg_subscription_rel` rows not yet
`'r'` and drives one `RunTableSync` COPY per rel.

## Dependencies

- **Used by** `internal/postmaster` (wires `NewApplyLauncher`, `NewHandler`) and
  `cmd/goopg/standby.go` (`StartWalReceiver`). Direction is **postmaster →
  replication → backup**; replication must not import postmaster.
- **Uses** `internal/libpq`, `internal/catalog` (`PubSub`), `internal/executor`
  (`ApplyWorker`), `internal/access/transam/xlog` (`Writer`, `Slots`, `SyncRep`,
  `SlotDecoder`, `PgOutput`, `RecordIterator`, …), `internal/backup`,
  `internal/storage`, `internal/utils/misc` + `errcodes`.

## Notable patterns / gotchas

- **LSN off-by-one is load-bearing and repeated** — slot `RestartLSN =
  WrittenLSN()+1`; walreceiver reconnect sends `WrittenLSN()+1`; the logical
  adapter invents strictly-increasing synthetic LSNs. A wrong anchor → garbage
  rmid decode.
- **Raw vs decoded WAL** — the physical receiver detects a verbatim chunk
  (`EndLSN-StartLSN == len(bytes)`) and uses `AppendRaw`; otherwise re-encodes.
- **No TLS** — `sslmode=require/verify-*` rejected; `disable/allow/prefer` fall
  back to plaintext.
- **Status frames** — physical reports write=flush=received, apply via
  `ApplyLSNFunc`; logical reports `applyLSN` for all three so `SyncRep` releases
  at `remote_apply`.
- **Reconnect policy** — physical 500 ms → 30 s exponential; logical 1 s → 30 s
  with jitter; clean EOF resets backoff. `isPermanent` is a fragile
  string-match classifier that aborts the loop on unrecoverable errors.
- **dbOid threading** — every logical-walsender catalog lookup resolves through
  `catalog.NamespaceDBOid`; a wrong oid silently drops all DML.
- **Sibling-path twins** — encode↔decode pairs (`EncodeWALData` ↔
  `DecodeReplicationMessage`), sender twins (physical ↔ `runLogicalWalsender`),
  receiver twins (`WalReceiver` ↔ `LogicalReceiver`), and duplicated conninfo
  parsers must all change together.
