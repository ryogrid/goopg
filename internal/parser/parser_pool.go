package parser

import "sync"

// perf-optimize-take3 candidate C: the goyacc parser struct is 26,664 bytes
// (25,088 of it the [16]yySymType value stack), heap-allocated and zeroed by
// `yyNewParser` on EVERY statement parse — 54% of all bytes the server
// allocates. PostgreSQL's equivalent is a 200-slot YYSTYPE array on the C
// stack, because its %union members are all pointers or scalars (8 bytes);
// Go has no unions, so the port's yySymType is the SUM of its 56 members.
//
// Pooling the struct removes the malloc, the heap-bitmap write and the GC
// pressure, but NOT the zeroing — and the zeroing cannot be skipped. goyacc
// seeds $$ for an ε-production from the slot ABOVE the stack top:
//
//	// reduced production is ε, $1 is possibly out of range.
//	yyVAL = yyS[yyp+1]                        (yacc_parser.go:10527)
//
// With a freshly allocated parser that slot reads as a zero yySymType. With a
// recycled one it would read the PREVIOUS statement's data, so an ε-production
// action that sets only some fields would hand the next reduction a stale
// value from an unrelated parse — a silently wrong AST, which is exactly the
// failure mode that got the M0069-0001 slot pool reverted twice. A SQL grammar
// is full of ε-productions (every opt_* clause), so clearing is mandatory, not
// defensive.
//
// This file is hand-written on purpose: `make gen-parser` overwrites
// yacc_parser.go, so the pool must live outside it.
var parserPool = sync.Pool{New: func() any { return new(yyParserImpl) }}

// parsePooled is yyParse with the parser struct recycled.
func parsePooled(yylex yyLexer) int {
	p := parserPool.Get().(*yyParserImpl)
	rc := p.Parse(yylex)
	// Reset before returning to the pool rather than after Get, so the pool
	// never holds references to a finished statement's AST (the value stack
	// slots alias parse-produced nodes) and the next Get is a plain hit.
	p.lval = yySymType{}
	p.char = 0
	clear(p.stack[:])
	parserPool.Put(p)
	return rc
}
