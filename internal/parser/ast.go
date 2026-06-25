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

// BeginStmt — `BEGIN [WORK | TRANSACTION] [transaction_mode ...]`.
//
// IsolationLevel holds the parsed isolation level name (e.g. "read committed",
// "repeatable read", "serializable", "read uncommitted") when ISOLATION LEVEL
// was supplied; "" means use the session default.  M0096-0002.
type BeginStmt struct {
	pos            int
	IsolationLevel string // "" = use session default
	ReadOnly       bool   // true when START TRANSACTION READ ONLY / BEGIN READ ONLY
	// Deferrable is true when the DEFERRABLE transaction mode was supplied
	// (and NOT cleared by a later NOT DEFERRABLE). For a SERIALIZABLE READ ONLY
	// transaction it requests the GetSafeSnapshot deferral (predicate.c): the
	// transaction waits for a safe snapshot instead of risking a 40001 abort.
	// M0118-0001 (read-only-anomaly-3).
	Deferrable bool
}

func (s *BeginStmt) Pos() int  { return s.pos }
func (s *BeginStmt) stmtNode() {}

// SetTransactionStmt — `SET [LOCAL] TRANSACTION transaction_mode ...`.
// Currently only parses ISOLATION LEVEL; other modes (READ ONLY / READ WRITE,
// DEFERRABLE) are accepted syntactically but silently ignored.  M0096-0002.
type SetTransactionStmt struct {
	pos            int
	IsolationLevel string // "" = not specified; set isolation level on session
	Local          bool
}

func (s *SetTransactionStmt) Pos() int  { return s.pos }
func (s *SetTransactionStmt) stmtNode() {}

// CommitStmt — `COMMIT [WORK | TRANSACTION]`. Aliased by `END`.
type CommitStmt struct{ pos int }

func (s *CommitStmt) Pos() int  { return s.pos }
func (s *CommitStmt) stmtNode() {}

// RollbackStmt — `ROLLBACK [WORK | TRANSACTION]`. Aliased by `ABORT`.
type RollbackStmt struct{ pos int }

func (s *RollbackStmt) Pos() int  { return s.pos }
func (s *RollbackStmt) stmtNode() {}

// SavepointStmt — `SAVEPOINT name`.
type SavepointStmt struct {
	pos  int
	Name string
}

func (s *SavepointStmt) Pos() int  { return s.pos }
func (s *SavepointStmt) stmtNode() {}

// ReleaseSavepointStmt — `RELEASE [SAVEPOINT] name`.
type ReleaseSavepointStmt struct {
	pos  int
	Name string
}

func (s *ReleaseSavepointStmt) Pos() int  { return s.pos }
func (s *ReleaseSavepointStmt) stmtNode() {}

// RollbackToSavepointStmt — `ROLLBACK TO [SAVEPOINT] name`.
type RollbackToSavepointStmt struct {
	pos  int
	Name string
}

func (s *RollbackToSavepointStmt) Pos() int  { return s.pos }
func (s *RollbackToSavepointStmt) stmtNode() {}

// VacuumStmt — `VACUUM [(opt [, opt …])] [target [, …]]`.
// Both legacy syntax (VACUUM [VERBOSE] [ANALYZE] [FULL] [FREEZE] …)
// and parenthesized syntax (VACUUM (option …) …) are accepted.
// Most parenthesized-only options are stored but treated as no-ops
// at execution time; they exist so vacuumdb SQL round-trips without
// a parser error.
type VacuumStmt struct {
	pos int
	// Options settable via both legacy and parenthesized syntax.
	Verbose bool
	Analyze bool
	Full    bool
	Freeze  bool
	// Options only in parenthesized syntax (treated as no-ops at execution).
	DisablePageSkipping bool
	NoIndexCleanup      bool
	ForceIndexCleanup   bool
	NoTruncate          bool
	NoProcessMain       bool
	NoProcessToast      bool
	SkipDatabaseStats   bool
	OnlyDatabaseStats   bool
	SkipLocked          bool
	ParallelWorkers     int          // -1 = not specified
	BufferUsageLimit    string       // "" = not specified
	Targets             []ObjectName // empty -> all relations
}

func (s *VacuumStmt) Pos() int  { return s.pos }
func (s *VacuumStmt) stmtNode() {}

// AnalyzeStmt — `ANALYZE [(opt [, opt …])] [target [, …]]`.
type AnalyzeStmt struct {
	pos        int
	Verbose    bool
	SkipLocked bool
	Targets    []ObjectName
}

func (s *AnalyzeStmt) Pos() int  { return s.pos }
func (s *AnalyzeStmt) stmtNode() {}

// ReindexStmt — `REINDEX [(VERBOSE)] [CONCURRENTLY]
// {INDEX|TABLE|DATABASE|SCHEMA|SYSTEM} [IF EXISTS] name`.
// M0095-0005: no-op executor stub; parser accepts the full syntax so
// reindexdb can interact with goopg without syntax errors.
type ReindexStmt struct {
	pos          int
	Verbose      bool
	Concurrently bool
	// Object type: one of "INDEX", "TABLE", "DATABASE", "SCHEMA", "SYSTEM".
	ObjectType string
	Name       string // qualified relation / database / schema name
}

func (s *ReindexStmt) Pos() int  { return s.pos }
func (s *ReindexStmt) stmtNode() {}

// PrepareStmt — `PREPARE name [(param_type, …)] AS query`.
// M0096-0006: executor stores the query text keyed by name for later EXECUTE.
type PrepareStmt struct {
	pos        int
	Name       string
	ParamTypes []string // declared parameter types; nil if no list was given
	Query      Stmt     // the SELECT/INSERT/UPDATE/DELETE being prepared
}

func (s *PrepareStmt) Pos() int  { return s.pos }
func (s *PrepareStmt) stmtNode() {}

// ExecuteStmt — `EXECUTE name [(param, …)]`.
// M0096-0006: executor retrieves and runs the named prepared statement.
type ExecuteStmt struct {
	pos    int
	Name   string
	Params []Expr
}

func (s *ExecuteStmt) Pos() int  { return s.pos }
func (s *ExecuteStmt) stmtNode() {}

// DeallocateStmt — `DEALLOCATE [PREPARE] name | ALL`.
// M0096-0006: removes a named prepared statement (or all).
type DeallocateStmt struct {
	pos  int
	Name string // "" means ALL
}

func (s *DeallocateStmt) Pos() int  { return s.pos }
func (s *DeallocateStmt) stmtNode() {}

// ── CURSOR (M0097-0003) ─────────────────────────────────────────────────────

// DeclareCursorStmt — `DECLARE name [SCROLL|NO SCROLL] CURSOR [WITH|WITHOUT HOLD] FOR query`.
type DeclareCursorStmt struct {
	pos   int
	Name  string
	Query Stmt // the SELECT
}

func (s *DeclareCursorStmt) Pos() int  { return s.pos }
func (s *DeclareCursorStmt) stmtNode() {}

// FetchStmt — `FETCH [FORWARD|BACKWARD] [ALL|n] [FROM|IN] cursor_name`.
// Count < 0 means ALL (fetch all remaining rows).
type FetchStmt struct {
	pos        int
	CursorName string
	Count      int64 // < 0 means ALL
	Forward    bool  // true = FORWARD (default), false = BACKWARD
}

func (s *FetchStmt) Pos() int  { return s.pos }
func (s *FetchStmt) stmtNode() {}

// CloseStmt — `CLOSE {cursor_name|ALL}`.
type CloseStmt struct {
	pos  int
	Name string // "" means ALL
}

func (s *CloseStmt) Pos() int  { return s.pos }
func (s *CloseStmt) stmtNode() {}

// ── MERGE INTO (M0096-0010) ─────────────────────────────────────────────────

// MergeActionKind discriminates the action in a MERGE WHEN clause.
type MergeActionKind int

const (
	MergeActionUpdate MergeActionKind = iota + 1
	MergeActionDelete
	MergeActionInsert
	// MergeActionDoNothing — WHEN … THEN DO NOTHING. M0097-0016.
	MergeActionDoNothing
)

// MergeWhenClause describes one WHEN MATCHED / WHEN NOT MATCHED arm.
type MergeWhenClause struct {
	pos     int
	Matched bool // true = WHEN MATCHED, false = WHEN NOT MATCHED
	// BySource is true for WHEN NOT MATCHED BY SOURCE. M0097-0016.
	BySource bool
	// ByTarget is true for WHEN NOT MATCHED BY TARGET (same as NOT MATCHED). M0097-0016.
	ByTarget  bool
	Condition Expr // optional AND condition; nil when absent
	Action    MergeActionKind

	// For UPDATE: set assignments and optional WHERE.
	UpdateAssigns []UpdateAssign

	// For INSERT: column list and value expressions.
	InsertColumns []string
	InsertValues  []Expr // nil → DEFAULT VALUES
}

func (w *MergeWhenClause) Pos() int { return w.pos }

// MergeStmt — `MERGE INTO target USING source ON cond WHEN … THEN …`.
// M0096-0010.
type MergeStmt struct {
	pos     int
	Target  RangeVar // merge target table (with optional alias)
	Source  RangeVar // USING source (table or subquery, with alias)
	On      Expr     // join condition
	Clauses []*MergeWhenClause
	// Returning holds the RETURNING target list. M0097-0016.
	// Parsed but not executed (v0 no-op).
	Returning []ResTarget
}

func (s *MergeStmt) Pos() int  { return s.pos }
func (s *MergeStmt) stmtNode() {}

// TriggerEvent discriminates BEFORE/AFTER and INSERT/UPDATE/DELETE.
// M0096-0012.
type TriggerTiming int

const (
	TriggerBefore TriggerTiming = iota + 1
	TriggerAfter
	TriggerInsteadOf
)

// CreateTriggerStmt — `CREATE [CONSTRAINT] TRIGGER name
//
//	BEFORE|AFTER|INSTEAD OF {INSERT|UPDATE|DELETE[, ...]}
//	ON table FOR [EACH] {ROW|STATEMENT}
//	EXECUTE {FUNCTION|PROCEDURE} funcname()`.
//
// M0096-0012.
type CreateTriggerStmt struct {
	pos        int
	Name       string
	Table      ObjectName
	Timing     TriggerTiming
	Events     []string // "insert", "update", "delete", "truncate"
	ForEachRow bool     // true = ROW, false = STATEMENT
	FuncName   ObjectName
	FuncArgs   []string // trigger function arguments (EXECUTE PROCEDURE name('arg1', 'arg2'))
	// IfNotExists: PostgreSQL 14+ only, not supported yet.
}

func (s *CreateTriggerStmt) Pos() int  { return s.pos }
func (s *CreateTriggerStmt) stmtNode() {}

