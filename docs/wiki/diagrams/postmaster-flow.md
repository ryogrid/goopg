# Postmaster Flow

Connection lifecycle, simple-query protocol flow, and extended-query protocol
flow through the goopg server.

## Connection Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Listener: Run
    Listener --> acceptLoop: bind TCP
    acceptLoop --> serveConn: new conn
    serveConn --> Startup: goroutine spawn
    Startup --> handleStartup: startup packet
    handleStartup --> checkAuth: SCRAM / trust
    checkAuth --> runPostStartupLoop: authenticated
    runPostStartupLoop --> MsgQuery: simple protocol
    runPostStartupLoop --> MsgParse: extended protocol
    runPostStartupLoop --> MsgTerminate: client disconnect
    MsgTerminate --> [*]: cleanup + slot release
    %% note right of runPostStartupLoop: one goroutine per conn; owns WAL, txn, buffers
```

## Simple-Query Protocol

```mermaid
sequenceDiagram
    participant C as Client
    participant S as postmaster.Server
    participant P as parser
    participant O as optimizer
    participant E as executor
    participant ST as storage.Pool

C->>S: MsgQuery('SELECT …')
    S->>S: handleQueryOrCopy
    S->>S: handleQuery (string fast paths)
    alt fast path match
        S-->>C: QueryResult + ReadyForQuery
    else full path
        S->>P: parser.Parse(sql)
        P-->>S: []Stmt
        loop per statement
            S->>S: plan-cache lookup
            S->>O: optimizer.Plan(stmt, catalog)
            O-->>S: optimizer.Node
            S->>E: executor.BuildFastIterator(node)
            S->>E: opOpen / opNext loop
            E->>ST: Pin(tag) / ReadBlock
            ST-->>E: page bytes
            E-->>S: TupleSlot rows
            S-->>C: RowDescription + DataRow
        end
        S->>S: applyTransactionVerb (COMMIT/ROLLBACK)
        S-->>C: CommandComplete + ReadyForQuery
    end
```

## Extended-Query Protocol

```mermaid
sequenceDiagram
    participant C as Client
    participant S as postmaster.Server
    participant P as parser
    participant O as optimizer
    participant E as executor

    C->>S: MsgParse(stmt, params)
    S->>P: parser.Parse(sql)
    P-->>S: []Stmt
    S->>O: optimizer.Plan(stmt, catalog)
    O-->>S: optimizer.Node
    S-->>C: ParseComplete

    C->>S: MsgBind(portal, params)
    S->>S: bindParams → datums
    S-->>C: BindComplete

    C->>S: MsgExecute(portal, maxRows)
    S->>E: BuildFastIterator(node)
    E->>E: opOpen / opNext (up to maxRows)
    E-->>S: TupleSlot rows
    S-->>C: DataRow(s)
    alt more rows remain
        S-->>C: PortalSuspended
        C->>S: MsgExecute(portal, …)  -- resume
    else all rows consumed
        S-->>C: CommandComplete
    end

    C->>S: MsgSync
    S->>S: applyTransactionVerb (auto-commit per stmt)
    S-->>C: ReadyForQuery

    Note over C,S: MsgDescribe / MsgClose also available
```