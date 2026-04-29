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

// ExplainFormat selects the rendering format for ExplainStmt
// output. M0018-0001 introduces the AST shape; the renderer
// honours TEXT today and JSON in 0018-0002 / 0018-0004.
type ExplainFormat int

const (
	ExplainFormatText ExplainFormat = iota
	ExplainFormatJSON
)

// ExplainOptions carries the parsed EXPLAIN options (M0018-0001).
// All flags default to their PG-zero-value form so a bare
// `EXPLAIN <stmt>` (no keyword form, no parenthesised list)
// produces an ExplainOptions struct that's byte-for-byte
// equivalent to the pre-M0018 zero-value behaviour.
//
// `Set` (M0018-0004) tracks which options the user wrote
// explicitly. The executor consults it under ANALYZE to
// distinguish "user said off" (Set.Timing=true && Timing=false)
// from "user said nothing" (Set.Timing=false) — the latter
// defaults to ON matching upstream's default-true semantics.
type ExplainOptions struct {
	Analyze  bool
	Verbose  bool
	Costs    bool
	Buffers  bool
	Settings bool
	Timing   bool
	Summary  bool
	Format   ExplainFormat
	Set      ExplainOptionsSet
}

// ExplainOptionsSet records which ExplainOptions fields the user
// wrote explicitly. Allows the executor to distinguish a user-set
// false from an unset zero-value bool.
type ExplainOptionsSet struct {
	Analyze, Verbose, Costs, Buffers, Settings, Timing, Summary, Format bool
}

// ExplainStmt — `EXPLAIN [ANALYZE] [VERBOSE] <stmt>` or
// `EXPLAIN (option [VALUE], ...) <stmt>`. Both surface forms
// produce the same AST shape. The Options field carries the
// parsed flags; v0 renderer (`internal/executor/operators_explain.go`)
// consumes Format today and the rest land alongside their
// renderer / instrumentation slices.
//
// Pre-M0018 `EXPLAIN <stmt>` callers see Options == zero-value
// (no flags, Format=Text) which is byte-for-byte the
// pre-existing behaviour.
type ExplainStmt struct {
	pos     int
	Options ExplainOptions
	Inner   Stmt
}

