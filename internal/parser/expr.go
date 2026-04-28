package parser

// Expr is implemented by every expression-tree node.
type Expr interface {
	Node
	exprNode()
}

// IntegerConst is a literal integer value parsed from the source.
// v0 keeps integers in int64 — pgbench fits comfortably.
type IntegerConst struct {
	pos   int
	Value int64
}

func (e *IntegerConst) Pos() int { return e.pos }
func (*IntegerConst) exprNode()  {}

// StringConst is a literal string value.
type StringConst struct {
	pos   int
	Value string
}

func (e *StringConst) Pos() int { return e.pos }
func (*StringConst) exprNode()  {}

// NullConst is the SQL NULL literal.
type NullConst struct{ pos int }

func (e *NullConst) Pos() int { return e.pos }
func (*NullConst) exprNode()  {}

// BooleanConst is TRUE / FALSE.
type BooleanConst struct {
	pos   int
	Value bool
}

func (e *BooleanConst) Pos() int { return e.pos }
func (*BooleanConst) exprNode()  {}

// ParamRef is a bind-parameter placeholder, $N in source.
type ParamRef struct {
	pos    int
	Number int
}

func (e *ParamRef) Pos() int { return e.pos }
func (*ParamRef) exprNode()  {}

// ColumnRef is one column reference, possibly schema/table-qualified.
// Forms accepted: name, table.name, schema.table.name.
type ColumnRef struct {
	pos    int
	Schema string
	Table  string
	Column string
}

func (e *ColumnRef) Pos() int { return e.pos }
func (*ColumnRef) exprNode()  {}

// StarExpr is the bare * appearing in SELECT *. Qualified forms
// (table.*) are also accepted; Schema/Table fields point at the
// qualifier when present.
type StarExpr struct {
	pos    int
	Schema string
	Table  string
}

func (e *StarExpr) Pos() int { return e.pos }
func (*StarExpr) exprNode()  {}

// BinaryOp is `Left Op Right` for the arithmetic, string-concat,
// comparison, and boolean operators v0 recognises.
type BinaryOp struct {
	pos   int
	Op    string
	Left  Expr
	Right Expr
}

func (e *BinaryOp) Pos() int { return e.pos }
func (*BinaryOp) exprNode()  {}

// UnaryOp is `Op Operand`. v0 uses it for `-x`, `+x`, and `NOT x`.
type UnaryOp struct {
	pos     int
	Op      string
	Operand Expr
}

func (e *UnaryOp) Pos() int { return e.pos }
func (*UnaryOp) exprNode()  {}

// FuncCall is `Name(args…)`. v0 covers the call shape only; argument
// type checking and overload resolution are the analyzer's job.
type FuncCall struct {
	pos      int
	Name     ObjectName
	Args     []Expr
	Star     bool // count(*)-style star argument
	Distinct bool // DISTINCT inside the arg list
}

func (e *FuncCall) Pos() int { return e.pos }
func (*FuncCall) exprNode()  {}
