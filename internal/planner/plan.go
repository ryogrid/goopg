// Package planner translates parser.Stmt trees into goopg plan
// nodes. Scope and growth path are documented in
// docs/design/0011-planner.md.
package planner

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Node is implemented by every plan-tree node. Pos is the byte offset
// of the originating SQL token; Output is the row schema this node
// emits to its parent.
type Node interface {
	Pos() int
	Output() Schema
}

// Schema is the ordered set of columns a plan node produces. Each
// column has an output name and a typed source.
type Schema []SchemaColumn

// SchemaColumn is one entry in a plan node's output schema.
type SchemaColumn struct {
	Name string
	Type catalog.Type
}

// Expr is implemented by every planner expression. The planner
// resolves parser.ColumnRef into an indexed reference into the
// child's output, so executor evaluation is a flat slice lookup
// rather than a name-based one.
type Expr interface {
	Pos() int
	exprNode()
}

// IntegerConst — int64 literal.
type IntegerConst struct {
	pos   int
	Value int64
}

func (e *IntegerConst) Pos() int { return e.pos }
func (*IntegerConst) exprNode()  {}

// StringConst — string literal.
type StringConst struct {
	pos   int
	Value string
}

func (e *StringConst) Pos() int { return e.pos }
func (*StringConst) exprNode()  {}

// NumericConst carries a verbatim NUMERIC / decimal literal (e.g.
// `123.45`, `1e-5`). v0 treats the value as opaque text — the
// executor's NUMERIC codec stores it byte-for-byte. Real
// arithmetic on NUMERIC waits on the type system.
type NumericConst struct {
	pos   int
	Value string
}

func (e *NumericConst) Pos() int { return e.pos }
func (*NumericConst) exprNode()  {}

// TypedStringLit mirrors parser.TypedStringLit. The executor
// parses `Value` per `Type` at evaluation time so callers can
// compare against catalog-typed columns without an explicit
// cast operator.
type TypedStringLit struct {
	pos   int
	Type  string
	Value string
}

func (e *TypedStringLit) Pos() int { return e.pos }
func (*TypedStringLit) exprNode()  {}

// IntervalLit mirrors parser.IntervalLit.
type IntervalLit struct {
	pos   int
	Value string
	Unit  string
}

func (e *IntervalLit) Pos() int { return e.pos }
func (*IntervalLit) exprNode()  {}

// ExtractExpr mirrors parser.ExtractExpr. Field is the
// lower-cased calendar component the executor switches on.
type ExtractExpr struct {
	pos    int
	Field  string
	Source Expr
}

func (e *ExtractExpr) Pos() int { return e.pos }
func (*ExtractExpr) exprNode()  {}

// CaseWhen mirrors parser.CaseWhen with planner-resolved
// expressions in place of parser-AST nodes.
type CaseWhen struct {
	When Expr
	Then Expr
}

// CaseExpr is the planner mirror of parser.CaseExpr. The simple
// form sets Operand and the executor evaluates each When as
// `Operand = When`; the searched form has Operand=nil and treats
// each When as a boolean predicate.
type CaseExpr struct {
	pos     int
	Operand Expr // nil for searched form
	Whens   []CaseWhen
	Else    Expr // nil if absent
}

func (e *CaseExpr) Pos() int { return e.pos }
func (*CaseExpr) exprNode()  {}

// NullConst — SQL NULL.
type NullConst struct{ pos int }

func (e *NullConst) Pos() int { return e.pos }
func (*NullConst) exprNode()  {}

// BooleanConst — TRUE / FALSE.
type BooleanConst struct {
	pos   int
	Value bool
}

func (e *BooleanConst) Pos() int { return e.pos }
func (*BooleanConst) exprNode()  {}

// ColumnRef points at a column in the child operator's output.
// Index is 0-based into the input row.
type ColumnRef struct {
	pos   int
	Index int
	Name  string // resolved column name (for diagnostics)
	Type  catalog.Type
}

func (e *ColumnRef) Pos() int { return e.pos }
func (*ColumnRef) exprNode()  {}

