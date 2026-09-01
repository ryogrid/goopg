# Replication Flow

Physical streaming (walsender → walreceiver), logical decoding (pgoutput),
and synchronous replication.

## Physical Streaming

```mermaid
sequenceDiagram
    participant S as standby (walreceiver)
    participant H as primary replication.Handler
    participant W as xlog.Writer
    participant R as xlog.RecordIterator

    S->>H: IDENTIFY_SYSTEM
    H-->>S: systemid, timeline, xlogpos
    S->>H: START_REPLICATION
    H->>H: replyStartReplication (physical mode)
    H->>R: xlog.NewRecordIterator from start LSN
    loop streaming
        R-->>H: next record
        H->>S: CopyData (raw WAL)
    end
    S->>S: EndLSN-StartLSN == len(bytes)?
    S->>W: AppendRaw (verbatim) / Append (re-encode)
    S->>H: StandbyStatusUpdate (write=flush=apply LSN)
    H->>H: SyncRep.UpdateStandbyProgress(appname, lsn)
```

## Logical Decoding (pgoutput)

```mermaid
flowchart TD
    Sub["START_REPLICATION … pgoutput<br/>(logical mode)"] --> Handler["runLogicalWalsender"]
    Handler --> Slot["xlog.SlotDecoder<br/>slot-backed decoding (logical slot)"]
    Slot --> Decode["decode WAL records → changes"]
    Decode --> PGO["xlog.PgOutput<br/>Begin / Relation / Insert / Update / Delete"]
    PGO --> Enc["encodePgoTuple<br/>row tuple → pgoutput text/bytea"]
    Enc --> Frames["'w' CopyData frames"]

    PGO --> Subscriber["LogicalReceiver (apply side)"]
Subscriber --> Dec["pgoutput_decoder: decode messages"]
    Dec --> Apply["executor.ApplyWorker<br/>applies rows to subscriber tables"]
    Apply --> Ack["LSN ack (apply for all three LSNs)"]
    Apply --> Sync["SyncRep releases at remote_apply"]

    %% note right of Apply: table sync: RunTableSyncManager drives<br/>COPY per rel through pg_subscription_rel i→d→s→r
```

## Synchronous Replication

```mermaid
stateDiagram-v2
    [*] --> PrimaryCommit: COMMIT on primary
    PrimaryCommit --> AppendWAL: xlog append + FlushUpTo
    AppendWAL --> WaitQuorum: SyncRep wait<br/>quorum / priority list
    WaitQuorum --> Received: standby reports write LSN
    WaitQuorum --> Flushed: standby reports flush LSN
    WaitQuorum --> Applied: standby reports apply LSN (remote_apply)
    Received --> Released: quorum satisfied at remote_write
    Flushed --> Released: quorum satisfied at remote_flush
    Applied --> Released
    Released --> Acked: SyncRep.UpdateStandbyProgress<br/>releases waiting commit
    Acked --> [*]: COMMIT returns to client
    WaitQuorum --> Timeout: standby lag / disconnect
    Timeout --> Forget: SyncRep.ForgetStandby<br/>loses quorum credit
    Forget --> [*]: commit may be delayed/failed
```