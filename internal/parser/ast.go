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

// SortBy is one entry in an ORDER BY list.
type SortBy struct {
	pos  int
	Expr Expr
	Desc bool // true for DESC, false for ASC (the default)
}

func (s SortBy) Pos() int { return s.pos }

// SelectStmt — `SELECT [DISTINCT] target_list
//
//	[FROM from_item [, …]] [WHERE expr] [ORDER BY sort_list]
//	[LIMIT n] [OFFSET n]`. JOINs, GROUP BY, HAVING, and set ops are
//
// deferred — see fix_plan.
type SelectStmt struct {
	pos      int
	Distinct bool
	Targets  []ResTarget
	From     []RangeVar
	Where    Expr // nil when absent
	OrderBy  []SortBy
	Limit    Expr // nil when absent; integer expression in v0
	Offset   Expr // nil when absent
}

func (s *SelectStmt) Pos() int  { return s.pos }
func (s *SelectStmt) stmtNode() {}