// DropRuleStmt — `DROP RULE [IF EXISTS] name ON table [CASCADE|RESTRICT]`.
// Rules are not implemented; the executor emits "rule does not exist" always.
type DropRuleStmt struct {
	pos      int
	Name     string
	Table    ObjectName
	IfExists bool
}

func (s *DropRuleStmt) Pos() int  { return s.pos }
func (s *DropRuleStmt) stmtNode() {}

// DropTriggerStmt — `DROP TRIGGER [IF EXISTS] name ON table [CASCADE|RESTRICT]`.
// M0096-0012.
type DropTriggerStmt struct {
	pos      int
	Name     string
	Table    ObjectName
	IfExists bool
}

func (s *DropTriggerStmt) Pos() int  { return s.pos }
func (s *DropTriggerStmt) stmtNode() {}

// ClusterStmt — `CLUSTER [VERBOSE] [tablename [USING indexname]]`.
// M0095-0008: no-op executor stub.  When a table name is provided the
// executor verifies the table exists; without a table it always succeeds.
type ClusterStmt struct {
	pos       int
	Verbose   bool
	Target    *ObjectName // nil when CLUSTER is called with no table
	IndexName string      // optional USING clause
}

func (s *ClusterStmt) Pos() int  { return s.pos }
func (s *ClusterStmt) stmtNode() {}

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

// DiscardStmt represents DISCARD { ALL | SEQUENCES | PLANS | TEMP | TEMPORARY }.
type DiscardStmt struct {
	pos  int
	Mode string // "ALL", "SEQUENCES", "PLANS", "TEMP", "TEMPORARY"
}

func (s *DiscardStmt) Pos() int  { return s.pos }
func (s *DiscardStmt) stmtNode() {}

// ListenStmt is `LISTEN channel` — registers the current session's interest in
// asynchronous notifications on Channel. The channel is an identifier (or
// double-quoted name); it is matched case-foldingly the same way PostgreSQL
// folds an unquoted identifier. M0118-0009 (async-notify).
type ListenStmt struct {
	pos     int
	Channel string
}

func (s *ListenStmt) Pos() int  { return s.pos }
func (s *ListenStmt) stmtNode() {}

// NotifyStmt is `NOTIFY channel [, 'payload']` — queues a notification that is
// delivered to every session LISTENing on Channel when the notifying
// transaction commits. HasPayload distinguishes an explicit empty payload from
// none (both deliver an empty payload string; tracked for fidelity). M0118-0009.
type NotifyStmt struct {
	pos        int
	Channel    string
	Payload    string
	HasPayload bool
}

func (s *NotifyStmt) Pos() int  { return s.pos }
func (s *NotifyStmt) stmtNode() {}

// UnlistenStmt is `UNLISTEN channel` or `UNLISTEN *` — removes one (or all, when
// All is true) of the current session's channel registrations. M0118-0009.
type UnlistenStmt struct {
	pos     int
	Channel string
	All     bool
}

func (s *UnlistenStmt) Pos() int  { return s.pos }
func (s *UnlistenStmt) stmtNode() {}

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
	pos       int
	Schema    string
	Name      string
	Alias     string   // empty when no AS clause
	Columns   []string // optional column-alias list: (SELECT …) AS t (c1, c2)
	Subquery  *SelectStmt
	TableFunc *TableFuncRef // M0096-0006: table-valued function (e.g. generate_series)
	Only      bool          // FROM ONLY tablename — skip inheritance children
}

// TableFuncRef is a table-valued function used in the FROM clause.
// Handles generate_series, unnest, user-defined SETOF functions, and ROWS FROM.
type TableFuncRef struct {
	pos            int
	Name           string
	Args           []Expr
	WithOrdinality bool            // WITH ORDINALITY appends a bigint ordinal column
	RowsFuncs      []RowsFromEntry // non-nil when ROWS FROM(...) syntax was used
}

// RowsFromEntry is one function call in a ROWS FROM(...) list.
type RowsFromEntry struct {
	Name string
	Args []Expr
}

func (r TableFuncRef) Pos() int { return r.pos }

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
	pos        int
	Expr       Expr
	Desc       bool   // true for DESC, false for ASC (the default)
	UsingOp    string // operator name from ORDER BY x USING op; "" if not used
	NullsFirst *bool  // nil = use default (ASC→nulls last, DESC→nulls first); &true = NULLS FIRST; &false = NULLS LAST
}

func (s SortBy) Pos() int { return s.pos }

// CommonTableExpr is one `WITH cte_name [(col, ...)] AS (SELECT ...)`
// entry. Stage A of M0016 restricts the body to a SelectStmt; the
// parser rejects data-modifying CTE bodies (INSERT / UPDATE /
// DELETE inside the parenthesised body) at parse time. See
// docs/design/0016-0001-with-parser-ast-and-name-resolution.md.
type CommonTableExpr struct {
	pos          int
	Name         string
	Columns      []string // optional column-alias list; nil when absent
	Query        *SelectStmt
	DMLBody      Stmt   // INSERT/UPDATE/DELETE/MERGE CTE body (nil for SELECT CTEs)
	Materialized string // "", "materialized", or "not materialized" (M0097-0047)
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
	// LockStrengthForNoKeyUpdate — `FOR NO KEY UPDATE`. Weaker write-intent
	// lock that skips key columns. v0 maps this to LockStrengthForUpdate
	// semantics (M0096-0004; separate key-level lock modes are out of scope).
	LockStrengthForNoKeyUpdate
	// LockStrengthForKeyShare — `FOR KEY SHARE`. Weaker read-intent lock
	// covering only key columns. v0 maps this to LockStrengthForShare
	// semantics (M0096-0004; separate key-level lock modes are out of scope).
	LockStrengthForKeyShare
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
	// DistinctOn holds the expression list from DISTINCT ON (expr [, ...]).
	// When non-nil, s.Distinct is also true. The planner resolves these
	// expressions to surface unknown-column errors. M0097-regress.
	DistinctOn []Expr
	Targets    []ResTarget
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
	// WithTies is true when `FETCH FIRST n ROWS WITH TIES` was used.
	// M0097-0042: returns additional rows tied on the ORDER BY key.
	WithTies bool
	SetOp    *SetOpClause
	// Parenthesized is set when this SelectStmt was the result of
	// parseParenthesisedSelectStmt() — i.e. the whole compound was
	// explicitly wrapped in parentheses. The planner uses this flag
	// to stop its left-associativity flattening loop from recursing
	// into the parenthesised content (which already has its own
	// correctly-ordered chain). M0097-0042.
	Parenthesized bool
	// InnerSegmentCount > 0 when a parenthesised compound query has an
	// ORDER BY/LIMIT/OFFSET that belongs to the INNER compound (not the
	// outer UNION/EXCEPT/INTERSECT that was appended after the closing
	// paren). The planner applies the sort/limit to the result of the
	// first InnerSegmentCount set-op segments, then continues building
	// the remaining outer segments without sorting. Example:
	//   (((A INTERSECT B ORDER BY 1))) UNION ALL C
	// -> InnerSegmentCount=1 means ORDER BY 1 sorts the INTERSECT result,
	//    not the final UNION ALL output. M0097-0044.
	InnerSegmentCount int
	// Locking holds parsed `FOR UPDATE / FOR SHARE [OF …]
	// [NOWAIT | SKIP LOCKED]` clauses (M0021-0001). Empty for
	// every pre-M0021 SELECT — preserves byte-for-byte
	// invariance across the existing parser/planner test suite.
	Locking []*LockingClause
	// ValuesRows is set when this SelectStmt represents a bare
	// `VALUES (r1), (r2), ...` statement (used as a subquery source
	// in FROM, e.g. `FROM (VALUES ...) t(col)`). When set, Targets
	// and From are empty. M0097-0003.
	ValuesRows [][]Expr
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
	pos     int
	With    *WithClause
	Target  RangeVar
	Columns []string    // empty when no column list — INSERT defaults to declared order
	Rows    [][]Expr    // each row is a parenthesised tuple; nil when Select != nil or DefaultValues
	Select  *SelectStmt // INSERT … SELECT support (M0096-0006); nil when Rows != nil
	// DefaultValues is true for `INSERT INTO t DEFAULT VALUES` — the
	// all-defaults form. Mutually exclusive with Rows/Select; Rows stays
	// nil at parse time. The planner expands this into a single row of
	// DefaultMarkers sized to the target's insertable columns. M0103-0007
	// rung 17.
	DefaultValues bool
	OnConflict    *OnConflictClause
	Returning     []ResTarget
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
	pos     int
	Columns []string // populated for `ON CONFLICT (col [, col, …])`
	// Exprs holds the parsed expression for expression-based conflict columns
	// (e.g. lower(key)). Parallel to Columns: Exprs[i] is non-nil when
	// Columns[i] == "" (expression column); nil for plain column names.
	Exprs      []Expr
	Constraint string // populated for `ON CONFLICT ON CONSTRAINT name`
	// Where holds the optional partial-index predicate from
	// `ON CONFLICT (cols) WHERE predicate`. nil when absent.
	Where Expr
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
	pos            int
	Column         string   // single-column form: "col = expr"
	TableQualifier string   // non-empty when user wrote "table.col = expr" (illegal; rejected in semantic layer)
	Columns        []string // multi-column form: "(c1, c2, …) = (e1, e2, …)"
	Expr           Expr     // RHS: single expr, or *RowExpr for multi-column form
}

func (a UpdateAssign) Pos() int { return a.pos }

// UpdateStmt — `[WITH ...] UPDATE target SET col = expr [, …]
//
//	[FROM from_list] [WHERE expr] [RETURNING target_list]`.
//
// The optional `With` field (M0016-0001) is nil when no WITH clause
// precedes the UPDATE. The optional `From` field (M0097-0065) holds
// the FROM-clause tables that provide additional columns for SET and
// WHERE expressions (PostgreSQL non-standard extension).
type UpdateStmt struct {
	pos       int
	With      *WithClause
	Target    RangeVar
	Set       []UpdateAssign
	From      []RangeVar // FROM-clause tables (nil when absent). M0097-0065.
	Where     Expr       // nil when absent
	CurrentOf string     // cursor name for WHERE CURRENT OF cursor. M0097-0069.
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
	Using     []RangeVar // USING-clause tables (nil when absent). M0097-0076.
	Where     Expr       // nil when absent
	CurrentOf string     // cursor name for WHERE CURRENT OF cursor. M0097-0069.
	Returning []ResTarget
}

func (s *DeleteStmt) Pos() int  { return s.pos }
func (s *DeleteStmt) stmtNode() {}

// ColumnType is the textual type spec on a column or function arg.
// Args are the optional parameter list — `char(22)` carries [22],
// `numeric(10,2)` carries [10, 2]. Schema-qualified type names
// (e.g. `pg_catalog.int4`) populate Schema.
type ColumnType struct {
	pos     int
	Schema  string
	Name    string
	Args    []int64
	IsArray bool // true if type has [] array suffix (e.g. int[], text[]); M0097-0071
}

