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

func NewInExpr(pos int, operand Expr, negated bool, anyOp OpCode, allOp bool, sub *SelectStmt, list []Expr) *InExpr {
	return &InExpr{pos: pos, Operand: operand, Negated: negated, AnyOp: anyOp, AllOp: allOp, Subquery: sub, List: list}
}

func NewLikeEscapePattern(pos int, pattern, escape Expr) *LikeEscapePattern {
	return &LikeEscapePattern{pos: pos, Pattern: pattern, Escape: escape}
}

func NewTypedStringLit(pos int, typ, value string) *TypedStringLit {
	return &TypedStringLit{pos: pos, Type: typ, Value: value}
}

func NewSimilarToPattern(pos int, left, pattern, escape Expr, negate bool) *SimilarToPattern {
	return &SimilarToPattern{pos: pos, Left: left, Pattern: pattern, Escape: escape, Negate: negate}
}

func NewCaseWhen(when, then Expr) CaseWhen { return CaseWhen{When: when, Then: then} }

func NewCaseExpr(pos int, operand Expr, whens []CaseWhen, elseExpr Expr) *CaseExpr {
	return &CaseExpr{pos: pos, Operand: operand, Whens: whens, Else: elseExpr}
}

func NewExistsExpr(pos int, negated bool, sub *SelectStmt) *ExistsExpr {
	return &ExistsExpr{pos: pos, Negated: negated, Subquery: sub}
}

func NewInListExpr(pos int, operand Expr, negated bool, list []Expr) *InExpr {
	return &InExpr{pos: pos, Operand: operand, Negated: negated, List: list}
}

func NewFuncCall(pos int, name ObjectName, args []Expr, star bool) *FuncCall {
	fc := &FuncCall{pos: pos, Name: name, Args: args, Star: star}
	// Legacy parity (parser/select.go:4875): one Variadic flag PER ARG;
	// the star path never appends (Variadic stays nil). The dump/diff
	// surface distinguishes []bool{false} from nil.
	if !star {
		for range args {
			fc.Variadic = append(fc.Variadic, false)
		}
	}
	return fc
}

func SetDistinct(fc *FuncCall) *FuncCall { fc.Distinct = true; return fc }
func SetOrderBy(fc *FuncCall, ob []SortBy) *FuncCall { fc.OrderBy = ob; return fc }

func NewCastExpr(pos int, operand Expr, typ ObjectName, typmods []int64) *CastExpr {
	return &CastExpr{pos: pos, Operand: operand, Type: typ, Typmods: typmods}
}

func NewCollateExpr(pos int, operand Expr, collation string) *CollateExpr {
	return &CollateExpr{pos: pos, Operand: operand, CollationName: collation}
}

func NewWindowDef(pos int) *WindowDef { return &WindowDef{pos: pos} }
func NewBareWindowRef(pos int, name string) *WindowDef {
	return &WindowDef{pos: pos, RefName: name, IsBareRef: true}
}

// NewSubqueryExpr wraps a SELECT used as an expression value (scalar
// subquery). Legacy parity: analyzer/planner/executor already speak
// *SubqueryExpr, so the yacc grammar emits it instead of inventing a node.
func NewSubqueryExpr(pos int, inner *SelectStmt) *SubqueryExpr {
	return &SubqueryExpr{pos: pos, Inner: inner}
}

func NewExtractExpr(pos int, field string, source Expr) *ExtractExpr {
	return &ExtractExpr{pos: pos, Field: field, Source: source}
}

// NewIntervalLitPre builds the PreComputed form of IntervalLit from components
// parsed via ParseIntervalBody (embedded-string `interval '<body>'` form).
func NewIntervalLitPre(pos int, months, days int32, micros int64) *IntervalLit {
	return &IntervalLit{pos: pos, PreComputed: true, PreMonths: months, PreDays: days, PreMicros: micros}
}

// NewArrayConstructorExpr builds ARRAY[e1, ..., en] (legacy parity).
func NewArrayConstructorExpr(pos int, elements []Expr) *ArrayConstructorExpr {
	return &ArrayConstructorExpr{pos: pos, Elements: elements}
}

// NewArraySubqueryExpr builds ARRAY(SELECT ...) (legacy parity).
func NewArraySubqueryExpr(pos int, inner *SelectStmt) *ArraySubqueryExpr {
	return &ArraySubqueryExpr{pos: pos, Inner: inner}
}

