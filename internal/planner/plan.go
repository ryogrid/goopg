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