func (s *ExplainStmt) Pos() int  { return s.pos }
func (s *ExplainStmt) stmtNode() {}

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
// `[schema.]table [AS alias]`. Subquery is non-nil when the
// FROM item is a derived table — `(SELECT …) AS alias`. In that
// case Name is empty and the parsed inner SELECT lives in
// Subquery; the alias is required (matches upstream).
type RangeVar struct {
	pos      int
	Schema   string
	Name     string
	Alias    string // empty when no AS clause
	Subquery *SelectStmt
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

// CommonTableExpr is one `WITH cte_name [(col, ...)] AS (SELECT ...)`
// entry. Stage A of M0016 restricts the body to a SelectStmt; the
// parser rejects data-modifying CTE bodies (INSERT / UPDATE /
// DELETE inside the parenthesised body) at parse time. See
// docs/design/0016-0001-with-parser-ast-and-name-resolution.md.
type CommonTableExpr struct {
	pos     int
	Name    string
	Columns []string // optional column-alias list; nil when absent
	Query   *SelectStmt
}

// Pos returns the position of the CTE's declaring identifier.
func (c *CommonTableExpr) Pos() int { return c.pos }

// WithClause is the optional `WITH [RECURSIVE] cte [, ...]` prefix
// on SELECT / INSERT / UPDATE / DELETE. The parser accepts the
// RECURSIVE keyword even though execution support lands in
// 0016-0003 — clean syntax-level errors are the same regardless of
// execution support.
type WithClause struct {
	pos       int
	Recursive bool
	CTEs      []*CommonTableExpr
}

// Pos returns the position of the WITH keyword.
func (w *WithClause) Pos() int { return w.pos }

// SelectStmt — `[WITH ...] SELECT [DISTINCT] target_list
//
//	[FROM from_item [, …]] [WHERE expr]
//	[GROUP BY expr [, …]] [HAVING expr]
//	[ORDER BY sort_list] [LIMIT n] [OFFSET n]
//	[{UNION|INTERSECT|EXCEPT} [ALL|DISTINCT] SELECT ...].
//
// The optional `With` field (M0016-0001) is nil when no WITH
// clause precedes the SELECT — pre-M0016 tests are byte-for-byte
// unchanged.
// LockStrength enumerates the row-locking strength a `FOR …` clause
// requests. v0 supports the two upstream strengths most commonly
// emitted by ORMs: `FOR UPDATE` (write intent) and `FOR SHARE`
// (read intent). `FOR NO KEY UPDATE` / `FOR KEY SHARE` are
// upstream extensions out of scope for M0021 — adding them is a
// future loop on top of the same AST shape.
type LockStrength int

const (
	// LockStrengthForUpdate — `FOR UPDATE`. Write-intent row
	// lock; mirrors upstream's LCS_FORUPDATE.
	LockStrengthForUpdate LockStrength = iota + 1
	// LockStrengthForShare — `FOR SHARE`. Read-intent row lock;
	// mirrors upstream's LCS_FORSHARE.
	LockStrengthForShare
)

// LockWaitPolicy enumerates the wait modifier on a row-locking
// clause. Block (the default) waits until the lock is released or
// a deadlock is detected; NoWait fails immediately on contention;
// SkipLocked silently omits contended rows from the result.
type LockWaitPolicy int

const (
	// LockWaitBlock is the default — wait for contended row locks.
	LockWaitBlock LockWaitPolicy = iota
	// LockWaitNoWait — `NOWAIT`. Fail with SQLSTATE 55P03 on
	// contention. Mirrors upstream's LockWaitError.
	LockWaitNoWait
	// LockWaitSkipLocked — `SKIP LOCKED`. Silently drop contended
	// rows. Mirrors upstream's LockWaitSkip.
	LockWaitSkipLocked
)

// LockingClause holds one parsed `FOR { UPDATE | SHARE } [ OF
// table_name [, …] ] [ NOWAIT | SKIP LOCKED ]` tail. PostgreSQL
// allows multiple locking clauses per SELECT (each with its own OF
// list and wait policy) so we store them as an ordered slice on
// SelectStmt.
//
// Targets is empty when no `OF` list was supplied — the lock
// applies to every range variable in the FROM clause; otherwise
// each name must resolve to a FROM-clause alias / table at
// analyze time.
type LockingClause struct {
	pos        int
	Strength   LockStrength
	Targets    []string
	WaitPolicy LockWaitPolicy
}

// Pos returns the position of the leading `FOR` keyword.
func (c *LockingClause) Pos() int { return c.pos }

type SelectStmt struct {
	pos      int
	With     *WithClause
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
	// Locking holds parsed `FOR UPDATE / FOR SHARE [OF …]
	// [NOWAIT | SKIP LOCKED]` clauses (M0021-0001). Empty for
	// every pre-M0021 SELECT — preserves byte-for-byte
	// invariance across the existing parser/planner test suite.
	Locking []*LockingClause
}

func (s *SelectStmt) Pos() int  { return s.pos }
func (s *SelectStmt) stmtNode() {}

// InsertStmt — `[WITH ...] INSERT INTO target [(col, …)]
//
//	VALUES (val, …) [, …] [ON CONFLICT …] [RETURNING target_list]`.
//
// v0 supports literal VALUES rows only; `INSERT … SELECT` is
// deferred. The optional `With` field (M0016-0001) is nil when no
// WITH clause precedes the INSERT. The optional `OnConflict` field
// (M0017-0001) is nil when no ON CONFLICT clause is present —
// existing INSERT call sites are byte-for-byte unchanged.
type InsertStmt struct {
	pos        int
	With       *WithClause
	Target     RangeVar
	Columns    []string // empty when no column list — INSERT defaults to declared order
	Rows       [][]Expr // each row is a parenthesised tuple
	OnConflict *OnConflictClause
	Returning  []ResTarget
}

// OnConflictAction enumerates the action the conflict resolver runs
// when an inserted row collides with an existing one. Mirrors
// upstream's `OnConflictAction` enum (parsenodes.h).
type OnConflictAction int

const (
	// OnConflictNone is the zero value reserved for "no ON
	// CONFLICT clause" — InsertStmt.OnConflict==nil — so a stray
	// zero-value OnConflictClause never gets misread as a valid
	// action.
	OnConflictNone OnConflictAction = iota
	// OnConflictNothing — `ON CONFLICT … DO NOTHING`. Skips
	// conflicting rows silently.
	OnConflictNothing
	// OnConflictUpdate — `ON CONFLICT … DO UPDATE SET … [WHERE …]`.
	// Re-applies the SET assignments to the existing row.
	OnConflictUpdate
)

// OnConflictTarget is the conflict-arbiter spec: which unique
// constraint or set of columns participates in conflict detection.
// Exactly one of (Columns, Constraint) is populated for the two
// upstream forms; both nil/empty means the no-target shape
// `ON CONFLICT DO NOTHING`. The constraint-name form lands as Stage
// B in M0017; the parser already accepts it so the AST shape is
// stable across stages.
type OnConflictTarget struct {
	pos        int
	Columns    []string // populated for `ON CONFLICT (col [, col, …])`
	Constraint string   // populated for `ON CONFLICT ON CONSTRAINT name`
}

// Pos returns the position of the leading token of the target
// (the `(` for the column-list form or `ON` for the constraint-name
// form).
func (t OnConflictTarget) Pos() int { return t.pos }

// OnConflictClause holds the parsed `ON CONFLICT …` tail of an
// INSERT statement. Target is nil for the no-target `ON CONFLICT
// DO NOTHING` form. UpdateSet / UpdateWhere are populated only when
// Action == OnConflictUpdate.
type OnConflictClause struct {
	pos         int
	Target      *OnConflictTarget
	Action      OnConflictAction
	UpdateSet   []UpdateAssign // populated when Action == OnConflictUpdate
	UpdateWhere Expr           // optional; nil when no WHERE follows the SET list
}

// Pos returns the position of the leading `ON` token.
func (c *OnConflictClause) Pos() int { return c.pos }

func (s *InsertStmt) Pos() int  { return s.pos }
func (s *InsertStmt) stmtNode() {}

// UpdateAssign is one `column = expr` pair in an UPDATE SET clause.
type UpdateAssign struct {
	pos    int
	Column string
	Expr   Expr
}

func (a UpdateAssign) Pos() int { return a.pos }

// UpdateStmt — `[WITH ...] UPDATE target SET col = expr [, …]
//
//	[WHERE expr] [RETURNING target_list]`. FROM-clause joins in
//
// UPDATE are deferred. The optional `With` field (M0016-0001) is
// nil when no WITH clause precedes the UPDATE.
type UpdateStmt struct {
	pos       int
	With      *WithClause
	Target    RangeVar
	Set       []UpdateAssign
	Where     Expr // nil when absent
	Returning []ResTarget
}

func (s *UpdateStmt) Pos() int  { return s.pos }
func (s *UpdateStmt) stmtNode() {}

// DeleteStmt — `[WITH ...] DELETE FROM target [WHERE expr]
//
//	[RETURNING target_list]`. USING-clause joins in DELETE are
//
// deferred. The optional `With` field (M0016-0001) is nil when no
// WITH clause precedes the DELETE.
type DeleteStmt struct {
	pos       int
	With      *WithClause
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

// CreateViewStmt — `CREATE [OR REPLACE] VIEW name [(col_list)] AS
// <select>`. v0 stores the parsed inner SELECT in the catalog;
// LookupTable returns the view as a Table whose VirtualRows
// hook materialises the SELECT at query time. Required for
// HammerDB TPC-H Q15 (`create or replace view
// revenue$<position> (supplier_no, total_revenue) as select
// …`).
type CreateViewStmt struct {
	pos       int
	OrReplace bool
	Name      ObjectName
	Columns   []string // optional explicit column-name list
	Query     *SelectStmt
}

func (s *CreateViewStmt) Pos() int  { return s.pos }
func (s *CreateViewStmt) stmtNode() {}

// DropViewStmt — `DROP VIEW [IF EXISTS] name [, …] [CASCADE|RESTRICT]`.
type DropViewStmt struct {
	pos      int
	IfExists bool
	Names    []ObjectName
	Behavior DropBehavior
}

func (s *DropViewStmt) Pos() int  { return s.pos }
func (s *DropViewStmt) stmtNode() {}

// CreatePublicationStmt — `CREATE PUBLICATION name [FOR ALL TABLES |
// FOR TABLE t1 [, t2 ...]] [WITH (k = v, ...)]`. v0 honours
// `publish = 'insert,update,delete'`; truncate / row filters /
// column lists are out of scope. See
// docs/design/0008-0003-publication-subscription-ddl.md.
type CreatePublicationStmt struct {
	pos       int
	Name      string
	AllTables bool
	Tables    []ObjectName
	With      map[string]string
}

func (s *CreatePublicationStmt) Pos() int  { return s.pos }
func (s *CreatePublicationStmt) stmtNode() {}

// DropPublicationStmt — `DROP PUBLICATION [IF EXISTS] name`.
type DropPublicationStmt struct {
	pos      int
	IfExists bool
	Name     string
}

func (s *DropPublicationStmt) Pos() int  { return s.pos }
func (s *DropPublicationStmt) stmtNode() {}

// CreateSubscriptionStmt — `CREATE SUBSCRIPTION name CONNECTION 'conninfo'
// PUBLICATION pub [, pub2 ...] [WITH (slot_name = ..., enabled = ...)]`.
type CreateSubscriptionStmt struct {
	pos          int
	Name         string
	Conninfo     string
	Publications []string
	With         map[string]string
}

func (s *CreateSubscriptionStmt) Pos() int  { return s.pos }
func (s *CreateSubscriptionStmt) stmtNode() {}

// DropSubscriptionStmt — `DROP SUBSCRIPTION [IF EXISTS] name`.
type DropSubscriptionStmt struct {
	pos      int
	IfExists bool
	Name     string
}

func (s *DropSubscriptionStmt) Pos() int  { return s.pos }
func (s *DropSubscriptionStmt) stmtNode() {}

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
	// AlterTableAddForeignKey is `ADD [CONSTRAINT name] FOREIGN
	// KEY (cols) REFERENCES table (cols) [NOT DEFERRABLE |
	// DEFERRABLE]`. v0 parses it for compatibility with HammerDB
	// TPC-H's post-load FK pass; enforcement is a no-op until
	// the constraint subsystem lands. See
	// docs/design/0003-0004-hammerdb-tpch-integration.md.
	AlterTableAddForeignKey
)

// AlterTableAction is one clause inside ALTER TABLE. v0 covers the
// ADD [CONSTRAINT name] PRIMARY KEY (cols) shape pgbench emits, the
// simpler ADD [COLUMN] coldef form, and the ADD CONSTRAINT name
// FOREIGN KEY (cols) REFERENCES table (cols) shape HammerDB TPC-H
// uses for its post-load FK pass.
type AlterTableAction struct {
	pos            int
	Kind           AlterTableActionKind
	ConstraintName string    // optional, ADD CONSTRAINT name PRIMARY KEY …
	Columns        []string  // populated for AddPrimaryKey + AddForeignKey (local cols)
	Column         ColumnDef // populated for AddColumn

	// Foreign-key extras (only populated for AddForeignKey).
	RefTable   ObjectName
	RefColumns []string
	Deferrable bool // true if `DEFERRABLE`; false (default) if NOT DEFERRABLE or omitted
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

// FuncArgMode classifies a routine parameter direction. Stage A only
// recognises IN (the default); OUT / INOUT / VARIADIC arrive in Stage
// B. Surface them at the parser level only when ready so unsupported
// modes return clean syntax errors instead of being silently lost.
type FuncArgMode int

const (
	// FuncArgIn is the default — input parameter. The Stage A
	// surface accepts the explicit `IN` keyword too (no behavioural
	// difference) so handwritten functions migrated from upstream
	// don't trip on it.
	FuncArgIn FuncArgMode = iota
)

// FunctionArg is one entry in a function's argument list. Name is
// optional (PostgreSQL allows positional-only args); Type is the
// declared SQL type; Default carries the parsed default expression
// when one was given (`arg INT DEFAULT 0`).
//
// Stage A pins `Mode` to `FuncArgIn` for every accepted argument —
// OUT / INOUT / VARIADIC remain Stage B. The field exists on the
// struct now so step 2's analyzer/runtime work doesn't have to
// retrofit the AST shape.
type FunctionArg struct {
	pos     int
	Name    string // empty for positional-only args
	Mode    FuncArgMode
	Type    ColumnType
	Default Expr // nil when no DEFAULT was given
}

func (a FunctionArg) Pos() int { return a.pos }

// CreateFunctionStmt — `CREATE [OR REPLACE] FUNCTION name([args])
// RETURNS rettype [LANGUAGE lang] AS $$body$$`. Stage A scope
// (M0015): function-first delivery — parser surface only, body
// stored as the raw source string captured between the dollar-quote
// delimiters. Analyzer / planner / executor wiring lands in
// subsequent loops; the analyzer rejects this statement now with a
// clean SQLSTATE 0A000.
//
// Language defaults to "sql" upstream; goopg requires it explicit
// for now so future PL/* additions are unambiguous. Stage A accepts
// `plpgsql` (case-insensitive) and `sql`; the analyzer is the gate
// that decides which languages run.
//
// See docs/design/0015-0001-create-function-parser-and-ast.md.
type CreateFunctionStmt struct {
	pos       int
	OrReplace bool
	Name      ObjectName
	Args      []FunctionArg
	ReturnType ColumnType
	Language  string // lower-cased, e.g. "plpgsql"
	Body      string // raw source between the dollar-quote delimiters
}

func (s *CreateFunctionStmt) Pos() int  { return s.pos }
func (s *CreateFunctionStmt) stmtNode() {}

// DropFunctionStmt — `DROP FUNCTION [IF EXISTS] name [(arg_decl,
// …)] [CASCADE|RESTRICT]`. Multi-target DROP (comma-separated
// names) is upstream syntax but rare in practice and out of Stage A
// scope; supporting one name keeps the slice small. The optional
// argument list is stored verbatim so a later loop can implement
// overload-resolution drop semantics without re-parsing.
type DropFunctionStmt struct {
	pos      int
	IfExists bool
	Name     ObjectName
	Args     []FunctionArg // nil when no parenthesised arg list was given
	Behavior DropBehavior
}

func (s *DropFunctionStmt) Pos() int  { return s.pos }
func (s *DropFunctionStmt) stmtNode() {}
