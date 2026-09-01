# Transaction Visibility

XID lifecycle, snapshot capture, commit/abort in clog, and MVCC visibility
rules.

## XID Lifecycle

````mermaid
stateDiagram-v2
    [*] --> NoXID: Manager.Begin
    NoXID --> NoXID: read-only stmts (never AssignXID)
    NoXID --> Assigned: AssignXID (first write)
    Assigned --> Writing: MaterializeWriterXID / stamp tuples
    Writing --> Committing: COMMIT
    Writing --> Aborting: ROLLBACK
    Committing --> Committed: Manager.Commit → clog.SetCommitted<br/>+ XACT_COMMIT WAL record
    Aborting --> Aborted: Manager.Rollback → clog.SetAborted<br/>+ XACT_ABORT WAL record
    Committed --> [*]: visible to later snapshots
    Aborted --> [*]: invisible forever
    NoXID --> [*]: read-only commit fast path
````

## Snapshot Capture + Commit/Abort in CLog

````mermaid
sequenceDiagram
    participant T as Transaction
    participant M as transam.Manager
    participant P as procarray
    participant C as clog.CLog
    participant X as xlog.Writer

    T->>M: SnapshotFor(tx) / FreshSnapshot()
    M->>P: read active XIDs + oldest xmin
    P-->>M: {Xmin, Xmax, InProgress, Aborted}
    M-->>T: Snapshot

    T->>M: Commit(tx)
    M->>M: CommandCounterIncrement
    M->>X: Append(XACT_COMMIT record)
    X-->>M: LSN
    M->>C: SetCommitted(xid) (with LSN)
    M->>M: CommitAsync: defer durable clog write<br/>behind LSN watermark

    Note over T,C: ReplayXactCommit reconstructs from WAL at recovery
````

## MVCC Visibility Rules

````mermaid
flowchart TD
    T["heap tuple header:<br/>xmin, xmax, cmin, cmax"] --> V["transam.TupleVisible(h, snap, currentXID, curcid)"]
    V --> XMIN{"xmin < snap.Xmin<br/>or xmin == currentXID?"}
    XMIN -->|"no (in-progress / future)"| INVIS["invisible (in progress)"]
    XMIN -->|"yes"| XMINCOMMIT{"xmin == self?"}
    XMINCOMMIT -->|"yes"| CMIN{"cmin < curcid?"}
    CMIN -->|"yes"| VIS["visible"]
    CMIN -->|"no"| INVIS2["invisible: inserted later in this txn"]
    XMINCOMMIT -->|"no"| STAT{"xmin committed?"}
    STAT -->|"clog GetStatus / DidCommit"| CMT["yes"]
    STAT -->|"aborted"| ABORTED["invisible (aborted)"]
    STAT -->|"in-progress (not self)"| INPROG["invisible (other txn in flight)"]
    CMT --> XMAX{"xmax set?"}
    XMAX -->|"no"| VIS2["visible"]
    XMAX -->|"deleted by self"| DEL{"delete cmin < curcid?"}
    DEL -->|"yes"| INVIS3["invisible: self-deleted"]
    DEL -->|"no"| VIS3["visible"]
    XMAX -->|"deleted by committed other"| INVIS4["invisible (deleted)"]
    XMAX -->|"xmax in-progress"| CONCURRENT["visible: lock/deleter uncommitted<br/>may trigger EPQ retry on write"]

    note right of V: hint-bit fast paths skip clog reads;<br/>sub-xids and multixacts walked when present;<br/>OldestXmin feeds vacuum horizon
````