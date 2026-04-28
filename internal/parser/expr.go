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

// NumericConst carries the verbatim text of a decimal / scientific
// literal so we don't lose precision before the executor's NUMERIC
// codec encodes it as varlen text. v0 doesn't compute on NUMERIC —
// the value flows through analyzer/planner unchanged. Real
// arithmetic waits on the type system milestone.
type NumericConst struct {
	pos   int
	Value string
}

func (e *NumericConst) Pos() int { return e.pos }
func (*NumericConst) exprNode()  {}

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

// CastExpr is `Operand :: TypeName` (upstream's `expr::type` shorthand
// for `CAST(expr AS type)`). v0 carries the type name through as a
// schema-qualified ObjectName so future loops can resolve it; the
// executor currently treats the cast as a no-op (the underlying
// expression evaluates and the requested type is ignored), which is
// good enough for pgbench's `oid=$1::pg_catalog.regclass` shape
// since the executor doesn't yet enforce typing.
type CastExpr struct {
	pos     int
	Operand Expr
	Type    ObjectName
	Typmods []int64 // optional `(N)` or `(N,M)` typmod arguments
}

func (e *CastExpr) Pos() int { return e.pos }
func (*CastExpr) exprNode()  {}

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
