// Package planner translates parser.Stmt trees into goopg plan
// nodes. Scope and growth path are documented in
// docs/design/0011-planner.md.
package planner

import (
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

	// Cached parsed N from `Value`. CacheValid signals populated.
	CacheValid bool
	CachedN    int32
}

func (e *IntervalLit) Pos() int { return e.pos }
func (*IntervalLit) exprNode()  {}

// ExtractExpr mirrors parser.ExtractExpr. Field is the
// lower-cased calendar component the executor switches on.
type ExtractExpr struct {
	pos            int
	Field          string
	Source         Expr
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
	pos             int
	Operand         Expr
	Negated         bool
	Plan            Node // populated when the source is a subquery
	List            []Expr
	IsNonCorrelated bool
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
}

func (e *SubqueryExpr) Pos() int { return e.pos }
func (*SubqueryExpr) exprNode()  {}

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
}

func (e *CastExpr) Pos() int { return e.pos }
func (*CastExpr) exprNode()  {}

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
	pos      int
	Name     string
	Args     []Expr
	Star     bool
	Variadic bool // true when args were expanded from VARIADIC array syntax
}

func (e *FuncCall) Pos() int { return e.pos }
func (*FuncCall) exprNode()  {}

// SeqScan — full heap scan of a single relation.
type SeqScan struct {
	pos    int
	Table  *catalog.Table
	Alias  string // FROM-clause alias; empty when not specified
	schema Schema
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
	pos     int
	Table   *catalog.Table
	Alias   string // FROM-clause alias; empty when not specified. M0062-0002
	// preserves the alias when an `IndexScan` is substituted for a
	// `SeqScan` (e.g. by `mhj_input_rewrite`); without it,
	// `buildBindingsPosMap` cannot disambiguate self-joins like Q8's
	// `nation n1, nation n2` after one side flips to IndexScan.
	Index   *catalog.Index
	Key     Expr  // non-nil for single-column equality scan (LowKey==HighKey implied)
	Keys    []Expr // M0054-0006-followup-Q9-composite: multi-column equality probe.
	// Keys[i] binds Index.Columns[i] in declared order. When non-empty, takes
	// priority over Key. len(Keys) == len(Index.Columns) means a full equality
	// probe (no suffix padding); a shorter prefix is rejected by the planner
	// to keep the executor probe path purely equality-shaped.
	LowKey  Expr  // inclusive lower bound for range scan; nil = no lower bound
	HighKey Expr  // inclusive upper bound for range scan; nil = no upper bound
	schema  Schema
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
	pos       int
	Type      JoinType
	Outer     Node
	Inner     *IndexScan
	Predicate Expr // residual filter applied per joined row
	schema    Schema
}

func (n *NestedLoopIndexJoin) Pos() int       { return n.pos }
func (n *NestedLoopIndexJoin) Output() Schema { return n.schema }

