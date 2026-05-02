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

// InExpr mirrors parser.InExpr after subquery / value-list
// resolution. Either Plan or List is non-nil. v0 supports the
// uncorrelated form only — the executor evaluates the inner
// once, builds a set, then probes per outer row.
type InExpr struct {
	pos     int
	Operand Expr
	Negated bool
	Plan    Node // populated when the source is a subquery
	List    []Expr
}

func (e *InExpr) Pos() int { return e.pos }
func (*InExpr) exprNode()  {}

// ExistsExpr mirrors parser.ExistsExpr. The executor opens the
// inner plan, asks for one row, and reports the bool. NOT
// EXISTS is the same path with the result negated.
type ExistsExpr struct {
	pos     int
	Negated bool
	Plan    Node
}

func (e *ExistsExpr) Pos() int { return e.pos }
func (*ExistsExpr) exprNode()  {}

// SubqueryExpr mirrors parser.SubqueryExpr after the inner
// SELECT has been planned. The executor opens / drains /
// closes Plan once at evaluation time and returns the single
// cell as the expression's value. Multi-row / multi-column
// subqueries trigger a runtime error.
type SubqueryExpr struct {
	pos  int
	Plan Node
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
type ColumnRef struct {
	pos   int
	Index int
	Name  string // resolved column name (for diagnostics)
	Type  catalog.Type
}

func (e *ColumnRef) Pos() int { return e.pos }
func (*ColumnRef) exprNode()  {}

// OuterColumnRef refers to a column in an enclosing query's
// row, set by the executor's outer-row stack before opening a
// correlated subquery. Level is 1-based (1 = immediate parent
// scope) — matches upstream's Var.varlevelsup. Only emitted
// by the planner when a ColumnRef in a subquery resolves up
// the parent chain instead of locally.
type OuterColumnRef struct {
	pos   int
	Level int
	Index int
	Name  string
	Type  catalog.Type
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

// WindowFunc is one supported window-function invocation in a
// WindowAgg node. Stage A supports row_number/rank only; both
// return int8.
type WindowFunc struct {
	pos  int
	Name string
	Type catalog.Type
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
//
// OnConflict carries the resolved `ON CONFLICT …` action (M0017-0002).
// nil for a plain INSERT — every existing test path keeps that
// nil-default. Non-nil for the upstream-compatible UPSERT shape.
type Insert struct {
	pos         int
	Table       *catalog.Table
	Source      Node
	ColumnIndex []int
	OnConflict  *OnConflictPlan
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
	// lookup.
	ArbiterColumns []int

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

// SetOp represents a UNION [ALL] set operation. v0 supports UNION ALL
// only; UNION DISTINCT, INTERSECT, and EXCEPT are rejected by the
// analyzer.
type SetOp struct {
	pos   int
	Left  Node
	Right Node
	All   bool
}

func (n *SetOp) Pos() int       { return n.pos }
func (n *SetOp) Output() Schema { return n.Left.Output() }

// RecursiveUnion implements a WITH RECURSIVE fixpoint (M0016-0004).
// Anchor is the non-recursive initial SELECT; Recursive is the
// recursive member referencing the CTE name via WorkTableScans.
type RecursiveUnion struct {
	pos       int
	Anchor    Node
	Recursive Node
	schema    Schema
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
