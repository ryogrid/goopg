package parser

// Node is implemented by every AST node. Pos returns the byte offset
// of the node's first token, used by error messages.
type Node interface {
	Pos() int
}

// Stmt is implemented by every top-level SQL statement.
type Stmt interface {
	Node
	stmtNode()
}

// ObjectName is a (possibly schema-qualified) relation/object name.
// "schema.table" or just "table". Quoted parts retain their case.
type ObjectName struct {
	pos    int
	Schema string // empty when unqualified
	Name   string
}

func (o ObjectName) Pos() int { return o.pos }

// String renders the name in canonical form (without quoting).
func (o ObjectName) String() string {
	if o.Schema == "" {
		return o.Name
	}
	return o.Schema + "." + o.Name
}

// BeginStmt — `BEGIN [WORK | TRANSACTION]`.
type BeginStmt struct{ pos int }

func (s *BeginStmt) Pos() int  { return s.pos }
func (s *BeginStmt) stmtNode() {}

// CommitStmt — `COMMIT [WORK | TRANSACTION]`. Aliased by `END`.
type CommitStmt struct{ pos int }

func (s *CommitStmt) Pos() int  { return s.pos }
func (s *CommitStmt) stmtNode() {}

// RollbackStmt — `ROLLBACK [WORK | TRANSACTION]`. Aliased by `ABORT`.
type RollbackStmt struct{ pos int }

func (s *RollbackStmt) Pos() int  { return s.pos }
func (s *RollbackStmt) stmtNode() {}

// VacuumStmt — `VACUUM [VERBOSE] [ANALYZE] [target [, …]]`.
type VacuumStmt struct {
	pos     int
	Verbose bool
	Analyze bool
	Targets []ObjectName // empty -> all relations
}

func (s *VacuumStmt) Pos() int  { return s.pos }
func (s *VacuumStmt) stmtNode() {}

// AnalyzeStmt — `ANALYZE [VERBOSE] [target [, …]]`.
type AnalyzeStmt struct {
	pos     int
	Verbose bool
	Targets []ObjectName
}

func (s *AnalyzeStmt) Pos() int  { return s.pos }
func (s *AnalyzeStmt) stmtNode() {}

// CheckpointStmt — `CHECKPOINT`. Triggers a synchronous checkpoint
// (see milestone 0002 §Checkpointing). The bare verb is the only
// form upstream accepts as well.
type CheckpointStmt struct{ pos int }

func (s *CheckpointStmt) Pos() int  { return s.pos }
func (s *CheckpointStmt) stmtNode() {}

// ShowStmt — `SHOW name | SHOW ALL`.
type ShowStmt struct {
	pos  int
	All  bool
	Name string
}

func (s *ShowStmt) Pos() int  { return s.pos }
func (s *ShowStmt) stmtNode() {}

// SetStmt — `SET [LOCAL|SESSION] name = value | TO value | TO DEFAULT`.
type SetStmt struct {
	pos     int
	Local   bool
	Name    string
	Value   string // raw textual value; quotes stripped for string literals
	Default bool   // TO DEFAULT (Value is unused)
}

func (s *SetStmt) Pos() int  { return s.pos }
func (s *SetStmt) stmtNode() {}

// ResetStmt — `RESET name | RESET ALL`.
type ResetStmt struct {
	pos  int
	All  bool
	Name string
}

func (s *ResetStmt) Pos() int  { return s.pos }
func (s *ResetStmt) stmtNode() {}

// ResTarget is one entry in a SELECT target list: `expr [AS alias]`.
type ResTarget struct {
	pos   int
	Alias string // empty when no AS clause
	Expr  Expr
}

func (r ResTarget) Pos() int { return r.pos }

// RangeVar is a relation reference appearing in a FROM clause:
// `[schema.]table [AS alias]`.
type RangeVar struct {
	pos    int
	Schema string
	Name   string
	Alias  string // empty when no AS clause
}

func (r RangeVar) Pos() int { return r.pos }

// JoinType classifies one JOIN variant.
type JoinType int

const (
	JoinInner JoinType = iota
	JoinLeft
	JoinRight
	JoinFull
	JoinCross
)

// JoinExpr is one JOIN clause attached to a FROM base item.
type JoinExpr struct {
	pos     int
	Type    JoinType
	Natural bool
	Right   RangeVar
	On      Expr
	Using   []string
}

func (j JoinExpr) Pos() int { return j.pos }

// FromExpr is one FROM item plus its trailing JOIN chain.
type FromExpr struct {
	pos   int
	Base  RangeVar
	Joins []JoinExpr
}

func (f FromExpr) Pos() int { return f.pos }

// SetOpType classifies SQL set operations.
type SetOpType int

const (
	SetOpUnion SetOpType = iota
	SetOpIntersect
	SetOpExcept
)

// SetOpClause is one trailing UNION/INTERSECT/EXCEPT branch.
// Chains are represented by SetOp on the right-hand SelectStmt.
type SetOpClause struct {
	pos   int
	Type  SetOpType
	All   bool
	Right *SelectStmt
}