func (c ColumnType) Pos() int { return c.pos }

// ColumnDef is one column declaration in CREATE TABLE.
// FKAction describes the referential action for ON DELETE / ON UPDATE.
// M0096-0011.
type FKAction int

const (
	FKActionNoAction   FKAction = iota // NO ACTION (default; deferrable)
	FKActionRestrict                   // RESTRICT (always immediate)
	FKActionCascade                    // CASCADE
	FKActionSetNull                    // SET NULL
	FKActionSetDefault                 // SET DEFAULT
)

type ColumnDef struct {
	pos     int
	Name    string
	Type    ColumnType
	NotNull bool
	Primary bool // inline `PRIMARY KEY` constraint
	Unique  bool // inline `UNIQUE` constraint
	// UniqueNullsNotDistinct is true when an inline column UNIQUE was declared
	// `UNIQUE NULLS NOT DISTINCT` (PostgreSQL 15+). It is threaded into
	// catalog.Index.NullsNotDistinct so pg_get_constraintdef re-emits the
	// clause. Only meaningful when Unique is true. DU-002 slice 136.
	UniqueNullsNotDistinct bool
	// UniqueConstraintName is the explicit constraint name from an inline named
	// column UNIQUE (`a int CONSTRAINT myname UNIQUE`). Empty for the anonymous
	// form, in which case the backing index name is auto-generated
	// (`tbl_col_key`). Only meaningful when Unique is true. DU-002 slice 137.
	UniqueConstraintName string
	// UniqueDeferrable / UniqueInitiallyDeferred capture a `[NOT] DEFERRABLE
	// [INITIALLY DEFERRED | INITIALLY IMMEDIATE]` trailer on an inline column
	// UNIQUE constraint (`a int UNIQUE DEFERRABLE INITIALLY DEFERRED`). They
	// thread onto the backing catalog.Index so pg_get_constraintdef / pg_dump
	// re-emit the clause and pg_constraint emits condeferrable/condeferred.
	// INITIALLY DEFERRED implies DEFERRABLE; IMMEDIATE is the default (both
	// false). Only meaningful when Unique is true. Dump-fidelity only — deferred
	// CHECKING is not implemented (constraints enforced per-row). DU-002 slice 141.
	UniqueDeferrable        bool
	UniqueInitiallyDeferred bool
	// PrimaryDeferrable / PrimaryInitiallyDeferred capture a `[NOT] DEFERRABLE
	// [INITIALLY DEFERRED | INITIALLY IMMEDIATE]` trailer on an inline column
	// PRIMARY KEY constraint (`a int PRIMARY KEY DEFERRABLE INITIALLY DEFERRED`).
	// They thread onto the backing catalog.Index so pg_get_constraintdef / pg_dump
	// re-emit the clause and pg_constraint emits condeferrable/condeferred.
	// INITIALLY DEFERRED implies DEFERRABLE; IMMEDIATE is the default (both
	// false). Only meaningful when Primary is true. Dump-fidelity only — deferred
	// CHECKING is not implemented (constraints enforced per-row). DU-002 slice 142.
	PrimaryDeferrable        bool
	PrimaryInitiallyDeferred bool
	// GeneratedAlways is true for `GENERATED ALWAYS AS (expr) STORED` columns.
	// M0096-0008.
	GeneratedAlways bool
	// GeneratedExpr holds the raw SQL expression text (without surrounding parens)
	// for a generated column. Empty for ordinary columns.
	GeneratedExpr string
	// GeneratedVirtual is true when the generated column is declared VIRTUAL
	// (explicitly, or implicitly via the bare `GENERATED ALWAYS AS (expr)` form,
	// whose PG18 default is VIRTUAL); false for an explicit STORED. M0110-0001
	// (DU-002 slice 194).
	GeneratedVirtual bool
	// IdentityColumn is true for `GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY` columns.
	// The sequence is registered during CREATE TABLE; INSERT uses nextval() as default.
	IdentityColumn bool
	// IdentityAlways is true when the identity generation is ALWAYS (vs BY DEFAULT).
	// ALWAYS means user-provided values are rejected; BY DEFAULT allows them.
	IdentityAlways bool
	// IdentityStart is the START WITH value from the sequence options clause, or 0
	// to use the type-default start (1 for ascending sequences). M0097-0024.
	IdentityStart int64

	// DefaultExpr holds the parsed AST of the column's DEFAULT clause when
	// one was given (`col INT DEFAULT 0`). nil for columns without a DEFAULT.
	// M0103-0007 rung 13.
	DefaultExpr Expr

	// FK fields — populated when the column has an inline REFERENCES clause.
	// M0096-0011.
	RefTable            ObjectName
	RefColumns          []string // empty = use parent PK
	OnDelete            FKAction
	OnUpdate            FKAction
	FKDeferrable        bool
	FKInitiallyDeferred bool

	// CheckExpr holds the raw SQL expression for an inline CHECK constraint.
	// M0097-0014.
	CheckExpr string
	// NotNullNoInherit is true when the NOT NULL constraint carries NO INHERIT
	// (PG18: `c int NOT NULL NO INHERIT`). goopg v0 tracks the flag to emit
	// the partitioned-table error for LIKE INCLUDING ALL. M0097-0023.
	NotNullNoInherit bool
	// NotNullConstraintName holds the user-given name when the inline NOT NULL is
	// written with an explicit `CONSTRAINT <name>` prefix
	// (`c int CONSTRAINT myname NOT NULL [NO INHERIT]`). When this differs from
	// PG's auto-name (<table>_<col>_not_null), pg_dump re-emits the inline
	// `CONSTRAINT <name> NOT NULL` form rather than a bare `NOT NULL`. Empty for
	// the common unnamed case. DU-002 slice 273.
	NotNullConstraintName string
	// CheckNoInherit is true when the inline CHECK constraint carries NO INHERIT.
	// Stored so LIKE INCLUDING ALL can error on partitioned tables. M0097-0023.
	CheckNoInherit bool
	// Compression is the per-column TOAST compression method from an inline
	// `COMPRESSION <method>` clause (`col text COMPRESSION lz4`) — "pglz" or
	// "lz4". Empty when no method was written. Threaded onto catalog.Column.
	// Compression purely so the column round-trips through pg_dump (goopg does
	// not actually TOAST/compress). DU-002 slice 183.
	Compression string
	// Collation is the collation name from an inline `COLLATE <name>` clause
	// (`col text COLLATE "C"`). Empty when none was written. Stored as the bare
	// (last-component, unquoted) collation name — e.g. "C", "POSIX", "ucs_basic".
	// Threaded onto catalog.Column.Collation so the synthesized pg_attribute
	// row reports the chosen attcollation and pg_dump re-emits the COLLATE
	// clause. goopg does not actually collate; this is recorded purely for
	// pg_dump round-trip fidelity. DU-002 slice 188.
	Collation string
}

func (c ColumnDef) Pos() int { return c.pos }

// TableConstraintDef describes a named UNIQUE or PRIMARY KEY table-level constraint
// that may carry INCLUDE covering columns. Used in CreateTableStmt.NamedConstraints.
type TableConstraintDef struct {
	Name           string   // constraint/index name
	Columns        []string // key columns
	IncludeColumns []string // non-key covering columns from INCLUDE (…)
	IsPrimary      bool     // true for PRIMARY KEY, false for UNIQUE
	IsExclusion    bool     // true for EXCLUDE USING constraints
	ExclusionOp    string   // per-column operator (e.g. "=") for single-column EXCLUDE
	Method         string   // index method for EXCLUDE (e.g. "btree")
	// NullsNotDistinct is true when a UNIQUE constraint was declared
	// `UNIQUE NULLS NOT DISTINCT (…)` (PostgreSQL 15+): NULL key values are
	// treated as equal for uniqueness. Threads to the backing index's
	// catalog.Index.NullsNotDistinct so pg_get_constraintdef re-emits it.
	// DU-002 slice 135.
	NullsNotDistinct bool
	// Deferrable / InitiallyDeferred capture a `[NOT] DEFERRABLE [INITIALLY
	// DEFERRED | INITIALLY IMMEDIATE]` trailer on a named table-level UNIQUE
	// constraint. They thread onto the backing catalog.Index so
	// pg_get_constraintdef / pg_dump re-emit the clause and pg_constraint emits
	// condeferrable/condeferred. INITIALLY DEFERRED implies DEFERRABLE; IMMEDIATE
	// is the default (both false). Dump-fidelity only — deferred CHECKING is not
	// implemented (constraints are enforced per-row). DU-002 slice 140.
	Deferrable        bool
	InitiallyDeferred bool
}

// TableForeignKeyDef describes a table-level FOREIGN KEY constraint, e.g.
// `FOREIGN KEY (a, b) REFERENCES t (x, y) ON DELETE CASCADE`. This is the
// multi-column sibling of the inline-on-column REFERENCES clause (whose fields
// live on ColumnDef). Used in CreateTableStmt.TableForeignKeys. DU-002 slice 53.
type TableForeignKeyDef struct {
	Name              string     // explicit CONSTRAINT name; "" when anonymous
	Columns           []string   // referencing (local) columns
	RefTable          ObjectName // referenced table
	RefColumns        []string   // referenced columns; empty = referenced table's PK
	OnDelete          FKAction   // referential action for ON DELETE (default NO ACTION)
	OnUpdate          FKAction   // referential action for ON UPDATE (default NO ACTION)
	Deferrable        bool
	InitiallyDeferred bool
}

