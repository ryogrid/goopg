# WAL Append

WAL record encoding, ring buffer append, segment write, and checkpointing.

## WAL Record Encoding

```mermaid
classDiagram
    class Record {
        +RecordKind kind
        +flags
        +payloadLength
        +xl_prev uint64
        +body []byte
    }
    class XLogPageHeader {
        +xlp_magic
        +xlp_info
        +xlp_tli
        +xlp_pageaddr
    }
    class RecordKind {
        <<constants in recovery.go>>
        +HEAP_INSERT
        +HEAP_UPDATE
        +HEAP_DELETE
        +BTREE_INSERT
        +BTREE_SPLIT
        +BTREE_DEDUP
        +XACT_COMMIT
        +XACT_ABORT
        +CLOG_ZERO_PAGE
        +SMGR_CREATE
        +CHECKPOINT
    }
    class MemRing {
        +cap int64
        +Append(pos, data)
        +ReadAt(pos, out)
        +Range() (head, tail)
        +PublishUpTo(upTo)
    }

    Record --> RecordKind : kind byte
    Record --> XLogPageHeader : page framing (8 KiB pages)
    Record "many" --> MemRing : appended
    note right of Record: xl_prev backpointer chain; validated by reader / pg_waldump
```

## Ring Buffer Append

```mermaid
sequenceDiagram
    participant C as Caller (executor/storage)
    participant W as xlog.Writer
    participant R as MemRing
    participant S as stripeAppend
    participant F as SegmentFile

    C->>C: Encode* record (recovery.go)
    C->>W: Append(payload)
    W->>R: copy into ring, stamp LSN<br/>WriteReserved / PublishUpTo
    R-->>W: LSN
    W-->>C: LSN returned
    Note over C,W: async: caller may continue

    C->>W: FlushUpTo(lsn)  [commit / sync_commit=on]
    W->>R: drain buffer up to LSN<br/>AdvanceWindow(lsn)
    R->>S: stripe of records
    S->>S: stripeAppendBuild<br/>compact contiguous records
    S->>F: xlogWrite (pwrite to segment)
    F-->>S: bytes written
    S->>S: doSync (fdatasync / sync_file_range)
    S-->>W: flush complete
    W-->>C: ok
```

## Segment Write + Checkpoint

```mermaid
flowchart TD
    W["Writer"] --> Pre["preallocateSegment / recycleSegmentFile"]
    Pre --> Seg["append into pg_wal segment<br/>XLogSegSize"]
    Seg --> Pos["detectWritePos / scanLastSegmentEnd<br/>find real end (don't trust recycled)"]
    Pos --> Ret["RemoveOldSegments<br/>respects retention horizon + MinRestartLSN"]

    CP["Checkpointer goroutine"] --> Int["tick interval"]
    Int --> Flush["WriteDirtyPages / flush dirty buffers"]
    Flush --> Emit["append CHECKPOINT record<br/>embedding CheckPointFields"]
Emit --> Fields["CheckPointFields:<br/>redo LSN, nextXID, nextOid, timeline']
    Fields --> LSN["LastCheckpointLSN advances recovery horizon"]

    note right of Fields: records must be PG-canonical so a vanilla PG 18.3 standby<br/>can replay them (pg_assembled_emit.go)
```