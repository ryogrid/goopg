# Additional Diagrams

## WAL Append-and-Flush Flow

The path a record takes from the caller (`Append`) through the in-memory ring
buffer, the segment writer, and the commit-LSN flush barrier.

```mermaid
sequenceDiagram
    participant C as Caller (executor/storage)
    participant W as xlog.Writer
    participant MR as MemRing
    participant SW as StripeWriter
    participant FS as SegmentFile
    participant SY as Sync (fsync)

    C->>W: Append(record)
    W->>MR: copy into ring buffer, stamp LSN
    MR-->>W: LSN, buffer position
    W-->>C: LSN returned
    Note over C,W: caller may continue immediately
    C->>W: FlushUpTo(lsn)  [commit / sync_commit=on]
    W->>MR: drain buffer up to LSN
    MR->>SW: stripe of records
    SW->>FS: xlogWrite (pwrite to segment)
    FS-->>SW: bytes written
    SW->>SY: doSync (fdatasync / sync_file_range)
    SY-->>SW: synced
    SW-->>W: flush complete
    W-->>C: ok
```

## Query Plan-to-Execution Flow

The path from `SELECT` text, through the optimizer's plan construction, to the
executor's fast-slab engine and the storage layer.

```mermaid
sequenceDiagram
    participant C as Client
    participant D as dispatch (postmaster)
    participant P as parser
    participant O as optimizer
    participant E as executor (fast slab)
    participant S as storage.Pool
    participant M as storage.Manager

    C->>D: MsgQuery / extended Parse+Bind+Execute
    D->>P: Parse(sql)
    P-->>D: []Stmt
    D->>O: Plan(stmt, catalog)
    O->>O: query_planner, join_search, path→plan
    O-->>D: optimizer.Node
    D->>E: BuildFastIterator(node)
    E->>E: build opTreeSlab + exprTreeSlab
    loop Open → Next → …
        E->>E: opOpen(root)
        E->>E: opNext() → evalFastExpr
        alt heap page needed
            E->>S: Pin(tag)
            S->>M: ReadBlock(rel, blk, buf)
            M-->>S: page bytes
            S-->>E: *Slot
        else index scan
            E->>S: Pin(btree tag)
            S->>M: ReadBlock(btree, blk, buf)
            M-->>S: btree page
            S-->>E: *Slot
        end
        E-->>D: TupleSlot
        D-->>C: DataRow
    end
    E->>E: opClose()
    D-->>C: CommandComplete + ReadyForQuery
```

## DDL Catalog-Heap Sync Flow

How a DDL statement (e.g., `CREATE TABLE`) mutates the in-memory catalog and
persists the change to the on-disk catalog heaps.

```mermaid
sequenceDiagram
    participant E as executor.ddlOp
    participant CAT as catalog.InMemory
    participant ST as storage
    participant X as xlog.Writer

    E->>CAT: CreateTable(schema, table, columns, ...)
    CAT-->>E: table OID
    E->>ST: syncTableToCatalogHeap(table, pg_class, pg_attribute, …)
    ST->>ST: heap write (pg_class row, pg_attribute rows)
    ST->>X: Append(PageImage or HeapInsert)
    X-->>ST: LSN
    ST->>ST: MarkDirty → WAL flush
    E->>CAT: CreateIndexes (if any)
    CAT-->>E: index OIDs
    E->>ST: syncIndexToCatalogHeap(index, pg_index, pg_class)
    ST->>X: Append(btree insert, page image)
    X-->>ST: LSN
    ST-->>E: DONE
    E-->>D: DDL complete
```

## Notes

- **WAL append is asynchronous** — the caller's `Append` returns the LSN
  immediately; the flush is deferred to `FlushUpTo` (called at commit time or
  when the ring buffer is full). The `commit_delay` / `commit_siblings` GUCs
  throttle the flush to batch more records.
- **Fast-slab engine** — the executor builds flat `opTreeSlab` + `exprTreeSlab`
  arrays (int32-indexed, GC-pointer-free) instead of the legacy `Operator`
  interface tree. Non-migrated operators are wrapped in an `OpAdapter`.
- **Catalog-heap dual path** — the in-memory `InMemory` registry is the live
  truth; the on-disk heaps (`base/<dbOid>/1259`, `1247`, `1249`) are a
  persistence projection. Every DDL must update both (the in-memory side via
  `CreateTable` etc., the on-disk side via `syncTableToCatalogHeap`).
- **DDL undo** — on transaction ABORT, the in-memory changes are reverted
  (via `NewAutocommitUndoSession` / `DropTable` etc.), but the heap writes
  are not rolled back; the on-disk state is reconciled by the next startup's
  catalog reload.