// CreateTableStmt — `CREATE [UNLOGGED] TABLE [IF NOT EXISTS] name
//
//	(column_def [, …]) [WITH (option = value [, …])]`. Foreign keys,
//
// CHECK constraints, partitioning, and inheritance are deferred.
type CreateTableStmt struct {
	pos         int
	IfNotExists bool
	Unlogged    bool
	Temporary   bool // SET when CREATE TEMP TABLE is used. M0097-0003.
	Name        ObjectName
	Columns     []ColumnDef
	// PrimaryKey holds the column names from a table-level
	// `PRIMARY KEY (a, b)` clause. Inline-on-column primary keys live
	// on ColumnDef.Primary instead.
	PrimaryKey []string
	// With carries the option list from `WITH (k = v, …)`. We keep it
	// as a string→string map; the analyzer interprets known options
	// (fillfactor, autovacuum_*, …) and rejects the rest.
	With     map[string]string
	WithOIDS bool // true when WITH OIDS or WITH (oids[=true]) is present
	// PartitionBy is non-nil when `PARTITION BY {LIST|RANGE|HASH} (col, …)` is present.
	// M0096-0007.
	PartitionBy *PartitionByClause
	// PartitionOf is non-nil for `CREATE TABLE child PARTITION OF parent FOR VALUES …`.
	// M0096-0007.
	PartitionOf *PartitionOfClause
	// Inherits lists the parent table names from `INHERITS (parent, …)`.
	// M0096-0009 will use these; for now the field is populated so the
	// syntax is accepted and the executor can create the child table.
	Inherits []ObjectName
	// ColumnAliases holds the optional column-name list from
	// `CREATE TABLE name (col1, col2, …) AS SELECT …`. When non-nil its
	// length must not exceed the number of columns the SELECT returns; alias
	// names replace the derived column names left-to-right; remaining columns
	// keep their SELECT-derived names. M0097-0020.
	ColumnAliases []string
	// SelectSource is non-nil for `CREATE TABLE name AS SELECT …` (CTAS).
	// The table is created with columns derived from the SELECT result. M0096-0008.
	SelectSource *SelectStmt
	// ExecuteSource is non-nil for `CREATE TABLE name AS EXECUTE name(params)`.
	ExecuteSource *ExecuteStmt
	// WithNoData is true for `CREATE TABLE … AS … WITH NO DATA`.
	WithNoData bool
	// TableChecks holds raw SQL expressions from table-level CHECK constraints.
	// M0097-0014.
	TableChecks []string
	// TableCheckNoInherit is parallel to TableChecks: entry i is true when the
	// anonymous table-level CHECK at TableChecks[i] carries NO INHERIT. Tracked
	// per-check (not just the aggregate TableHasNoInheritCheck) so an anonymous
	// `CHECK (...) NO INHERIT` re-emits its suffix through pg_get_constraintdef
	// on dump. DU-002 slice 128.
	TableCheckNoInherit []bool
	// TableNamedChecks holds explicitly named table-level CHECK constraints,
	// e.g. `CONSTRAINT check_a CHECK (a > 0)`. M0097-0023.
	TableNamedChecks []PartitionCheckConstraint
	// TableUniques holds column-name lists from table-level UNIQUE (col1, col2)
	// constraints. Each entry is the list of columns for one UNIQUE clause.
	// Anonymous constraints only; named ones are in NamedConstraints. M0097-0028.
	TableUniques [][]string
	// TableUniqueIncludes holds the INCLUDE covering columns parallel to
	// TableUniques: TableUniqueIncludes[i] is the include list for TableUniques[i].
	// May be shorter than TableUniques if trailing entries have no INCLUDE. M0097-0023.
	TableUniqueIncludes [][]string
	// TableUniqueNullsNotDistinct is parallel to TableUniques:
	// TableUniqueNullsNotDistinct[i] is true when TableUniques[i] was declared
	// `UNIQUE NULLS NOT DISTINCT (…)` (PostgreSQL 15+). May be shorter than
	// TableUniques if trailing entries use the default NULLS DISTINCT.
	// DU-002 slice 135.
	TableUniqueNullsNotDistinct []bool
	// TableUniqueDeferrable / TableUniqueInitiallyDeferred are parallel to
	// TableUniques: entry [i] is true when TableUniques[i] was declared
	// `DEFERRABLE` / `DEFERRABLE INITIALLY DEFERRED`. May be shorter than
	// TableUniques when trailing entries use the NOT DEFERRABLE default.
	// pg_get_constraintdef re-emits the clause so pg_dump round-trips it.
	// DU-002 slice 139.
	TableUniqueDeferrable        []bool
	TableUniqueInitiallyDeferred []bool
	// PrimaryKeyInclude holds the INCLUDE covering columns for an anonymous
	// table-level PRIMARY KEY constraint. M0097-0023.
	PrimaryKeyInclude []string
	// PrimaryKeyDeferrable / PrimaryKeyInitiallyDeferred capture a `[NOT]
	// DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE]` trailer on an
	// anonymous table-level PRIMARY KEY constraint (`PRIMARY KEY (a) DEFERRABLE
	// INITIALLY DEFERRED`). They thread onto the backing catalog.Index so
	// pg_get_constraintdef / pg_dump re-emit the clause and pg_constraint emits
	// condeferrable/condeferred. INITIALLY DEFERRED implies DEFERRABLE; IMMEDIATE
	// is the default (both false). Dump-fidelity only. DU-002 slice 142.
	PrimaryKeyDeferrable        bool
	PrimaryKeyInitiallyDeferred bool
	// NamedConstraints holds explicitly named UNIQUE/PRIMARY KEY/EXCLUDE constraints,
	// which may carry INCLUDE covering columns. The parser places named constraints
	// here so the executor can use the constraint name as the index name. M0097-0023.
	NamedConstraints []TableConstraintDef
	// TableExclusions holds anonymous EXCLUDE USING constraints (no CONSTRAINT name).
	// Named ones are folded into NamedConstraints. M0097-0023.
	TableExclusions []TableConstraintDef
	// TableForeignKeys holds table-level FOREIGN KEY constraints (both anonymous
	// and CONSTRAINT-named), e.g. `FOREIGN KEY (a, b) REFERENCES t (x, y)`. The
	// inline-on-column REFERENCES sibling lives on ColumnDef instead. DU-002 slice 53.
	TableForeignKeys []TableForeignKeyDef
	// TableHasNoInheritCheck is true when any table-level CHECK constraint carries
	// NO INHERIT. Partitioned tables reject such constraints. M0097-0023.
	TableHasNoInheritCheck bool
	// LikeTables holds the source table names from LIKE clauses.
	// `CREATE TABLE t (LIKE src INCLUDING DEFAULTS)` copies src's columns. M0097-0069.
	// Deprecated: use BodyOrder for positional interleaving.
	LikeTables []ObjectName
	// BodyOrder tracks the order of explicit columns and LIKE clauses in the
	// CREATE TABLE body so the executor can interleave columns correctly.
	// Each element is either a column name (for explicit columns) or
	// "@@LIKE:schema.table" (for LIKE source_table clauses). M0097-0069.
	BodyOrder []string
}

func (s *CreateTableStmt) Pos() int  { return s.pos }
func (s *CreateTableStmt) stmtNode() {}

// PartitionByClause describes a PARTITION BY … clause.  M0096-0007.
type PartitionByClause struct {
	pos        int
	Method     string   // "LIST", "RANGE", or "HASH"
	KeyCols    []string // partition key column names; empty string for expression keys
	KeyExprs   []Expr   // expression-based partition keys; nil for plain column keys (M0097-0023)
	OpClasses  []string // operator class per key col (empty string = default); M0097-0027
	Collations []string // per-key collation name ("" = default, i.e. not shown); M0097-0023
}

// PartitionOfClause describes a PARTITION OF parent FOR VALUES … clause.
// Only one of InValues, FromValues+ToValues, or Modulus+Remainder is populated.
// M0096-0007; HASH bounds added M0097-0015.
type PartitionOfClause struct {
	pos    int
	Parent ObjectName
	// LIST partitioning: FOR VALUES IN (v1, v2, …)
	InValues []Expr
	// RANGE partitioning: FOR VALUES FROM (lo) TO (hi)
	FromValues []Expr
	ToValues   []Expr
	Default    bool // FOR VALUES DEFAULT
	// HASH partitioning: FOR VALUES WITH (MODULUS n, REMAINDER r)
	Modulus   int64
	Remainder int64
	IsHash    bool
	// UniqueColumns holds column names from the optional column-constraint list
	// in PARTITION OF, e.g. `CREATE TABLE t1 PARTITION OF t (b UNIQUE) FOR VALUES IN (1)`.
	// Each entry is a column name to create a unique index on. M0097-0028.
	UniqueColumns []string
	// NotNullColumns holds column names from the PARTITION OF column-constraint
	// list that carry an explicit NOT NULL, e.g. `(b NOT NULL DEFAULT 1)`.
	// Used to create named NOT NULL constraints on the child. M0097-0023.
	NotNullColumns []string
	// CheckConstraints holds named CHECK constraints from the PARTITION OF
	// column-constraint list, e.g. `CONSTRAINT check_b CHECK (b > 0)`.
	// Used to create local check constraints on the child. M0097-0023.
	CheckConstraints []PartitionCheckConstraint
	// ColDefaults holds per-column DEFAULT expression overrides declared in the
	// PARTITION OF column-override list, e.g. `(b DEFAULT 1)`. M0097-0023.
	ColDefaults []PartitionColDefault
	// ColGeneratedExprs holds GENERATED ALWAYS AS expression overrides declared
	// in the PARTITION OF column-override list, e.g.
	// `(d WITH OPTIONS GENERATED ALWAYS AS (a + b + 1000) STORED)`. M0100-0010.
	ColGeneratedExprs []PartitionColGenerated
	// DuplicateColumn is non-empty when a column name appears more than once
	// in the PARTITION OF column override list (e.g. `(b NOT NULL, b DEFAULT 1, ...)`).
	// The executor returns an error when this is set.
	DuplicateColumn string
}

// PartitionColDefault holds a DEFAULT expression override for a single column
// in a PARTITION OF column-override list. M0097-0023.
type PartitionColDefault struct {
	ColName string
	Expr    Expr
}

// PartitionColGenerated holds a GENERATED ALWAYS AS expression override for a
// single column in a PARTITION OF column-override list. M0100-0010.
type PartitionColGenerated struct {
	ColName string
	Expr    string // raw SQL expression text (without wrapping parens)
}

