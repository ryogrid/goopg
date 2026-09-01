# Catalog Architecture

The in-memory `InMemory` registry structure, the virtual vs heap-backed
catalog split, and the DDL sync path to on-disk catalog heaps.

## InMemory Structure

```mermaid
classDiagram
    class InMemory {
        +mu RWMutex
        +namespaces map[dbOid]*tableNamespace
        +enums map[dbOid]map[enumKey]*EnumType
        +domains map[dbOid]map[domainKey]*Domain
        +composites map[dbOid]map[compositeKey]*CompositeType
        +ranges map[dbOid]map[rangeKey]*RangeType
        +roles map[oid]*Role
        +nextOID uint32
    }
    class tableNamespace {
        +tables map[name]*Table
        +indexes map[name]*Index
        +byTable map[oid]*Table
    }
    class Table {
        +Schema, Name
        +OID, RelFileNodeOID, DBOid
        +Columns []Column
        +Indexes []Index
        +Virtual bool
        +VirtualRows func()[][]string
        +View *parser.SelectStmt
        +TempOwner string
        +Stats *TableStats
    }
    class Column {
        +Type Type
        +Ordinal int
        +Dropped bool
        +MissingValue any
        +Identity, Generated fields
        +Storage, Compression, Collation
    }

    InMemory --> tableNamespace : namespaces[dbOid]
    tableNamespace --> Table : byTable[oid]
    Table --> Column : Columns
```

## Virtual vs Heap-Backed Catalogs

```mermaid
flowchart TD
    CAT["catalog.InMemory"]

CAT --> Virtual["Virtual: true<br/>registerSystemTables']
    Virtual --> PGC["pg_class (1259)<br/>PGClassRowsForDBOid<br/>34-col PG18-canonical tupdesc"]
    Virtual --> PGD["pg_database, pg_namespace,<br/>pg_constraint, pg_depend,<br/>pg_stat_*, information_schema.*"]
    Virtual --> PGN["…all Go builders,<br/>no backing relfile"]

    CAT --> Heap["RegisterRealTable<br/>requires OID < FirstUserOID(16384)"]
    Heap --> PGT["pg_type (1247)<br/>heap-backed relfile"]
    Heap --> PGA["pg_attribute (1249)<br/>heap-backed relfile"]

    CAT --> Dual["pg_class also gets real heap image"]
    Dual --> Sync["syncTableToCatalogHeap<br/>writes 34-col pg_class + 25-col pg_attribute<br/>+ btree indexes + pg_attrdef + pg_inherits"]
Sync --> Reload["startup: loadUserTablesFromHeap<br/>→ reload*FromHeap passes<br/>→ rebuild InMemory from disk']

    note right of Dual: pg_class virtual rows serve live queries;<br/>heap image serves PG-standby / pg_dump parity
```

## DDL Sync to Catalog Heaps

```mermaid
sequenceDiagram
    participant E as executor.ddlOp
    participant CAT as catalog.InMemory
    participant ST as storage
    participant X as xlog.Writer

    E->>CAT: CreateTable(schema, table, cols, …)
    CAT->>CAT: AllocOID, getOrCreateNS, mu.Lock
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

    Note over E: DDL ABORT: NewAutocommitUndoSession undoes InMemory changes (heap writes persist)
```