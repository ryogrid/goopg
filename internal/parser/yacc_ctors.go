package parser

// Constructors for the goyacc parser front end (parser-rewrite project,
// docs/design/not_ralph/02-grammar-porting-guide.md §3).
//
// The AST node structs keep their `pos` fields unexported (they predate the
// rewrite and hundreds of consumers rely on that encapsulation), but yacc
// actions live in a different package and must seed positions. These
// New* constructors are the sanctioned seam: thin, allocation-transparent,
// and named after the node they build so grammar actions stay diffable
// against upstream text.
//
// Conventions:
//   - first argument is always the byte position of the production's first
//     meaningful token (same units the legacy parser stores),
//   - remaining arguments map to exported fields in declaration order,
//   - nodes with only a pos (NullConst) take just the position.

func NewIntegerConst(pos int, value int64) *IntegerConst {
	return &IntegerConst{pos: pos, Value: value}
}

func NewNumericConst(pos int, value string) *NumericConst {
	return &NumericConst{pos: pos, Value: value}
}

func NewStringConst(pos int, value string) *StringConst {
	return &StringConst{pos: pos, Value: value}
}

func NewBooleanConst(pos int, value bool) *BooleanConst {
	return &BooleanConst{pos: pos, Value: value}
}

func NewNullConst(pos int) *NullConst { return &NullConst{pos: pos} }

func NewParamRef(pos int, number int) *ParamRef {
	return &ParamRef{pos: pos, Number: number}
}

// NewColumnRef builds from qualifier parts already split by the grammar:
// 1 part → Column; 2 parts → Table+Column; 3 parts → Schema+Table+Column.
// n must be 1..3.
func NewColumnRef(pos int, parts []string) *ColumnRef {
	var c ColumnRef
	c.pos = pos
	switch len(parts) {
	case 1:
		c.Column = parts[0]
	case 2:
		c.Table, c.Column = parts[0], parts[1]
	default:
		c.Schema, c.Table, c.Column = parts[0], parts[1], parts[2]
	}
	return &c
}

func NewStarExpr(pos int, schema, table string) *StarExpr {
	return &StarExpr{pos: pos, Schema: schema, Table: table}
}

func NewBinaryOp(pos int, op OpCode, left, right Expr) *BinaryOp {
	return &BinaryOp{pos: pos, Op: op, Left: left, Right: right}
}

func NewUnaryOp(pos int, op OpCode, operand Expr) *UnaryOp {
	return &UnaryOp{pos: pos, Op: op, Operand: operand}
}

func NewResTarget(pos int, alias string, expr Expr) ResTarget {
	return ResTarget{pos: pos, Alias: alias, Expr: expr}
}

func NewRangeVar(pos int, schema, name, alias string) RangeVar {
	return RangeVar{pos: pos, Schema: schema, Name: name, Alias: alias}
}

func NewSelectStmt(pos int) *SelectStmt { return &SelectStmt{pos: pos} }

// ParseBinaryOp re-exported for the grammar support file's convenience — it
// already exists in op.go; this alias documents the cross-package usage.
// (No new code; see op.go ParseBinaryOp/ParseUnaryOp.)

func NewFromExpr(pos int, base RangeVar, joins []JoinExpr) FromExpr {
	return FromExpr{pos: pos, Base: base, Joins: joins}
}
func NewSortBy(pos int, expr Expr, desc bool, usingOp string) SortBy {
	return SortBy{pos: pos, Expr: expr, Desc: desc, UsingOp: usingOp}
}

func NewJoinExpr(pos int, jt JoinType, natural bool, right RangeVar, on Expr, using []string) JoinExpr {
	return JoinExpr{pos: pos, Type: jt, Natural: natural, Right: right, On: on, Using: using}
}

func NewTableFuncRef(pos int, name string, args []Expr, withOrdinality bool, rows []RowsFromEntry) *TableFuncRef {
	return &TableFuncRef{pos: pos, Name: name, Args: args, WithOrdinality: withOrdinality, RowsFuncs: rows}
}

func NewSetOpClause(pos int, typ SetOpType, all bool, right *SelectStmt) *SetOpClause {
	return &SetOpClause{pos: pos, Type: typ, All: all, Right: right}
}

func NewWithClause(pos int, recursive bool, ctes []*CommonTableExpr) *WithClause {
	return &WithClause{pos: pos, Recursive: recursive, CTEs: ctes}
}

func NewCommonTableExpr(pos int, name string, cols []string, query *SelectStmt) *CommonTableExpr {
	return &CommonTableExpr{pos: pos, Name: name, Columns: cols, Query: query}
}

func NewIsNullExpr(pos int, operand Expr, negated bool) *IsNullExpr {
	return &IsNullExpr{pos: pos, Operand: operand, Negated: negated}
}

func NewIsBoolExpr(pos int, operand Expr, testTrue, testFalse, negated bool) *IsBoolExpr {
	return &IsBoolExpr{pos: pos, Operand: operand, TestTrue: testTrue, TestFalse: testFalse, Negated: negated}
}

func NewIsDistinctFromExpr(pos int, left, right Expr, negated bool) *IsDistinctFromExpr {
	return &IsDistinctFromExpr{pos: pos, Left: left, Right: right, Negated: negated}
}
