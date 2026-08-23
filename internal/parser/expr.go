package parser

import (
	"strconv"
	"strings"
)

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

// decodeBitStringLit validates and decodes a B'...'/X'...' bit-string
// literal token (Value = marker byte 'b'/'x' + raw source digits, see
// lexBitOrHexString) into its canonical binary-digit text ('0'/'1' chars
// only — a hex nibble expands to 4 bits, MSB first, matching varbit.c's
// bit_in/varbit_in). PG calls bit_in eagerly while building the A_Const
// (parse_node.c transformExprRecurse, T_BitString case) rather than
// deferring to assignment time, so an invalid digit errors immediately
// with ERRCODE_INVALID_TEXT_REPRESENTATION (22P02) and an errposition at
// the literal's start — reproduced here the same way.
//
// The decoded text is wrapped in a plain *StringConst rather than a
// dedicated bit-typed node: goopg's existing string->bit(n)/varbit(n)
// column coercion already reproduces PG's length-mismatch errors
// byte-for-byte for a plain quoted-string literal (verified: `INSERT INTO
// t(bit_col) VALUES ('10')` already raises "bit string length 2 does not
// match type bit(11)"), so reusing that path gets INSERT/SELECT round-trips
// right for free. Known gap (ledgered, M0134-0092): the literal ends up
// typed UNKNOWN/text instead of BITOID, so an untyped context (a bare
// `SELECT b'1010'` with no target column) and bit-typed operator dispatch
// without an explicit cast are not PG-faithful — bit.sql's literal-syntax
// cases don't exercise that gap, only its bit(n)/varbit(n) column cases do.
func decodeBitStringLit(t Token) (Expr, error) {
	marker := t.Value[0]
	body := t.Value[1:]
	if marker == 'b' {
		for i := 0; i < len(body); i++ {
			if body[i] != '0' && body[i] != '1' {
				return nil, &SyntaxError{
					Pos: t.Pos, Raw: true, Code: "22P02",
					Message: strconv.Quote(body[i:i+1]) + " is not a valid binary digit",
				}
			}
		}
		return &StringConst{pos: t.Pos, Value: body}, nil
	}
	var out strings.Builder
	out.Grow(len(body) * 4)
	for i := 0; i < len(body); i++ {
		c := body[i]
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		default:
			return nil, &SyntaxError{
				Pos: t.Pos, Raw: true, Code: "22P02",
				Message: strconv.Quote(body[i:i+1]) + " is not a valid hexadecimal digit",
			}
		}
		for bit := 3; bit >= 0; bit-- {
			if v&(1<<uint(bit)) != 0 {
				out.WriteByte('1')
			} else {
				out.WriteByte('0')
			}
		}
	}
	return &StringConst{pos: t.Pos, Value: out.String()}, nil
}

// PartitionRangeBoundKeyword is the MINVALUE / MAXVALUE keyword in a RANGE
// partition bound (an unbounded edge: -∞ / +∞). It is intentionally a distinct
// node from StringConst: without it the parser collapsed the bare keyword into
// StringConst{Value:"MINVALUE"}, which is byte-identical to the *quoted text
// literal* 'MINVALUE'. That collision silently corrupted both the dumped
// relpartbound (a real text bound 'MINVALUE' dumped as the bare unbounded
// keyword) and value routing (a real text key/bound "MINVALUE" treated as ±∞).
// DU-002 slice 261.
type PartitionRangeBoundKeyword struct {
	pos   int
	IsMax bool // false = MINVALUE (-∞), true = MAXVALUE (+∞)
}

func (e *PartitionRangeBoundKeyword) Pos() int { return e.pos }
func (*PartitionRangeBoundKeyword) exprNode()  {}

// Keyword renders the node as its canonical uppercase SQL keyword.
func (e *PartitionRangeBoundKeyword) Keyword() string {
	if e.IsMax {
		return "MAXVALUE"
	}
	return "MINVALUE"
}

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