// PartitionCheckConstraint is a named CHECK constraint declared in a PARTITION
// OF column-override list. M0097-0023.
type PartitionCheckConstraint struct {
	Name string // constraint name (from CONSTRAINT name CHECK (...))
	Expr string // raw SQL expression text
	// NoInherit is true for `CONSTRAINT c CHECK (...) NO INHERIT` (PG18
	// connoinherit='t'); the dumped constraintdef must re-emit the ` NO INHERIT`
	// suffix and pg_constraint must report connoinherit. DU-002 slice 129.
	NoInherit bool
}

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
	// ColExprs holds the parsed expression for expression-based index columns
	// (e.g. lower(col)). Parallel to Columns: ColExprs[i] is non-nil when
	// Columns[i] == "" (expression column); nil for plain column names.
	ColExprs   []Expr
	Fillfactor int // 0 means unset; valid range 10–100
	// OpClassWithOptions holds the name of the first operator class that
	// specifies parameters (e.g. "int4_ops" from `id int4_ops(foo=1)`).
	// Empty string means no operator class options were given. In PostgreSQL
	// most built-in operator classes have no options, so a non-empty value
	// here causes an error at execution time. M0097-0023.
	OpClassWithOptions string
	HasPredicate       bool     // true when a WHERE clause (partial index predicate) is present
	Predicate          Expr     // WHERE predicate expression (nil if no WHERE clause)
	IncludeColumns     []string // non-key covering columns from INCLUDE (…)
	OnOnly             bool     // ON ONLY — create on parent table without propagating to children
	// Concurrently records the CONCURRENTLY keyword. goopg still builds the
	// index synchronously, but a concurrent build waits for transactions that
	// were already running when it started to drain before it completes — the
	// observable wait modelled by the isolation spec multiple-cic.
	Concurrently bool
	// ColOrders captures the per-key-column ASC/DESC + NULLS FIRST/LAST ordering,
	// parallel to Columns (ColOrders[i] applies to Columns[i]). Mirrors PG's
	// pg_index.indoption so pg_get_indexdef can reproduce a non-default ordering.
	ColOrders []IndexColOrder
	// NullsNotDistinct is true when the index was declared with the PostgreSQL 15+
	// `NULLS NOT DISTINCT` option (treat NULL key values as equal for uniqueness).
	// Mirrors pg_index.indnullsnotdistinct so pg_get_indexdef can reproduce it.
	// The default (omitted, or explicit `NULLS DISTINCT`) leaves this false.
	NullsNotDistinct bool
	// DeduplicateItems captures the btree `WITH (deduplicate_items=on|off)`
	// boolean storage parameter. nil means unset (PG default ON); a non-nil
	// pointer records the explicitly-declared value so pg_get_indexdef can
	// re-emit `WITH (deduplicate_items='on'|'off')`. DU-002 slice 219.
	DeduplicateItems *bool
	// FastUpdate captures the GIN `WITH (fastupdate=on|off)` boolean storage
	// parameter. nil means unset (PG default ON); a non-nil pointer records the
	// explicitly-declared value so pg_get_indexdef can re-emit
	// `WITH (fastupdate='on'|'off')`. DU-002 slice 220.
	FastUpdate *bool
	// GinPendingListLimit captures the GIN `WITH (gin_pending_list_limit=N)`
	// integer storage parameter (max pending-list size in kB). 0 means unset
	// (PG default -1 = use the GUC); a non-zero value records the explicitly-
	// declared limit so pg_get_indexdef can re-emit
	// `WITH (gin_pending_list_limit='N')`. Valid range 64–2097151. DU-002 slice 221.
	GinPendingListLimit int
	// PagesPerRange captures the BRIN `WITH (pages_per_range=N)` integer storage
	// parameter (number of heap pages per summarized range). 0 means unset (PG
	// default 128); a non-zero value records the explicitly-declared value so
	// pg_get_indexdef can re-emit `WITH (pages_per_range='N')`. Valid range
	// 1–131072. DU-002 slice 222.
	PagesPerRange int
	// AutoSummarize captures the BRIN `WITH (autosummarize=on|off)` boolean
	// storage parameter (summarize the previous range when a new page range is
	// created). nil means unset (PG default OFF); a non-nil pointer records the
	// explicitly-declared value so pg_get_indexdef can re-emit
	// `WITH (autosummarize='on'|'off')`. DU-002 slice 223.
	AutoSummarize *bool
}

// IndexColOrder captures the ASC/DESC + NULLS ordering of one CREATE INDEX key
// column. Descending mirrors PG's INDOPTION_DESC; NullsFirst mirrors
// INDOPTION_NULLS_FIRST. When the NULLS clause is omitted PG defaults NullsFirst
// to Descending (NULLS FIRST is the default for DESC, NULLS LAST for ASC), so the
// parser pre-resolves NullsFirst to that effective value.
type IndexColOrder struct {
	Descending bool
	NullsFirst bool
}

func (s *CreateIndexStmt) Pos() int  { return s.pos }
func (s *CreateIndexStmt) stmtNode() {}

// DropBehavior is the trailing CASCADE/RESTRICT on DROP statements.
type DropBehavior int

const (
	DropDefault DropBehavior = iota // RESTRICT (the default)
	DropCascade
)

// CreateSequenceStmt — `CREATE [TEMP] SEQUENCE [IF NOT EXISTS] name
//
//	[AS datatype] [INCREMENT [BY] n] [MINVALUE n | NO MINVALUE]
//	[MAXVALUE n | NO MAXVALUE] [START [WITH] n] [CACHE n]
//	[NO CYCLE | CYCLE] [OWNED BY column]`. M0097-0009.
type CreateSequenceStmt struct {
	pos         int
	Name        ObjectName
	IfNotExists bool
	Temporary   bool
	DataType    string // "smallint", "integer", "bigint" (default)
	Increment   *int64
	MinValue    *int64
	MaxValue    *int64
	Start       *int64
	Cache       *int64
	Cycle       bool
	OwnedBy     string // "table.column" or empty
}

func (s *CreateSequenceStmt) Pos() int  { return s.pos }
func (s *CreateSequenceStmt) stmtNode() {}

// CreateExtensionStmt — `CREATE EXTENSION [IF NOT EXISTS] name
//
//	[WITH] [SCHEMA schema_name] [VERSION version] [CASCADE]`.
//
// The executor inserts a pg_extension catalog row. goopg supports only the
// built-in extensions whose SQL surface it implements (e.g. amcheck); unknown
// names error like PG's missing control file. M0110-0003 (amcheck SQL surface).
type CreateExtensionStmt struct {
	pos         int
	Name        string
	IfNotExists bool
	Schema      string // optional SCHEMA clause; "" → default (public)
	Version     string // optional VERSION clause; "" → extension default
	Cascade     bool
}

func (s *CreateExtensionStmt) Pos() int  { return s.pos }
func (s *CreateExtensionStmt) stmtNode() {}

// CreateTablespaceStmt — `CREATE TABLESPACE name [OWNER role] LOCATION 'dir' [WITH (opts)]`.
// goopg supports only the developer/regression-test in-place form (empty LOCATION
// with allow_in_place_tablespaces on), which creates pg_tblspc/<oid> as a real
// directory. Mirrors gram.y's CreateTableSpaceStmt. M0095-0003 (011 in-place tablespace).
type CreateTablespaceStmt struct {
	pos      int
	Name     string
	Owner    string // optional OWNER role; "" → current user
	Location string // LOCATION string literal; "" → in-place tablespace
	Options  []string
}

func (s *CreateTablespaceStmt) Pos() int  { return s.pos }
func (s *CreateTablespaceStmt) stmtNode() {}

// DropTablespaceStmt — `DROP TABLESPACE [IF EXISTS] name`. Removes the runtime
// tablespace registry entry and its in-place pg_tblspc/<oid> directory.
// M0095-0003 (011 in-place tablespace).
type DropTablespaceStmt struct {
	pos      int
	IfExists bool
	Name     string
}

func (s *DropTablespaceStmt) Pos() int  { return s.pos }
func (s *DropTablespaceStmt) stmtNode() {}

// AlterSequenceStmt — `ALTER SEQUENCE [IF EXISTS] name [option …]`. M0097-0009.
type AlterSequenceStmt struct {
	pos          int
	Name         ObjectName
	IfExists     bool
	DataType     string // AS type: "smallint", "integer", "bigint"
	Increment    *int64 // INCREMENT [BY] n
	MinValue     *int64 // MINVALUE n (nil = unchanged)
	NoMinValue   bool   // NO MINVALUE (reset to type default)
	MaxValue     *int64 // MAXVALUE n
	NoMaxValue   bool   // NO MAXVALUE (reset to type default)
	StartWith    *int64 // START [WITH] n — updates stored start_value only
	Restart      bool   // RESTART (no value) — reset current to start_value
	RestartWith  *int64 // RESTART [WITH] n — reset current and update start_value
	Cache        *int64 // CACHE n (nil = unchanged)
	Cycle        bool   // CYCLE
	NoCycle      bool   // NO CYCLE
	OwnedBy      string // "table.column" or "" from OWNED BY clause
	ClearOwnedBy bool   // OWNED BY NONE
}

func (s *AlterSequenceStmt) Pos() int  { return s.pos }
func (s *AlterSequenceStmt) stmtNode() {}

// DropTableStmt — `DROP TABLE [IF EXISTS] name [, …] [CASCADE|RESTRICT]`.
type DropTableStmt struct {
	pos      int
	IfExists bool
	Names    []ObjectName
	Behavior DropBehavior
}

func (s *DropTableStmt) Pos() int  { return s.pos }
func (s *DropTableStmt) stmtNode() {}

