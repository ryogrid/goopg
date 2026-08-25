// grammar/header.y — goyacc prologue copied verbatim into the generated
// parser (docs/design/not_ralph/01-architecture.md §3). Concatenation order
// (see Makefile gen-parser): header.y → tokens_gen.y → pg_grammar.y →
// goopg_ext.y → closing "%%".
%{

package sqlparser

import (
	"github.com/goopg/goopg/internal/parser"
)

%}

%union {
	str   string           // IDENT / SCONST / keyword text / Op value ...
	ival  int              // ICONST / PARAM numbers
	pos   int              // byte offset of the production's first token
	stmt  parser.Stmt      // single-statement productions (P1+)
	stmts []parser.Stmt    // statement lists (root)
	expr  parser.Expr      // expression productions (P2+)
	list  []any            // heterogeneous lists, mirroring PG's untyped List
}
