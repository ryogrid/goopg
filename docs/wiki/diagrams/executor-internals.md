# Executor Internals

The two execution engines, the operator tree structure, and expression
evaluation dispatch.

## Operator Tree Structure

````mermaid
classDiagram
    class Operator {
        <<interface>>
        +Open(ctx *Context) error
        +Next() TupleSlot
        +Close() error
        +Schema() optimizer.Schema
    }
    class OpIterator {
        +tree *opTreeSlab
        +rootIdx int32
        +Open() error
        +Next() TupleSlot
        +Close() error
    }
    class opTreeSlab {
        +ops []OpNode
        +exprs *exprTreeSlab
        +buildRec()
    }
    class OpNode {
        +kind opKind
        +children []int32
        +exprIdx []int32
        +storage info (RelFileNode, *Table)
    }
    class exprTreeSlab {
        +nodes []ExprNode
    }
    class seqScanOp {
        +rel storage.RelFileNode
        +table *catalog.Table
        +Next() TupleSlot
    }
    class joinOp {
        +hash / merge / nl variants
        +Next() TupleSlot
    }
    class ddlOp {
        +~50-arm Next switch
        +exec* methods
    }

    Operator <|.. seqScanOp
    Operator <|.. joinOp
    Operator <|.. ddlOp
    OpIterator --> opTreeSlab : tree
    OpIterator --> OpNode : opOpen / opNext dispatch
    opTreeSlab --> exprTreeSlab : exprs
    OpNode --> exprTreeSlab : exprIdx
````

## Fast-Slab vs Legacy Engine

````mermaid
flowchart TD
    Node["optimizer.Node"] --> Legacy{"entry point?"}
    Legacy -->|"Build"| L1["buildNode recursive<br/>Operator tree"]
    L1 --> L2["legacy Open / Next / Close<br/>per-operator structs"]
    L2 --> L3["test / drain helper Run(op, ctx)"]

    Legacy -->|"BuildFast / BuildFastIterator"| F1["build opTreeSlab + exprTreeSlab<br/>flat int32-indexed arrays"]
    F1 --> F2{"op migrated?"}
    F2 -->|yes| F3["opOpen / opNext<br/>direct dispatch on OpNode"]
    F2 -->|no| F4["OpAdapter wraps legacy<br/>Operator interface"]
    F4 --> F5["forward to legacy op"]
    F3 --> F6["Slots are GC-pointer-free;<br/>int32 indices instead of pointers"]
````

## Expression Evaluation Dispatch

````mermaid
flowchart TD
    Expr["parser.Expr / nodes.Expr"] --> A{"engine?"}
    A -->|"interpreter (legacy)"| I1["evalExprSlot"]
    I1 --> I2["giant type switch on Datum.Kind"]
    I2 --> I3["builtin bodies<br/>(to_char, regexp, arithmetic, …)"]

    A -->|"compiled (fast)"| C1["exprTreeSlab build<br/>→ ExprNode + evalFastExpr"]
    C1 --> C2{"node migratable?"}
    C2 -->|yes| C3["evalFastExpr<br/>direct kind dispatch"]
    C2 -->|no| C4["ExprAdapter<br/>falls back to evalExprSlot"]
    C3 --> C5["boolean short-circuit on KindBool"]
    C5 --> R["Datum result"]
    I3 --> R
````