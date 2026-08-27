/* grammar/header.y — goyacc prologue copied verbatim into the generated */
/* parser (docs/design/not_ralph/01-architecture.md §3). Concatenation order */
/* (see Makefile gen-parser): header.y → tokens_gen.y → pg_grammar.y → */
/* goopg_ext.y → closing "%%". */
%{

package sqlparser

import (
	"strconv"

	"github.com/goopg/goopg/internal/parser"
)

%}

/* TYPEDLIT — synthetic terminal for the typed-literal forms the adapter
   folds (IDENT date/time/timestamp + SCONST; gram.y AexprConst Typename
   Sconst). str carries "type\x1fvalue"; see typedLitParts in support.go.
   interval is NOT folded here: it is a real kwlist keyword and goes through
   the grammar's INTERVAL SCONST rule. */
%token <str> TYPEDLIT
%token <str> VALUES_LA
/* CHECKBODY: `CHECK ( ... )` folded by the adapter into ONE terminal whose
   pos is the '(' and whose ival is the ')' — legacy never parses a check
   body, it records a token join of whatever sits between the parens. */
%token <ival> CHECKBODY

%union {
	str    string           // IDENT / SCONST / keyword text / Op value ...
	ival   int              // ICONST / PARAM numbers
	i64    int64            // signed integer literals in option lists
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
	vrows   [][]parser.Expr
	ctes    []*parser.CommonTableExpr
	withc   *parser.WithClause
	rfe     parser.RowsFromEntry
	strs   []string         // identifier lists
	qn     qname            // dotted-name carrier (support.go)
	node   any              // multi-value carriers like distinctInfo
	wd     *parser.WindowDef
	fr     *parser.WindowFrame
	ct     castType
	isrc   *insSrc
	onames []parser.ObjectName
	fargs  []parser.FunctionArg
	ivq    ivQual
	fitems []parser.DropFunctionItem
	ctt    *ctTail
	wp     [2]string
	oc     *parser.OnConflictClause
	oct    *parser.OnConflictTarget
	ualist []parser.UpdateAssign
	ua     parser.UpdateAssign
	nwd    parser.NamedWindowDef
	nwds   []parser.NamedWindowDef
	list   []any            // heterogeneous lists, mirroring PG's untyped List
}
