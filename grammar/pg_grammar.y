// grammar/pg_grammar.y — goyacc port of postgres/src/backend/parser/gram.y
// (PostgreSQL 18.3, READ-ONLY oracle). Porting conventions live in
// docs/design/not_ralph/02-grammar-porting-guide.md; rule blocks cite their
// upstream line ranges.
//
// Keyword tokens are NOT declared here: they come from grammar/tokens_gen.y,
// generated from kwlist.h (replaces gram.y :700-795 — names identical).
//
// SKELETON STATE (P0): declarations + a root production that accepts only an
// empty input. Statement rules arrive wave by wave (TODO.md P1..P6); the
// precedence block below lands with P2's operator expressions and must then
// be byte-identical to gram.y :824-903.

%start root

// Non-keyword terminals (gram.y :692-699 for the base set; named operator
// terminals per scan.l :968-977 — our legacy lexer folds these into generic
// operator strings, so the adapter splits by value, 05-risks #11).
%token <str> IDENT UIDENT FCONST SCONST USCONST BCONST XCONST Op
%token <ival> ICONST PARAM
%token TYPECAST DOT_DOT COLON_EQUALS EQUALS_GREATER
%token LESS_EQUALS GREATER_EQUALS NOT_EQUALS
%token NOT_LA NULLS_LA WITH_LA WITHOUT_LA FORMAT_LA

// NOTE: single-character symbol terminals (';' '(' ',' ...) are NOT declared
// here — matching upstream. goyacc maps them by ASCII CODE through yyTok1
// (see tables.go); declaring them would be redundant noise.

%%

root:
		/* empty */
		{
			yylex.(*lexerState).out = nil
		}
