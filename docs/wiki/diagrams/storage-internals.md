# Storage Internals

Buffer pool pin/unpin cycle, clock-sweep eviction, and the page write + WAL
flush barrier.

## Buffer Pool Pin/Unpin Cycle

````mermaid
sequenceDiagram
    participant E as executor op (scan / insert)
    participant P as storage.Pool
    participant M as storage.Manager
    participant F as file (os.File)

    E->>P: Pin(tag)
    P->>P: bufmap lookup (lock-free hash)
    alt cache hit
        P->>P: atomic CAS pinCount+1
        P-->>E: *Slot
    else cache miss
        P->>P: take pinMu, clock-sweep victim
        P->>P: flushSlot(dirty victim) → WAL barrier
        P->>M: ReadBlock(rel, blk, buf)
        M->>F: pwrite/pread
        F-->>M: page bytes
        M-->>P: 8 KiB page
        P-->>E: *Slot
    end
    E->>E: read/write page (MarkDirty*)
    E->>P: Unpin(s)
    P->>P: atomic CAS pinCount-1
````

## Clock-Sweep Eviction

````mermaid
flowchart TD
    Start["Pin miss: need a buffer"] --> Hand["advance clockHand (atomic)"]
    Hand --> Check["inspect slot state word"]
    Check --> PinBusy{"pinCount > 0?"}
    PinBusy -->|yes| Hand
    Check --> Usage{"usageCount > 0?"}
    Usage -->|yes| Clear["usageCount = 0 (second chance)<br/>atomic CAS"]
    Clear --> Hand
    Usage -->|no| Dirty{"dirty?"}
    Dirty -->|yes| Flush["flushSlot:<br/>WAL to max(pd_lsn, hintFlushBarrier)<br/>then WriteBlock"]
    Flush --> Evict
    Dirty -->|no| Evict["evictVictim: bmDelete,<br/>gen+1 (ABA defense)"]
    Evict --> Read["ReadBlock new page into slot"]
    Read --> Done["return *Slot"]

    note right of Hand: state word: pinCount 22b | usageCount 8b<br/>dirty | valid | ioInflight | gen 15b
````

## Page Write + WAL Flush

````mermaid
sequenceDiagram
    participant E as executor
    participant P as storage.Pool
    participant X as xlog.Writer
    participant M as storage.Manager

    E->>P: MarkDirtyWithLSN(s, lsn)
    Note over P: FPI watermark on Slot.nativeImageLSN<br/>suppresses re-imaging hot pages
    E->>P: FlushAll() / WriteDirtyPages(n) (bgwriter)
    P->>X: FlushUpTo(max(pd_lsn, hintFlushBarrier))
    X->>X: drain MemRing → segments + fsync
    X-->>P: ok
    P->>M: WriteBlock(rel, blk, buf)
    M-->>P: bytes durable
    Note over P,M: WAL-before-data: page hits disk<br/>only after WAL is flushed past its LSN
````