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

// TypedStringLit represents the SQL-standard typed-string-literal
// syntax — `<type> 'value'`. The two we care about for TPC-H:
// `date '1998-12-01'` and `timestamp '1998-12-01 00:00:00'`. The
// parser emits this when it sees an unquoted ident matching a
// recognised type-name (date / timestamp / timestamptz)
// immediately followed by a TokenStringLit. The executor parses
// `Value` per `Type` at evaluation time.
type TypedStringLit struct {
	pos   int
	Type  string // lower-cased type name: "date", "timestamp", "timestamptz"
	Value string
}

func (e *TypedStringLit) Pos() int { return e.pos }
func (*TypedStringLit) exprNode()  {}

// ExtractExpr is the SQL-standard `EXTRACT(field FROM source)`
// where `field` is an unquoted identifier naming a calendar
// component (year, month, day, hour, minute, second, dow, doy,
// epoch, …). v0 implements the subset TPC-H needs (year, month,
// day) plus the obvious neighbours; sub-second fields wait on
// the type system. Stored separately from `FuncCall` because
// upstream's grammar is `EXTRACT(extract_arg FROM expr)` —
// the field is a keyword-position identifier, not a value
// expression.
type ExtractExpr struct {
	pos    int
	Field  string // lower-cased: "year"/"month"/"day"/...
	Source Expr
}

func (e *ExtractExpr) Pos() int { return e.pos }
func (*ExtractExpr) exprNode()  {}

// IntervalLit represents the SQL-standard `interval 'N' unit`
// shape used heavily by TPC-H (Q1: `interval '90' day`, Q4:
// `interval '3' month`, Q5/6/12/14: `interval '1' year`, etc.).
// v0 supports the integer-count + single-unit form only (day,
// month, year and their plurals); upstream's full ISO 8601 and
// multi-field forms wait on the type system.
type IntervalLit struct {
	pos   int
	Value string // verbatim numeric body of the literal (e.g. "90")
	Unit  string // lower-cased unit: "day"/"month"/"year"
}

func (e *IntervalLit) Pos() int { return e.pos }
func (*IntervalLit) exprNode()  {}

// InExpr is `expr [NOT] IN (subquery | value_list)`. The
// executor handles the uncorrelated case: drain the inner
// subquery (or evaluate the value-list expressions) into a
// set once per outer Open and probe per outer row.
//
// Either Subquery or List is non-nil but not both. NULL values
// in the inner set follow upstream's three-valued semantics:
// `x IN (a, NULL)` returns NULL when x doesn't match a.
type InExpr struct {
	pos         int
	Operand     Expr
	Negated     bool        // NOT IN
	NotEqualAny bool        // != ANY semantics (OR of != comparisons). M0097-0067.
	Subquery    *SelectStmt // populated for IN (subquery)
	List        []Expr      // populated for IN (val_list)
}

func (e *InExpr) Pos() int { return e.pos }
func (*InExpr) exprNode()  {}

// ExistsExpr is `EXISTS (subquery)` and its NOT variant. The
// executor opens the inner plan, asks for the first row, and
// returns true (or false for NOT EXISTS) iff at least one row
// is produced. The body of the subquery is otherwise ignored
// — output columns are discarded; upstream-aligned semantics.
type ExistsExpr struct {
	pos      int
	Negated  bool        // NOT EXISTS
	Subquery *SelectStmt
}

func (e *ExistsExpr) Pos() int { return e.pos }
func (*ExistsExpr) exprNode()  {}

// IsNullExpr is `expr IS [NOT] NULL`. Unlike a UnaryOp, the result is
// never NULL — it always returns a boolean. Negated=true for IS NOT NULL.
// Added M0096-0004 to support `pg_advisory_lock(key) IS NOT NULL` in
// WHERE clauses used by isolation specs.
type IsNullExpr struct {
	pos     int
	Operand Expr
	Negated bool // true for IS NOT NULL
}

func (e *IsNullExpr) Pos() int { return e.pos }
func (*IsNullExpr) exprNode()  {}