// DropIndexStmt — `DROP INDEX [CONCURRENTLY] [IF EXISTS] name [, …] [CASCADE|RESTRICT]`.
type DropIndexStmt struct {
	pos        int
	Concurrent bool
	IfExists   bool
	Names      []ObjectName
	Behavior   DropBehavior
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
	Temporary bool // CREATE TEMP[ORARY] VIEW
	Name      ObjectName
	Columns   []string // optional explicit column-name list
	Query     *SelectStmt
	// RawDef is the raw SQL text of the view body (everything after `AS`,
	// trimmed of trailing whitespace/semicolons). Captured verbatim so
	// pg_get_viewdef can echo it for pg_dump. Empty if the source text was
	// unavailable to the parser.
	RawDef string
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

// CreateMatViewStmt — `CREATE MATERIALIZED VIEW [IF NOT EXISTS] name AS
// query [WITH NO DATA]`. M0097-0013.
type CreateMatViewStmt struct {
	pos           int
	Name          ObjectName
	Query         *SelectStmt
	WithNoData    bool
	IfNotExists   bool
	ColumnAliases []string // optional (col1, col2, ...) after the name
	// RawDef is the raw SQL text of the matview body (everything after `AS`,
	// before any `WITH [NO] DATA`), captured verbatim so pg_get_viewdef can
	// echo it for pg_dump (which dumps a matview's body via createViewAsClause,
	// exactly like a plain view). Empty when the source text was unavailable.
	RawDef string
}

func (s *CreateMatViewStmt) Pos() int  { return s.pos }
func (s *CreateMatViewStmt) stmtNode() {}

// RefreshMatViewStmt — `REFRESH MATERIALIZED VIEW [CONCURRENTLY] name
// [WITH [NO] DATA]`. M0097-0013.
type RefreshMatViewStmt struct {
	pos          int
	Name         ObjectName
	Concurrently bool
	WithNoData   bool
}

func (s *RefreshMatViewStmt) Pos() int  { return s.pos }
func (s *RefreshMatViewStmt) stmtNode() {}

// DropCompatStmt is a compatibility stub for DROP statements whose object
// types are not fully implemented in goopg v0 (SEQUENCE, SCHEMA, TYPE,
// DOMAIN, AGGREGATE, COLLATION, etc.). The executor handles IF EXISTS by
// emitting a NOTICE and silently succeeds for the rest. M0097-0008.
type DropCompatStmt struct {
	pos      int
	ObjType  string // "sequence", "schema", "type", "domain", etc.
	IfExists bool
	Names    []ObjectName
	Behavior DropBehavior
	// ArgTypes holds parsed argument types for AGGREGATE and OPERATOR.
	// AGGREGATE: [argtype]; OPERATOR: [leftType, rightType] ("" = missing/NONE).
	ArgTypes []string
	// UsingMethod holds the access method name for DROP OPERATOR CLASS/FAMILY.
	// E.g. "btree", "hash", "gist". M0097-0071.
	UsingMethod string
	// CastTypes holds [fromType, toType] for DROP CAST (fromType AS toType). M0097-0071.
	CastTypes []string
}

// CreateAggregateStmt is a minimal representation of CREATE AGGREGATE used to
// validate that the basetype (input type) is specified.  Full aggregate
// implementation is out of scope; we just reject missing basetype. M0097-regress.
type CreateAggregateStmt struct {
	pos         int
	Name        ObjectName
	HasBaseType bool
	BaseType    string // e.g. "int4"
	Variadic    bool   // true when declared with VARIADIC arg (variadic agg)
	SType       string // state type (e.g. "int4")
	SFunc       string // state transition function name
	FinalFunc   string // final function name
	CombineFunc string // combine function name (for parallel aggregation)
	InitCond    string // initial condition string (e.g. "0" or "{0,0}")
}

func (s *CreateAggregateStmt) Pos() int  { return s.pos }
func (s *CreateAggregateStmt) stmtNode() {}

// AlterAggregateRenameStmt renames a user-defined aggregate. M0097-0035.
// ALTER AGGREGATE name(argtype_list) RENAME TO newname
type AlterAggregateRenameStmt struct {
	pos     int
	OldName ObjectName
	NewName string
}

func (s *AlterAggregateRenameStmt) Pos() int  { return s.pos }
func (s *AlterAggregateRenameStmt) stmtNode() {}

// CreateOpClassStmt is a minimal representation of CREATE OPERATOR CLASS used
// to register custom hash support functions for hash partitioning. M0097-0027.
// We only capture the FUNCTION 2 (hash extended) entry; everything else is
// accepted-and-discarded so the statement doesn't produce a parse error.
type CreateOpClassStmt struct {
	pos          int
	Name         string // operator class name
	ForType      string // e.g. "int4", "text"
	HashFuncName string // name of the FUNCTION 2 (hash extended) routine
}

func (s *CreateOpClassStmt) Pos() int  { return s.pos }
func (s *CreateOpClassStmt) stmtNode() {}

// DoStmt represents DO $$ body $$ — an anonymous PL/pgSQL block. M0097-0003.
type DoStmt struct {
	pos      int
	Language string // "plpgsql" (default)
	Body     string // the PL/pgSQL block body (dollar-quoted content)
}

func (s *DoStmt) Pos() int  { return s.pos }
func (s *DoStmt) stmtNode() {}

// CompatNoopStmt is a compatibility stub for SQL statements that goopg
// accepts syntactically but does not execute (GRANT, REVOKE, COMMENT ON,
// SECURITY LABEL, etc.). The executor silently succeeds. M0097-0016.
type CompatNoopStmt struct {
	pos       int
	Tag       string     // CommandComplete tag, e.g. "GRANT", "REVOKE", "COMMENT"
	ObjType   string     // optional: object type for compat registry (e.g. "conversion")
	ObjName   ObjectName // optional: primary object name for compat registry
	ArgTypes  []string   // optional: arg types for operator compat registry (e.g. ["bigint","bigint"])
	TableName ObjectName // optional: table name for rule compat registry
	RuleKind  string     // optional: rule kind for COPY DML error messages (M0097-0140)
	// DatabaseACL is set for a GRANT/REVOKE … ON DATABASE … statement. Such an
	// ACL change takes no heavyweight lock in PostgreSQL — its lock is the
	// pg_database tuple's xmax — so the executor records the writer XID
	// (Catalog.SetDatabaseACLChangeXID) and a concurrent database-wide VACUUM
	// waits on it before advancing datfrozenxid in place. Design 0118-0098.
	DatabaseACL bool
	// TableACL holds the relation name of a GRANT/REVOKE … ON [TABLE] <name>
	// statement (the default object class for GRANT is TABLE). Like DatabaseACL,
	// such an ACL change takes no heavyweight lock — its lock is the pg_class
	// tuple's xmax — so the executor records the writer XID keyed by the table
	// OID (Catalog.SetTableACLChangeXID) and a concurrent in-place pg_class update
	// (ALTER TABLE ADD PRIMARY KEY setting relhasindex) waits on it. Empty when the
	// grant targets a non-table object class. Design 0118-0109 (intra-grant-inplace).
	TableACL string
}

func (s *DropCompatStmt) Pos() int  { return s.pos }
func (s *DropCompatStmt) stmtNode() {}

func (s *CompatNoopStmt) Pos() int  { return s.pos }
func (s *CompatNoopStmt) stmtNode() {}

// CommentOnStmt represents a COMMENT ON statement for objects goopg tracks
// (TABLE, INDEX, COLUMN, CONSTRAINT, STATISTICS, VIEW, SEQUENCE, SCHEMA). The
// executor stores the description in pg_description via catalog.SetComment.
// M0097-0023; VIEW/SEQUENCE/SCHEMA added in DU-002 slice 145.
type CommentOnStmt struct {
	pos         int
	ObjKind     string        // "table", "index", "column", "constraint", "statistics", "view", "sequence", "schema", "function"
	ObjName     ObjectName    // table/index name, or table for constraint/column, or routine name for function
	SubName     string        // column name (ObjKind=column) or constraint name (ObjKind=constraint)
	Args        []FunctionArg // routine argument signature (ObjKind=function); nil for all other kinds
	Description string        // comment text; empty string = IS NULL (delete comment)
}

func (s *CommentOnStmt) Pos() int  { return s.pos }
func (s *CommentOnStmt) stmtNode() {}

// CreateStatisticsStmt represents CREATE STATISTICS name ON ... FROM table. M0097-0023.
type CreateStatisticsStmt struct {
	pos         int
	Name        ObjectName // statistics object name (possibly schema-qualified)
	FromTable   ObjectName // the table the statistics are defined on
	IfNotExists bool
}

func (s *CreateStatisticsStmt) Pos() int  { return s.pos }
func (s *CreateStatisticsStmt) stmtNode() {}

// LockTableRelation is one relation target inside a LOCK TABLE statement.
type LockTableRelation struct {
	Schema string
	Name   string
}

// LockTableStmt — `LOCK [TABLE] [ONLY] rel [, ...] [IN lock_mode MODE] [NOWAIT]`.
// Mode is the PostgreSQL lock mode name (e.g. "AccessExclusiveLock"). M0097.
type LockTableStmt struct {
	pos       int
	Relations []LockTableRelation
	Mode      string // PostgreSQL lock mode name
	NoWait    bool
}

func (s *LockTableStmt) Pos() int  { return s.pos }
func (s *LockTableStmt) stmtNode() {}

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
	pos             int
	Names           []ObjectName
	Only            []bool // parallel to Names: true = ONLY (skip inheritance children)
	Behavior        DropBehavior
	RestartIdentity bool // RESTART IDENTITY (false = CONTINUE IDENTITY default)
}

func (s *TruncateStmt) Pos() int  { return s.pos }
func (s *TruncateStmt) stmtNode() {}

// AlterTableActionKind discriminates the per-clause action of an
// ALTER TABLE statement.
type AlterTableActionKind int