// NegatedLiteralSQL renders a unary minus applied DIRECTLY to a numeric literal
// (`-5`, `-3.5`) the way PostgreSQL folds and deparses it, shared by the pg_dump
// deparse twins (catalog.formatExprForAttrdef and executor.defaultExprToSQL).
//
// gram.y's doNegate folds the minus into the literal, producing a negative typed
// Const; ruleutils.c get_const_expr then prints any constant whose text leads
// with '-' as the quoted value plus an explicit `::type` cast, so the dump
// re-parses as a single constant rather than a constant-plus-operator. The cast
// type follows the LITERAL's own type — resolved from the (negated) magnitude the
// way PG's make_const does, NOT from any surrounding column type: an integer
// literal whose negation still fits int4 casts to `integer`, a wider one to
// `bigint`, and a decimal/scientific literal to `numeric`. Empirically vs pg_dump
// 18.3: `a < -5` → `(a < '-5'::integer)`, `c <> -100` on a bigint column →
// `'-100'::integer` (literal type, not column type), `DEFAULT -2147483648` →
// `'-2147483648'::integer` but `-2147483649` → `'-2147483649'::bigint`.
//
// Returns "" when operand is not a bare numeric literal; callers fall back to the
// compound `(- (operand))` form get_rule_expr uses for a non-folded negation.
func NegatedLiteralSQL(operand Expr) string {
	switch lit := operand.(type) {
	case *IntegerConst:
		// PG types the NEGATED value: it fits int4 iff -Value >= math.MinInt32,
		// i.e. Value <= 1<<31 (2147483648 = -INT_MIN); otherwise int8. Value holds
		// the positive magnitude (the parser tags `-N` as UnaryOp over IntegerConst).
		if lit.Value > 1<<31 {
			return "'-" + strconv.FormatInt(lit.Value, 10) + "'::bigint"
		}
		return "'-" + strconv.FormatInt(lit.Value, 10) + "'::integer"
	case *NumericConst:
		return "'-" + lit.Value + "'::numeric"
	}
	return ""
}

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

// GroupingCall is the SQL-standard `GROUPING(expr [, ...])` pseudo-function,
// valid only in the SELECT list / HAVING / ORDER BY of a query whose GROUP
// BY used GROUPING SETS/ROLLUP/CUBE. Its value depends solely on which
// generated grouping set produced the current row — bit i (counting from
// the least-significant bit, rightmost arg) is 1 iff Args[i] is NOT part of
// that set — so it never needs a per-row evaluation: the planner gives each
// distinct call an aggregate OUTPUT COLUMN carrying its bitmask per set, and
// the target list reads that column (M0125-0048; it used to resolve to a
// per-branch integer literal, back when the clause was expanded into a UNION
// ALL). Stored separately from FuncCall (like ExtractExpr) because it isn't a
// real catalog function. M0122-0004.
type GroupingCall struct {
	pos  int
	Args []Expr
}

func (g *GroupingCall) Pos() int { return g.pos }
func (*GroupingCall) exprNode()  {}

// IntervalLit represents the SQL-standard `interval 'N' unit`
// shape used heavily by TPC-H (Q1: `interval '90' day`, Q4:
// `interval '3' month`, Q5/6/12/14: `interval '1' year`, etc.).
// v0 supports the integer-count + single-unit form only (day,
// month, year and their plurals); upstream's full ISO 8601 and
// multi-field forms wait on the type system.
type IntervalLit struct {
	pos   int
	Value string // verbatim numeric body of the literal (e.g. "90")
	Unit  string // lower-cased singular unit: "day"/"month"/"year"/"hour"/...

	// Qualified marks the trailing-qualifier form `interval 'N' <unit>`
	// (Form 1), where the unit is an SQL interval typmod field that
	// truncates the value to that field's granularity — e.g.
	// `interval '1.5' hour` = 01:00:00. The embedded-string form
	// `interval '1.5 hours'` (Form 2) carries no typmod (Qualified=false)
	// and keeps the fractional remainder (01:30:00). See the executor's
	// evalIntervalLit / truncIntervalToUnit.
	Qualified bool

	// HasPrec/Prec carry an explicit fractional-seconds precision from a
	// SECOND(p) typmod field (`interval '1.23456789' second(2)` → 00:00:01.23,
	// `interval '5' minute to second(3)`). Set only when the trailing qualifier
	// ends in SECOND with a parenthesised precision; the executor rounds the
	// micros to 10^(6-p) via AdjustIntervalForTypmod (postgres timestamp.c).
	// unimplemented_feat #5(d-iv, interval typmod range/precision grammar).
	HasPrec bool
	Prec    int

	// PreComputed marks a multi-field / HH:MM:SS embedded body
	// (`interval '1 day 05:00:00'`, `interval '1 year 2 mons 3 days'`) that
	// tryTypedLiteral decoded via ParseIntervalBody (unimplemented_feat
	// #5(b)). When set, PreMonths/PreDays/PreMicros hold the final interval
	// components and Value/Unit/Qualified are unused.
	PreComputed bool
	PreMonths   int32
	PreDays     int32
	PreMicros   int64
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
	Negated     bool // NOT IN
	NotEqualAny bool // != ANY semantics (OR of != comparisons). M0097-0067.
	// AnyOp, when non-zero, indicates `left op ANY|SOME|ALL(...)` with the
	// given operator. Used for non-equality ANY/ALL predicates such as
	// `col ~ ANY(ARRAY[...])` or `col < ALL(SELECT ...)`.
	AnyOp OpCode
	// AllOp selects ALL (AND of the per-element comparison) instead of the
	// default ANY/SOME (OR) semantics when AnyOp is set. M0122-0004.
	AllOp    bool
	Subquery *SelectStmt // populated for IN/ANY/ALL (subquery)
	List     []Expr      // populated for IN/ANY/ALL (val_list / array)
}