// IsBoolExpr is `expr IS [NOT] TRUE/FALSE/UNKNOWN`.
// Result is always boolean (never NULL). M0097-0003.
// TestTrue/TestFalse/TestUnknown define what the LHS must equal;
// Negated inverts the test (IS NOT TRUE, etc.).
type IsBoolExpr struct {
	pos       int
	Operand   Expr
	TestTrue  bool // target is TRUE
	TestFalse bool // target is FALSE (if neither TestTrue nor TestFalse: UNKNOWN)
	Negated   bool
}

func (e *IsBoolExpr) Pos() int { return e.pos }
func (*IsBoolExpr) exprNode()  {}

// IsDistinctFromExpr is `expr IS [NOT] DISTINCT FROM expr`.
// Result is always boolean (never NULL):
//   - IS DISTINCT FROM     = NOT (a = b OR (a IS NULL AND b IS NULL))
//   - IS NOT DISTINCT FROM = (a = b OR (a IS NULL AND b IS NULL))
//
// Negated=true for IS NOT DISTINCT FROM.
type IsDistinctFromExpr struct {
	pos     int
	Left    Expr
	Right   Expr
	Negated bool // true for IS NOT DISTINCT FROM
}

func (e *IsDistinctFromExpr) Pos() int { return e.pos }
func (*IsDistinctFromExpr) exprNode()  {}

// SubqueryExpr is the v0 scalar-subquery expression: a
// parenthesised SELECT used in expression position. The
// executor evaluates it once per Open and binds the single
// returned cell as the expression's value. Multi-row /
// multi-column subqueries surface as runtime 21000 errors.
//
// IN (subquery), EXISTS (subquery), and correlated subqueries
// are deliberately deferred to a follow-up loop — they need
// distinct planner/executor paths from the scalar form.
type SubqueryExpr struct {
	pos   int
	Inner *SelectStmt
}

func (e *SubqueryExpr) Pos() int { return e.pos }
func (*SubqueryExpr) exprNode()  {}

// CaseWhen is one (WHEN cond THEN result) clause inside a CASE
// expression.
type CaseWhen struct {
	When Expr
	Then Expr
}

// CaseExpr is the SQL CASE expression. Two forms upstream
// supports:
//
//	-- searched form
//	CASE WHEN cond THEN result [WHEN cond THEN result …]
//	     [ELSE result] END
//	-- simple form
//	CASE operand WHEN val THEN result [WHEN val THEN result …]
//	     [ELSE result] END
//
// The searched form has a nil Operand. The simple form sets
// Operand and evaluates each `When` as `Operand = When` per
// upstream.
type CaseExpr struct {
	pos     int
	Operand Expr // nil for the searched form
	Whens   []CaseWhen
	Else    Expr // nil if ELSE omitted
}

func (e *CaseExpr) Pos() int { return e.pos }
func (*CaseExpr) exprNode()  {}

// NullConst is the SQL NULL literal.
type NullConst struct{ pos int }

// DefaultMarker is the row-cell sentinel for `INSERT … VALUES (…,
// DEFAULT, …)`. It is produced by parseValuesRow when a cell is the
// bare DEFAULT keyword; the planner's planInsert substitutes it for
// the target column's catalog DefaultExpr (or NullConst when the
// column has no DEFAULT). Mirrors upstream's rewriteValuesRTE pass —
// it never reaches the executor.
type DefaultMarker struct{ pos int }

func (e *DefaultMarker) Pos() int { return e.pos }
func (*DefaultMarker) exprNode()  {}

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

// IndirectionStar represents the postfix `(expr).*` form: a parenthesised
// source expression followed by `.*` indirection that expands a composite
// (record) return value into its constituent columns. Upstream PG's
// libpqrcv `fetch_table_list` probe uses this shape to flatten a
// set-returning function whose return type is a composite (relid, attrs,
// qual) tuple into three regular target-list columns. Pure parser-level
// node; the planner is responsible for routing it into a set-expanding
// plan (FROM-clause SRF rewrite). M0103-0008 probe-survival.
type IndirectionStar struct {
	pos    int
	Source Expr
}

func (e *IndirectionStar) Pos() int { return e.pos }
func (*IndirectionStar) exprNode()  {}