func (s SetOpClause) Pos() int { return s.pos }

// SortBy is one entry in an ORDER BY list.
type SortBy struct {
	pos  int
	Expr Expr
	Desc bool // true for DESC, false for ASC (the default)
}

func (s SortBy) Pos() int { return s.pos }

// SelectStmt — `SELECT [DISTINCT] target_list
//
//	[FROM from_item [, …]] [WHERE expr]
//	[GROUP BY expr [, …]] [HAVING expr]
//	[ORDER BY sort_list] [LIMIT n] [OFFSET n]
//	[{UNION|INTERSECT|EXCEPT} [ALL|DISTINCT] SELECT ...].
type SelectStmt struct {
	pos      int
	Distinct bool
	Targets  []ResTarget
	// From keeps a flattened range-var list for v0 planner
	// compatibility.
	From []RangeVar
	// FromExprs preserves explicit JOIN structure.
	FromExprs []FromExpr
	Where     Expr // nil when absent
	GroupBy   []Expr
	Having    Expr
	OrderBy   []SortBy
	Limit     Expr // nil when absent; integer expression in v0
	Offset    Expr // nil when absent
	SetOp     *SetOpClause
}

func (s *SelectStmt) Pos() int  { return s.pos }
func (s *SelectStmt) stmtNode() {}

// InsertStmt — `INSERT INTO target [(col, …)] VALUES (val, …) [, …]
//
//	[RETURNING target_list]`. v0 supports literal VALUES rows only;
//
// `INSERT … SELECT` and ON CONFLICT clauses are deferred.
type InsertStmt struct {
	pos       int
	Target    RangeVar
	Columns   []string // empty when no column list — INSERT defaults to declared order
	Rows      [][]Expr // each row is a parenthesised tuple
	Returning []ResTarget
}

func (s *InsertStmt) Pos() int  { return s.pos }
func (s *InsertStmt) stmtNode() {}

// UpdateAssign is one `column = expr` pair in an UPDATE SET clause.
type UpdateAssign struct {
	pos    int
	Column string
	Expr   Expr
}

func (a UpdateAssign) Pos() int { return a.pos }

// UpdateStmt — `UPDATE target SET col = expr [, …] [WHERE expr]
//
//	[RETURNING target_list]`. FROM-clause joins in UPDATE are
//
// deferred.
type UpdateStmt struct {
	pos       int
	Target    RangeVar
	Set       []UpdateAssign
	Where     Expr // nil when absent
	Returning []ResTarget
}

func (s *UpdateStmt) Pos() int  { return s.pos }
func (s *UpdateStmt) stmtNode() {}

// DeleteStmt — `DELETE FROM target [WHERE expr] [RETURNING target_list]`.
// USING-clause joins in DELETE are deferred.
type DeleteStmt struct {
	pos       int
	Target    RangeVar
	Where     Expr // nil when absent
	Returning []ResTarget
}

func (s *DeleteStmt) Pos() int  { return s.pos }
func (s *DeleteStmt) stmtNode() {}

// ColumnType is the textual type spec on a column or function arg.
// Args are the optional parameter list — `char(22)` carries [22],
// `numeric(10,2)` carries [10, 2]. Schema-qualified type names
// (e.g. `pg_catalog.int4`) populate Schema.
type ColumnType struct {
	pos    int
	Schema string
	Name   string
	Args   []int64
}

func (c ColumnType) Pos() int { return c.pos }

// ColumnDef is one column declaration in CREATE TABLE.
type ColumnDef struct {
	pos     int
	Name    string
	Type    ColumnType
	NotNull bool
	Primary bool // inline `PRIMARY KEY` constraint
}

func (c ColumnDef) Pos() int { return c.pos }

// CreateTableStmt — `CREATE [UNLOGGED] TABLE [IF NOT EXISTS] name
//
//	(column_def [, …]) [WITH (option = value [, …])]`. Foreign keys,
//
// CHECK constraints, partitioning, and inheritance are deferred.
type CreateTableStmt struct {
	pos         int
	IfNotExists bool
	Unlogged    bool
	Name        ObjectName
	Columns     []ColumnDef
	// PrimaryKey holds the column names from a table-level
	// `PRIMARY KEY (a, b)` clause. Inline-on-column primary keys live
	// on ColumnDef.Primary instead.
	PrimaryKey []string
	// With carries the option list from `WITH (k = v, …)`. We keep it
	// as a string→string map; the analyzer interprets known options
	// (fillfactor, autovacuum_*, …) and rejects the rest.
	With map[string]string
}

func (s *CreateTableStmt) Pos() int  { return s.pos }
func (s *CreateTableStmt) stmtNode() {}

// CreateIndexStmt — `CREATE [UNIQUE] INDEX [IF NOT EXISTS] [name]
//
//	ON table [USING method] (col [, …])`. Index-only-on-expression,
//
// WHERE predicates, INCLUDE columns, and storage parameters are
// deferred.
type CreateIndexStmt struct {
	pos         int
	IfNotExists bool
	Unique      bool
	Name        string // empty for "auto-named" indexes (CREATE INDEX ON t (c))
	Table       ObjectName
	Method      string // empty defaults to "btree" downstream
	Columns     []string
}

