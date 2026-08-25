package sqlparser

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// Batch-level routing (docs/design/not_ralph/03-strangler-migration.md §2).
//
// Semantics pinned at P0.8:
//
//   - The WHOLE batch routes to the new parser only when EVERY statement
//     fragment's leading terminal belongs to a routed class. Mixed batches
//     (one routed + one unrouted statement) stay on the legacy parser for
//     now — per-fragment mixing would require legacy to parse from token
//     slices, which it cannot do yet. Revisit per wave; the wrapper
//     invariant below is what later waves must preserve.
//   - Deterministic: no try-new-fall-back. A class is added to routedStmts
//     only after its wave gate is green.

// SplitStatements splits a token slice into per-statement fragments at
// top-level ';' symbols. Dollar-quoted bodies are opaque single tokens by
// the time we see them (legacy lexer, lexer.go:430-464), so every ';' symbol
// token is a genuine separator; a ';' that would land inside parens belongs
// to invalid SQL and both parsers reject it either way.
func SplitStatements(toks []parser.Token) [][]parser.Token {
	var out [][]parser.Token
	start := 0
	for i, t := range toks {
		if t.Kind == parser.TokenSymbol && t.Value == ";" && i > start {
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	if start < len(toks) {
		rest := toks[start:]
		// The legacy lexer appends an explicit TokenEOF; a trailing
		// semicolon leaves just that sentinel behind — not a fragment.
		for i := range rest {
			if rest[i].Kind != parser.TokenEOF {
				out = append(out, rest)
				break
			}
		}
	}
	return out
}

// routedStmts maps leading-terminal KEYS to their routing decision. Keys are
// lowercase leading words — keywords arrive via TokenKeyword/TokenIdent and
// ident-led statements (start/discard/fetch/move/close/refresh/grant/revoke/
// listen/notify/unlisten/lock) via TokenIdent, matched case-insensitively.
//
// EMPTY: P2-F flip attempted 2026-08-25 but reverted — TPC-H Q12 fails
// (grammar gap). Re-enable after fixing; see TODO P2-F for details.
var routedStmts = map[string]bool{"select": true}

// routeBatch reports whether the whole batch can go to the new parser, plus
// the parsed statements when it can.
func routeBatch(toks []parser.Token) ([]parser.Stmt, bool, error) {
	frags := SplitStatements(toks)
	if len(frags) == 0 {
		return nil, false, nil // let legacy decide what an empty batch means
	}
	for _, f := range frags {
		if !fragmentRouted(f) {
			return nil, false, nil
		}
	}
	var all []parser.Stmt
	for _, f := range frags {
		stmts, err := ParseOne(f, fragmentBase(toks, f))
		if err != nil {
			return nil, true, err
		}
		all = append(all, stmts...)
	}
	return all, true, nil
}

// fragmentRouted inspects the fragment's first meaningful token.
func fragmentRouted(frag []parser.Token) bool {
	t := firstMeaningful(frag)
	if t == nil {
		return false // empty fragments stay legacy
	}
	switch t.Kind {
	case parser.TokenKeyword, parser.TokenIdent:
		key := strings.ToLower(t.Value)
		if key == "with" {
			return withFollowerRouted(frag)
		}
		return routedStmts[key]
	case parser.TokenSymbol:
		if t.Value == "(" {
			return routedStmts["select"]
		}
		return false
	default:
		return false
	}
}

// withFollowerRouted scans past the balanced-paren CTE list after WITH and
// checks whether the FOLLOWER keyword (SELECT/INSERT/etc) is routed.
// Returns false if the leading token is not WITH or if the scan hits an
// unexpected token.
// withFollowerRouted scans past WITH tracking paren depth and returns true
// iff any keyword at depth 0 is in routedStmts. The CTE bodies are inside
// parens (depth > 0) so they're skipped naturally; the main query keyword
// (SELECT/INSERT/etc) appears at depth 0 after the last CTE body closes.
func withFollowerRouted(toks []parser.Token) bool {
	depth := 0
	for i := 1; i < len(toks); i++ { // skip WITH
		tok := toks[i]
		if tok.Kind == parser.TokenSymbol {
			switch tok.Value {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		if depth > 0 || tok.Kind != parser.TokenKeyword {
			continue
		}
		kw := strings.ToLower(tok.Value)
		switch kw {
		case "as":
			continue // part of cte definition
		default:
			return routedStmts[kw]
		}
	}
	return false
}



func firstMeaningful(frag []parser.Token) *parser.Token {
	for i := range frag {
		switch frag[i].Kind {
		case parser.TokenEOF:
			continue
		default:
			return &frag[i]
		}
	}
	return nil
}

// fragmentBase returns the absolute byte offset of a fragment's first token
// relative to the parent slice start (both carry absolute positions today,
// so this is 0 in practice; kept for the day slices become re-based).
func fragmentBase(parent, frag []parser.Token) int {
	if len(parent) == 0 || len(frag) == 0 {
		return 0
	}
	base := parent[0].Pos
	if frag[0].Pos >= base {
		return frag[0].Pos - base
	}
	return frag[0].Pos
}

// RouteBatch is the production wiring point: postmaster assigns
// parser.RouteBatch = sqlparser.RouteBatch at startup. See the hook doc in
// internal/parser for the contract.
func RouteBatch(toks []parser.Token) ([]parser.Stmt, bool, error) {
	return routeBatch(toks)
}
