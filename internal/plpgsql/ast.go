// Package plpgsql parses PL/pgSQL routine bodies stored verbatim in
// the catalog into structured AST nodes the future interpreter can
// walk. M0015 Stage A step 4: parser + AST only — interpreter and
// SPI bridge land in step 5.
//
// The parser reuses goopg's main SQL lexer (parser.Lex) for
// tokenisation; PL/pgSQL keywords (BEGIN / END / RETURN / etc.) are
// declared in `internal/parser/token.go`. Inline SQL expressions
// (e.g. the value in `RETURN expr`) parse via the public
// `parser.ParseExpr` so type-checking / planning / execution can
// reuse the same AST machinery as top-level SELECT targets.
//
// See docs/design/0015-0004-plpgsql-body-parser-and-ast.md.
package plpgsql

import (
	"github.com/goopg/goopg/internal/parser"
)

// Stmt is a PL/pgSQL routine-body statement node. Implementations
// must be value-comparable enough that future interpreter tests
// can equality-check expected shapes; the embedded `pos` keeps
// error messages pointed at the source.
type Stmt interface {
	// Pos returns the 0-based byte offset within the routine body
	// where this statement begins.
	Pos() int
	plpgsqlStmtNode()
}

// Block is the upstream PL/pgSQL block:
//
//	[ DECLARE <declarations> ]
//	BEGIN
//	    <statements>
//	END [ ';' ]
//
// Stage A 4a accepted the BEGIN/END skeleton with RETURN as the only
// statement; 4b adds the optional DECLARE prefix and assignment so
// trivial functions with locals (`x := 1; RETURN x;`) parse. Label
// and EXCEPTION-block forms remain Stage A 4d+ scope.
type Block struct {
	pos          int
	Declarations []*Declaration
	Statements   []Stmt
}

func (b *Block) Pos() int         { return b.pos }
func (b *Block) plpgsqlStmtNode() {}

// Declaration is one entry in a `DECLARE` section:
//
//	varname [CONSTANT] type [NOT NULL] [DEFAULT expr | := expr] ';'
//
// Stage A 4b drops CONSTANT and NOT NULL — both surface a clean
// "Stage A 4b" diagnostic so handwritten PL/pgSQL using them gets
// a specific message instead of a generic syntax error. The future
// 4c slice extends `Declaration` with the missing fields rather
// than reshape the struct.
type Declaration struct {
	pos     int
	Name    string
	Type    parser.ColumnType
	Default parser.Expr // nil when no DEFAULT / := initializer
}

func (d *Declaration) Pos() int { return d.pos }

// AssignStmt is `target := value;`. Stage A 4b accepts simple
// variable targets only; future slices extend this for record-
// field and array-element assignment.
type AssignStmt struct {
	pos    int
	Target string
	Value  parser.Expr
}

func (a *AssignStmt) Pos() int         { return a.pos }
func (a *AssignStmt) plpgsqlStmtNode() {}

// IfStmt is `IF condition THEN statements [ ELSIF condition THEN
// statements ]* [ ELSE statements ] END IF;`.
type IfStmt struct {
	pos     int
	Cond    parser.Expr
	Then    []Stmt
	Elsifs  []*Elsif
	Else    []Stmt
}

func (i *IfStmt) Pos() int         { return i.pos }
func (i *IfStmt) plpgsqlStmtNode() {}

type Elsif struct {
	pos  int
	Cond parser.Expr
	Then []Stmt
}

// LoopStmt is `LOOP statements END LOOP;`. Stage A 4d adds the
// bare loop; WHILE and FOR variants arrive in the same slice.
type LoopStmt struct {
	pos  int
	Body []Stmt
}

func (l *LoopStmt) Pos() int         { return l.pos }
func (l *LoopStmt) plpgsqlStmtNode() {}

// WhileStmt is `WHILE condition LOOP statements END LOOP;`.
type WhileStmt struct {
	pos  int
	Cond parser.Expr
	Body []Stmt
}

func (w *WhileStmt) Pos() int         { return w.pos }
func (w *WhileStmt) plpgsqlStmtNode() {}

// ForStmt is `FOR var IN [REVERSE] lower..upper [BY step] LOOP
// statements END LOOP;`. Stage A 4d only supports integer-range
// FOR loops.
type ForStmt struct {
	pos     int
	Var     string
	Reverse bool
	Lower   parser.Expr
	Upper   parser.Expr
	Step    parser.Expr // nil if missing
	Body    []Stmt
}

