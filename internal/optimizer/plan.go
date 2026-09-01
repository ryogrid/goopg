// Package planner translates parser.Stmt trees into goopg plan
// nodes. Scope and growth path are documented in
// docs/design/0011-planner.md.
package optimizer

import (
	"strconv"
	"strings"
	"time"

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
//
// SourceTableIdx (M0071-0009) identifies which FROM-clause range
// binding produced this column. The binder assigns 1..N (one per
// base-table FROM-binding within a single planning scope) when
// the column originates from a base-table scan. Zero (the Go
// zero-value) means "unknown / derived" — used for Project
// targets that aren't pure ColumnRef pass-throughs, aggregate
// outputs, computed expressions, and subquery-derived columns.
// Used by `findColumnIndexByNameAndSource` and `predRebind` to
// disambiguate self-joins (e.g. Q21's three lineitem aliases
// l1/l2/l3 sharing `l_suppkey`) when MHJ OID-sorts the schema
// and Name-only disambiguation fails.
type SchemaColumn struct {
	Name           string
	Type           catalog.Type
	SourceTableIdx int16
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

// TableOidExpr is the per-binding `tableoid` system column for a
// non-partitioned base relation. resolveColumnRefAt synthesises this
// when the binding is bound to a single concrete table whose OID is
// known at plan time (i.e. the `tableoid::regclass` value is a
// constant). Partition-aware unions instead emit a real per-leaf
// `tableoid` column via the per-leaf Project wrapping (see
// planFromTable's partition arm + rangeBinding.tableOidColIdx) so the
// outer `tableoid` reference becomes an ordinary ColumnRef into the
// trailing slot. (M0100-0005y)
type TableOidExpr struct {
	pos      int
	TableOID uint32
}

func (e *TableOidExpr) Pos() int { return e.pos }
func (*TableOidExpr) exprNode()  {}

// MergeActionExpr evaluates to the action string ('INSERT','UPDATE','DELETE')
// within a MERGE RETURNING expression. The executor reads ctx.MergeAction.
// M0100-0007.
type MergeActionExpr struct{ pos int }

func (e *MergeActionExpr) Pos() int { return e.pos }
func (*MergeActionExpr) exprNode()  {}

// MergeWholeRowRef evaluates to the composite row value for the old or new
// target row in a MERGE RETURNING clause. Returns a NULL datum when the row
// is absent (old is absent for INSERT; new is absent for DELETE), rather than
// a non-null composite with all-null fields. M0100-0007.
type MergeWholeRowRef struct {
	pos   int
	IsOld bool // true = old (pre-action) row; false = new (post-action) row
}

func (e *MergeWholeRowRef) Pos() int { return e.pos }
func (*MergeWholeRowRef) exprNode()  {}

// CTIDExpr is the per-row `ctid` system column for a heap scan.
// The block/offset pair is injected at runtime by seqScanOp via
// MaterializedSlot.hasCTID. M0097-0038.
type CTIDExpr struct {
	pos int
}

func (e *CTIDExpr) Pos() int { return e.pos }
func (*CTIDExpr) exprNode()  {}

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
//
// M0066-0002: `Cached*` fields hold a once-parsed result so
// repeated evaluations of the same literal in a hot loop (e.g.
// Q5's `o_orderdate >= date '1994-01-01'` evaluated per row)
// avoid the `time.Parse` cost — pprof showed `time.parse` at
// 10.5 % cumulative CPU on Q5 SF=1. Each plan tree is owned by
// one query so no cross-query sharing; the operator pipeline
// for a query is single-threaded so no intra-query race. The
// planner leaves these zero; the executor populates on first
// eval.
type TypedStringLit struct {
	pos   int
	Type  string
	Value string

	// Cached parsed values. CacheValid signals which fields are
	// populated (so the zero `time.Time` is distinguishable from
	// an unparsed value).
	CacheValid bool
	CachedTime time.Time
}

func (e *TypedStringLit) Pos() int { return e.pos }
func (*TypedStringLit) exprNode()  {}

// IntervalLit mirrors parser.IntervalLit.
//
// M0066-0002: `Cached*` fields hold the parsed integer count
// from `Value` so per-row evaluation doesn't repeat the
// `strconv.ParseInt` cost (Q5's `interval '1' year` evaluated
// per orders row pre-cache showed evalIntervalLit at ~2 % cum).
type IntervalLit struct {
	pos   int
	Value string
	Unit  string

	// Qualified marks the trailing-qualifier form `interval 'N' <unit>`
	// (an SQL interval typmod field that truncates below its granularity);
	// see internal/executor/expr.go evalIntervalLit / truncIntervalToUnit.
	Qualified bool

	// HasPrec/Prec carry an explicit fractional-seconds precision from a
	// SECOND(p) typmod field; the executor rounds the micros to 10^(6-p) after
	// the range truncation (see evalIntervalLit). unimplemented_feat #5(d-iv).
	HasPrec bool
	Prec    int

	// PreComputed marks a multi-field / HH:MM:SS embedded body
	// (`interval '1 day 05:00:00'`, `interval '1 year 2 mons 3 days'`) that
	// the parser already decoded via parser.ParseIntervalBody
	// (unimplemented_feat #5(b)). When set, PreMonths/PreDays/PreMicros hold
	// the final components and Value/Unit/Qualified are unused.
	PreComputed bool
	PreMonths   int32
	PreDays     int32
	PreMicros   int64

	// Cached parsed interval components from `Value`+`Unit`.
	// CacheValid signals populated. Widened from a single int32 count
	// (M0066-0002) to the full months/days/micros triple so fractional
	// magnitudes (`interval '1.5 hours'`) — whose spill into smaller
	// units cannot be represented by one integer count — are also
	// cached (see internal/executor/expr.go evalIntervalLit).
	CacheValid   bool
	CachedMonths int32
	CachedDays   int32
	CachedMicros int64
}

func (e *IntervalLit) Pos() int { return e.pos }
func (*IntervalLit) exprNode()  {}

// ExtractExpr mirrors parser.ExtractExpr. Field is the
// lower-cased calendar component the executor switches on.
type ExtractExpr struct {
	pos    int
	Field  string
	Source Expr
	// SourceTypeName carries the declared type of Source (e.g. "time", "timestamp").
	// The executor uses it to reject fields that are invalid for time-only types. M0097-0004.
	SourceTypeName string
}

func (e *ExtractExpr) Pos() int { return e.pos }
func (*ExtractExpr) exprNode()  {}

// InExpr mirrors parser.InExpr after subquery / value-list
// resolution. Either Plan or List is non-nil. v0 supports the
// uncorrelated form only — the executor evaluates the inner
// once, builds a set, then probes per outer row.
//
// IsNonCorrelated is true when the inner Plan contains zero
// OuterColumnRef nodes; the executor uses a constant cache
// key in that case so the SubPlan executes only once across
// all outer rows. (M0058-0001.)
type InExpr struct {
	pos     int
	Operand Expr
	Negated bool
	// NotEqualAny marks `x != ANY(list)` semantics: true if operand is
	// not equal to at least one element in List (OR of != comparisons).
	// Distinct from Negated which means NOT IN (AND of != comparisons).
	// M0097-0067.
	NotEqualAny bool
	// AnyOp, when non-zero, indicates `left op ANY|SOME|ALL(...)` with the
	// given operator. Used for non-equality ANY/ALL predicates such as
	// `col ~ ANY(ARRAY[...])` or `col < ALL(SELECT ...)`.
	AnyOp parser.OpCode
	// AllOp selects ALL (AND) instead of ANY/SOME (OR) semantics when AnyOp
	// is set. M0122-0004.
	AllOp           bool
	Plan            Node // populated when the source is a subquery
	List            []Expr
	IsNonCorrelated bool
	// ParParam/Args: PARAM_EXEC lowering (D4.1, subplan_lower.go).
	// Args[i] is evaluated against the current outer row and written to
	// ParamExec slot ParParam[i] before Plan runs; Plan then reads the
	// slots via ExecParamRef instead of walking ctx.OuterRows. Empty =
	// non-correlated, or an unlowered shape still on the stack path.
	ParParam []int
	Args     []Expr
}

func (e *InExpr) Pos() int { return e.pos }
func (*InExpr) exprNode()  {}

// ExistsExpr mirrors parser.ExistsExpr. The executor opens the
// inner plan, asks for one row, and reports the bool. NOT
// EXISTS is the same path with the result negated.
//
// IsNonCorrelated is true when Plan contains zero
// OuterColumnRef nodes — see InExpr for the cache implication.
type ExistsExpr struct {
	pos             int
	Negated         bool
	Plan            Node
	IsNonCorrelated bool
	// ParParam/Args: see InExpr — PARAM_EXEC lowering (D4.1).
	ParParam []int
	Args     []Expr
}

func (e *ExistsExpr) Pos() int { return e.pos }
func (*ExistsExpr) exprNode()  {}

// IsNullExpr mirrors parser.IsNullExpr after the operand has been planned.
// Negated=true for IS NOT NULL. Always returns a boolean (never NULL itself).
type IsNullExpr struct {
	pos     int
	Operand Expr
	Negated bool
}

func (e *IsNullExpr) Pos() int { return e.pos }
func (*IsNullExpr) exprNode()  {}

// IsBoolExpr mirrors parser.IsBoolExpr after the operand has been planned.
// IS [NOT] TRUE/FALSE/UNKNOWN. Always returns boolean. M0097-0003.
type IsBoolExpr struct {
	pos       int
	Operand   Expr
	TestTrue  bool
	TestFalse bool
	Negated   bool
}

func (e *IsBoolExpr) Pos() int { return e.pos }
func (*IsBoolExpr) exprNode()  {}

// IsDistinctFromExpr mirrors parser.IsDistinctFromExpr after both operands
// have been planned. Negated=true for IS NOT DISTINCT FROM.
type IsDistinctFromExpr struct {
	pos     int
	Left    Expr
	Right   Expr
	Negated bool
}

func (e *IsDistinctFromExpr) Pos() int { return e.pos }
func (*IsDistinctFromExpr) exprNode()  {}

// SubqueryExpr mirrors parser.SubqueryExpr after the inner
// SELECT has been planned. The executor opens / drains /
// closes Plan once at evaluation time and returns the single
// cell as the expression's value. Multi-row / multi-column
// subqueries trigger a runtime error.
//
// IsNonCorrelated is true when Plan contains zero
// OuterColumnRef nodes — see InExpr for the cache implication.
type SubqueryExpr struct {
	pos             int
	Plan            Node
	IsNonCorrelated bool
	// ParParam/Args: see InExpr — PARAM_EXEC lowering (D4.1).
	ParParam []int
	Args     []Expr
}

// ArraySubqueryExpr represents ARRAY(SELECT ...) — collects all rows of the
// inner plan (must be single-column) into a PostgreSQL text-array. M0097-0127.
type ArraySubqueryExpr struct {
	pos             int
	Plan            Node
	IsNonCorrelated bool
}

// CollateExpr wraps an expression with an explicit collation name for
// mismatch detection in WITHIN GROUP ORDER BY validation. M0097-0127.
type CollateExpr struct {
	pos           int
	Operand       Expr
	CollationName string
}

func (e *CollateExpr) Pos() int { return e.pos }
func (*CollateExpr) exprNode()  {}

func (e *ArraySubqueryExpr) Pos() int { return e.pos }
func (*ArraySubqueryExpr) exprNode()  {}

func (e *SubqueryExpr) Pos() int { return e.pos }
func (*SubqueryExpr) exprNode()  {}

// MultiAssignSubqRow represents a multi-column sub-SELECT used on the RHS of
// a tuple SET assignment: SET (a, b) = (SELECT x, y FROM …).
// A single MultiAssignSubqRow is shared by all MultiAssignSubqElem expressions
// for the same assignment; the executor evaluates the subquery once per row and
// caches the result tuple in Context.MultiAssignSubqCache keyed by this pointer.
type MultiAssignSubqRow struct {
	pos             int
	Plan            Node
	NCols           int // expected number of output columns
	IsNonCorrelated bool
}

func (e *MultiAssignSubqRow) Pos() int { return e.pos }
func (*MultiAssignSubqRow) exprNode()  {}

// MultiAssignSubqElem extracts one column from a MultiAssignSubqRow result.
type MultiAssignSubqElem struct {
	pos    int
	Row    *MultiAssignSubqRow
	ColIdx int // 0-based index into the subquery result row
}

func (e *MultiAssignSubqElem) Pos() int { return e.pos }
func (*MultiAssignSubqElem) exprNode()  {}

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
//
// SourceTableIdx (M0071-0009) carries the source-table identity
// that the binder resolved this reference against, so downstream
// rewrites that rebuild the row layout (MHJ OID-sort, Join key
// rebind) can disambiguate by source when Name alone is
// ambiguous. Zero (the Go zero-value) means "unknown / derived";
// in that case rebind helpers fall back to Name-only resolution.
type ColumnRef struct {
	pos            int
	Index          int
	Name           string // resolved column name (for diagnostics)
	Type           catalog.Type
	SourceTableIdx int16
}

func (e *ColumnRef) Pos() int { return e.pos }
func (*ColumnRef) exprNode()  {}

// OuterColumnRef refers to a column in an enclosing query's
// row, set by the executor's outer-row stack before opening a
// correlated subquery. Level is 1-based (1 = immediate parent
// scope) — matches upstream's Var.varlevelsup. Only emitted
// by the planner when a ColumnRef in a subquery resolves up
// the parent chain instead of locally.
//
// SourceTableIdx mirrors `ColumnRef.SourceTableIdx` for the
// outer-scope binding, used by `unnestExistsExpr.resolveOuterIdx`
// (M0071-0009) to disambiguate self-join outer references when
// the Anti-join's residual `l3.l_suppkey <> l1.l_suppkey`
// otherwise falls back to the original (potentially stale) Index.
type OuterColumnRef struct {
	pos            int
	Level          int
	Index          int
	Name           string
	Type           catalog.Type
	SourceTableIdx int16
}

func (e *OuterColumnRef) Pos() int { return e.pos }
func (*OuterColumnRef) exprNode()  {}

// unnestParam records one correlation pair extracted from an
// unnestable subquery: the outer-scope column reference and
// the subquery-side column it is equijoined with.
type unnestParam struct {
	OuterRef *OuterColumnRef
	SubCol   *ColumnRef
}

// ParamRef passes through a bind-parameter placeholder. The executor
// supplies values at execute time.
type ParamRef struct {
	pos    int
	Number int
}

func (e *ParamRef) Pos() int { return e.pos }
func (*ParamRef) exprNode()  {}

// ExecParamRef reads a PARAM_EXEC-style parameter slot
// (Context.ParamExec[ID]) filled by an enclosing SubPlan eval site just
// before the inner plan runs. It is the plan-internal correlation
// parameter of D4.1 (design bundle correlated-subquery-planning), the
// analog of upstream's PARAM_EXEC Params produced by
// SS_replace_correlation_vars — and deliberately a separate node from
// ParamRef, which is the PARAM_EXTERN client bind-parameter side of the
// same paramkind split PG makes.
//
// IDs come from one flat per-statement slot space (subplan_lower.go), so
// nesting levels cannot collide and evaluation is position-independent:
// unlike OuterColumnRef there is no lexical-scope stack walk.
type ExecParamRef struct {
	pos  int
	ID   int
	Type catalog.Type
}

func (e *ExecParamRef) Pos() int { return e.pos }
func (*ExecParamRef) exprNode()  {}

// BinaryOp — Left Op Right.
//
// M0073-0003: Op is now parser.OpCode (int8 enum), was
// string. Mirror of parser.BinaryOp's field type.
// ResultType is non-empty for arithmetic with typed result (e.g., "int2").
// M0097-0003.
type BinaryOp struct {
	pos        int
	Op         parser.OpCode
	Left       Expr
	Right      Expr
	ResultType string // non-empty for arithmetic with typed result (e.g., "int2")
}

// LikeEscapePattern is the optimizer-side mirror of parser.LikeEscapePattern.
// It appears ONLY as the Right operand of a BinaryOp{Op: OpLike/OpNotLike/
// OpILike/OpNotILike} when the source had a LIKE...ESCAPE clause; the
// executor evaluates Pattern and Escape and rewrites the pattern into
// PostgreSQL's standard backslash-escape convention before the normal LIKE
// match runs (PG oracle: postgres/src/backend/utils/adt/like_match.c:392-486
// do_like_escape). M0134-0070.
type LikeEscapePattern struct {
	pos     int
	Pattern Expr
	Escape  Expr
}

func (e *LikeEscapePattern) Pos() int { return e.pos }
func (*LikeEscapePattern) exprNode()  {}

func (e *BinaryOp) Pos() int { return e.pos }
func (*BinaryOp) exprNode()  {}

// CastExpr preserves the cast target type for type inference and runtime coercion.
// v0's planner previously discarded cast targets (no-op); CastExpr retains the
// TargetType so exprType() can return the declared type and the executor can
// coerce values at runtime (e.g., string→bool, string→int2 with range check).
// M0097-0003.
type CastExpr struct {
	pos        int
	Operand    Expr
	TargetType string // normalized lowercase type name (e.g., "int2", "bool")
	SourceType string // operand's declared type — used by executor to pick rounding mode. M0097-0003.
	Typmod     int64  // optional precision/scale modifier (e.g., 4 for ::timetz(4)); 0 means no typmod.
}

func (e *CastExpr) Pos() int { return e.pos }
func (*CastExpr) exprNode()  {}

// NewCastExprFromParser builds the resolved CastExpr for a parser-level cast
// whose operand has already been lowered by a caller OUTSIDE this package —
// today PL/pgSQL expression lowering in executor/plpgsql_runtime.go, which
// cannot reach exprType/encodeTypmod. review/260831-2 ES-7: that lowerer used
// to return the bare operand for `*parser.CastExpr`, silently DROPPING every
// cast written inside a PL/pgSQL expression (`i::text`, `n::numeric`), so the
// expression evaluated at the operand's own type.
func NewCastExprFromParser(x *parser.CastExpr, operand Expr) *CastExpr {
	typeName := strings.ToLower(x.Type.Name)
	return &CastExpr{
		pos:        x.Pos(),
		Operand:    operand,
		TargetType: typeName,
		SourceType: exprType(operand).Name,
		Typmod:     encodeTypmod(typeName, x.Typmods),
	}
}

// RowExpr is a resolved row constructor `(a, b, c)`. At evaluation time it
// produces a text composite representation `(v1,v2,...,vN)`. Used for
// whole-row variable refs and row-constructor IN comparisons. M0097-0020.
type RowExpr struct {
	pos   int
	Elems []Expr
	Types []catalog.Type
}

func (e *RowExpr) Pos() int { return e.pos }
func (*RowExpr) exprNode()  {}

// UnaryOp — Op Operand.
//
// M0073-0003: Op is parser.OpCode (int8 enum), was string.
type UnaryOp struct {
	pos     int
	Op      parser.OpCode
	Operand Expr
}

func (e *UnaryOp) Pos() int { return e.pos }
func (*UnaryOp) exprNode()  {}

// FuncCall — identified by its planner-resolved name. Argument
// expressions live under Args; v0 doesn't yet resolve overloads.
type FuncCall struct {
	pos        int
	Name       string
	Args       []Expr
	Star       bool
	Variadic   bool   // true when args were expanded from VARIADIC array syntax
	ReturnType string // return type for user-defined functions; empty for unknown
	// ArgWidth is the resolved overload width for width-sensitive builtins
	// such as to_hex; empty means default int4.
	ArgWidth string
}

func (e *FuncCall) Pos() int { return e.pos }
func (*FuncCall) exprNode()  {}

// SeqScan — full heap scan of a single relation.
// TableSampleSpec is the planner-side form of a TABLESAMPLE clause: the
// parser's RangeTableSample with its argument expressions RESOLVED against the
// scan's scope, so the executor can evaluate them with the ordinary evalExpr
// rather than reaching back into the parser AST. M0134-0175.
//
// Method is NOT validated here. Upstream validates it in the parser
// (parse_tablesample_method, parse_clause.c:929) and goopg validates it in the
// executor; either way the message is 42704 "tablesample method X does not
// exist" rather than a syntax error, which is what the oracle's caret under
// the method name requires.
type TableSampleSpec struct {
	pos        int // the method name's offset, for the error caret
	Method     string
	Args       []Expr
	Repeatable Expr
}

// Pos returns the method name's byte offset — the position upstream stamps
// (gram.y:14001 `n->location = @2`) and the one the oracle's caret points at.
func (t *TableSampleSpec) Pos() int { return t.pos }

type SeqScan struct {
	// searchedTree: a one-relation search root is a bare scan (searchedtree.go).
	searchedTree
	pos    int
	Table  *catalog.Table
	Alias  string // FROM-clause alias; empty when not specified
	schema Schema
	// TableSample carries the FROM item's TABLESAMPLE clause (M0134-0175),
	// nil for an ordinary scan. Upstream builds a distinct SampleScan plan
	// node (nodeSamplescan.c) whose only structural difference from SeqScan
	// is the sampler callback pair; goopg keeps one scan node and switches on
	// this field, so every existing SeqScan consumer (cost, EXPLAIN naming,
	// parallel-scan claiming) keeps working unchanged. EXPLAIN renders
	// "Sample Scan on t" instead of "Seq Scan on t" when it is set.
	TableSample *TableSampleSpec
	// EstRelRows is the relation-size fallback's row estimate for Table,
	// stamped once at plan-build time and 0 when it did not apply (the
	// GOOPG_RELSIZE_FALLBACK flag is off, the relation is ANALYZEd, or no
	// live block count was available). EstimateRows reads it only when
	// TableStats.RowCount is absent.
	//
	// Stamped rather than computed on demand for two reasons. It mirrors
	// PostgreSQL, which resolves relation size ONCE in get_relation_info and
	// stores it in RelOptInfo.pages/.tuples rather than re-reading the smgr
	// per cost call. And EstimateRows takes only a Node — it is called from
	// the executor's EXPLAIN as well as from the planner — so there is no
	// catalog in scope at the point of use, and threading one through a
	// package-level variable would leak between concurrently planning
	// sessions the way planParent already can.
	//
	// Consequence worth knowing: a cached plan carries the block count that
	// was live when it was planned. PostgreSQL has the same exposure and
	// answers it with plan invalidation, which goopg does not have yet — see
	// the deferral ledger row for M0125-0003.
	EstRelRows int64
	// SmallDim is the small-dimension property of Table, derived from its size
	// by `smallDimensionTag` and stamped here at plan-build time for the same
	// reason EstRelRows is (see above: the consumers take a Node and have no
	// catalog in scope). Before M0125-0043 this was a name lookup living on
	// `catalog.Table.SmallDimension`; see
	// docs/design/0125-0043-smalldimension-name-tag-extinction.md.
	SmallDim bool
	// UniqueKeys is Table's uniqueness evidence: the column-name list of every
	// UNIQUE index on the relation, stamped at plan-build time by
	// `uniqueKeyColumnSets` (joinkeyproof.go).
	//
	// It exists for the same reason EstRelRows and SmallDim do, and the reason
	// is sharper here. `estimateJoin` is the only place goopg can reproduce
	// `get_foreign_key_join_selectivity` (costsize.c:5651) on the legacy
	// planner, and it takes a bare `Node` — no catalog — because EXPLAIN in
	// the executor calls it too. The index LIST is the one piece of the
	// evidence that is not already reachable from the node: raw tuple counts
	// come from `Table.Stats.RowCount` and declared FKs from
	// `Table.ForeignKeys`, but a table's indexes live only in the catalog, and
	// resolving them there per estimate call would additionally reintroduce
	// the dbOid hazard (a bare `InMemory` answers for `DefaultDBOid` whatever
	// database is active, so a uniqueness proof could fire off another
	// database's index — cost-model/14 §2). Stamping through the planner's own
	// `cat`, once, at the site that already stamps SmallDim, settles both.
	//
	// Same cached-plan exposure as EstRelRows: a plan carries the index set
	// that was live when it was planned. M0127-P5.6-f.
	UniqueKeys [][]string
	// LockParentOID, when non-zero, is the OID of a partitioned parent that was
	// expanded into this leaf scan. Scanning a partitioned table THROUGH the
	// parent takes AccessShare on the parent relation too (PostgreSQL locks the
	// whole hierarchy from the queried root), so a concurrent AccessExclusive
	// holder on the parent (e.g. DROP of a partition pending detach, which grabs
	// the parent lock) blocks the scan. Zero for a leaf scanned directly.
	// M0118-0008 (detach-partition-concurrently-3).
	LockParentOID uint32
	// SkipIfVanished is set on a SeqScan produced by expanding an inheritance
	// parent into its children. Such a child is identified at plan time but
	// locked only when the scan opens; if a concurrent transaction committed a
	// DROP of that child while this scan waited on its lock, the child relation
	// is gone and must be skipped (zero rows) rather than erroring — mirroring
	// PostgreSQL's try_table_open → NULL during inheritance expansion. A
	// directly-scanned relation never sets this (a plain SELECT on a dropped
	// table still errors "does not exist"). M0118-0008 (alter-table-4 perm 3:
	// `DROP TABLE c1` concurrent with `SELECT SUM(a) FROM p`).
	SkipIfVanished bool
	// InheritParentOID, when non-zero, is the OID of the inheritance parent this
	// child scan was expanded from. After the scan acquires the child's lock
	// (i.e. once any concurrent ALTER on the child has committed), the child's
	// column types are re-validated against the parent's — a column whose type no
	// longer matches the parent's raises "attribute %s of relation %s does not
	// match parent's type", mirroring PostgreSQL's make_inh_translation_list
	// (optimizer/util/appendinfo.c). Set together with SkipIfVanished on every
	// inheritance-child scan. M0118-0008 (alter-table-4 perm 4: concurrent
	// `ALTER TABLE c1 ALTER COLUMN a TYPE float`).
	InheritParentOID uint32
	// PrivilegeCheckRole / PrivilegeCheckRoleSet override which role's SELECT
	// grant the executor checks against Table: set by tagViewOwnerScans when
	// this scan sits inside an inlined, non-security_invoker view (PostgreSQL
	// runs a view's underlying-table reads as the view owner, not the
	// querying role). Unset (PrivilegeCheckRoleSet == false) means "use the
	// querying session's own role", the direct-table-scan default. M0122-0008
	// (view-owner privilege gap).
	PrivilegeCheckRole    string
	PrivilegeCheckRoleSet bool
	// Parallel mirrors PostgreSQL's Plan.parallel_aware: true when this scan
	// was chosen as the worker-read driving scan under a Gather/GatherMerge
	// (PG sets it in create_seqscan_path, pathnode.c:996, gated on
	// parallel_workers > 0). Stamped once, at Gather-construction time, by
	// parallel.go's stampParallelScan — NOT inferred at render time (see
	// that function's comment for why). Only affects the "Parallel " EXPLAIN
	// text prefix (operators_explain.go describePlan).
	Parallel bool
}

func (n *SeqScan) Pos() int       { return n.pos }
func (n *SeqScan) Output() Schema { return n.schema }

// IndexScan probes a single-column B-tree index with an equality key
// or a range of keys.
//
// Equality scan (col = key): Key is non-nil; LowKey and HighKey are nil.
// Range scan (lo ≤ col ≤ hi): Key is nil; LowKey and/or HighKey are set.
//   - LowKey non-nil means inclusive lower bound (col >= LowKey).
//   - HighKey non-nil means inclusive upper bound (col <= HighKey).
//   - Either bound may be nil for an open-ended range.
type IndexScan struct {
	// searchedTree: see *SeqScan above (searchedtree.go).
	searchedTree
	pos   int
	Table *catalog.Table
	Alias string // FROM-clause alias; empty when not specified. M0062-0002
	// preserves the alias when an `IndexScan` is substituted for a
	// `SeqScan` (e.g. by `mhj_input_rewrite`); without it,
	// `buildBindingsPosMap` cannot disambiguate self-joins like Q8's
	// `nation n1, nation n2` after one side flips to IndexScan.
	Index *catalog.Index
	Key   Expr   // non-nil for single-column equality scan (LowKey==HighKey implied)
	Keys  []Expr // M0054-0006-followup-Q9-composite: multi-column equality probe.
	// Keys[i] binds Index.Columns[i] in declared order. When non-empty, takes
	// priority over Key. len(Keys) == len(Index.Columns) means a full equality
	// probe (no suffix padding); a shorter prefix is rejected by the planner
	// to keep the executor probe path purely equality-shaped.
	LowKey  Expr // inclusive lower bound for range scan; nil = no lower bound
	HighKey Expr // inclusive upper bound for range scan; nil = no upper bound
	// LowOp / HighOp preserve the ORIGINAL comparison operator in its canonical
	// col-op-key form (see tryRangeIndexScan / flipRangeOp) for the low/high
	// bound. The zero value (OpUnknown) means INCLUSIVE — the historical
	// behavior, and what every caller that does not set the fields gets.
	// OpGt on LowOp / OpLt on HighOp make the executor stop at an EXCLUSIVE
	// bound (M0134-0001 S4 class 8) so the redundant Filter can be dropped.
	LowOp  parser.OpCode
	HighOp parser.OpCode
	// Cond is a residual filter evaluated per HEAP TUPLE the probe returns —
	// PostgreSQL's `Filter:` line on an Index Scan node, which sits alongside
	// `Index Cond:` rather than in a separate node. Its ColumnRefs are in the
	// scan's OWN output coordinates (leaf-local), the same space
	// `IndexOnlyScan.Cond` uses. Nil means no residual filtering.
	//
	// It exists so a relation's local quals have somewhere to live INSIDE the
	// node when the scan is a `NestedLoopIndexJoin.Inner` — that field is typed
	// `*IndexScan` and cannot carry the `*Filter` wrappers the quals otherwise
	// sit in, which is what used to make `addParameterizedIndexPaths` decline
	// every filtered leaf (`scanLeafIsBare`). The alternative it replaces is the
	// D6.3b Q9 blowup: hoisting the quals onto the join residual re-evaluates,
	// once per probed PAIR, a clause the path was costed as applying once per
	// inner row. Here it is applied once per index match, which is the costed
	// semantics.
	//
	// Only the NLI arm sets it. On a plain (unparameterised) index path the
	// leaf's `*Filter` wrappers are rebuilt above the scan by `scanLeafFor`'s
	// rewrapper as before, and Cond stays nil — one predicate evaluated in one
	// place either way, never both.
	Cond   Expr
	schema Schema
	// PrivilegeCheckRole / PrivilegeCheckRoleSet — see SeqScan's field of the
	// same name. M0122-0008 (view-owner privilege gap).
	PrivilegeCheckRole    string
	PrivilegeCheckRoleSet bool
	// SmallDim — see SeqScan's field of the same name (M0125-0043). An
	// IndexScan substituted for a SeqScan by a later pass copies it from the
	// scan it replaces, so promoting a leaf to an index probe never changes
	// the relation's small-dimension answer.
	SmallDim bool
	// UniqueKeys — see SeqScan's field of the same name (M0127-P5.6-f).
	// Copied, like SmallDim, by every pass that substitutes an IndexScan for
	// a SeqScan: the relation's uniqueness evidence is a property of the
	// relation, not of how this plan chose to read it.
	UniqueKeys [][]string
}

func (n *IndexScan) Pos() int       { return n.pos }
func (n *IndexScan) Output() Schema { return n.schema }

// NestedLoopIndexJoin (M0054-0006) joins Outer (any plan node) with
// Inner (an `*IndexScan`) by re-probing the index for each outer row.
// The inner's `Key` / `LowKey` / `HighKey` `Expr` may reference outer-
// row columns via `*ColumnRef` whose `Index` is offset by the outer
// schema width — the executor binds the outer row before each
// `Rescan` so `evalExpr` resolves correctly. Predicate carries any
// non-equi residual conjuncts that the IndexScan probe alone does
// not enforce.
//
// Supported `JoinType` set is INNER and LEFT. For LEFT, when the
// inner probe yields no rows, the operator emits `outer ++
// nullRow(innerWidth)` to preserve outer rows.
type NestedLoopIndexJoin struct {
	// searchedTree: the parameterised arm of createNestLoopPlan (searchedtree.go).
	searchedTree
	pos       int
	Type      JoinType
	Outer     Node
	// Inner is the parameterised probe this join re-executes per outer row.
	//
	// Typed `Node` rather than `*IndexScan` so an `*IndexOnlyScan` can sit here
	// too — PG's Q13/Q16/Q22 all put one on the inner side, and for a SEMI or
	// ANTI join it is provably safe to narrow because the join's schema is the
	// OUTER's alone (see the schema construction in nl_index_join.go: "the inner
	// side is consumed only for matching, never projected").
	//
	// The set of concrete types is CLOSED and is enumerated by
	// `nliInnerProbe` — everything that needs the probe's index or keys goes
	// through it rather than type-asserting locally, so adding a third inner
	// kind is one edit and not a hunt.
	Inner     Node
	Predicate Expr // residual filter applied per joined row
	schema    Schema

	// InnerMemo, when non-nil, interposes a Memoize cache between the
	// join driver and the inner index probe (bundle phase S7 / D5.1).
	// Its Child field ALIASES the same *IndexScan as Inner, so every
	// pass that rewrites Inner in place (remaps, re-resolution) keeps
	// working unmodified; the Memoize node exists for EXPLAIN fidelity
	// and executor construction. Nil = no cache (the common case; the
	// insertion gate requires ANALYZE stats, which are in-memory and
	// restart-lost).
	InnerMemo *Memoize
}

// nliInnerProbe reads the probe fields shared by every legal
// `NestedLoopIndexJoin.Inner`. ok=false means the node is not a probe shape at
// all, which every caller treats as "decline", never as an error.
//
// This exists so the Inner field can be widened without scattering type
// assertions: the switch below is the ONE place that enumerates which node
// kinds may be an inner.
func nliInnerProbe(n Node) (idx *catalog.Index, key Expr, keys []Expr, ok bool) {
	switch x := n.(type) {
	case *IndexScan:
		return x.Index, x.Key, x.Keys, true
	case *IndexOnlyScan:
		return x.Index, x.Key, x.Keys, true
	case *BitmapHeapScan:
		// A keyed bitmap probe is a legal NLI inner: `bitmapHeapScanOp`
		// implements the full `nliInner` interface (BindOuter forwards to the
		// producer, Rescan drops the stale TID set). The probe keys live one
		// node down, on the single `*BitmapIndexScan` producer — an And/Or
		// tree carries several probes and is not a single-key inner, so it
		// stays illegal. Without this arm, `clonePlanReplacingOuter`'s NLI
		// guard declined any subplan whose probe planned as a bitmap and the
		// scalar silently stayed a per-row SubPlan (M0134-0186; TPC-H q02's
		// decorrelation, 588 calls at ~150k rows each).
		if bis, ok := x.Outer.(*BitmapIndexScan); ok {
			return bis.Index, bis.Key, bis.Keys, true
		}
	}
	return nil, nil, nil, false
}

func (n *NestedLoopIndexJoin) Pos() int       { return n.pos }
func (n *NestedLoopIndexJoin) Output() Schema { return n.schema }

// Memoize is a parameterized result cache on the inner side of a
// NestedLoopIndexJoin (bundle phase S7; PG oracle:
// postgres/src/backend/executor/nodeMemoize.c, inserted where
// get_memoize_path fires in optimizer/path/joinpath.c). It caches the
// inner index probe's result rows keyed by the probe parameter values,
// serving repeats without re-scanning.
//
// KeyExprs are the probe-key expressions (they reference OUTER columns
// and are evaluated against the bound outer slot — the same expressions
// the aliased Child IndexScan consumes as Key/Keys). SingleRow marks a
// provably-unique probe (entries complete after the first row, PG's
// `singlerow`). EstEntries is the planner's cache-population estimate
// for initial sizing (cost_memoize_rescan analog); the executor clamps
// by the runtime memory budget.
type Memoize struct {
	pos        int
	Child      *IndexScan
	KeyExprs   []Expr
	SingleRow  bool
	EstEntries int64
}

func (n *Memoize) Pos() int       { return n.pos }
func (n *Memoize) Output() Schema { return n.Child.Output() }

// IndexOnlyScan is a covered index scan (M0046-0004): all projected columns
// come from the B-tree index key, so no heap fetch is needed when the
// visibility map reports ALL_VISIBLE for the target page.
//
// The output schema contains ONLY the projected covered columns (a subset
// of the full table schema). When the VM bit is not set for a page the
// executor falls back to a regular heap fetch.
type IndexOnlyScan struct {
	pos   int
	Table *catalog.Table
	Index *catalog.Index
	Key   Expr
	// Keys mirrors IndexScan.Keys: a full multi-column equality probe
	// (one Expr per Index.Columns entry, in declared order). When set,
	// takes priority over Key. Carries the M0054-0006 composite probe
	// across IOS promotion so multi-column equality stays index-only.
	Keys    []Expr
	LowKey  Expr
	HighKey Expr
	// LowOp / HighOp mirror IndexScan's fields of the same name: the bound's
	// strictness. `OpGt` (low) / `OpLt` (high) make that end of the range
	// EXCLUSIVE; the zero value `OpUnknown` — what every producer other than
	// the IndexScan promotion leaves — means inclusive, which is the only
	// shape those producers build. Before M0134-0001's class-8 gap was closed
	// the executor could not express exclusivity at all, so the promotion
	// refused strict-bound scans outright.
	LowOp   parser.OpCode
	HighOp  parser.OpCode
	// Covered is the slice of catalog.Column entries that the output schema
	// contains (a subset of Index.Columns, in projection order).
	Covered []catalog.Column
	// Cond is an additional filter evaluated per index row (S6 min/max
	// rewrite: the `col IS NOT NULL` qual). IndexOnlyScan's primary probe
	// (Key/Keys/LowKey/HighKey) is equality/range-shaped; this general
	// expression cannot be pushed into the btree probe, so the executor
	// applies it as a residual predicate. When nil, no extra filtering.
	Cond Expr
	// Backward emits the materialised rows in reverse (S6 max rewrite: PG's
	// `Index Only Scan Backward` over an ASC index delivers DESC NULLS FIRST).
	Backward bool
	// Parallel mirrors PostgreSQL's Plan.parallel_aware — see SeqScan's field
	// of the same name. Stamped once by parallel.go's stampParallelScan, never
	// inferred at render time. When set, each worker's scan processes only the
	// index LEAF BLOCKS it claims from the shared parallelIndexScanState
	// (M0134-0189), so the union over workers is the whole scan exactly once.
	Parallel bool
	schema   Schema
	// PrivilegeCheckRole / PrivilegeCheckRoleSet — see SeqScan's field of the
	// same name. M0122-0008 (view-owner privilege gap).
	PrivilegeCheckRole    string
	PrivilegeCheckRoleSet bool
}

func (n *IndexOnlyScan) Pos() int       { return n.pos }
func (n *IndexOnlyScan) Output() Schema { return n.schema }

// JoinType is the physical join shape emitted by the planner.
type JoinType int

const (
	JoinTypeInner JoinType = iota
	JoinTypeLeft
	JoinTypeRight
	JoinTypeFull
	JoinTypeCross
	// JoinTypeSemi emits each left (probe) row exactly once if it
	// has at least one match on the right (build). Output schema is
	// the left side only. Produced by EXISTS-unnesting (M0061-0001).
	JoinTypeSemi
	// JoinTypeAnti emits each left (probe) row exactly once if it
	// has NO match on the right (build). Output schema is the left
	// side only. Produced by NOT-EXISTS-unnesting (M0061-0001).
	JoinTypeAnti
)

// JoinAlgo is the physical algorithm the executor uses for a Join.
// v0 has three: nested-loop (the universal fallback), hash join
// (used for INNER/LEFT equality joins with disjoint-side keys),
// and merge join (used for RIGHT/FULL equality joins with
// disjoint-side keys).
type JoinAlgo int

const (
	JoinAlgoNestedLoop JoinAlgo = iota
	JoinAlgoHash
	JoinAlgoMerge
)

// Join combines two child relations with an optional predicate.
// Predicate is nil for CROSS JOIN and NATURAL/USING joins with no
// shared columns.
//
// When Algo == JoinAlgoHash or JoinAlgoMerge, LeftKey and
// RightKey carry the equality operands (`LeftKey = RightKey` is
// exactly Predicate). Hash join builds a hash table on one input
// and probes from the other. The default build side is the right
// input (matches the executor's historical convention); the
// planner sets BuildLeft=true when EstimateRows says the left
// side is smaller — building on the smaller relation cuts both
// memory and hash-table population time. Since M0127-P4.2 the
// executor fills EITHER side (07 §3), so BuildLeft is no longer
// INNER-only: RIGHT sets it deliberately (build the non-preserved
// left, probe the preserved right), and an outer join's
// preservation follows from Type plus BuildLeft rather than from
// the build side alone. Semi/Anti remain build-right by contract.
// Merge join sorts both sides on their keys and merges the two
// ordered streams.
type Join struct {
	// searchedTree: the usual search root — every join arm but the
	// parameterised nested loop emits one (searchedtree.go).
	searchedTree
	pos       int
	Type      JoinType
	Algo      JoinAlgo
	Left      Node
	Right     Node
	Predicate Expr
	LeftKey   Expr // populated when Algo == JoinAlgoHash
	RightKey  Expr
	// HashKeys holds EVERY usable equi-pair of this join, not just the
	// one (LeftKey, RightKey) the executor currently hashes on — PG's
	// `hashclauses` / `mergeclauses` list. HashKeys[0] IS
	// (LeftKey, RightKey), by pointer. Populated for JoinAlgoHash and
	// JoinAlgoMerge by fillJoinHashKeys, a single late pass at the tail
	// of Plan() (see join_hash_keys.go for why it is derived once at the
	// end rather than maintained at the nine construction sites). Empty
	// means "no list available" and every consumer must fall back to the
	// single pair. M0127-P2.1; design leftdeep-joins/05 §5.
	HashKeys  []JoinKeyPair
	BuildLeft bool // hash join: build on left input instead of right
	// UsingLeftCols / UsingRightCols hold the ABSOLUTE column
	// indices (relative to the merged schema) of the USING
	// columns from the left and right sides respectively.
	// Set only for FULL JOIN USING / FULL JOIN NATURAL. The
	// executor uses them to coalesce unmatched right-row output:
	// when the left side is NULL (right-only row), each
	// UsingLeftCols[i] is set to the value at UsingRightCols[i].
	// M0097-0060.
	UsingLeftCols  []int
	UsingRightCols []int
	// Lateral marks the right child as referencing the left
	// child's columns through a FROM-clause LATERAL SRF (M0103-0008).
	// The executor must drive the right per-outer-row, binding the
	// left row as the lateral outer slot (BindLateralOuter contract)
	// instead of materialising both sides up front.
	Lateral bool
	// NullAware marks a JoinTypeAnti built from unnesting a
	// non-correlated `x NOT IN (subquery)` (M0122-0011). Plain Anti
	// join semantics (used for NOT EXISTS) keep a probe row whenever
	// no hash match is found, including when the probe key is NULL —
	// correct for NOT EXISTS, but not for NOT IN's three-valued
	// semantics: a NULL anywhere in the subquery's output poisons
	// the whole predicate to NULL/false for every outer row unless
	// the subquery is empty, and a NULL outer value never matches
	// (excluded) unless the subquery is empty. The executor's
	// nextLazy/openLazyHashJoin special-case this flag instead of
	// reusing the NOT-EXISTS-shaped default.
	NullAware bool
	// AvgVarBytes is the average total variable-width payload of a build-side
	// row, fed from the build relation's RelOptInfo.AvgVarBytes. Zero means
	// "unknown" (no ANALYZE stats, or a fixed-width relation) and the geometry
	// falls back to sizing by column count alone — which is what the hardcoded
	// zero did before M0128-P3.1.
	AvgVarBytes float64
	// OuterRows / InnerRows are the join search's row-count estimates for the
	// two sides, threaded from the paths' Rows (which are the post-qual
	// RelOptInfo.Rows values the planner costed). Zero means the plan was not
	// built through the PG-shaped search (legacy path), and geometry falls back
	// to EstimateRows on the child node.
	//
	// M0128-P3.2: buildGeometry reads these instead of calling EstimateRows
	// on the build-side child node, because EstimateRows dispatches to
	// seqScanRows — which ignores on-scan quals — while the search's
	// RelOptInfo.Rows has baserestrictinfo selectivity already applied (§3.23).
	OuterRows float64
	InnerRows float64
	schema     Schema
}

func (n *Join) Pos() int { return n.pos }

// Output publishes the join's column layout.
//
// M0125-0008: Semi / Anti joins emit the OUTER (Left) row only, so
// their layout is by definition Left's *current* output. Every
// construction site already sets `schema` to a copy of
// `Left.Output()` (unnest.go, three sites) and predp.go refreshes it
// after join-order search — but `rewriteMultiWayChain` runs later and
// re-sorts the subtree below the pinned semi/anti spine IN PLACE,
// which leaves that copy a stale *permutation* of the real layout.
// `reresolveJoinByName` then re-resolves an ancestor's keys by name
// against the phantom layout, so the ancestor's key lands on the
// wrong column and its conjunct silently stops filtering: an
// `EXISTS … AND NOT EXISTS …` pair over one outer relation returned
// MORE rows than either conjunct alone (TPC-DS Q16 / Q94, and the
// non-subset signature that named this task).
//
// Deriving the layout here makes the invariant structural, so it
// cannot be re-broken by a future pass that rewrites Left in place
// and forgets to refresh the cache. `schema` is still the source of
// truth for every other join type, where it holds the merged layout.
func (n *Join) Output() Schema {
	if n.Type == JoinTypeSemi || n.Type == JoinTypeAnti {
		if n.Left != nil {
			return n.Left.Output()
		}
	}
	return n.schema
}

// AggregateCall is one aggregate function invocation in an Aggregate node.
type AggregateCall struct {
	pos  int
	Name string
	Arg  Expr // nil for count(*)
	Arg2 Expr // second arg for two-argument aggregates (regr_*, covar_*, corr)
	// ExtraArgs holds the 3rd and subsequent arguments for user-defined
	// multi-arg aggregates (e.g. aggfns(a, b, c) where Arg=a, Arg2=b, ExtraArgs=[c]).
	ExtraArgs []Expr
	Star      bool
	Distinct  bool
	Type      catalog.Type
	// InputType is the type of the primary argument expression, used for
	// precision-sensitive aggregates (e.g. float4 sum/variance use float32 semantics).
	InputType catalog.Type
	// WithinGroupKeyType is the type of the first ORDER BY column in WITHIN GROUP
	// ordered-set aggregates. Used for percentile_cont float32 precision rounding.
	WithinGroupKeyType catalog.Type
	// Filter is the resolved FILTER (WHERE ...) predicate. M0097-0007.
	Filter Expr
	// OrderBy is the ORDER BY clause inside the aggregate call, e.g.
	// array_agg(x ORDER BY y). Only used for ordering-sensitive aggregates.
	OrderBy []SortKey
	// WithinGroup is true when this is an ordered-set aggregate using
	// WITHIN GROUP (ORDER BY ...) syntax. M0097-0035.
	WithinGroup bool
	// WithinGroupOrderBy holds the sort keys from WITHIN GROUP (ORDER BY ...).
	// The executor accumulates these per-row values, sorts them, and then
	// applies the aggregate function (percentile_cont/disc, rank, etc.).
	WithinGroupOrderBy []SortKey
	// UserAgg is non-nil for user-defined aggregates registered via
	// CREATE AGGREGATE. The executor uses it to call sfunc/finalfunc.
	UserAgg *catalog.UserAggregate
	// SharedStateSlot is the index into aggregateOp.sharedUserStates for
	// user-defined aggregates that share transition state (same sfunc/stype/args/distinct/filter).
	// -1 means no sharing. When ≥ 0, applyAgg uses the shared slot instead of
	// the per-call aggRuntime.userState. M0097-0035.
	SharedStateSlot int
}

func (a AggregateCall) Pos() int { return a.pos }

// Aggregate groups rows by GroupExprs and computes Aggs.
// Output columns are [group exprs..., aggregate calls..., passthrough cols...].
// Passthrough holds expressions for columns that are functionally determined
// by the GROUP BY key (e.g. non-key cols when GROUP BY covers a primary key).
// The executor evaluates them from the first row of each group. M0097-0003.
type Aggregate struct {
	pos         int
	Child       Node
	GroupExprs  []Expr
	Aggs        []AggregateCall
	Passthrough []Expr
	schema      Schema

	// Mode splits an aggregate across a parallel boundary (P9). The zero
	// value is AggModeSimple, so every existing construction site keeps
	// today's semantics without being touched.
	Mode AggMode
	// PartialSource is set on a Finalize node and points at the Partial node
	// below the Gather whose per-group states it combines. Stated explicitly
	// rather than rediscovered by walking down through the Gather, because the
	// two nodes are created together and the link is what pairs them at
	// runtime.
	PartialSource *Aggregate

	// GroupingSets is non-nil for GROUP BY GROUPING SETS / ROLLUP / CUBE.
	// Each entry is one grouping set, listed as ASCENDING indices into
	// GroupExprs (which holds the deduplicated union of every set's
	// expressions — PostgreSQL's `parse->groupClause`). The executor makes
	// one pass over the child and keeps one hash table per set, the way
	// nodeAgg.c does for AGG_HASHED/AGG_MIXED; a GroupExprs column that is
	// not listed in the current set is emitted NULL, which is the SQL
	// standard's value for a dimension rolled up away at that level.
	//
	// nil (the common case) means the ordinary single-set aggregate, and
	// every construction site that does not set it keeps today's behaviour.
	// M0125-0048.
	GroupingSets [][]int
	// GroupingMasks carries one entry per distinct GROUPING(...) call in the
	// query, in the order the columns are appended to the output schema:
	// output column len(GroupExprs)+len(Aggs)+i holds
	// GroupingMasks[i][setIdx] for a row produced by grouping set setIdx.
	//
	// Materialising the bitmask as an output column (rather than as an
	// expression over a hidden set-id) is what lets the target list resolve
	// GROUPING(a,b) to a plain ColumnRef: the mask depends only on which set
	// produced the row, never on data (PostgreSQL evaluates GroupingFunc
	// from AggState->current_set for the same reason).
	//
	// Passthrough columns are appended AFTER these, so a functionally
	// determined column discovered during target resolution never displaces
	// a grouping column. M0125-0048.
	GroupingMasks [][]int64

	// Strategy selects hashed vs sorted aggregation. The zero value is
	// AggStrategyHashed, so every existing construction site and test
	// fixture keeps today's hash-only behavior without being touched.
	// The planner does not set it yet (M0134-0001 S8 lands the executor
	// capability first); sorted mode is reachable only via direct node
	// construction until the pathkey slice wires the choice in.
	Strategy AggStrategy

	// GroupKeyOrder is an EXPLAIN-only permutation: indices into GroupExprs,
	// in the order applyIndexOrderedGroupingRule's chosen index lays its key
	// columns out (S8 Slice 2c-i, 0134-0001 P2). GroupExprs itself is NEVER
	// reordered — every output-column binding downstream of buildAggregateStage
	// (target list, HAVING, ORDER BY) is fixed to GroupExprs' written
	// position, and finalizeGroup's group-boundary test
	// (internal/executor/operators_join_agg.go) is order-independent, so a
	// permutation is needed only to print PG's reordered `Group Key:` line
	// (mirrors PG's group_keys_reorder_by_pathkeys, pathkeys.c:375-450, which
	// reorders the printed key list without touching the parse tree's
	// groupClause). nil means "written order" — the fallback every existing
	// construction site gets, so this field never displaces the
	// remapAggExprsWithBindings-style compensating permutation that would be
	// the alternative design (see docs/design/0134-0001-p2-explain-format.md
	// §"S8 Slice 2c").
	GroupKeyOrder []int
}

// GroupingMaskColOffset is the index of the first GROUPING(...) output
// column: group expressions, then aggregates, then grouping masks, then
// passthrough columns.
func (n *Aggregate) GroupingMaskColOffset() int {
	return len(n.GroupExprs) + len(n.Aggs)
}

// AggStrategy is an aggregate node's grouping strategy. The zero value is
// AggStrategyHashed, mirroring the pre-S8 engine where every grouped
// aggregate was a hash aggregate (aggregateOp.Open builds a groups map).
type AggStrategy int

const (
	// AggStrategyHashed groups input rows by hashing the GROUP BY key.
	AggStrategyHashed AggStrategy = iota
	// AggStrategySorted (PostgreSQL's AGG_SORTED) groups a child that already
	// arrives in GROUP BY key order, collapsing runs of equal keys instead of
	// hashing (nodeAgg.c agg_retrieve_direct, postgres/src/backend/executor/
	// nodeAgg.c:2280-2619).
	AggStrategySorted
)

// AggMode is an aggregate node's role in a parallel split.
type AggMode int8

const (
	// AggModeSimple is an ordinary, whole-input aggregate.
	AggModeSimple AggMode = iota
	// AggModePartial aggregates one worker's share and publishes the per-group
	// transition states instead of finished values.
	AggModePartial
	// AggModeFinal combines the published states and produces the result.
	AggModeFinal
)

func (n *Aggregate) Pos() int       { return n.pos }
func (n *Aggregate) Output() Schema { return n.schema }

// WindowFunc is one supported window-function invocation in a
// WindowAgg node. Stage A supports row_number/rank (no args,
// int8 return); Stage B adds lag/lead (1-3 args, return type
// matches first arg); Stage C adds the frame-consuming aggregates
// sum/count/avg/min/max (0 or 1 args, evaluated over the default
// frame — RANGE UNBOUNDED PRECEDING when ORDER BY is present,
// otherwise the whole partition).
type WindowFunc struct {
	pos  int
	Name string
	Type catalog.Type
	Args []Expr // lag/lead: [value, offset?, default?]; agg: [value] or empty for count(*)
	// Star is true for count(*) OVER (...).
	Star bool
	// Filter is the resolved FILTER (WHERE ...) predicate, aggregate
	// window functions only (e.g. sum(x) FILTER (WHERE c) OVER (...)).
	Filter Expr
	// InputType is the type of Args[0], used by the executor to reuse
	// the ordinary-aggregate accumulator (precision-sensitive sum/avg
	// formatting for float4/float8 inputs).
	InputType catalog.Type
}

func (w WindowFunc) Pos() int { return w.pos }

// WindowAgg evaluates window functions over the child rows.
// Output columns are [child output..., window func outputs...].
// Stage A uses one shared PARTITION BY / ORDER BY spec for all
// funcs in the node.
type WindowAgg struct {
	pos         int
	Child       Node
	PartitionBy []Expr
	OrderBy     []SortKey
	Funcs       []WindowFunc
	// Frame is the resolved window frame clause shared by every
	// func in this node (nil when no explicit frame clause was
	// written — the executor's default frame applies). The analyzer
	// already validated bound ordering and restricted RANGE to
	// UNBOUNDED/CURRENT ROW bounds (RANGE value offsets rejected), so
	// by the time a Frame reaches the planner it is a well-formed
	// ROWS/GROUPS/RANGE frame (M0122-0004 frame-clause slice).
	Frame  *WindowFrame
	schema Schema
}

// WindowFrame is the planner-resolved form of parser.WindowFrame:
// StartOffset/EndOffset are planner Exprs (resolved against the
// window's input schema, mirroring how Limit.Limit/Offset are
// resolved) instead of raw parser.Expr. StartKind/EndKind/Exclusion
// reuse the parser package's small bound-kind enums directly rather
// than duplicating them, matching the existing convention of BinaryOp/
// UnaryOp.Op reusing parser.OpCode.
type WindowFrame struct {
	Mode        parser.FrameMode // FrameModeRows/Groups, or Range with UNBOUNDED/CURRENT ROW bounds only (RANGE value offsets rejected by the analyzer)
	StartKind   parser.FrameBoundKind
	StartOffset Expr // non-nil only for FrameBoundOffsetPreceding/Following
	EndKind     parser.FrameBoundKind
	EndOffset   Expr // non-nil only for FrameBoundOffsetPreceding/Following
	Exclusion   parser.FrameExclusion
}

func (n *WindowAgg) Pos() int       { return n.pos }
func (n *WindowAgg) Output() Schema { return n.schema }

// Filter — applies a predicate to its child's rows.
type Filter struct {
	// searchedTree: the scan arms' leaf rewrapper can restore the leaf's
	// original *Filter around a rebuilt scan, so a one-relation search root
	// can be a Filter (searchedtree.go).
	searchedTree
	pos       int
	Child     Node
	Predicate Expr
	// LeafLocal marks Filter wrappers attached by Slice A
	// (M0077-0001 / attachRelationLocalFilters) directly above
	// a leaf scan. The Predicate's ColumnRef.Index values are
	// in LEAF-LOCAL coordinates, NOT FROM-cumulative — they
	// must NOT be touched by the post-rewrite posMap passes
	// (applyJoinTreePosMap, remapPosMapAfterRewrite), which
	// assume cumulative coordinates.
	LeafLocal bool
	// PushedBelow lists the conjuncts of Predicate that a qual-placement
	// pass DUPLICATED onto a descendant node — `pushInnerJoinInputQuals`
	// and `pushResidualQualsIntoMHJTables` both copy a single-relation
	// restriction down to the relation it references and deliberately
	// leave `Predicate` untouched (their "property 2"), so the executor
	// evaluates it twice and the ESTIMATOR charged it twice.
	//
	// Upstream has no such list because it has no such duplicate:
	// `distribute_restrictinfo_to_rels` (initsplan.c) MOVES a
	// single-relation clause into that baserel's `baserestrictinfo`, so
	// `set_baserel_size_estimates` prices it once and the joinrel above
	// never sees it again — which is the invariant
	// `calc_joinrel_size_estimate`'s opening comment asserts ("we are not
	// double-counting them because they were not considered in estimating
	// the sizes of the component rels").
	//
	// goopg cannot move the clause — the copy left above the join is what
	// keeps the join's own residual evaluation correct — so it records the
	// duplication instead and `filterSelectivity` skips these conjuncts.
	// M0127-P5.6-f-vi.
	PushedBelow []Expr
}

func (n *Filter) Pos() int       { return n.pos }
func (n *Filter) Output() Schema { return n.Child.Output() }

// Project — evaluates the target list against its child's rows.
// Result is the goopg analog of PostgreSQL's T_Result (nodeResult.c). In its
// childless shape it emits exactly ONE row by evaluating Targets against an
// empty input — the S6 min/max rewrite top node above an InitPlan carrying the
// rewritten scalar aggregate. With a Child it emits one projected row per child
// row, exactly PG's `outerPlan(plan)` variant (nodeResult.c ExecResult).
//
// OneTimeFilter is the optional resconstantqual: evaluated ONCE at Open, and a
// NULL/false result short-circuits the node to emit no rows at all (PG's
// rs_checkqual latch). Both Child and OneTimeFilter are nil for the plain
// childless Result (the min/max InitPlan hangs off a SubqueryExpr target, not a
// child scan — see rewriteMinMaxAggregates); the const-arg rewrite
// (`SELECT max(100) FROM t`) sets both.
type Result struct {
	searchedTree
	pos int
	// Targets: evaluated once per emitted row (childless) or once per child
	// row (child). The S6 InitPlan SubqueryExpr target hangs here.
	Targets []Expr
	// OneTimeFilter: PG's resconstantqual, evaluated once at Open (nil = none).
	OneTimeFilter Expr
	// Child: nil = childless single-emit Result; set = one row per child row.
	Child  Node
	schema Schema
}

func (n *Result) Pos() int       { return n.pos }
func (n *Result) Output() Schema { return n.schema }

type Project struct {
	// searchedTree: the boundary node P5.5-f-i emits is a *Project, and it is
	// the node the legacy posmap family must most carefully not walk into
	// (searchedtree.go).
	searchedTree
	pos     int
	Child   Node
	Targets []Expr
	schema  Schema
	// IsolatedScope is set on Projects that wrap an isolated
	// subquery scope (e.g. M0063-0001's view-rename wrapping).
	// applyJoinTreePosMap / remapPosMapAfterRewrite skip the
	// Child when this is true; only the Targets are
	// outer-scope (and even then their ColumnRefs are inner-
	// indexed-then-outer-relabeled, so they should not be
	// remapped by the outer FROM-bindings posMap).
	IsolatedScope bool
}

func (n *Project) Pos() int       { return n.pos }
func (n *Project) Output() Schema { return n.schema }

// CTEScan wraps a cloned CTE body so EXPLAIN can label the
// inlined subtree with the CTE's name and alias. Stage A
// inlining clones the body per consumer (M0016-0002); this node
// sits between the consumer's FROM-clause reference and the
// cloned plan, carrying enough metadata for operator-level
// triage. The executor's Build switch unwraps it to Child — no
// new operator type is needed; the wrap is purely a labeling
// artifact.
//
// See docs/design/0016-0004-cte-observability-and-compat-tests.md.
type CTEScan struct {
	pos    int
	Name   string // CTE name from the WITH list
	Alias  string // alias used at this consumer site (defaults to Name)
	Child  Node
	schema Schema
	// cte points back at the planner-side WITH-list entry this scan
	// consumes. pushQualsThroughSingleRefCTEs reads its statement-wide
	// reference count to decide whether Child is private to this one
	// reference (PG 12+ `cte_inline` refcount==1 criterion) — both the
	// plan Node and the executor's CTERowCache entry (keyed by DeclKey)
	// are shared between references, so a per-reference qual may cross
	// into the body only when no second reference exists. nil for scans built
	// outside preplanWithClause (tests). M0125-0035 CTE-body arm.
	cte *plannedCTE
}

// CTEDMLPrefix executes data-modifying CTEs (INSERT/UPDATE/DELETE/MERGE)
// before the outer query. DMls are executed in order; each plan's RETURNING
// rows are collected into ctx.MaterializedCTEs[Names[i]] for CTEScan
// (IsDML=true) consumers.
type CTEDMLPrefix struct {
	pos   int
	Names []string // CTE name for each DML plan
	DMls  []Node   // DML plans to execute first
	Body  Node     // outer query plan
}

func (n *CTEDMLPrefix) Pos() int       { return n.pos }
func (n *CTEDMLPrefix) nodeTag()       {}
func (n *CTEDMLPrefix) Output() Schema { return n.Body.Output() }

// MaterializedCTEScan reads rows from a pre-executed DML CTE stored in
// ctx.MaterializedCTEs[Name]. Used when a DML CTE body's RETURNING rows
// are consumed by the outer SELECT.
type MaterializedCTEScan struct {
	pos    int
	Name   string // CTE name (key into ctx.MaterializedCTEs)
	Alias  string
	schema Schema
}

func (n *MaterializedCTEScan) Pos() int       { return n.pos }
func (n *MaterializedCTEScan) nodeTag()       {}
func (n *MaterializedCTEScan) Output() Schema { return n.schema }

func (n *CTEScan) Pos() int       { return n.pos }
func (n *CTEScan) Output() Schema { return n.schema }

// DeclSeq exposes the WITH-list declaration order of the CTE this scan
// consumes, so EXPLAIN can print its `CTE <name>` section in PG's order
// (`SS_process_ctes` walks the WITH list left to right). 0 when the scan was
// built outside preplanWithClause (tests), which sorts it first — harmless,
// since such a plan has at most one CTE. M0125-0049.
func (n *CTEScan) DeclSeq() int {
	if n.cte == nil {
		return 0
	}
	return n.cte.declSeq
}

// DeclKey identifies the DECLARATION this scan consumes, and is the key the
// executor materializes under (ctx.CTERowCache) and EXPLAIN hoists `CTE <name>`
// sections by. It is goopg's analogue of PG's `CteScan.ctePlanId`
// (postgres/src/backend/executor/nodeCtescan.c), which is per-declaration by
// construction because `SS_process_ctes` makes one subplan per WITH entry.
//
// Name alone is NOT the identity, which is what M0125-0050 fixed: `WITH x` in
// two disjoint scopes is two declarations, and keying by "x" made the second
// replay the first's rows (goopg answered 1,1 where PG answers 1,2).
//
// The key is the declaring CommonTableExpr's source offset plus its name, not
// the plannedCTE pointer, because ONE declaration can legitimately be planned
// more than once: planSelect re-enters on the head operand of a set-op chain,
// which yielded two distinct plannedCTEs for the one synthetic `__gs_src_N`
// AST node M0125-0040's grouping-sets rewrite built. That rewrite is gone
// (M0125-0048 replaced it with a single-pass aggregate that needs no synthetic
// CTE), but the re-entry it exposed is not: any declaration reached through a
// set-op head operand is planned twice and must keep sharing one
// materialization, and a declaration site is stable across replanning where a
// pointer is not.
//
// Both producers of a CommonTableExpr give distinct declarations distinct
// (pos, name) pairs: the parser stamps pos from the declaring identifier token,
// and the synthetic producer (CREATE RECURSIVE VIEW) leaves pos 0 but generates
// a name unique within the statement.
//
// Falls back to the bare name for a CTEScan built outside preplanWithClause
// (tests), which is the pre-M0125-0050 behaviour and unambiguous there: such a
// plan has at most one declaration per name.
func (n *CTEScan) DeclKey() string {
	name := strings.ToLower(n.Name)
	if n.cte == nil {
		return name
	}
	return strconv.Itoa(n.cte.declPos) + ":" + name
}

// Sort — orders the child's rows by the given keys.
type SortKey struct {
	Expr       Expr
	Desc       bool
	NullsFirst bool // true = NULLs sort before non-NULLs; false = after (PostgreSQL default: ASC→last, DESC→first)
}

type Sort struct {
	// searchedTree: the PathSort arm's root (searchedtree.go).
	searchedTree
	pos   int
	Child Node
	Keys  []SortKey
}

func (n *Sort) Pos() int       { return n.pos }
func (n *Sort) Output() Schema { return n.Child.Output() }

// `MultiHashKey`/`MultiHashJoin` lived here until M0127-P6.2 deleted them
// (leftdeep-joins 08 §4). The N-way packed node had no PG counterpart: PG
// expresses the same shape as a left-deep cascade of binary `HashJoin`s, and
// once the PG-shaped search became the only search (P5.9) nothing constructed
// an MHJ any more — `mhjPackingEnabled` had been default-off since M0126-0005
// and `generateMultiHashJoinPath` never had a production caller. Historical
// design: docs/design/0038-0001-multi-way-hash-join.md (superseded).

// Limit — caps the number of rows; both fields are optional.
type Limit struct {
	pos    int
	Child  Node
	Limit  Expr // nil when no limit
	Offset Expr // nil when no offset
	// WithTies is true when FETCH FIRST n ROWS WITH TIES was used. M0097-0042.
	WithTies bool
	// TiesKeys holds the ORDER BY expressions for WITH TIES comparison.
	// When WithTies=true, after emitting LimitCount rows the executor continues
	// emitting rows until the ORDER BY key changes from the last emitted row.
	TiesKeys []Expr
}

func (n *Limit) Pos() int       { return n.pos }
func (n *Limit) Output() Schema { return n.Child.Output() }

// Values — produces literal rows for INSERT, or rematerialises a
// virtual catalog table's current row set at execute time when
// VirtualSource is non-nil. The Rows slice is the snapshot captured
// at plan time; when VirtualSource is set, the executor refreshes it
// by calling VirtualSource.VirtualRows() on Open so the cross-session
// plan cache cannot serve a stale snapshot of a dynamic view
// (M0094-0005).
type Values struct {
	pos           int
	Rows          [][]Expr
	schema        Schema
	VirtualSource *catalog.Table
}

func (n *Values) Pos() int       { return n.pos }
func (n *Values) Output() Schema { return n.schema }

// GenerateSeries produces a sequence of integer rows for
// generate_series(start, stop[, step]) in the FROM clause.
// M0096-0006.
type GenerateSeries struct {
	pos    int
	Start  Expr
	Stop   Expr
	Step   Expr // nil means step=1
	// Alias is the FROM-clause alias; defaults to the function name when
	// not specified. Consumed by the EXPLAIN Function Scan label.
	Alias  string
	schema Schema
}

// UserSrfScan executes a user-defined SETOF SQL/plpgsql function in the FROM
// clause and emits each returned value as one output row. M0097-0153.
type UserSrfScan struct {
	pos     int
	Routine *catalog.Routine
	Args    []Expr
	// Alias is the FROM-clause alias; defaults to the lowercased function
	// name when not specified. Consumed by the EXPLAIN Function Scan label.
	Alias   string
	schema  Schema
}

func (n *UserSrfScan) Pos() int       { return n.pos }
func (n *UserSrfScan) Output() Schema { return n.schema }

func (n *GenerateSeries) Pos() int       { return n.pos }
func (n *GenerateSeries) Output() Schema { return n.schema }

// GenerateSubscripts produces subscript integers 1..array_length for
// generate_subscripts(anyarray, dim[, reverse]) in the FROM clause. M0097-0117.
type GenerateSubscripts struct {
	pos      int
	ArrExpr  Expr
	Dim      Expr
	Reversed Expr // optional; nil = false
	// Alias is the FROM-clause alias; defaults to the function name when
	// not specified. Consumed by the EXPLAIN Function Scan label.
	Alias    string
	schema   Schema
}

func (n *GenerateSubscripts) Pos() int       { return n.pos }
func (n *GenerateSubscripts) Output() Schema { return n.schema }

// PgInputErrorInfo implements pg_input_error_info(value, type) as a
// set-returning function in the FROM clause. Returns 0 rows if the
// input is valid, or 1 row with (message, detail, hint, sql_error_code)
// if it is invalid. M0097-0003.
// FromUnnest expands an array expression into one row per element in the
// FROM clause: `FROM unnest(arr_expr) alias(col)`. M0097-0035.
// For multi-arg unnest: `FROM unnest(arr1, arr2, ...)`, ArrExprs holds each
// array and the schema has one column per array (NULL-padded ZIP semantics).
type FromUnnest struct {
	pos      int
	ArrExpr  Expr   // single-arg form (len(ArrExprs)==0)
	ArrExprs []Expr // multi-arg form (len>=2)
	// Alias is the FROM-clause alias; defaults to "unnest" when not
	// specified. Consumed by the EXPLAIN Function Scan label.
	Alias    string
	schema   Schema
}

func (n *FromUnnest) Pos() int       { return n.pos }
func (n *FromUnnest) Output() Schema { return n.schema }

// OrdinalityWrap wraps a child FROM-clause SRF and appends a bigint ordinal
// column (1-based, named by OrdColName). Used by WITH ORDINALITY.
type OrdinalityWrap struct {
	pos        int
	Child      Node
	OrdColName string
	schema     Schema // child schema + ordinality column
}

func (n *OrdinalityWrap) Pos() int       { return n.pos }
func (n *OrdinalityWrap) Output() Schema { return n.schema }

// RowsFrom zips multiple FROM-clause SRFs side-by-side, NULL-padding the
// shorter ones. Used by `ROWS FROM(f1, f2, ...)`. Each entry in Funcs is a
// planned SRF node; the combined schema is the concatenation of all schemas.
type RowsFrom struct {
	pos    int
	Funcs  []Node // one per ROWS FROM entry
	schema Schema
}

func (n *RowsFrom) Pos() int       { return n.pos }
func (n *RowsFrom) Output() Schema { return n.schema }

type PgInputErrorInfo struct {
	pos    int
	Value  Expr
	Type   Expr
	schema Schema
}

// PgGetPublicationTables implements pg_get_publication_tables(VARIADIC text[])
// as a FROM-clause SRF. It walks the PubSub registry, filtering by the supplied
// publication-name list, and returns one row per (publication, table) pair.
// Used by libpqrcv's CREATE SUBSCRIPTION fetch_table_list probe (M0103-0008
// probe-survival). Composite-expansion of `(srf()).*` in scalar position is
// out of scope for this loop; callers must invoke this function from the FROM
// clause. The output schema mirrors upstream's return type as closely as
// possible without composite-type support: `(relid oid, attrs text, qual text)`.
type PgGetPublicationTables struct {
	pos    int
	Args   []Expr // raw VARIADIC argument list (text[] or text values)
	schema Schema
}

// ProjectSet evaluates a single set-returning function call per Child row
// and emits each row of its composite result as one output row.
//
// Currently implemented only for `pg_get_publication_tables`; SrfArgs are
// resolved Exprs whose ColumnRefs index into Child.Output(), so an
// `Aggregate → ProjectSet(srf(<agg-output-col>))` plan can spread the
// SRF's expansion of an aggregated text[] back over multiple rows.
//
// The output schema is the SRF's expanded composite (relid, attrs, qual
// for pg_get_publication_tables). M0103-0008 final sub-step.
// SrfCol describes one generate_series call in the SELECT target list
// for the SELECT-list SRF expansion mode of ProjectSet. M0097-0045.
type SrfCol struct {
	ColIdx int  // which output column this SRF fills
	Start  Expr // generate_series start arg
	Stop   Expr // generate_series stop arg
	Step   Expr // generate_series step arg (nil → step 1)
	// Wrapped marks an SRF that feeds an enclosing scalar expression (e.g.
	// `generate_series(1,n) % 4`). When true the executor writes the raw
	// per-step SRF value into the eval row at ColIdx (a temp slot beyond the
	// child width, NOT a visible output column) so the matching ProjectSet
	// wrapper expression can reference it via a ColumnRef. M0118-0008.
	Wrapped bool
}

// UnnestCol represents an unnest(array) SRF column in a SELECT list. M0097-0106.
type UnnestCol struct {
	ColIdx   int    // which output column this SRF fills
	ArrExpr  Expr   // the array argument
	CastType string // cast each element to this type (e.g. "int4"), empty=no cast. M0097-0035.
	// Wrapped: see SrfCol.Wrapped. M0118-0008.
	Wrapped bool
}

// RegexpMatchesCol describes one regexp_matches(string, pattern[, flags])
// call in the SELECT target list. Unlike UnnestCol (which flattens one
// array's elements one-per-row), each row's value here is a WHOLE match's
// capture-group array: one row per match when flags contains 'g', otherwise
// at most one row — mirroring regexp_matches' own SRF semantics (PG's
// setup_regexp_matches). The FROM-clause form (`FROM regexp_matches(...)`)
// is not covered by this — target-list only. M0122-0002 (regexp_matches-srf).
type RegexpMatchesCol struct {
	ColIdx      int  // which output column this SRF fills
	StringExpr  Expr // the subject string argument
	PatternExpr Expr // the regex pattern argument
	FlagsExpr   Expr // optional flags argument; nil when not given
}

// SrfWrapper applies an enclosing scalar expression to a set-returning function
// nested inside a SELECT-list target (e.g. `generate_series(1,n) % 4`). Expr is
// the resolved target expression with the SRF call replaced by a ColumnRef that
// reads the SRF's expanded per-step value from the eval row; the result is
// written to output column OutCol. Without this, a SELECT-list SRF buried in an
// expression collapsed to a single scalar (the SRF's start value), silently
// dropping rows. M0118-0008.
type SrfWrapper struct {
	OutCol int  // visible output column to write
	Expr   Expr // wrapper expression referencing the SRF value via a ColumnRef
}

// UserSrfCol describes one user-defined SETOF SQL function call in the SELECT
// target list. The executor calls the function body and collects all rows.
// M0097-0020.
type UserSrfCol struct {
	ColIdx  int    // which output column this SRF fills
	FuncPos int    // source position for error reporting
	Args    []Expr // resolved argument expressions
	Routine *catalog.Routine
}

type ProjectSet struct {
	pos     int
	Child   Node
	SrfName string
	SrfArgs []Expr
	schema  Schema
	// SELECT-list SRF mode (generate_series in target list). M0097-0045.
	// When non-empty, the operator expands the SRFs and zips them
	// together, repeating OtherExprs for each step. The output schema
	// covers both SRF and non-SRF columns.
	SrfCols           []SrfCol           // one per generate_series call in target list
	UnnestCols        []UnnestCol        // one per unnest(array) call in target list. M0097-0106.
	RegexpMatchesCols []RegexpMatchesCol // one per regexp_matches call in target list. M0122-0002.
	UserSrfCols       []UserSrfCol       // one per user-defined SETOF function call. M0097-0020.
	OtherExprs        []Expr             // non-SRF target expressions; nil slot = SRF slot
	// Wrappers hold enclosing scalar expressions over a nested SRF (e.g.
	// `generate_series(1,n) % 4`). When non-empty the executor builds a
	// per-step eval row of width EvalRowWidth (child row in [0:ChildWidth),
	// wrapped-SRF raw values in temp slots above) and evaluates each wrapper
	// against it. M0118-0008.
	Wrappers     []SrfWrapper
	ChildWidth   int // width of the child schema; base index of wrapped-SRF temp slots
	EvalRowWidth int // ChildWidth + number of wrapped SRFs (eval-row size)
}

func (n *ProjectSet) Pos() int       { return n.pos }
func (n *ProjectSet) Output() Schema { return n.schema }

func (n *PgGetPublicationTables) Pos() int       { return n.pos }
func (n *PgGetPublicationTables) Output() Schema { return n.schema }

func (n *PgInputErrorInfo) Pos() int       { return n.pos }
func (n *PgInputErrorInfo) Output() Schema { return n.schema }

// PgGetSequenceData implements pg_get_sequence_data(regclass) as a FROM-clause
// SRF. pg_dump's getSequences comma-joins it with pg_catalog.pg_sequence
// (`FROM pg_sequence, pg_get_sequence_data(seqrelid)`) to read each sequence's
// runtime (last_value int8, is_called bool). goopg's pg_sequence virtual view
// is empty (sequences are not surfaced in pg_class, so pg_dump never discovers
// one to dump), so this SRF is only ever planned, never executed over a
// non-empty left side — it always returns 0 rows. The correlated seqrelid
// argument is resolved against the lateral outer context so the comma-join
// binds as a LATERAL join (mirrors PgGetPublicationTables). M0110-0001
// (DU-002 slice 32).
type PgGetSequenceData struct {
	pos    int
	Args   []Expr
	schema Schema
}

func (n *PgGetSequenceData) Pos() int       { return n.pos }
func (n *PgGetSequenceData) Output() Schema { return n.schema }

// PgSequenceParameters implements pg_sequence_parameters(regclass) as a
// FROM-clause SRF. Returns the persisted DDL parameters of a sequence
// (start_value, minimum_value, maximum_value, increment, cycle_option,
// cache_size, data_type) — unlike PgGetSequenceData, this reads catalog
// state, not the live current value. Takes a plain constant regclass arg
// (no lateral correlation observed in PG's grammar/pg_proc.dat for this
// function). PG oracle: postgres/src/backend/commands/sequence.c:1740
// pg_sequence_parameters; pg_proc.dat:3426-3431. M0134-0069.
type PgSequenceParameters struct {
	pos    int
	Arg    Expr
	schema Schema
}

func (n *PgSequenceParameters) Pos() int       { return n.pos }
func (n *PgSequenceParameters) Output() Schema { return n.schema }

// TSTokenType implements ts_token_type(parser_oid) as a FROM-clause SRF:
// (tokid int4, alias text, description text). pg_dump's dumpTSConfig issues
// `FROM pg_catalog.ts_token_type('%u'::oid) AS t` (a literal argument, not
// lateral) to resolve a pg_ts_config_map row's maptokentype back to its
// alias. goopg models only the one built-in "default" parser
// (catalog.BuiltinTSParserOID), so the operator returns
// catalog.DefaultParserTokenTypes when Args[0] evaluates to that parser's
// OID, and 0 rows for any other input (including a user-defined parser,
// which cannot exist — CREATE TEXT SEARCH PARSER is unimplemented). DU-002
// slice 446 (M0119-0004).
type TSTokenType struct {
	pos    int
	Arg    Expr
	schema Schema
}

func (n *TSTokenType) Pos() int       { return n.pos }
func (n *TSTokenType) Output() Schema { return n.schema }

// PgAvailableWalSummaries implements pg_available_wal_summaries() as a
// FROM-clause SRF. Returns (tli int8, start_lsn pg_lsn, end_lsn pg_lsn)
// for each available WAL summary file. goopg v0 has no WAL summarizer
// (summarize_wal is always off), so this always returns 0 rows. M0095-0002.
type PgAvailableWalSummaries struct {
	pos    int
	schema Schema
}

func (n *PgAvailableWalSummaries) Pos() int       { return n.pos }
func (n *PgAvailableWalSummaries) Output() Schema { return n.schema }

// PgGetCatalogForeignKeys implements pg_get_catalog_foreign_keys() as a
// FROM-clause SRF. Returns one row per compiled-in system-catalog FK
// relationship (fktable regclass, fkcols text[], pktable regclass, pkcols
// text[], is_array bool, is_opt bool) — a static table mirroring PG's
// genbki-generated sys_fk_relationships[] (postgres/src/include/catalog/
// system_fk_info.h). Used by oidjoins.sql to self-check catalog FK
// integrity. M0134-0146.
type PgGetCatalogForeignKeys struct {
	pos    int
	schema Schema
}

func (n *PgGetCatalogForeignKeys) Pos() int       { return n.pos }
func (n *PgGetCatalogForeignKeys) Output() Schema { return n.schema }

// ScalarFuncScan returns a single row from a scalar function call used in
// the FROM clause (e.g. `FROM parse_ident(...) AS a`). The function result
// is returned as a single column named ColName with type ColType. M0097-0003.
type ScalarFuncScan struct {
	pos    int
	Func   Expr
	schema Schema
}

func (n *ScalarFuncScan) Pos() int       { return n.pos }
func (n *ScalarFuncScan) Output() Schema { return n.schema }

// PgPartitionTree is a multi-row SRF plan node for pg_partition_tree and
// pg_partition_ancestors. It traverses the catalog partition hierarchy and
// returns one row per node (table or index). M0097-0023.
type PgPartitionTree struct {
	pos      int
	FuncName string // "pg_partition_tree" or "pg_partition_ancestors"
	Arg      Expr   // the input regclass expression
	schema   Schema
}

func (n *PgPartitionTree) Pos() int       { return n.pos }
func (n *PgPartitionTree) Output() Schema { return n.schema }

// PgOptionsToTable is the FROM-clause SRF plan node for
// pg_options_to_table(text[]). It parses each "name=value" (or bare "name")
// element of the option array into a row of (option_name text, option_value
// text); option_value is NULL when the element has no '='. Mirrors
// untransformRelOptions / pg_options_to_table in
// src/backend/foreign/foreign.c. DU-002 slice 17 (M0110-0001) — pg_dump's
// getForeignDataWrappers expands fdwoptions via this SRF.
type PgOptionsToTable struct {
	pos    int
	Arg    Expr // the input text[] expression
	schema Schema
}

func (n *PgOptionsToTable) Pos() int       { return n.pos }
func (n *PgOptionsToTable) Output() Schema { return n.schema }

// FromRegexpMatches is the FROM-clause SRF plan node for
// FROM regexp_matches(string, pattern[, flags]). Unlike RegexpMatchesCol
// (SELECT-list position), this produces its own range-table entry with a
// single text[] output column — one row per match when flags contains 'g',
// otherwise at most one row, zero rows on no match (matches PG's SRF
// row-count semantics, not the scalar fallback's NULL). M0122-0002 follow-up.
type FromRegexpMatches struct {
	pos         int
	StringExpr  Expr
	PatternExpr Expr
	FlagsExpr   Expr // nil when not given
	schema      Schema
}

func (n *FromRegexpMatches) Pos() int       { return n.pos }
func (n *FromRegexpMatches) Output() Schema { return n.schema }

// FromRegexpSplitToTable is the FROM-clause SRF plan node for
// FROM regexp_split_to_table(string, pattern[, flags]). Unlike
// FromRegexpMatches, output is a single plain text column (one row per
// substring produced by splitting on the pattern) — mirrors PG's
// regexp_split_to_table / build_regexp_split_result. N matches produce N+1
// rows, always (the 'g' flag is rejected up front, then glob=true is forced
// internally per postgres/src/backend/utils/adt/regexp.c:1748-1797). M0134-0070
// Round D.
type FromRegexpSplitToTable struct {
	pos         int
	StringExpr  Expr
	PatternExpr Expr
	FlagsExpr   Expr // nil when not given
	schema      Schema
}

func (n *FromRegexpSplitToTable) Pos() int       { return n.pos }
func (n *FromRegexpSplitToTable) Output() Schema { return n.schema }

// VerifyHeapam is the FROM-clause SRF plan node for amcheck's
// verify_heapam(regclass, ...) — slice S3 of docs/design/0110-0008. It carries
// the relation argument plus the optional startblock / endblock block-range
// arguments; the executor op resolves the relation, walks its heap blocks
// through the committed internal/amcheck engine (VerifyHeapRelation), and emits
// one (blkno, offnum, attnum, msg) row per structural-corruption finding. The
// remaining SRF arguments (on_error_stop, check_toast, skip) are accepted by the
// parser but have no effect — check_toast is goopg-divergent (see 0110-0005) and
// the others do not change which structural checks run. M0110-0003.
type VerifyHeapam struct {
	pos        int
	Arg        Expr // the regclass relation argument (required)
	StartBlock Expr // startblock int8 (optional, nil = whole relation)
	EndBlock   Expr // endblock int8 (optional, nil = whole relation)
	schema     Schema
}

func (n *VerifyHeapam) Pos() int       { return n.pos }
func (n *VerifyHeapam) Output() Schema { return n.schema }

// Insert — writes rows from Source into Table. ColumnIndex maps each
// source column to a target heap-tuple ordinal; columns not listed
// receive NULL (or their declared default once defaults are wired).
//
// OnConflict carries the resolved `ON CONFLICT …` action (M0017-0002).
// nil for a plain INSERT — every existing test path keeps that
// nil-default. Non-nil for the upstream-compatible UPSERT shape.
type Insert struct {
	pos             int
	Table           *catalog.Table
	Source          Node
	ColumnIndex     []int
	OnConflict      *OnConflictPlan
	Returning       []Expr // per-target RETURNING expressions (nil = no RETURNING)
	ReturningSchema Schema // output schema when Returning is non-nil

	// ViewCheckQual/ViewCheckName are set when this INSERT's original target
	// was a `WITH CHECK OPTION` view that was rewritten onto Table (its
	// auto-updatable base relation, see viewAutoUpdatableChain). The executor
	// evaluates ViewCheckQual against each finalized row and raises 44000
	// ("new row violates check option for view") when it isn't true — mirrors
	// execMain.c's WCO_VIEW_CHECK. nil when the target wasn't a CHECK OPTION
	// view. M0119-0004 slice-365 follow-up.
	ViewCheckQual Expr
	ViewCheckName string
}

func (n *Insert) Pos() int       { return n.pos }
func (n *Insert) Output() Schema { return n.ReturningSchema }

// LockStrength enumerates the row-locking strength a SELECT
// requested via FOR UPDATE / FOR SHARE. Mirrors upstream's
// LockClauseStrength enum (parsenodes.h). The zero value is
// reserved for "no lock" — Stage A only emits the two upstream-
// canonical strengths goopg's parser accepts (M0021-0001).
type LockStrength int

const (
	// LockStrengthForUpdate — `FOR UPDATE`. Write-intent row
	// lock. Mirrors upstream's LCS_FORUPDATE.
	LockStrengthForUpdate LockStrength = iota + 1
	// LockStrengthForShare — `FOR SHARE`. Read-intent row lock.
	// Mirrors upstream's LCS_FORSHARE.
	LockStrengthForShare
	// LockStrengthForNoKeyUpdate — `FOR NO KEY UPDATE`. Weaker write-intent
	// lock; v0 maps to ForUpdate (M0096-0004 — key-level modes out of scope).
	LockStrengthForNoKeyUpdate
	// LockStrengthForKeyShare — `FOR KEY SHARE`. Weaker read-intent lock;
	// v0 maps to ForShare (M0096-0004 — key-level modes out of scope).
	LockStrengthForKeyShare
)

// LockWaitPolicy enumerates how a row-locking clause should
// behave when a target row is already locked by another
// transaction. Stage A executor only honors LockWaitBlock;
// LockWaitNoWait / LockWaitSkipLocked stay deferred to
// M0021-0003 but are carried through the plan node so the
// executor can branch on them once support lands.
type LockWaitPolicy int

const (
	LockWaitBlock      LockWaitPolicy = iota // wait for contention
	LockWaitNoWait                           // NOWAIT — fail with 55P03
	LockWaitSkipLocked                       // SKIP LOCKED — drop row
)

// LockedRel is the per-relation locking intent the planner
// resolved from a SELECT's `FOR UPDATE / FOR SHARE [OF …]`
// tail. One LockedRel per FROM-clause range variable in the
// effective target set; multiple clauses may produce duplicate
// entries today (Stage A executor will merge under a
// strongest-wins rule when it lands).
type LockedRel struct {
	Table      *catalog.Table
	Alias      string // FROM-clause alias for diagnostics; empty when bare table
	Strength   LockStrength
	WaitPolicy LockWaitPolicy
	// ColOffset is the first output-column index of this relation in the
	// SELECT result schema. Set by resolveLockedRels from rangeBinding.offset
	// so the executor can merge EPQ-refetched values at the correct position
	// even when the locked table is not the leftmost in the join (M0100-0010).
	ColOffset int
	// RowMarkId is a 1-based statement-local rowmark identifier (PG's
	// rowmarkId). Assigned sequentially by resolveLockedRels. Used by the
	// executor to locate the junk ctid column in the child row.
	// M0128-P6.1 resjunk-ctid rowmark.
	RowMarkId int
	// CtidResno is the column index of the resjunk ctid attribute in the
	// child plan's output row. Set at plan-build time when the ctid column
	// is appended to the scan's schema; lockRowsOp reads the TID from the
	// row at this position rather than from the walker/side-channel. -1
	// when not yet wired (the executor falls back to the walker path).
	// M0128-P6.1 resjunk-ctid rowmark.
	CtidResno int
}

// LockRows is the upstream-shape wrapper that adds row-lock
// acquisition over its child SELECT plan. Mirrors upstream's
// LockRows operator (createplan.c). The planner emits one
// LockRows at the top of the plan tree when the SELECT carries
// any locking clause; pre-M0021 SELECTs never see this node.
//
// Output schema is the child's schema with resjunk ctid columns
// stripped — callers see only the user-visible columns.
type LockRows struct {
	pos   int
	Child Node
	Locks []LockedRel

	// LimitCount / OffsetCount carry the LIMIT / OFFSET expressions when a
	// SKIP LOCKED query's Limit was lifted ABOVE this LockRows. PG plans
	// `Limit → LockRows → Sort` so the row lock claims rows in the LIMIT's
	// order and stops after LIMIT (+OFFSET) *successfully-locked* rows,
	// skipping rows held by other transactions. goopg's default plan puts the
	// Limit below the LockRows, which would cut the scan before locking and
	// turn a skipped row into a missing result. The executor evaluates these
	// at Open to cap drainAndStamp at LIMIT+OFFSET locked rows. nil when no
	// Limit was lifted (unbounded drain). M0118-0003.
	LimitCount  Expr
	OffsetCount Expr

	// NumCtidCols is the number of resjunk ctid columns appended to the
	// child's output (one per locked relation with a wired CtidResno).
	// Output() strips these trailing columns so callers see only the
	// user-visible projection. The executor trims the ctid columns from
	// returned rows. M0128-P6.1 resjunk-ctid rowmark.
	NumCtidCols int
}

func (n *LockRows) Pos() int { return n.pos }

// Output returns the user-visible schema (child schema with trailing ctid
// junk columns stripped). When no ctid columns are wired, this is identical
// to Child.Output(). M0128-P6.1 resjunk-ctid rowmark.
func (n *LockRows) Output() Schema {
	if n.NumCtidCols == 0 {
		return n.Child.Output()
	}
	child := n.Child.Output()
	if n.NumCtidCols >= len(child) {
		return Schema{}
	}
	return child[:len(child)-n.NumCtidCols]
}

// OnConflictAction enumerates the resolved conflict action — mirrors
// the parser's parser.OnConflictAction enum, but the analyzer-only
// `OnConflictNone` placeholder doesn't appear here because the
// planner only produces nodes for actual clauses.
type OnConflictAction int

const (
	// OnConflictActionNothing — `DO NOTHING`. The executor skips
	// the row when any conflict on ArbiterIndex is detected (or
	// any unique index, when ArbiterIndex == nil for the
	// no-target form).
	OnConflictActionNothing OnConflictAction = iota
	// OnConflictActionUpdate — `DO UPDATE SET … [WHERE …]`. The
	// executor reads the conflicting tuple, evaluates UpdateSet
	// (and optional UpdateWhere) against a row exposing both the
	// existing tuple (output indices 0..N-1) and the inserted
	// tuple (output indices N..2N-1; addressed as `excluded.col`
	// in user SQL), and writes the result back.
	OnConflictActionUpdate
)

// OnConflictPlan is the resolved planner-side state for an
// `INSERT … ON CONFLICT …` clause. Mirrors upstream's
// OnConflictExpr (parsenodes.h) at the level of detail goopg's
// executor needs.
type OnConflictPlan struct {
	Action OnConflictAction

	// ArbiterIndex is the resolved unique/primary index that
	// arbitrates conflict detection. Non-nil for any ON CONFLICT
	// (cols) form; nil only for the bare `ON CONFLICT DO NOTHING`
	// shape (no-target — executor must check every unique index).
	ArbiterIndex *catalog.Index

	// ArbiterColumns are the column ordinals on Table.Columns that
	// match ArbiterIndex.Columns in catalog order. Same length as
	// ArbiterIndex.Columns when ArbiterIndex != nil; nil otherwise.
	// Useful at runtime so the executor can extract the conflict
	// key from the inserted-row tuple without re-doing a name
	// lookup. For expression-based index columns, the corresponding
	// entry is -1 and ArbiterExprs[i] holds the expression to evaluate.
	ArbiterColumns []int

	// ArbiterExprs is parallel to ArbiterColumns. For expression-based
	// arbiter columns (ArbiterColumns[i] == -1), ArbiterExprs[i] is the
	// resolved planner expression to evaluate against the inserted row.
	// nil for plain column-reference arbiter columns.
	ArbiterExprs []Expr

	// UpdateSet is parallel to Table.Columns: nil means "leave the
	// existing value alone", non-nil is an Expr to evaluate
	// against the merged target+excluded row at conflict time.
	// Only populated when Action == OnConflictActionUpdate.
	UpdateSet []Expr

	// UpdateWhere is the optional predicate from `DO UPDATE SET …
	// WHERE …`. nil when absent. Evaluated against the same
	// target+excluded row; rows whose predicate evaluates false
	// are left unchanged (no DO NOTHING fallback — matches
	// upstream's silent-skip rule).
	UpdateWhere Expr
}

// Update — overwrites visible rows of Table with Set assignments.
// Set is parallel to the table's columns: nil entries leave the
// existing value alone; non-nil entries are evaluated against the
// child's rows.
//
// When FromTables is non-empty (UPDATE … FROM …, M0097-0065) the
// executor performs a nested-loop cross-product between the target
// table scan and the FROM tables, applying the predicate to the
// combined row. The FromSchema field holds the concatenated schema
// of all FROM tables so the executor can allocate the right row size.
type Update struct {
	pos             int
	Table           *catalog.Table
	Child           Node
	Only            bool   // UPDATE ONLY <table> — skip inheritance/partition children
	Set             []Expr // len == len(Table.Columns)
	Returning       []Expr // per-target RETURNING expressions (nil = no RETURNING)
	ReturningSchema Schema // output schema when Returning is non-nil
	// UPDATE … FROM (M0097-0065): additional source tables.
	// When non-empty the executor iterates all FROM tables as a nested
	// loop cross-product against the target scan and applies FromPred
	// (which may reference both target and FROM columns) to select
	// matching rows. Set expressions are also evaluated against the
	// combined (target ++ from...) row.
	FromTables []*catalog.Table
	FromScans  []Node // one per FROM table (may be SeqScan or subquery node)
	FromSchema Schema // combined schema of all FROM tables
	FromPred   Expr   // WHERE predicate over combined row (nil = no filter)

	// ViewCheckQual/ViewCheckName — see Insert.ViewCheckQual. Evaluated
	// against the post-SET row before it is written back.
	ViewCheckQual Expr
	ViewCheckName string
}

func (n *Update) Pos() int       { return n.pos }
func (n *Update) Output() Schema { return n.ReturningSchema }

// Delete — marks the visible rows of Table that survive the child's
// filter as dead.
type Delete struct {
	pos             int
	Table           *catalog.Table
	Child           Node
	Only            bool // DELETE FROM ONLY <table> — skip inheritance/partition children
	Returning       []Expr
	ReturningSchema Schema
	// DELETE … USING (M0097-0076): additional source tables.
	// Same semantics as Update.FromTables: when non-empty the executor
	// iterates all USING tables as a nested-loop cross-product against
	// the target scan and applies UsingPred (which may reference both
	// target and USING columns) to select matching victim rows.
	// RETURNING may reference USING columns via the combined row.
	UsingTables []*catalog.Table
	UsingScans  []Node // one per USING table (may be SeqScan or subquery node)
	UsingSchema Schema
	UsingPred   Expr
}

func (n *Delete) Pos() int       { return n.pos }
func (n *Delete) Output() Schema { return n.ReturningSchema }

// ── Merge plan node (M0096-0010) ─────────────────────────────────────────────

// MergeActionKind mirrors parser.MergeActionKind without importing the parser.
type MergeActionKind int

const (
	MergeActionUpdate MergeActionKind = iota + 1
	MergeActionDelete
	MergeActionInsert
	// MergeActionDoNothing — WHEN … THEN DO NOTHING. M0097-0016.
	MergeActionDoNothing
)

// MergeWhenClause is the planned form of one WHEN arm.
type MergeWhenClause struct {
	Matched   bool
	BySource  bool // true for WHEN NOT MATCHED BY SOURCE. M0100-0007.
	Condition Expr // nil when no AND condition
	Action    MergeActionKind

	// UPDATE: parallel to target columns (nil = keep existing).
	UpdateSet []Expr

	// INSERT: InsertExprs are evaluated against the source row at runtime.
	// InsertColIdx maps source → target column ordinals (same length).
	// nil InsertExprs means DEFAULT VALUES.
	InsertExprs  []Expr
	InsertColIdx []int
}

// Merge is the plan node for MERGE INTO target USING source ON cond WHEN ….
// Source is the planned USING clause. Target is the merge-target table.
type Merge struct {
	pos             int
	Target          *catalog.Table
	Source          Node // USING clause scan
	On              Expr // join condition (source cols at offset len(Target.Columns))
	Clauses         []*MergeWhenClause
	Returning       []Expr // RETURNING expressions (nil if absent)
	ReturningSchema Schema // output schema when Returning != nil
}

func (n *Merge) Pos() int       { return n.pos }
func (n *Merge) Output() Schema { return n.ReturningSchema }

// DDL — passes the original parser DDL statement through to the
// executor's DDL path. The planner doesn't decompose DDL further in
// v0; the catalog is mutated as the executor runs the statement.
type DDL struct {
	pos  int
	Stmt parser.Stmt
}

func (n *DDL) Pos() int       { return n.pos }
func (n *DDL) Output() Schema { return nil }

// Transaction — BEGIN / COMMIT / ROLLBACK / SAVEPOINT / RELEASE / ROLLBACK TO.
type TransactionVerb int

const (
	TxBegin TransactionVerb = iota
	TxCommit
	TxRollback
	TxSavepoint  // SAVEPOINT name
	TxRelease    // RELEASE [SAVEPOINT] name
	TxRollbackTo // ROLLBACK TO [SAVEPOINT] name
)

type Transaction struct {
	pos            int
	Verb           TransactionVerb
	Name           string // savepoint name for TxSavepoint / TxRelease / TxRollbackTo
	IsolationLevel string // for TxBegin: "read committed", "repeatable read", etc.; "" = session default
	ReadOnly       bool   // for TxBegin: true when BEGIN/START TRANSACTION READ ONLY
	Deferrable     bool   // for TxBegin: true when BEGIN/START TRANSACTION DEFERRABLE
}

func (n *Transaction) Pos() int       { return n.pos }
func (n *Transaction) Output() Schema { return nil }

// Utility — VACUUM / ANALYZE / SHOW / SET / RESET; carries the
// original parser statement.
type Utility struct {
	pos  int
	Stmt parser.Stmt
}

func (n *Utility) Pos() int { return n.pos }
func (n *Utility) Output() Schema {
	switch stmt := n.Stmt.(type) {
	case *parser.ShowStmt:
		if stmt.All {
			return Schema{
				{Name: "name", Type: catalog.Type{Name: "text"}},
				{Name: "setting", Type: catalog.Type{Name: "text"}},
			}
		}
		return Schema{{Name: stmt.Name, Type: catalog.Type{Name: "text"}}}
	}
	return nil
}

// Checkpoint — `CHECKPOINT`. Distinct from Utility because it has
// real side-effects (synchronous flush + WAL marker), wired through
// executor.Context.Checkpointer.
type Checkpoint struct{ pos int }

func (n *Checkpoint) Pos() int       { return n.pos }
func (n *Checkpoint) Output() Schema { return nil }

// Call — `CALL proc(...)`. Holds the parsed call statement so the
// executor can resolve and execute the procedure.
type Call struct {
	pos  int
	Stmt *parser.CallStmt
}

func (n *Call) Pos() int       { return n.pos }
func (n *Call) Output() Schema { return nil }

// Explain — `EXPLAIN <stmt>`. Wraps the planned inner node so
// the executor can render it as a single-column QUERY PLAN
// text result-set without re-running planning. The Options
// field carries the parsed EXPLAIN flags (M0018-0001) — the
// executor consults Format / Verbose to switch between TEXT /
// JSON / verbose-mode rendering. Zero-value Options preserves
// the pre-M0018 bare-EXPLAIN behaviour.
type Explain struct {
	pos     int
	Options parser.ExplainOptions
	Child   Node
}

func (n *Explain) Pos() int { return n.pos }
func (n *Explain) Output() Schema {
	return Schema{SchemaColumn{Name: "QUERY PLAN", Type: catalog.Type{Name: "text"}}}
}

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

// SetOp represents a UNION / INTERSECT / EXCEPT set operation, with an
// optional ALL modifier. UNION ALL streams the two children back-to-back;
// all other variants buffer and apply multiset semantics in the executor
// (operators_setop.go). M0097-0024.
type SetOp struct {
	pos   int
	Left  Node
	Right Node
	// Op is the set-operation kind (UNION / INTERSECT / EXCEPT).
	// The zero value (parser.SetOpUnion) keeps the implicit
	// partition/inheritance UNION ALL construction sites working
	// without an explicit Op assignment.
	Op  parser.SetOpType
	All bool
}

func (n *SetOp) Pos() int       { return n.pos }
func (n *SetOp) Output() Schema { return n.Left.Output() }

// Distinct eliminates duplicate rows from its child, implementing
// SELECT DISTINCT. Deduplication uses the same rowKey hash as the
// recursive UNION dedup path. M0097-0005.
// Gather is the boundary between parallel and serial execution: Child is run
// by WorkersPlanned goroutines whose output this node interleaves in arbitrary
// order. Everything below a Gather is a PARTIAL plan — run by several workers
// simultaneously, it collectively produces the full result exactly once.
//
// P4 of docs/design/parallel-query/ (chapter 05). Nothing in the planner
// inserts one yet; insertion is P6.
//
// Divergence from PostgreSQL: PG's Gather is substantially a transport
// implementation — one shm_mq per worker, tuples serialised in and out — while
// goopg's is a fan-out over a Go channel, since workers share the address
// space. The risk correspondingly moves from "is the transport correct" to "is
// the shutdown correct".
type Gather struct {
	pos   int
	Child Node
	// WorkersPlanned is the worker count chosen at plan time. EXPLAIN renders
	// it as PG's `Workers Planned:`; the number actually launched can be lower
	// when the cluster-wide cap is exhausted, which is why PG reports both.
	WorkersPlanned int
	// SingleCopy mirrors PG's single_copy: run Child in exactly one worker
	// rather than partitioning it. Reserved; nothing sets it yet.
	SingleCopy bool
	schema     Schema
}

func (n *Gather) Pos() int       { return n.pos }
func (n *Gather) Output() Schema { return n.schema }

// NewGather wraps child in a Gather that plans nWorkers workers.
func NewGather(pos int, child Node, nWorkers int) *Gather {
	return &Gather{pos: pos, Child: child, WorkersPlanned: nWorkers, schema: child.Output()}
}

// GatherMerge is Gather with output ordering preserved: each worker produces a
// stream already sorted by Keys, and the leader merges them with a heap.
//
// The node exists — rather than sorting above a plain Gather — so the sort can
// be PARTIAL: N workers each sort their own share in parallel and the leader
// only merges. Sorting above a Gather would serialise the whole sort in the
// leader and give back most of the benefit.
//
// P7 of docs/design/parallel-query/ (chapter 05 §4).
type GatherMerge struct {
	pos            int
	Child          Node
	WorkersPlanned int
	// Keys is the ordering every worker's stream is already sorted by, and
	// which the leader's merge preserves.
	Keys   []SortKey
	schema Schema
}

func (n *GatherMerge) Pos() int       { return n.pos }
func (n *GatherMerge) Output() Schema { return n.schema }

// NewGatherMerge wraps child in an order-preserving Gather.
func NewGatherMerge(pos int, child Node, nWorkers int, keys []SortKey) *GatherMerge {
	return &GatherMerge{pos: pos, Child: child, WorkersPlanned: nWorkers, Keys: keys, schema: child.Output()}
}

type Distinct struct {
	pos    int
	Child  Node
	schema Schema
}

func (n *Distinct) Pos() int       { return n.pos }
func (n *Distinct) Output() Schema { return n.schema }

// DistinctOn implements SELECT DISTINCT ON (expr,...) semantics: the child
// must already be sorted by the DISTINCT ON key columns (as a prefix);
// this node emits only the first row per distinct combination of key values.
// KeyCols holds the output column indices that form the DISTINCT ON key.
// M0097-0005.
type DistinctOn struct {
	pos     int
	Child   Node
	KeyCols []int // indices into the output schema for DISTINCT ON keys
	schema  Schema
}

func (n *DistinctOn) Pos() int       { return n.pos }
func (n *DistinctOn) Output() Schema { return n.schema }

// RecursiveUnion implements a WITH RECURSIVE fixpoint (M0016-0004).
// Anchor is the non-recursive initial SELECT; Recursive is the
// recursive member referencing the CTE name via WorkTableScans.
// When UnionAll is false, duplicate rows are suppressed at each
// iteration step (UNION semantics); iteration stops when the new
// working set contains no rows not already in the output.
type RecursiveUnion struct {
	pos       int
	Anchor    Node
	Recursive Node
	schema    Schema
	UnionAll  bool // true = UNION ALL (append all), false = UNION (dedup)
}

func (n *RecursiveUnion) Pos() int       { return n.pos }
func (n *RecursiveUnion) Output() Schema { return n.schema }

// WorkTableScan reads rows from the RecursiveUnion's working table
// during fixpoint iteration. Only valid inside a RecursiveUnion's
// Recursive subtree.
type WorkTableScan struct {
	pos    int
	schema Schema
}

func (n *WorkTableScan) Pos() int       { return n.pos }
func (n *WorkTableScan) Output() Schema { return n.schema }

// BitmapIndexScan is a leaf node: scan one index and produce a TIDBitmap.
// It is NEVER executed via the standard pull-model Next() — it is a
// MultiExec-style whole-result producer called once by BitmapHeapScan.
// (M0128-P2.3: P2.2 design doc §3.2)
type BitmapIndexScan struct {
	pos   int
	Table *catalog.Table
	Alias string
	Index *catalog.Index
	Key   Expr   // single-column equality (non-nil for equality scan)
	Keys  []Expr // multi-column equality (Keys[i] binds Index.Columns[i])
	// Pred is the full index condition (for recheck). When Key/Keys
	// cover only a prefix, Pred holds the remaining index quals.
	Pred   []Expr
	schema Schema
}

func (n *BitmapIndexScan) Pos() int       { return n.pos }
func (n *BitmapIndexScan) Output() Schema { return n.schema }

// BitmapHeapScan reads a relation via a TID bitmap produced by its outer
// (a BitmapIndexScan or BitmapAnd/BitmapOr tree).
// (M0128-P2.3: P2.2 design doc §3.2)
type BitmapHeapScan struct {
	pos   int
	Table *catalog.Table
	Alias string
	// BitmapQual is the original index qual, re-evaluated per tuple when
	// the bitmap entry is lossy or the index AM requires recheck.
	BitmapQual []Expr
	// Outer is the bitmap-producing subtree (BitmapIndexScan / BitmapAnd / BitmapOr).
	Outer Node
	// Cond is a residual filter evaluated per heap tuple — PostgreSQL's
	// `Filter:` line on a Bitmap Heap Scan, which sits alongside
	// `Recheck Cond:` rather than in a separate node. ColumnRefs are in the
	// scan's OWN output coordinates (leaf-local), the same space
	// `IndexScan.Cond` uses. Nil means no residual filtering.
	//
	// It exists for the same reason `IndexScan.Cond` does: a relation's local
	// quals need somewhere to live INSIDE the node when the scan is a
	// `NestedLoopIndexJoin` inner, because the join re-probes it per outer row
	// and cannot carry the `*Filter` wrappers the quals otherwise sit in. Until
	// this field existed, `addParameterizedBitmapPaths` declined every FILTERED
	// relation — which is four of PG's six TPC-H bitmap scans (Q2, Q11, Q20,
	// Q21), all of which have filtered leaves.
	//
	// Distinct from `BitmapQual`: that is the RECHECK list, re-evaluated only
	// when a bitmap entry is lossy or the AM demands it. `Cond` is evaluated on
	// every tuple, lossy or not.
	Cond   Expr
	schema Schema
	// Parallel mirrors PostgreSQL's Plan.parallel_aware: true when this scan
	// was chosen as the worker-read driving scan under a Gather/GatherMerge
	// (PG sets it in create_bitmap_heap_path, pathnode.c:1115, gated on
	// parallel_degree > 0). Stamped once, at Gather-construction time, by
	// parallel.go's stampParallelScan — NOT inferred at render time (see
	// that function's comment for why). Only affects the "Parallel " EXPLAIN
	// text prefix (operators_explain.go describePlan).
	Parallel bool
}

func (n *BitmapHeapScan) Pos() int       { return n.pos }
func (n *BitmapHeapScan) Output() Schema { return n.schema }

// BitmapAnd combines multiple bitmap sub-trees via intersection.
// (M0128-P2.3: P2.2 design doc §3.2)
type BitmapAnd struct {
	pos    int
	Inputs []Node // []*BitmapIndexScan or nested []*BitmapAnd/[]*BitmapOr
	schema Schema
}

func (n *BitmapAnd) Pos() int       { return n.pos }
func (n *BitmapAnd) Output() Schema { return n.schema }

// BitmapOr combines multiple bitmap sub-trees via union.
// (M0128-P2.3: P2.2 design doc §3.2)
type BitmapOr struct {
	pos    int
	Inputs []Node
	schema Schema
}

func (n *BitmapOr) Pos() int       { return n.pos }
func (n *BitmapOr) Output() Schema { return n.schema }
