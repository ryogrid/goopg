/* grammar/header.y — goyacc prologue copied verbatim into the generated */
/* parser (docs/design/not_ralph/01-architecture.md §3). Concatenation order */
/* (see Makefile gen-parser): header.y → tokens_gen.y → pg_grammar.y → */
/* goopg_ext.y → closing "%%". */
%{

package parser

import (
	"strconv"
	"strings"
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
	stmt   Stmt      // single-statement productions
	stmts  []Stmt    // statement lists (root)
	expr   Expr      // expression productions
	targets []ResTarget
	rt     ResTarget
	rvar   RangeVar
	rvars   []RangeVar
	sortbys []SortBy
	sortby  SortBy
	joins   []JoinExpr
	fexpr   FromExpr
	fexprs  []FromExpr
	jspec   *joinSpec
	exprs   []Expr
	tfr     *TableFuncRef
	rfes    []RowsFromEntry
	vrows   [][]Expr
	ctes    []*CommonTableExpr
	withc   *WithClause
	rfe     RowsFromEntry
	strs   []string         // identifier lists
	qn     qname            // dotted-name carrier (support.go)
	node   any              // multi-value carriers like distinctInfo
	wd     *WindowDef
	fr     *WindowFrame
	ct     castType
	isrc   *insSrc
	onames []ObjectName
	fargs  []FunctionArg
	ivq    ivQual
	lrels  []LockTableRelation
	lrel   LockTableRelation
	mwc    *MergeWhenClause
	mwcs   []*MergeWhenClause
	tflds  []TypeField
	tfld   TypeField
	nodes  []any
	copts  []CopyOption
	telems []*tableElem
	copt   CopyOption
	fitems []DropFunctionItem
	ctt    *ctTail
	wp     [2]string
	oc     *OnConflictClause
	oct    *OnConflictTarget
	ualist []UpdateAssign
	ua     UpdateAssign
	nwd    NamedWindowDef
	nwds   []NamedWindowDef
	list   []any            // heterogeneous lists, mirroring PG's untyped List
}