func (f *ForStmt) Pos() int         { return f.pos }
func (f *ForStmt) plpgsqlStmtNode() {}

// ForSelectStmt is `FOR rec IN query LOOP stmts END LOOP;`.
// M0097-0012: query cursor loop over a SQL SELECT result.
type ForSelectStmt struct {
	pos  int
	Var  string // record variable name
	SQL  string // raw SQL text of the SELECT query
	Body []Stmt
}

func (f *ForSelectStmt) Pos() int         { return f.pos }
func (f *ForSelectStmt) plpgsqlStmtNode() {}

// ExitStmt is `EXIT [ WHEN condition ];`. Stage A 4d only
// supports exiting the innermost loop.
type ExitStmt struct {
	pos  int
	Cond parser.Expr // optional
}

func (e *ExitStmt) Pos() int         { return e.pos }
func (e *ExitStmt) plpgsqlStmtNode() {}

// ContinueStmt is `CONTINUE [ WHEN condition ];`.
type ContinueStmt struct {
	pos  int
	Cond parser.Expr // optional
}

func (c *ContinueStmt) Pos() int         { return c.pos }
func (c *ContinueStmt) plpgsqlStmtNode() {}

// PerformStmt is `PERFORM expression;`. Stage A only supports
// scalar expressions.
type PerformStmt struct {
	pos  int
	Expr parser.Expr
}

func (p *PerformStmt) Pos() int         { return p.pos }
func (p *PerformStmt) plpgsqlStmtNode() {}

// ReturnStmt is `RETURN expr;`. Stage A only emits scalar return
// values; SETOF / TABLE / RETURN NEXT / RETURN QUERY arrive in
// Stage B.
type ReturnStmt struct {
	pos  int
	Expr parser.Expr
}

func (r *ReturnStmt) Pos() int          { return r.pos }
func (r *ReturnStmt) plpgsqlStmtNode() {}

// SQLStmt is an embedded SQL DML/query statement in a PL/pgSQL body.
// The raw SQL text is stored verbatim; at execution time the executor
// substitutes OLD.* / NEW.* references with the trigger context rows,
// then plans and executes via the normal SQL path. M0096-0012.
type SQLStmt struct {
	pos  int
	SQL  string // raw SQL text (untransformed)
}

func (s *SQLStmt) Pos() int          { return s.pos }
func (s *SQLStmt) plpgsqlStmtNode() {}

// ExceptionHandler is one WHEN clause in an EXCEPTION block.
// Conditions are SQLSTATE codes or condition names. M0097-0012.
type ExceptionHandler struct {
	Conditions []string
	Body       []Stmt
}

// ExceptionBlock wraps a BEGIN...EXCEPTION...END section.
// TryBody is the protected statement list. M0097-0012.
type ExceptionBlock struct {
	pos      int
	TryBody  []Stmt
	Handlers []*ExceptionHandler
}

func (e *ExceptionBlock) Pos() int         { return e.pos }
func (e *ExceptionBlock) plpgsqlStmtNode() {}

// ExecuteStmt is `EXECUTE expr [INTO var] [USING expr, ...]`.
// Executes a dynamically-constructed SQL string. M0100-0005.
type ExecuteStmt struct {
	pos     int
	Query   parser.Expr   // expression producing the SQL string
	IntoVar string        // target variable name (empty if no INTO clause)
	Using   []parser.Expr // USING argument expressions (may be nil)
}

func (e *ExecuteStmt) Pos() int          { return e.pos }
func (e *ExecuteStmt) plpgsqlStmtNode() {}

// RaiseStmt is `RAISE [level] 'message'`. Level is "notice",
// "warning", "exception" / "error" (default). NOTICE and WARNING are
// silently discarded; ERROR/EXCEPTION surfaces an ExecError.
// M0096-0012.
type RaiseStmt struct {
	pos           int
	Level         string // "notice", "warning", "error"
	Msg           string // static message text (format substitution deferred)
	ConditionName string // set when RAISE condition_name (not a level keyword)
}

func (r *RaiseStmt) Pos() int          { return r.pos }
func (r *RaiseStmt) plpgsqlStmtNode() {}