// IndexOnlyScan is a covered index scan (M0046-0004): all projected columns
// come from the B-tree index key, so no heap fetch is needed when the
// visibility map reports ALL_VISIBLE for the target page.
//
// The output schema contains ONLY the projected covered columns (a subset
// of the full table schema). When the VM bit is not set for a page the
// executor falls back to a regular heap fetch.
type IndexOnlyScan struct {
	pos     int
	Table   *catalog.Table
	Index   *catalog.Index
	Key     Expr
	LowKey  Expr
	HighKey Expr
	// Covered is the slice of catalog.Column entries that the output schema
	// contains (a subset of Index.Columns, in projection order).
	Covered []catalog.Column
	schema  Schema
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
// memory and hash-table population time. BuildLeft is INNER-only
// because LEFT JOIN's outer-row preservation depends on which
// side drives the probe loop. Merge join sorts both sides on
// their keys and merges the two ordered streams, preserving
// RIGHT/FULL outer-row semantics.
type Join struct {
		pos       int
		Type      JoinType
		Algo      JoinAlgo
		Left      Node
		Right     Node
		Predicate Expr
		LeftKey   Expr // populated when Algo == JoinAlgoHash
		RightKey  Expr
		BuildLeft bool // hash join: build on left input instead of right
		// Lateral marks the right child as referencing the left
		// child's columns through a FROM-clause LATERAL SRF (M0103-0008).
		// The executor must drive the right per-outer-row, binding the
		// left row as the lateral outer slot (BindLateralOuter contract)
		// instead of materialising both sides up front.
		Lateral bool
		schema  Schema
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
	// Filter is the resolved FILTER (WHERE ...) predicate. M0097-0007.
	Filter Expr
	// OrderBy is the ORDER BY clause inside the aggregate call, e.g.
	// array_agg(x ORDER BY y). Only used for ordering-sensitive aggregates.
	OrderBy []SortKey
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
}

func (n *Aggregate) Pos() int       { return n.pos }
func (n *Aggregate) Output() Schema { return n.schema }

// WindowFunc is one supported window-function invocation in a
// WindowAgg node. Stage A supports row_number/rank (no args,
// int8 return); Stage B adds lag/lead (1-3 args, return type
// matches first arg).
type WindowFunc struct {
	pos  int
	Name string
	Type catalog.Type
	Args []Expr // lag/lead: [value, offset?, default?]
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
	schema      Schema
}

func (n *WindowAgg) Pos() int       { return n.pos }
func (n *WindowAgg) Output() Schema { return n.schema }

// Filter — applies a predicate to its child's rows.
type Filter struct {
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
}

func (n *Filter) Pos() int       { return n.pos }
func (n *Filter) Output() Schema { return n.Child.Output() }

// Project — evaluates the target list against its child's rows.
type Project struct {
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

func (n *CTEDMLPrefix) Pos() int      { return n.pos }
func (n *CTEDMLPrefix) nodeTag()      {}
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

func (n *MaterializedCTEScan) Pos() int      { return n.pos }
func (n *MaterializedCTEScan) nodeTag()      {}
func (n *MaterializedCTEScan) Output() Schema { return n.schema }

func (n *CTEScan) Pos() int       { return n.pos }
func (n *CTEScan) Output() Schema { return n.schema }

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

// MultiHashKey is one equijoin edge in a MultiHashJoin node.
type MultiHashKey struct {
	LeftTable  int // index into Tables[]
	LeftCol    int // column index in left table's schema
	RightTable int
	RightCol   int
}

// MultiHashJoin replaces a chain of N binary hash joins with a single
// operator that builds N-1 hash tables from small tables and probes
// one fact table via chain-lookups. Eliminates N-1 intermediate
// result sets. See docs/design/0038-0001-multi-way-hash-join.md.
type MultiHashJoin struct {
	pos        int
	Tables     []Node         // N child plan nodes (SeqScans or simple)
	Keys       []MultiHashKey // N-1 equijoin edges forming a chain
	ProbeTable int            // which child drives the probe loop
	Filters    []Expr         // residual WHERE filters to apply
	schema     Schema         // concatenated child schemas
}

func (n *MultiHashJoin) Pos() int       { return n.pos }
func (n *MultiHashJoin) Output() Schema { return n.schema }

// Limit — caps the number of rows; both fields are optional.
type Limit struct {
	pos    int
	Child  Node
	Limit  Expr // nil when no limit
	Offset Expr // nil when no offset
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
	schema Schema
}

func (n *GenerateSeries) Pos() int       { return n.pos }
func (n *GenerateSeries) Output() Schema { return n.schema }

// PgInputErrorInfo implements pg_input_error_info(value, type) as a
// set-returning function in the FROM clause. Returns 0 rows if the
// input is valid, or 1 row with (message, detail, hint, sql_error_code)
// if it is invalid. M0097-0003.
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
type ProjectSet struct {
	pos     int
	Child   Node
	SrfName string
	SrfArgs []Expr
	schema  Schema
}

func (n *ProjectSet) Pos() int       { return n.pos }
func (n *ProjectSet) Output() Schema { return n.schema }

func (n *PgGetPublicationTables) Pos() int       { return n.pos }
func (n *PgGetPublicationTables) Output() Schema { return n.schema }

func (n *PgInputErrorInfo) Pos() int       { return n.pos }
func (n *PgInputErrorInfo) Output() Schema { return n.schema }

// ScalarFuncScan returns a single row from a scalar function call used in
// the FROM clause (e.g. `FROM parse_ident(...) AS a`). The function result
// is returned as a single column named ColName with type ColType. M0097-0003.
type ScalarFuncScan struct {
	pos     int
	Func    Expr
	schema  Schema
}

func (n *ScalarFuncScan) Pos() int       { return n.pos }
func (n *ScalarFuncScan) Output() Schema { return n.schema }

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
}

func (n *Insert) Pos() int       { return n.pos }
func (n *Insert) Output() Schema { return nil }

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
}

// LockRows is the upstream-shape wrapper that adds row-lock
// acquisition over its child SELECT plan. Mirrors upstream's
// LockRows operator (createplan.c). The planner emits one
// LockRows at the top of the plan tree when the SELECT carries
// any locking clause; pre-M0021 SELECTs never see this node.
//
// Output schema is the child's schema unchanged — locking is a
// side effect on storage, not a row-shape transformation.
type LockRows struct {
	pos   int
	Child Node
	Locks []LockedRel
}

func (n *LockRows) Pos() int       { return n.pos }
func (n *LockRows) Output() Schema { return n.Child.Output() }

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
type Update struct {
	pos             int
	Table           *catalog.Table
	Child           Node
	Set             []Expr // len == len(Table.Columns)
	Returning       []Expr // per-target RETURNING expressions (nil = no RETURNING)
	ReturningSchema Schema // output schema when Returning is non-nil
}

func (n *Update) Pos() int       { return n.pos }
func (n *Update) Output() Schema { return n.ReturningSchema }

// Delete — marks the visible rows of Table that survive the child's
// filter as dead.
type Delete struct {
	pos             int
	Table           *catalog.Table
	Child           Node
	Returning       []Expr
	ReturningSchema Schema
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
	Source          Node            // USING clause scan
	On              Expr            // join condition (source cols at offset len(Target.Columns))
	Clauses         []*MergeWhenClause
	Returning       []Expr          // RETURNING expressions (nil if absent)
	ReturningSchema Schema          // output schema when Returning != nil
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
	TxBegin      TransactionVerb = iota
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
type Distinct struct {
	pos    int
	Child  Node
	schema Schema
}

func (n *Distinct) Pos() int       { return n.pos }
func (n *Distinct) Output() Schema { return n.schema }

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