// ParamRef passes through a bind-parameter placeholder. The executor
// supplies values at execute time.
type ParamRef struct {
	pos    int
	Number int
}

func (e *ParamRef) Pos() int { return e.pos }
func (*ParamRef) exprNode()  {}

// BinaryOp — Left Op Right.
type BinaryOp struct {
	pos   int
	Op    string
	Left  Expr
	Right Expr
}

func (e *BinaryOp) Pos() int { return e.pos }
func (*BinaryOp) exprNode()  {}

// UnaryOp — Op Operand.
type UnaryOp struct {
	pos     int
	Op      string
	Operand Expr
}

func (e *UnaryOp) Pos() int { return e.pos }
func (*UnaryOp) exprNode()  {}

// FuncCall — identified by its planner-resolved name. Argument
// expressions live under Args; v0 doesn't yet resolve overloads.
type FuncCall struct {
	pos  int
	Name string
	Args []Expr
	Star bool
}

func (e *FuncCall) Pos() int { return e.pos }
func (*FuncCall) exprNode()  {}

// SeqScan — full heap scan of a single relation.
type SeqScan struct {
	pos    int
	Table  *catalog.Table
	schema Schema
}

func (n *SeqScan) Pos() int       { return n.pos }
func (n *SeqScan) Output() Schema { return n.schema }

// IndexScan probes a single-column B-tree index with an equality key.
// v0 supports only `col = const` / `col = $N` shapes.
type IndexScan struct {
	pos    int
	Table  *catalog.Table
	Index  *catalog.Index
	Key    Expr
	schema Schema
}

func (n *IndexScan) Pos() int       { return n.pos }
func (n *IndexScan) Output() Schema { return n.schema }

// JoinType is the physical join shape emitted by the planner.
type JoinType int

const (
	JoinTypeInner JoinType = iota
	JoinTypeLeft
	JoinTypeRight
	JoinTypeFull
	JoinTypeCross
)

// Join combines two child relations with an optional predicate.
// Predicate is nil for CROSS JOIN and NATURAL/USING joins with no
// shared columns.
type Join struct {
	pos       int
	Type      JoinType
	Left      Node
	Right     Node
	Predicate Expr
	schema    Schema
}

func (n *Join) Pos() int       { return n.pos }
func (n *Join) Output() Schema { return n.schema }

// AggregateCall is one aggregate function invocation in an Aggregate node.
type AggregateCall struct {
	pos      int
	Name     string
	Arg      Expr // nil for count(*)
	Star     bool
	Distinct bool
	Type     catalog.Type
}

func (a AggregateCall) Pos() int { return a.pos }

// Aggregate groups rows by GroupExprs and computes Aggs.
// Output columns are [group exprs..., aggregate calls...].
type Aggregate struct {
	pos        int
	Child      Node
	GroupExprs []Expr
	Aggs       []AggregateCall
	schema     Schema
}

func (n *Aggregate) Pos() int       { return n.pos }
func (n *Aggregate) Output() Schema { return n.schema }

// Filter — applies a predicate to its child's rows.
type Filter struct {
	pos       int
	Child     Node
	Predicate Expr
}

func (n *Filter) Pos() int       { return n.pos }
func (n *Filter) Output() Schema { return n.Child.Output() }

// Project — evaluates the target list against its child's rows.
type Project struct {
	pos     int
	Child   Node
	Targets []Expr
	schema  Schema
}

func (n *Project) Pos() int       { return n.pos }
func (n *Project) Output() Schema { return n.schema }

// Sort — orders the child's rows by the given keys.
type SortKey struct {
	Expr Expr
	Desc bool
}

type Sort struct {
	pos   int
	Child Node
	Keys  []SortKey
}

func (n *Sort) Pos() int       { return n.pos }
func (n *Sort) Output() Schema { return n.Child.Output() }

// Limit — caps the number of rows; both fields are optional.
type Limit struct {
	pos    int
	Child  Node
	Limit  Expr // nil when no limit
	Offset Expr // nil when no offset
}

func (n *Limit) Pos() int       { return n.pos }
func (n *Limit) Output() Schema { return n.Child.Output() }