func (s *CreateIndexStmt) Pos() int  { return s.pos }
func (s *CreateIndexStmt) stmtNode() {}

// DropBehavior is the trailing CASCADE/RESTRICT on DROP statements.
type DropBehavior int

const (
	DropDefault DropBehavior = iota // RESTRICT (the default)
	DropCascade
)

// DropTableStmt — `DROP TABLE [IF EXISTS] name [, …] [CASCADE|RESTRICT]`.
type DropTableStmt struct {
	pos      int
	IfExists bool
	Names    []ObjectName
	Behavior DropBehavior
}

func (s *DropTableStmt) Pos() int  { return s.pos }
func (s *DropTableStmt) stmtNode() {}

// DropIndexStmt — `DROP INDEX [IF EXISTS] name [, …] [CASCADE|RESTRICT]`.
type DropIndexStmt struct {
	pos      int
	IfExists bool
	Names    []ObjectName
	Behavior DropBehavior
}

func (s *DropIndexStmt) Pos() int  { return s.pos }
func (s *DropIndexStmt) stmtNode() {}

// TruncateStmt — `TRUNCATE [TABLE] name [, …] [CASCADE|RESTRICT]`.
type TruncateStmt struct {
	pos      int
	Names    []ObjectName
	Behavior DropBehavior
}

func (s *TruncateStmt) Pos() int  { return s.pos }
func (s *TruncateStmt) stmtNode() {}

// AlterTableActionKind discriminates the per-clause action of an
// ALTER TABLE statement.
type AlterTableActionKind int

const (
	AlterTableAddPrimaryKey AlterTableActionKind = iota
	AlterTableAddColumn
)

// AlterTableAction is one clause inside ALTER TABLE. v0 covers the
// ADD [CONSTRAINT name] PRIMARY KEY (cols) shape pgbench emits and
// the simpler ADD [COLUMN] coldef form.
type AlterTableAction struct {
	pos            int
	Kind           AlterTableActionKind
	ConstraintName string    // optional, ADD CONSTRAINT name PRIMARY KEY …
	Columns        []string  // populated for AddPrimaryKey
	Column         ColumnDef // populated for AddColumn
}

func (a AlterTableAction) Pos() int { return a.pos }

// AlterTableStmt — `ALTER TABLE [IF EXISTS] name action [, action …]`.
// pgbench emits:
//
//	alter table pgbench_branches add primary key (bid)
//
// so v0 supports comma-separated ADD actions; DROP/ALTER COLUMN and
// other variants are deferred.
type AlterTableStmt struct {
	pos      int
	IfExists bool
	Name     ObjectName
	Actions  []AlterTableAction
}

func (s *AlterTableStmt) Pos() int  { return s.pos }
func (s *AlterTableStmt) stmtNode() {}

// CopyDirection records whether a COPY moves data into the table
// (FROM) or out of it (TO).
type CopyDirection int

const (
	CopyFrom CopyDirection = iota
	CopyTo
)

// CopyEndpoint discriminates the file/program/standard-stream sink or
// source for a COPY. v0 implements stdin/stdout end-to-end and parses
// the others so the analyzer can reject them with a stable SQLSTATE.
type CopyEndpoint int

const (
	CopyEndpointStdin CopyEndpoint = iota
	CopyEndpointStdout
	CopyEndpointFile    // 'path/to/file'
	CopyEndpointProgram // PROGRAM 'cmd'
)

// CopyOption is one entry in the WITH (...) option list. Name is
// lower-cased; Value carries the textual value (string literal stripped
// of surrounding quotes, numeric/identifier values verbatim). Bool
// records HEADER's optional explicit value; when the option appears
// without arguments (e.g. `HEADER`, `FREEZE`), Bool is true and Value
// is empty. Star/Cols carry FORCE_QUOTE/FORCE_NOT_NULL/FORCE_NULL
// payloads.
type CopyOption struct {
	pos   int
	Name  string
	Value string
	Bool  bool
	Star  bool
	Cols  []string
}

func (o CopyOption) Pos() int { return o.pos }

// CopyStmt — `COPY (table [(cols)] | (query)) {FROM|TO}
//
//	{ 'file' | PROGRAM 'cmd' | STDIN | STDOUT } [WITH] [(option, …)]`.
//
// Exactly one of Table/Query is set; Columns is empty for query-form
// COPY TO. Filename is populated when Endpoint == CopyEndpointFile or
// CopyEndpointProgram. Options carries the WITH list verbatim — the
// analyzer interprets known names and rejects the rest.
type CopyStmt struct {
	pos       int
	Direction CopyDirection
	Table     ObjectName  // empty when Query is set
	Columns   []string    // empty when no column list
	Query     *SelectStmt // populated for COPY (query) TO …
	Endpoint  CopyEndpoint
	Filename  string // filename or program command, when applicable
	Options   []CopyOption
}

func (s *CopyStmt) Pos() int  { return s.pos }
func (s *CopyStmt) stmtNode() {}