// RowExpr represents a row constructor `(a, b, c)` (shorthand for ROW(a,b,c)).
// Emitted by parsePrimary when a comma follows the first parenthesised expression. M0097-0020.
type RowExpr struct {
	pos   int
	Elems []Expr
}

func (e *RowExpr) Pos() int { return e.pos }
func (*RowExpr) exprNode()  {}

// BinaryOp is `Left Op Right` for the arithmetic, string-concat,
// comparison, and boolean operators v0 recognises.
//
// M0073-0003: Op is now an OpCode int8 enum (was string).
// Switching on OpCode produces a jump-table dispatch in the
// hot path (`evalBinary`); the per-row string-compare cost
// flagged by Q5's M0072-final pprof is gone.
type BinaryOp struct {
	pos   int
	Op    OpCode
	Left  Expr
	Right Expr
}

func (e *BinaryOp) Pos() int { return e.pos }
func (*BinaryOp) exprNode()  {}

// UnaryOp is `Op Operand`. v0 uses it for `-x`, `+x`, and `NOT x`.
//
// M0073-0003: Op is now an OpCode int8 enum (was string).
type UnaryOp struct {
	pos     int
	Op      OpCode
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
//
// The optional `Over *WindowDef` (M0020-0001) field is non-nil when
// the call carried an `OVER (…)` tail — promoting it from a scalar
// or aggregate to a window function. nil for plain calls so every
// existing parser/planner test path stays byte-for-byte unchanged.
type FuncCall struct {
	pos      int
	Name     ObjectName
	Args     []Expr
	Star     bool // count(*)-style star argument
	Distinct bool // DISTINCT inside the arg list
	Over     *WindowDef
	// Filter is the optional FILTER (WHERE condition) clause (M0097-0007).
	// Non-nil only for aggregate function calls with FILTER.
	Filter Expr
	// OrderBy is the optional ORDER BY within the aggregate argument list
	// (M0097-0007), e.g. string_agg(x, ',' ORDER BY x).
	OrderBy []SortBy
	// WithinGroup holds the sort list from `WITHIN GROUP (ORDER BY ...)`.
	// Non-nil only for ordered-set aggregate functions (percentile_cont,
	// percentile_disc, rank, dense_rank, mode). M0097-0035.
	WithinGroup []SortBy
	// Variadic is a parallel slice to Args. When Variadic[i] is true, the i-th
	// argument was prefixed with the VARIADIC keyword (M0103-0008 probe-
	// survival: libpqrcv fetch_table_list emits
	// `pg_get_publication_tables(VARIADIC array_agg(...))`). The flag is
	// recorded but treated as a no-op marker by callees that expect an array
	// argument; the actual spread-to-varargs semantics is deferred until a
	// caller needs it.
	Variadic []bool
}

func (e *FuncCall) Pos() int { return e.pos }
func (*FuncCall) exprNode()  {}

// WindowDef is the parsed `OVER ( [PARTITION BY exprs]
// [ORDER BY sortlist] )` tail attached to a FuncCall (M0020 step
// 1). Frame clauses (ROWS / RANGE / GROUPS), frame exclusion, and
// named-window references (`OVER win_name`) are deferred to a
// later slice — they're optional in upstream and ROW_NUMBER /
// RANK / LAG / LEAD don't need explicit frames at the SQL surface
// (the executor uses default frames).
type WindowDef struct {
	pos         int
	PartitionBy []Expr
	OrderBy     []SortBy
}

// Pos returns the position of the leading `OVER` keyword.
func (w *WindowDef) Pos() int { return w.pos }

// ArraySubscriptExpr is `expr[index]` — array element access.
type ArraySubscriptExpr struct {
	pos   int
	Base  Expr
	Index Expr
}

func (e *ArraySubscriptExpr) Pos() int { return e.pos }
func (*ArraySubscriptExpr) exprNode()  {}

// ArrayConstructorExpr is ARRAY[e1, e2, ...] — an array constructor
// that evaluates its elements and formats them as a PostgreSQL array
// literal {v1,v2,...}. M0097-0042.
type ArrayConstructorExpr struct {
	pos      int
	Elements []Expr
}

func (e *ArrayConstructorExpr) Pos() int { return e.pos }
func (*ArrayConstructorExpr) exprNode()  {}