// NewArraySubscriptExpr builds base[i] or base[l:u] / base[:u] / base[l:] /
// base[:] slices (legacy parity).
func NewArraySubscriptExpr(pos int, base Expr, isSlice bool, index, upper Expr) *ArraySubscriptExpr {
	return &ArraySubscriptExpr{pos: pos, Base: base, IsSlice: isSlice, Index: index, Upper: upper}
}

// NewIntervalLitQualified builds the Form-1 `interval '<body>' <qualifier>`
// literal: Unit carries the LOW field of an optional `<hi> TO <lo>` range.
func NewIntervalLitQualified(pos int, body, unit string, hasPrec bool, prec int) *IntervalLit {
	return &IntervalLit{pos: pos, Value: body, Unit: unit, Qualified: true, HasPrec: hasPrec, Prec: prec}
}

// NewInsertStmt builds the v0 INSERT shape (VALUES rows; SELECT/DEFAULTS via
// the setters below). ON CONFLICT / RETURNING arrive with later P3 stages.
func NewInsertStmt(pos int, target RangeVar, columns []string, rows [][]Expr) *InsertStmt {
	return &InsertStmt{pos: pos, Target: target, Columns: columns, Rows: rows}
}

// SetInsertSelect switches an INSERT to the `INSERT ... SELECT` form.
func SetInsertSelect(is *InsertStmt, sel *SelectStmt) *InsertStmt {
	is.Rows = nil
	is.Select = sel
	return is
}

// SetInsertDefaultValues switches an INSERT to DEFAULT VALUES.
func SetInsertDefaultValues(is *InsertStmt) *InsertStmt {
	is.Rows = nil
	is.DefaultValues = true
	return is
}

// NewInsertReturning attaches a RETURNING target list to an INSERT.
func NewInsertReturning(is *InsertStmt, ret []ResTarget) *InsertStmt {
	is.Returning = ret
	return is
}

// NewOnConflictTarget builds the conflict arbiter (columns form, constraint
// form, or index-inference WHERE).
func NewOnConflictTarget(columns []string, constraint string, where Expr) *OnConflictTarget {
	return &OnConflictTarget{pos: 0, Columns: columns, Constraint: constraint, Where: where}
}

// NewUpdateAssign builds one DO UPDATE SET entry.
func NewUpdateAssign(column, tableQualifier string, columns []string, expr Expr) *UpdateAssign {
	return &UpdateAssign{pos: 0, Column: column, TableQualifier: tableQualifier, Columns: columns, Expr: expr}
}

// NewOnConflictClause assembles the ON CONFLICT tail for an INSERT.
func NewOnConflictClause(target *OnConflictTarget, action OnConflictAction, set []UpdateAssign, where Expr) *OnConflictClause {
	return &OnConflictClause{pos: 0, Target: target, Action: action, UpdateSet: set, UpdateWhere: where}
}

// NewUpdateStmt builds the P3.2 UPDATE shape.
func NewUpdateStmt(pos int, target RangeVar, set []UpdateAssign, from []RangeVar, where Expr) *UpdateStmt {
	return &UpdateStmt{pos: pos, Target: target, Set: set, From: from, Where: where}
}

// SetUpdateWhereCurrentOf switches the WHERE clause to CURRENT OF.
func SetUpdateWhereCurrentOf(u *UpdateStmt, cursor string) *UpdateStmt {
	u.Where = nil
	u.CurrentOf = cursor
	return u
}

// NewDeleteStmt builds the P3.3 DELETE shape (Using tables supported).
func NewDeleteStmt(pos int, target RangeVar, using []RangeVar, where Expr) *DeleteStmt {
	return &DeleteStmt{pos: pos, Target: target, Using: using, Where: where}
}

// SetDeleteWhereCurrentOf switches the WHERE clause to CURRENT OF.
func SetDeleteWhereCurrentOf(d *DeleteStmt, cursor string) *DeleteStmt {
	d.Where = nil
	d.CurrentOf = cursor
	return d
}

// NewColumnType builds a column type (CREATE TABLE positions).
func NewColumnType(schema, name string, args []int64, isArray bool) ColumnType {
	return ColumnType{Schema: schema, Name: name, Args: args, IsArray: isArray}
}

// NewColumnDef builds one column definition.
func NewColumnDef(name string, ct ColumnType) *ColumnDef {
	return &ColumnDef{pos: 0, Name: name, Type: ct}
}

// NewCreateTableStmt assembles the P4.1 v0 CREATE TABLE shape.
func NewCreateTableStmt(pos int, name ObjectName, cols []ColumnDef, pk []string) *CreateTableStmt {
	return &CreateTableStmt{pos: pos, Name: name, Columns: cols, PrimaryKey: pk, With: map[string]string{}}
}