// Values — produces literal rows for INSERT.
type Values struct {
	pos    int
	Rows   [][]Expr
	schema Schema
}

func (n *Values) Pos() int       { return n.pos }
func (n *Values) Output() Schema { return n.schema }

// Insert — writes rows from Source into Table. ColumnIndex maps each
// source column to a target heap-tuple ordinal; columns not listed
// receive NULL (or their declared default once defaults are wired).
type Insert struct {
	pos         int
	Table       *catalog.Table
	Source      Node
	ColumnIndex []int
}

func (n *Insert) Pos() int       { return n.pos }
func (n *Insert) Output() Schema { return nil }

// Update — overwrites visible rows of Table with Set assignments.
// Set is parallel to the table's columns: nil entries leave the
// existing value alone; non-nil entries are evaluated against the
// child's rows.
type Update struct {
	pos   int
	Table *catalog.Table
	Child Node
	Set   []Expr // len == len(Table.Columns)
}

func (n *Update) Pos() int       { return n.pos }
func (n *Update) Output() Schema { return nil }

// Delete — marks the visible rows of Table that survive the child's
// filter as dead.
type Delete struct {
	pos   int
	Table *catalog.Table
	Child Node
}

func (n *Delete) Pos() int       { return n.pos }
func (n *Delete) Output() Schema { return nil }

// DDL — passes the original parser DDL statement through to the
// executor's DDL path. The planner doesn't decompose DDL further in
// v0; the catalog is mutated as the executor runs the statement.
type DDL struct {
	pos  int
	Stmt parser.Stmt
}

func (n *DDL) Pos() int       { return n.pos }
func (n *DDL) Output() Schema { return nil }

// Transaction — BEGIN / COMMIT / ROLLBACK.
type TransactionVerb int

const (
	TxBegin TransactionVerb = iota
	TxCommit
	TxRollback
)

type Transaction struct {
	pos  int
	Verb TransactionVerb
}

func (n *Transaction) Pos() int       { return n.pos }
func (n *Transaction) Output() Schema { return nil }

// Utility — VACUUM / ANALYZE / SHOW / SET / RESET; carries the
// original parser statement.
type Utility struct {
	pos  int
	Stmt parser.Stmt
}

func (n *Utility) Pos() int       { return n.pos }
func (n *Utility) Output() Schema { return nil }

// Checkpoint — `CHECKPOINT`. Distinct from Utility because it has
// real side-effects (synchronous flush + WAL marker), wired through
// executor.Context.Checkpointer.
type Checkpoint struct{ pos int }

func (n *Checkpoint) Pos() int       { return n.pos }
func (n *Checkpoint) Output() Schema { return nil }

// CopyDirection records whether the COPY moves data into or out of
// the table. Mirrors parser.CopyDirection so the executor doesn't
// need to import the parser package directly.
type CopyDirection int

const (
	CopyFrom CopyDirection = iota
	CopyTo
)

// CopyEndpoint discriminates the source/sink. v0 implements
// stdin/stdout end-to-end and parses file/PROGRAM into stable
// "feature_not_supported" errors at execute time.
type CopyEndpoint int

const (
	CopyEndpointStdin CopyEndpoint = iota
	CopyEndpointStdout
	CopyEndpointFile
	CopyEndpointProgram
)

// Copy — `COPY (table [(cols)] | (query)) {FROM|TO} {endpoint} [opts]`.
//
// Exactly one of Table/Query is set. ColumnIndex lists table column
// ordinals for the table-form COPY; for query-form COPY TO it's nil
// (the schema comes from Query.Output()). Options is a passthrough
// of the parser option list — execute-time interpretation is
// centralised in the executor so the wire layer doesn't have to
// agree on a normalisation path.
type Copy struct {
	pos         int
	Direction   CopyDirection
	Table       *catalog.Table
	ColumnIndex []int
	Query       Node
	Endpoint    CopyEndpoint
	Filename    string
	Options     []parser.CopyOption
	schema      Schema
}

func (n *Copy) Pos() int       { return n.pos }
func (n *Copy) Output() Schema { return n.schema }
