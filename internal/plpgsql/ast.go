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

// ReturnStmt is `RETURN expr;`. Stage A only emits scalar return
// values; SETOF / TABLE / RETURN NEXT / RETURN QUERY arrive in
// Stage B.
type ReturnStmt struct {
	pos  int
	Expr parser.Expr
}

func (r *ReturnStmt) Pos() int          { return r.pos }
func (r *ReturnStmt) plpgsqlStmtNode() {}
