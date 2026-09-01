# Class Diagram

goopg is Go, so this models the central **structs and interfaces** and their
composition/ownership relationships rather than class inheritance. The diagram
spans the query path (optimizer → executor → storage) plus the postmaster and
replication lifecycles.

```mermaid
classDiagram
    class Node {
        <<interface>>
        +Pos() int
        +Output() Schema
    }
    class RelOptInfo {
        +Relids RelSet
        +Rows
        +Pathlist
        +CheapestTotal
    }
    class Path {
        +Cost
        +Rows
        +Pathkeys
        +Children
    }
    class Operator {
        <<interface>>
        +Open(ctx) error
        +Next() TupleSlot
        +Close() error
        +Schema() Schema
    }
    class Context {
        +Catalog
        +TxnMgr
        +Session
        +Pool
        +Params
    }
    class Datum {
        +Kind
        +value
    }
    class OpIterator {
        +tree *opTreeSlab
        +rootIdx int32
    }
    class Session {
        <<interface>>
        +Tx()
        +InExplicit()
    }
    class Pool {
        +slots []Slot
        +arena
        +bufmap
    }
    class Slot {
        +state atomic
        +page []byte
    }
    class Manager {
        +rels map~RelFileNode~relFile
    }
    class Runtime {
        +StorageMgr
        +Pool
        +TxnMgr
        +Catalog
        +WAL
    }
    class Server {
        +Run() error
    }
    class connTxState {
        +Tx()
        +LoginUser
        +SessionUser
    }
    class Handler {
        +HandleCommand()
    }
    class WalReceiver {
        +Run() error
    }
    class LogicalReceiver {
        +Run() error
    }
    class ApplyLauncher {
        +Run()
    }

RelOptInfo --> Path : 'add_path / set_cheapest'
    Path --> Node : createPlanNode
    Operator <|.. joinOp
    Operator <|.. seqScanOp
    OpIterator --> Operator : OpAdapter (non-migrated)
    Context --> Session
    Context --> Pool
    Context --> Datum : Row
    Pool --> Slot : owns
    Pool --> Manager : all I/O routed through
    Runtime --> Pool
    Runtime --> Manager
    Server --> connTxState : per connection
    Server --> Handler : replication.Handler
    Server --> ApplyLauncher
    ApplyLauncher --> LogicalReceiver : spawns
    WalReceiver --> Slot : appends WAL via xlog.Writer
```

## Notes

- **Ownership** — `Context` is per-statement, created and torn down by
  `postmaster` dispatch; `Session` is per-connection and referenced (not owned)
  by `Context`.
- **Concurrency** — `Pool`/`Slot` pin/unpin are lock-free (atomic state word);
  `Manager` serializes AIO-vs-sync on the same block via `relFile.blockBusy`.
- **Two engines** — `Operator` (legacy tree) and `OpIterator`/`opTreeSlab` (fast
  slab) are siblings; non-migrated ops are wrapped in an `OpAdapter`.
- **Threading** — the postmaster runs one goroutine per connection; checkpointer,
  walwriter, autovacuum, and the replication receivers are separate goroutines.