const (
	AlterTableAddPrimaryKey AlterTableActionKind = iota
	AlterTableAddColumn
	AlterTableAddCheck // ADD [CONSTRAINT name] CHECK (expr)
	AlterTableNoOp     // Unknown constraint type accepted as no-op
	// AlterTableAddForeignKey is `ADD [CONSTRAINT name] FOREIGN
	// KEY (cols) REFERENCES table (cols) [NOT DEFERRABLE |
	// DEFERRABLE]`. v0 parses it for compatibility with HammerDB
	// TPC-H's post-load FK pass; enforcement is a no-op until
	// the constraint subsystem lands. See
	// docs/design/0003-0004-hammerdb-tpch-integration.md.
	AlterTableAddForeignKey
	// AlterTableAttachPartition — `ATTACH PARTITION child FOR VALUES …`.
	// Registers an existing table as a partition of the parent. M0096-0007.
	AlterTableAttachPartition
	// AlterTableDetachPartition — `DETACH PARTITION child`.
	// Removes a table from its partition parent. M0097-0028.
	AlterTableDetachPartition
	// AlterTableDropConstraint — `DROP CONSTRAINT name [RESTRICT|CASCADE]`.
	// For PK constraints, checks view→constraint dependencies before dropping.
	// M0097-0036 (functional_deps).
	AlterTableDropConstraint
	// AlterTableRenameTable — `RENAME TO new_name`.
	// Checks that the new name is not already taken. M0097-regress.
	AlterTableRenameTable
	// AlterTableRenameColumn — `RENAME COLUMN old TO new`.
	// Validates old column exists, new name not a system column, and no
	// inheritance child already has a column with the new name. M0097-regress.
	AlterTableRenameColumn
	// AlterTableInherit — `INHERIT parent_table`.
	// Registers an existing table as an inheritance child of the given parent.
	// Mirrors `CREATE TABLE child () INHERITS (parent)` at run time. M0097-0048.
	AlterTableInherit
	// AlterTableNoInherit — `NO INHERIT parent_table`.
	// Removes the inheritance relationship; no-op in goopg v0. M0097-0048.
	AlterTableNoInherit
	// AlterTableAlterColumnSet — `ALTER COLUMN name SET (options)`.
	// For heap tables this is a no-op in goopg v0; for indexes it raises
	// an appropriate error (M0097-0023 btree_index parity).
	AlterTableAlterColumnSet
	// AlterTableAlterColumnType — `ALTER COLUMN name TYPE newtype`.
	// Changes the column type in the catalog and rewrites existing heap rows
	// to re-encode them with the new type. M0097-0022.
	AlterTableAlterColumnType
	// AlterTableDropColumn — `DROP [COLUMN] [IF EXISTS] name [RESTRICT|CASCADE]`.
	// Marks the named column as dropped (hidden from user queries) while
	// retaining its heap slot for backward heap-tuple compatibility. M0097-0028.
	AlterTableDropColumn
	// AlterTableAddUnique — `ADD [CONSTRAINT name] UNIQUE (cols) [INCLUDE (incl)]`.
	// Creates a btree unique index; the index name comes from ConstraintName or
	// is auto-generated. M0097-0023.
	AlterTableAddUnique
	// AlterTableSetStatistics — `ALTER INDEX name ALTER COLUMN N SET STATISTICS value`.
	// Raises appropriate errors based on the column position and kind (key/expression/include).
	// ColumnName holds the column number as a string; CheckExpr holds the statistics value. M0097-0023.
	AlterTableSetStatistics
	// AlterTableSetStorage — `ALTER COLUMN name SET STORAGE type`.
	// Records the storage type (plain/main/external/extended) on the catalog column.
	// StorageType holds the storage name; ColumnName holds the column. M0097-0023.
	AlterTableSetStorage
	// AlterTableSetCompression — `ALTER COLUMN name SET COMPRESSION method`.
	// Records the per-column TOAST compression method (pglz/lz4) on the catalog
	// column purely for pg_dump round-trip fidelity (goopg does not TOAST).
	// CompressionType holds the method; ColumnName holds the column. DU-002 slice 183.
	AlterTableSetCompression
	// AlterTableSetDefault — `ALTER COLUMN name SET DEFAULT expr`.
	// Records the parsed DEFAULT expression on the catalog column
	// (Column.DefaultExpr) so it surfaces in pg_attrdef and round-trips
	// through pg_dump — inline on a printed local column, or as a separate
	// `ALTER TABLE ONLY ... ALTER COLUMN ... SET DEFAULT` when the column is a
	// suppressed inherited column. DefaultExpr holds the expression;
	// ColumnName holds the column. DU-002 slice 269.
	AlterTableSetDefault
	// AlterTableDropDefault — `ALTER COLUMN name DROP DEFAULT`.
	// Clears the catalog column's DEFAULT expression. ColumnName holds the
	// column. DU-002 slice 269.
	AlterTableDropDefault
	// AlterTableSetNotNull — `ALTER COLUMN name SET NOT NULL`.
	// Marks the catalog column NOT NULL (Column.NotNull) and records a named
	// NOT NULL constraint (pg_constraint contype='n', conislocal='t') so it
	// surfaces in pg_attribute.attnotnull and round-trips through pg_dump —
	// inline on a printed local column, or as a standalone `NOT NULL <col>`
	// constraint item in the child CREATE TABLE body when the column is a
	// suppressed inherited column (pg_dump.c:17213). ColumnName holds the
	// column. DU-002 slice 270.
	AlterTableSetNotNull
	// AlterTableDropNotNull — `ALTER COLUMN name DROP NOT NULL`.
	// Clears the catalog column's NOT NULL and drops its contype='n'
	// constraint. ColumnName holds the column. DU-002 slice 270.
	AlterTableDropNotNull
	// AlterTableAddNotNull — `ADD [CONSTRAINT name] NOT NULL col [NO INHERIT]`.
	// The named (PG18) counterpart of AlterTableSetNotNull: marks the column
	// NOT NULL and records a contype='n' constraint carrying the EXPLICIT name
	// from ConstraintName (auto-named `<table>_<col>_not_null` when omitted).
	// When the name differs from that default, pg_dump prints the
	// `CONSTRAINT <name> NOT NULL <col>` form (pg_dump.c:17228). ColumnName
	// holds the column; ConstraintName holds the optional name; NoInherit is
	// set for the `NO INHERIT` trailer. DU-002 slice 271.
	AlterTableAddNotNull
	// AlterIndexAttachPartition — `ALTER INDEX parent ATTACH PARTITION child`.
	// Registers child as a partition of parent in the index partition tree.
	// ConstraintName holds the parent index name; ChildIndexName holds the child. M0097-0023.
	AlterIndexAttachPartition
	// AlterTableSetReloptions — `ALTER TABLE name SET (param = value, …)`.
	// Merges table-level storage parameters into the relation's reloptions
	// (e.g. parallel_workers, fillfactor, autovacuum_enabled, toast_tuple_target).
	// With holds the parsed name→value pairs. Only options goopg models as
	// pg_class.reloptions take effect; others are accepted and ignored, matching
	// the CREATE TABLE WITH path. M0118-0001.
	AlterTableSetReloptions
	// AlterTableResetReloptions — `ALTER TABLE name RESET (param, …)`.
	// Clears the named table-level storage parameters back to their defaults.
	// With holds the option names as keys (values empty). M0118-0001.
	AlterTableResetReloptions
	// AlterTableValidateConstraint — `VALIDATE CONSTRAINT name`.
	// Validates a constraint previously added with NOT VALID, flipping
	// pg_constraint.convalidated from 'f' to 't'. Takes only a
	// ShareUpdateExclusiveLock (AlterTableGetLockLevel → AT_ValidateConstraint),
	// so it does not conflict with concurrent reads or writes. ConstraintName
	// holds the constraint name. M0118-0008 (alter-table-1 isolation spec).
	AlterTableValidateConstraint
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
	IncludeColumns []string  // non-key covering columns from INCLUDE (…) for PK/UNIQUE
	Column         ColumnDef // populated for AddColumn

	// Foreign-key extras (only populated for AddForeignKey).
	RefTable   ObjectName
	RefColumns []string
	Deferrable bool     // true if `DEFERRABLE`; false (default) if NOT DEFERRABLE or omitted
	OnDelete   FKAction // referential action for ON DELETE (default NO ACTION)
	OnUpdate   FKAction // referential action for ON UPDATE (default NO ACTION)
	NotValid   bool     // true if `NOT VALID` (skip validation of existing rows)

	// AttachPartitionOf is populated for AlterTableAttachPartition.
	// It holds the child table name and partition bounds. M0096-0007.
	AttachPartitionOf *PartitionOfClause
	// Restrict is set for AlterTableDropConstraint: true means RESTRICT
	// (default — error if dependents exist), false means CASCADE.
	Restrict bool

	// NewName is populated for AlterTableRenameTable and AlterTableRenameColumn.
	// Holds the new relation or column name. M0097-regress.
	NewName string
	// OldColumnName is populated for AlterTableRenameColumn and holds the
	// existing column name to be renamed. M0097-regress.
	OldColumnName string

	// InheritParent is populated for AlterTableInherit and AlterTableNoInherit.
	// Holds the parent table name. M0097-0048.
	InheritParent ObjectName

	// CheckExpr is the raw SQL expression for AlterTableAddCheck.
	CheckExpr string

	// NewType is the target column type for AlterTableAlterColumnType.
	// M0097-0022.
	NewType ColumnType
	// ColumnName is the column being modified for AlterTableAlterColumnType.
	// M0097-0022.
	ColumnName string
	// StorageType is the storage strategy for AlterTableSetStorage.
	// Values: "plain", "main", "external", "extended". M0097-0023.
	StorageType string
	// CompressionType is the TOAST compression method for AlterTableSetCompression.
	// Values: "pglz", "lz4". DU-002 slice 183.
	CompressionType string
	// DefaultExpr is the parsed DEFAULT expression for AlterTableSetDefault
	// (`ALTER COLUMN name SET DEFAULT expr`). Nil for AlterTableDropDefault.
	// DU-002 slice 269.
	DefaultExpr Expr
	// SetOptions holds the per-column attribute options captured from
	// `ALTER COLUMN name SET (opt=value, …)` for AlterTableAlterColumnSet, each
	// entry normalized to PG's stored `name=value` form (e.g. "n_distinct=0.5").
	// Recorded on catalog.Column.Options so pg_dump re-emits the clause via
	// pg_attribute.attoptions. DU-002 slice 185.
	SetOptions []string
	// With holds the table-level storage parameters for AlterTableSetReloptions
	// (name→value) and the option names for AlterTableResetReloptions (names as
	// keys, empty values). Parsed by parseWithOptions. M0118-0001.
	With map[string]string
	// NoInherit is set for AlterTableAddNotNull when the constraint carries a
	// `NO INHERIT` trailer (contype='n', connoinherit='t'). DU-002 slice 271.
	NoInherit bool
	// ChildIndexName is populated for AlterIndexAttachPartition and holds
	// the name of the child index to attach. M0097-0023.
	ChildIndexName string
	// DetachPartitionChild is populated for AlterTableDetachPartition and
	// holds the child table name. M0097-0028.
	DetachPartitionChild ObjectName
	// DetachConcurrently records the `CONCURRENTLY` trailer on
	// `DETACH PARTITION child CONCURRENTLY` (PG14+). The trailer follows the
	// child name; it is recorded here so the (deferred) two-phase concurrent
	// detach semantics can branch on it. The executor currently performs a
	// synchronous detach regardless. M0118-0008.
	DetachConcurrently bool
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
	// SetSchema holds the target schema name for ALTER TABLE/VIEW/MATERIALIZED VIEW
	// ... SET SCHEMA <newschema>. Empty means no SET SCHEMA action. M0097-0025.
	SetSchema string
	// SetLogged is "logged" or "unlogged" when ALTER TABLE SET LOGGED/UNLOGGED is parsed.
	SetLogged string
	// EnableDisableTrigger is true for ALTER TABLE ... ENABLE/DISABLE TRIGGER.
	// The trigger enable/disable itself is a semantic no-op in v0, but the
	// statement takes a transaction-scoped ShareRowExclusiveLock in PostgreSQL,
	// which the executor must acquire (alter-table-3 isolation spec, M0118-0008).
	EnableDisableTrigger bool
	// OwnerTo holds the target role name for ALTER TABLE ... OWNER TO role. Empty
	// means no OWNER TO action. The executor records it as the table's owning
	// role so the VACUUM/ANALYZE/CLUSTER maintenance-privilege check can tell
	// whether a SET ROLE session owns the relation. M0118-0008 (vacuum-conflict /
	// cluster-conflict).
	OwnerTo string
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
	Query     *SelectStmt // populated for COPY (SELECT …) TO …
	QueryDML  Stmt        // populated for COPY (INSERT/UPDATE/DELETE … RETURNING) TO …
	// SelectInto is set when the inner query is the deprecated
	// `SELECT … INTO …` form. PostgreSQL's grammar accepts it inside
	// COPY (...) but DoCopy rejects it; planCopy emits the matching
	// "COPY (SELECT INTO) is not supported" (0A000). M0097-0024.
	SelectInto bool
	Endpoint   CopyEndpoint
	Filename   string // filename or program command, when applicable
	Options    []CopyOption
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
	// FuncArgOut is an output parameter — set by the procedure body,
	// returned to the caller. Stage B (procedure follow-up).
	FuncArgOut
	// FuncArgInout is a bidirectional parameter — caller passes a
	// value that the procedure can modify and return. Stage B.
	FuncArgInout
	// FuncArgVariadic marks the last parameter as variadic. Stage B.
	FuncArgVariadic
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
	pos          int
	Name         string // empty for positional-only args
	Mode         FuncArgMode
	ModeExplicit bool // true when a mode keyword (IN/OUT/INOUT/VARIADIC) was explicitly specified
	Type         ColumnType
	Default      Expr // nil when no DEFAULT was given
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
	pos             int
	OrReplace       bool
	Name            ObjectName
	Args            []FunctionArg
	ReturnType      ColumnType
	ReturnsSet      bool   // RETURNS SETOF ... M0097-0020
	ReturnsTable    bool   // RETURNS TABLE (...) — table columns stored as trailing OUT args
	Language        string // lower-cased, e.g. "plpgsql"
	Body            string // raw source between the dollar-quote delimiters
	Strict          bool   // STRICT / RETURNS NULL ON NULL INPUT M0097-0035
	Volatile        string // "v"=volatile (default), "s"=stable, "i"=immutable
	SecurityDefiner bool   // SECURITY DEFINER
	Leakproof       bool   // LEAKPROOF
	BeginAtomic     bool   // PG14 BEGIN ATOMIC ... END body (no AS keyword)
	IsReturnForm    bool   // PG14 RETURN expr body (no AS keyword, body stored as "SELECT expr")
	Window          bool   // WINDOW attribute — marks function as a window function
	Parallel        string // proparallel: "u"=unsafe (default), "s"=safe, "r"=restricted
	Cost            string // procost: planner per-row cost override (COST n); "" = language default
	Rows            string // prorows: SRF result-row estimate override (ROWS n); "" = default
}