func (e *InExpr) Pos() int { return e.pos }
func (*InExpr) exprNode()  {}

// LikeEscapePattern wraps a LIKE/ILIKE pattern together with its ESCAPE
// clause operand. It implements Expr and appears ONLY as the Right operand
// of a BinaryOp{Op: OpLike/OpNotLike/OpILike/OpNotILike} when an ESCAPE
// clause was present in the source; ordinary LIKE (no ESCAPE) still puts
// the bare pattern expr directly in Right, so this is fully backward
// compatible with every other BinaryOp use. M0134-0070.
//
// PG oracle: postgres/src/backend/parser/gram.y (a_expr LIKE a_expr ESCAPE
// a_expr productions).
type LikeEscapePattern struct {
	pos     int
	Pattern Expr
	Escape  Expr
}

func (n *LikeEscapePattern) Pos() int { return n.pos }
func (*LikeEscapePattern) exprNode()  {}

// SimilarToPattern is a full `expr SIMILAR TO pattern [ESCAPE escape]`
// expression, used only when Pattern and/or Escape are not literal at parse
// time (buildSimilarTo could not constant-fold them into the usual
// BinaryOp{Op: OpRegexMatch/OpRegexNoMatch} shape — see
// internal/parser/select.go buildSimilarTo). Escape is nil when the source
// had no ESCAPE clause (runtime evaluation must then default to
// similarto.DefaultEscape). Not exercised by strings.sql — see M0134-0070
// report for the coverage note. PG oracle: postgres/src/backend/parser/
// gram.y:15080-15115 (similar_to_escape_1/2 desugar),
// postgres/src/backend/utils/adt/regexp.c:768-1063 (similar_escape_internal).
type SimilarToPattern struct {
	pos     int
	Left    Expr
	Pattern Expr
	Escape  Expr // nil = no ESCAPE clause (default backslash escape)
	Negate  bool // NOT SIMILAR TO
}

func (n *SimilarToPattern) Pos() int { return n.pos }
func (*SimilarToPattern) exprNode()  {}

