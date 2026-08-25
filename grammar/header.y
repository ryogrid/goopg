/* grammar/header.y — goyacc prologue copied verbatim into the generated */
/* parser (docs/design/not_ralph/01-architecture.md §3). Concatenation order */
/* (see Makefile gen-parser): header.y → tokens_gen.y → pg_grammar.y → */
/* goopg_ext.y → closing "%%". */
%{

package sqlparser

import (
	"github.com/goopg/goopg/internal/parser"
)

%}

%union {
	str    string           // IDENT / SCONST / keyword text / Op value ...
	ival   int              // ICONST / PARAM numbers
	b      bool             // set_quantifier ALL flag etc
	p      int              // byte position threading (see select_pos helper)
	stmt   parser.Stmt      // single-statement productions
	stmts  []parser.Stmt    // statement lists (root)
	expr   parser.Expr      // expression productions
	targets []parser.ResTarget
	rt     parser.ResTarget
	rvar   parser.RangeVar
	rvars   []parser.RangeVar
	sortbys []parser.SortBy
	sortby  parser.SortBy
	joins   []parser.JoinExpr
	fexpr   parser.FromExpr
	fexprs  []parser.FromExpr
	jspec   *joinSpec
	exprs   []parser.Expr
	tfr     *parser.TableFuncRef
	rfes    []parser.RowsFromEntry
	rfe     parser.RowsFromEntry
	strs   []string         // identifier lists
	qn     qname            // dotted-name carrier (support.go)
	node   any              // multi-value carriers like distinctInfo
	list   []any            // heterogeneous lists, mirroring PG's untyped List
}
