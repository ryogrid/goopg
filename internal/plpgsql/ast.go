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

// Block is `BEGIN <statements> END` — the top-level shape of every
// PL/pgSQL routine body Stage A 4a accepts. Future slices add the
// optional `DECLARE` prefix and label / loop forms.
type Block struct {
	pos        int
	Statements []Stmt
}

func (b *Block) Pos() int          { return b.pos }
func (b *Block) plpgsqlStmtNode() {}

// ReturnStmt is `RETURN expr;`. Stage A only emits scalar return
// values; SETOF / TABLE / RETURN NEXT / RETURN QUERY arrive in
// Stage B.
type ReturnStmt struct {
	pos  int
	Expr parser.Expr
}

func (r *ReturnStmt) Pos() int          { return r.pos }
func (r *ReturnStmt) plpgsqlStmtNode() {}
