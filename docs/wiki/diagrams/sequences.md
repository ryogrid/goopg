# Sequence Diagrams

Three core workflows, each traced through the real entry points.

## Workflow: Simple-query lifecycle

A client sends a `SELECT` via the simple protocol; the postmaster parses,
plans, executes, and streams rows back.

```mermaid
sequenceDiagram
    participant C as Client
    participant S as postmaster.Server
    participant P as parser
    participant O as optimizer
    participant E as executor
    participant ST as storage.Pool

    C->>S: MsgQuery("SELECT …")
    S->>P: Parse(sql)
    P-->>S: parser.Stmt
    S->>O: Plan(stmt, catalog)
    O-->>S: optimizer.Node
    S->>E: BuildFastIterator(node)
    S->>E: op.Open(ctx) / op.Next() loop
    E->>ST: Pin / ReadBlock
    ST-->>E: page bytes
    E-->>S: TupleSlot rows
    S-->>C: RowDescription + DataRow + CommandComplete + ReadyForQuery
```

### Walkthrough

1. **Accept + handshake** — `postmaster.Run` → `acceptLoop` → `serveConn`
   (`internal/postmaster/server.go:855/886`), `handleStartup`, `checkAuth`.
2. **Dispatch** — `MsgQuery` → `handleQueryOrCopy` (`copy.go:51`) →
   `dispatchSimpleQueryViaExecutor` (`dispatch.go:139`).
3. **Parse** — `parser.Parse` (`dispatch.go:166`).
4. **Plan** — plan-cache miss → `optimizer.Plan` (`dispatch.go:1161`).
5. **Execute** — `executor.BuildFastIterator` (`dispatch.go:3398`) → `Open` /
   `Next` / `Close`.
6. **Respond** — `commandTagFor` → `WriteCommandComplete` → `ReadyForQuery`.

## Workflow: Startup recovery and catalog reload

`cmd/goopg start` opens an existing data directory, replays WAL, and rebuilds
the in-memory catalog from the on-disk catalog heaps.

```mermaid
sequenceDiagram
    participant CMD as cmd/goopg
    participant I as initdb
    participant X as xlog
    participant CAT as catalog

    CMD->>I: Open(OpenOptions)
    I->>I: verifyInitialized / storage.NewManager
    I->>X: beginRecovery + ReplayFromDirWithMgrAt
    X-->>I: redo LSN applied
    I->>CAT: reload*FromHeap passes (pg_class, pg_attribute, …)
    CAT-->>I: in-memory Catalog
    I-->>CMD: *Runtime (Pool + TxnMgr + Catalog + WAL)
    CMD->>I: hand Runtime to postmaster.New(cfg)
```

### Walkthrough

1. **Open** — `initdb.Open` (`internal/initdb/open.go:278`) verifies the dir and
   builds the storage manager.
2. **Recovery** — `beginRecovery` stamps `DB_IN_CRASH_RECOVERY`; WAL replay via
   `xlog.ReplayFromDirWithMgrAt`.
3. **Catalog reload** — `runCatalogReloads` (`catalog_heap_reload.go:1204`)
   drives the per-catalog `reload*FromHeap` passes.
4. **Handoff** — the `Runtime` is returned and passed to `postmaster.New`.

## Workflow: Streaming replication

A standby continuously streams WAL from the primary and reports back its
write/flush/apply LSN.

```mermaid
sequenceDiagram
    participant P as primary (walsender.Handler)
    participant S as standby (walreceiver)
    participant W as xlog.Writer

    S->>P: IDENTIFY_SYSTEM / START_REPLICATION
    P-->>S: WAL records (CopyData)
    S->>W: AppendRaw / Append
    S-->>P: StandbyStatusUpdate (write=flush=apply LSN)
    P->>P: SyncRep.UpdateStandbyProgress(appname, lsn)
```

### Walkthrough

1. **Send** — `replication.Handler.HandleCommand` → `replyStartReplication`
   (`internal/replication/walsender.go:524`) → physical iterator loop or
   `runLogicalWalsender`.
2. **Receive** — `replication.StartWalReceiver` (`walreceiver.go:542`) with
   exponential-backoff reconnect.
3. **Ack** — status frames feed `xlog.SyncRep.UpdateStandbyProgress` keyed by
   `application_name`; `ForgetStandby` on disconnect removes quorum credit.