// ExistsExpr is `EXISTS (subquery)` and its NOT variant. The
// executor opens the inner plan, asks for the first row, and
// returns true (or false for NOT EXISTS) iff at least one row
// is produced. The body of the subquery is otherwise ignored
// — output columns are discarded; upstream-aligned semantics.
type ExistsExpr struct {
	pos      int
	Negated  bool // NOT EXISTS
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

// CollateExpr represents `expr COLLATE collation_name`. goopg doesn't enforce
// collations at runtime, but the node preserves the collation name so the
// planner can detect explicit-collation mismatches (e.g. "C" vs "POSIX") in
// WITHIN GROUP ORDER BY validation. M0097-0127.
type CollateExpr struct {
	pos           int
	Operand       Expr
	CollationName string
}

func (e *CollateExpr) Pos() int { return e.pos }
func (*CollateExpr) exprNode()  {}

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
	// WithinGroupPos is the byte offset of the `within` keyword of the
	// WITHIN GROUP clause, or 0 when absent. PG reports the caret for
	// "cannot use multiple ORDER BY clauses with WITHIN GROUP" at this
	// keyword (gram.y func_expr: parser_errposition(@2)); the funcall head
	// is wrong. M0134-0001 P3b.
	WithinGroupPos int
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

// FrameMode identifies which of ROWS/RANGE/GROUPS an explicit window
// frame clause uses (SQL:2003 <window frame units>). WindowDef.Frame
// stays nil when no explicit frame clause was written — the default
// frame (RANGE UNBOUNDED PRECEDING, peer-inclusive) applies.
type FrameMode int

const (
	FrameModeRows FrameMode = iota
	FrameModeRange
	FrameModeGroups
)

// FrameBoundKind identifies one endpoint of a window frame
// (SQL:2003 <window frame bound>). Ordered UNBOUNDED PRECEDING <
// OFFSET PRECEDING < CURRENT ROW < OFFSET FOLLOWING < UNBOUNDED
// FOLLOWING to mirror gram.y's bound-ordering validation.
type FrameBoundKind int

const (
	FrameBoundUnboundedPreceding FrameBoundKind = iota
	FrameBoundOffsetPreceding
	FrameBoundCurrentRow
	FrameBoundOffsetFollowing
	FrameBoundUnboundedFollowing
)

// FrameExclusion identifies an optional EXCLUDE clause
// (SQL:2003 <window frame exclusion>). FrameExcludeNone covers both
// an omitted clause and the explicit EXCLUDE NO OTHERS spelling —
// both mean "exclude nothing".
type FrameExclusion int

const (
	FrameExcludeNone FrameExclusion = iota
	FrameExcludeCurrentRow
	FrameExcludeGroup
	FrameExcludeTies
)

// WindowFrame is the parsed `{ROWS|RANGE|GROUPS} BETWEEN bound AND
// bound [EXCLUDE ...]` frame clause of a window definition. Only
// FrameModeRows reaches the executor (M0122-0004 frame-clause slice);
// RANGE/GROUPS parse structurally but are rejected by the analyzer
// (0A000) — see docs/design/0020-0001-window-parser-and-ast.md.
type WindowFrame struct {
	Mode        FrameMode
	StartKind   FrameBoundKind
	StartOffset Expr // non-nil only for FrameBoundOffsetPreceding/Following
	EndKind     FrameBoundKind
	EndOffset   Expr // non-nil only for FrameBoundOffsetPreceding/Following
	Exclusion   FrameExclusion
	// HasBetween is true for the `BETWEEN bound AND bound` spelling
	// and false for the single-bound `frame_bound` spelling (where
	// EndKind defaults to FrameBoundCurrentRow). The analyzer's
	// bound-ordering validation needs this to reproduce gram.y's
	// distinct wording for the two forms (frame_extent productions).
	HasBetween bool
}

// WindowDef is the parsed `OVER ( [PARTITION BY exprs]
// [ORDER BY sortlist] [frame clause] )` tail attached to a FuncCall
// (M0020 step 1; frame clause added M0122-0004).
//
// RefName is set instead of PartitionBy/OrderBy/Frame for the bare
// `OVER window_name` form (M0020 named-window slice); the analyzer
// resolves it against the enclosing SelectStmt.WindowClause and
// copies the definition's PartitionBy/OrderBy/Frame in, so every
// downstream consumer (planner, executor) only ever sees the
// resolved form and needs no RefName-awareness of its own.
type WindowDef struct {
	pos         int
	PartitionBy []Expr
	OrderBy     []SortBy
	Frame       *WindowFrame
	RefName     string
	// IsBareRef is true only for the parenthesis-free `OVER window_name`
	// form (gram.y's `over_clause: OVER ColId`), which is a transparent
	// alias to that WINDOW-clause entry — PartitionBy/OrderBy/Frame are
	// always empty here and the referenced window's fields are copied
	// wholesale with no override validation. It is false for the
	// parenthesized `OVER (window_name ...)` combining form (gram.y's
	// `over_clause: OVER window_specification` with a leading
	// opt_existing_window_name) and for every `WINDOW name AS (...)`
	// clause entry, both of which go through mergeWindowDef's SQL:2008
	// override rules even when they add no clauses of their own.
	IsBareRef bool
}

// Pos returns the position of the leading `OVER` keyword.
func (w *WindowDef) Pos() int { return w.pos }

// ArraySubscriptExpr is `expr[index]` — array element access — or, when
// IsSlice is set, `expr[lower:upper]` — array slice access (M0134-0079).
// PG's grammar (`indirection_el`, gram.y) allows either bound to be
// omitted (`expr[:upper]`, `expr[lower:]`, `expr[:]`), in which case the
// corresponding field is nil and the slice defaults to that dimension's
// actual lower/upper bound. Index (renamed Lower for slice reads) always
// holds the sole bound for plain element access.
type ArraySubscriptExpr struct {
	pos     int
	Base    Expr
	Index   Expr
	IsSlice bool
	Upper   Expr
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

// ArraySubqueryExpr represents ARRAY(SELECT ...) — a subquery whose single-column
// results are collected into a PostgreSQL array. M0097-0127.
type ArraySubqueryExpr struct {
	pos   int
	Inner *SelectStmt
}

func (e *ArraySubqueryExpr) Pos() int { return e.pos }
func (*ArraySubqueryExpr) exprNode()  {}

func (e *ArrayConstructorExpr) Pos() int { return e.pos }
func (*ArrayConstructorExpr) exprNode()  {}