// AlterFunctionStmt — `ALTER FUNCTION name([argtypes]) attribute ...`
// Updates mutable function/procedure attributes (volatile, security, leakproof, strict).
type AlterFunctionStmt struct {
	pos         int
	Name        ObjectName
	Args        []FunctionArg // nil means no arg list given (applies to all overloads)
	IsProcedure bool
	IsRoutine   bool   // ALTER ROUTINE: applies to both functions and procedures (M0097-0022)
	RenameTo    string // RENAME TO new_name (M0097-0022)
	// Updated attributes (nil = not changed)
	Volatile        *string // "v", "s", "i"
	SecurityDefiner *bool
	Leakproof       *bool
	Strict          *bool
}

func (s *AlterFunctionStmt) Pos() int  { return s.pos }
func (s *AlterFunctionStmt) stmtNode() {}

func (s *CreateFunctionStmt) Pos() int  { return s.pos }
func (s *CreateFunctionStmt) stmtNode() {}

// DropFunctionStmt — `DROP FUNCTION [IF EXISTS] name [(arg_decl,
// …)] [CASCADE|RESTRICT]`. Multi-target DROP (comma-separated
// names) is upstream syntax but rare in practice and out of Stage A
// scope; supporting one name keeps the slice small. The optional
// argument list is stored verbatim so a later loop can implement
// overload-resolution drop semantics without re-parsing.
// DropFunctionItem is one function in a multi-name DROP FUNCTION.
type DropFunctionItem struct {
	Name ObjectName
	Args []FunctionArg
}

type DropFunctionStmt struct {
	pos      int
	IfExists bool
	Name     ObjectName
	Args     []FunctionArg // nil when no parenthesised arg list was given
	Behavior DropBehavior
	// Extras for multi-target DROP FUNCTION f1(args), f2(args).
	Extras []DropFunctionItem
}

func (s *DropFunctionStmt) Pos() int  { return s.pos }
func (s *DropFunctionStmt) stmtNode() {}

// CreateProcedureStmt is the AST for `CREATE [OR REPLACE] PROCEDURE ...`.
// Stage B (procedure follow-up) of M0015.
type CreateProcedureStmt struct {
	pos             int
	OrReplace       bool
	Name            ObjectName
	Args            []FunctionArg
	Language        string // lower-cased, e.g. "plpgsql"
	Body            string // raw source between the dollar-quote delimiters
	SecurityDefiner bool   // SECURITY DEFINER
	BeginAtomic     bool   // PG14 BEGIN ATOMIC ... END body (no AS keyword)
	Strict          bool   // STRICT / RETURNS NULL ON NULL INPUT
	Window          bool   // WINDOW attribute (invalid for procedures)
	Volatile        string // "v"=volatile (default), "s"=stable, "i"=immutable
}

func (s *CreateProcedureStmt) Pos() int  { return s.pos }
func (s *CreateProcedureStmt) stmtNode() {}

// CallStmt is the AST for `CALL procedure_name([arg [, ...]])`.
// Stage B (procedure follow-up) of M0015.
type CallStmt struct {
	pos      int
	Name     ObjectName
	Args     []Expr   // expressions for IN arguments
	ArgNames []string // parallel to Args; non-empty when caller used name=>val syntax
	// OUT/INOUT parameter result handling deferred
}

func (s *CallStmt) Pos() int  { return s.pos }
func (s *CallStmt) stmtNode() {}

// DropProcedureStmt is the AST for `DROP PROCEDURE/ROUTINE [IF EXISTS] name [(arg, ...)]`.
// Stage B (procedure follow-up) of M0015.
type DropProcedureStmt struct {
	pos      int
	IfExists bool
	Name     ObjectName
	Names    []ObjectName  // additional names for multi-name DROP (DROP PROCEDURE a, b)
	Args     []FunctionArg // nil when no parenthesised arg list was given
	Behavior DropBehavior
	ObjKind  string // "procedure" or "routine" (default "procedure")
}

func (s *DropProcedureStmt) Pos() int  { return s.pos }
func (s *DropProcedureStmt) stmtNode() {}

// TypeField describes one field in a composite type definition.
// M0097-composite.
type TypeField struct {
	Name      string // lower-case field name
	ColType   string // column type string (e.g. "bigint", "text")
	Collation string // per-field COLLATE name (e.g. "C"); empty = type default
}

// CreateTypeStmt — CREATE TYPE name AS ENUM (val1, val2, …) or
// CREATE TYPE name AS (field type, …). M0097-0017, M0097-composite.
type CreateTypeStmt struct {
	pos             int
	Name            string
	Schema          string
	IsEnum          bool
	EnumValues      []string
	IsComposite     bool
	CompositeFields []TypeField
}

func (s *CreateTypeStmt) Pos() int  { return s.pos }
func (s *CreateTypeStmt) stmtNode() {}

// AlterTypeStmt — ALTER TYPE name ADD VALUE [IF NOT EXISTS] val [BEFORE|AFTER ref]. M0097-0017.
// Also handles RENAME VALUE 'old' TO 'new' (M0097-0022).
type AlterTypeStmt struct {
	pos            int
	Name           string
	Schema         string
	AddValue       string
	IfNotExists    bool
	Before         string // reference value for BEFORE positioning
	After          string // reference value for AFTER positioning
	RenameOldValue string // RENAME VALUE: existing label (empty when ADD VALUE)
	RenameNewValue string // RENAME VALUE: replacement label
	RenameTo       string // RENAME TO: new type name (M0097-enum-rename)
	// ADD ATTRIBUTE col_name type — appends a field to a composite type.
	// DU-002 slice 253.
	AddAttrName string // new composite-type attribute name (empty when not ADD ATTRIBUTE)
	AddAttrType string // its type string (space-joined, e.g. "numeric ( 10 , 2 )")
	// Optional per-attribute COLLATE on ADD ATTRIBUTE (empty when none). DU-002 slice 258.
	AddAttrCollation string
	// RENAME ATTRIBUTE old TO new — renames a composite type field. DU-002 slice 254.
	RenameAttrOld string // existing attribute name (empty when not RENAME ATTRIBUTE)
	RenameAttrNew string // replacement attribute name
	// DROP ATTRIBUTE [IF EXISTS] attname — removes a composite type field. DU-002 slice 255.
	DropAttrName     string // attribute to drop (empty when not DROP ATTRIBUTE)
	DropAttrIfExists bool   // DROP ATTRIBUTE IF EXISTS — missing attr → NOTICE, not error
	// ALTER ATTRIBUTE attname [SET DATA] TYPE newtype — re-types a composite
	// type field in place. DU-002 slice 256.
	AlterAttrName string // attribute whose type changes (empty when not ALTER ATTRIBUTE)
	AlterAttrType string // its new type string (space-joined, e.g. "numeric ( 12 , 3 )")
	// Optional per-attribute COLLATE on ALTER ATTRIBUTE … TYPE (empty when none). DU-002 slice 259.
	AlterAttrCollation string
	// AttrCmds is the comma-combined attribute-subcommand list for
	// ALTER TYPE … (ADD|DROP|ALTER ATTRIBUTE …, …). PG only allows these three
	// actions to be combined in one statement (ADD VALUE / RENAME / OWNER are
	// singular). The parser populates one element per subcommand and mirrors
	// AttrCmds[0] into the legacy scalar fields above so the single-subcommand
	// executor branches + parser tests are unchanged; the executor routes the
	// multi-subcommand case (len > 1) through execAlterTypeAttrCmds. DU-002
	// slice 260.
	AttrCmds []AlterTypeAttrCmd
}

// AlterTypeAttrCmd is one attribute subcommand of an ALTER TYPE statement.
// DU-002 slice 260 (multi-subcommand ALTER TYPE).
type AlterTypeAttrCmd struct {
	Kind      string // "add" | "drop" | "alter"
	Name      string // target attribute name (lowercased)
	Type      string // ADD/ALTER: the (space-joined) new type string
	Collation string // ADD/ALTER: optional COLLATE name (empty when none)
	IfExists  bool   // DROP: IF EXISTS → a missing attribute is a NOTICE, not an error
}

func (s *AlterTypeStmt) Pos() int  { return s.pos }
func (s *AlterTypeStmt) stmtNode() {}

// DropTypeStmt — DROP TYPE [IF EXISTS] name [CASCADE|RESTRICT]. M0097-0017.
type DropTypeStmt struct {
	pos      int
	Names    []ObjectName
	IfExists bool
	Cascade  bool
}

func (s *DropTypeStmt) Pos() int  { return s.pos }
func (s *DropTypeStmt) stmtNode() {}

// CreateDomainStmt — CREATE DOMAIN name [AS] base_type [constraints]. M0097-0017.
type CreateDomainStmt struct {
	pos           int
	Name          string
	Schema        string
	BaseType      string  // base type name
	BaseTypeArgs  []int64 // base-type modifier args: varchar(20)→[20], numeric(10,2)→[10,2]. DU-002 slice 95.
	NotNull       bool
	CheckInValues []string // allowed values from CHECK (VALUE IN ('a','b','c')), M0097-domain-check
	Default       Expr     // DEFAULT expression AST, nil when no DEFAULT clause. DU-002 slice 92.
	// CheckExpr holds the raw SQL text of a generic (non-IN) domain CHECK
	// expression, e.g. `VALUE > 0`. CheckName is the explicit CONSTRAINT name
	// when one is given, "" for the auto-generated `<domain>_check`. The
	// `CHECK (VALUE IN (...))` form is kept separately in CheckInValues (its
	// ANY/ARRAY deparse is not yet rendered). DU-002 slice 96.
	CheckExpr string
	CheckName string
}

func (s *CreateDomainStmt) Pos() int  { return s.pos }
func (s *CreateDomainStmt) stmtNode() {}

// DropDomainStmt — DROP DOMAIN [IF EXISTS] name [CASCADE|RESTRICT]. M0097-0017.
type DropDomainStmt struct {
	pos      int
	Names    []ObjectName
	IfExists bool
	Cascade  bool
}

func (s *DropDomainStmt) Pos() int  { return s.pos }
func (s *DropDomainStmt) stmtNode() {}
