package planner

import (
	"github.com/goopg/goopg/internal/parser"
)

// ExtractLikePrefix returns the fixed prefix of a SQL LIKE pattern.
//
// The prefix is the maximal sequence of literal characters before the first
// unescaped wildcard (`%` or `_`). The `exact` flag is true iff the entire
// pattern is literal (no wildcards at all), meaning the LIKE is equivalent
// to a plain equality check.
//
// Returns ("", false, false) when no useful prefix can be derived — i.e.,
// the pattern begins with a wildcard and no initial range can be formed.
//
// Escape handling follows SQL's default escape character `\`:
//   - `\%` → literal `%` in prefix
//   - `\_` → literal `_` in prefix
//   - `\\` → literal `\` in prefix
//
// Upstream reference: postgres/src/backend/utils/adt/like_match.c —
// pattern-prefix extraction for index optimisation.
func ExtractLikePrefix(pattern string) (prefix string, exact bool, ok bool) {
	var buf []byte
	i := 0
	for i < len(pattern) {
		ch := pattern[i]
		// SQL default escape: '\' followed by a meta character.
		if ch == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			if next == '%' || next == '_' || next == '\\' {
				buf = append(buf, next)
				i += 2
				continue
			}
			// Unknown escape: treat '\' as literal.
			buf = append(buf, ch)
			i++
			continue
		}
		if ch == '%' || ch == '_' {
			// Unescaped wildcard: prefix ends here.
			// ok=true iff we consumed at least one literal char before
			// the wildcard (empty prefix gives no useful range bound).
			return string(buf), false, len(buf) > 0
		}
		buf = append(buf, ch)
		i++
	}
	// Reached end without a wildcard: the entire pattern is literal.
	return string(buf), true, len(buf) > 0
}

// IncrementString returns the lexicographically-smallest string strictly
// greater than s under C-collation byte ordering (the same ordering goopg's
// B-tree uses for text/varchar/char columns).
//
// Algorithm: scan right-to-left for the first byte < 0xFF, increment it,
// and drop all subsequent bytes. Returns ("", false) when every byte in s
// is 0xFF or s is empty (no finite successor exists).
//
// Upstream reference: postgres/src/backend/utils/adt/selfuncs.c —
// `make_greater_string`.
func IncrementString(s string) (string, bool) {
	if len(s) == 0 {
		return "", false
	}
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false // all bytes were 0xFF
}

// injectLikeRangePredicates rewrites a WHERE-clause parser.Expr by adding
// synthetic range predicates alongside each `col LIKE 'prefix%'` conjunct.
//
// For each `col LIKE 'literal_prefix%'` found at the top AND-level:
//   - If the pattern has no wildcards (exact match): injects `col = 'prefix'`.
//   - If a fixed prefix exists: injects `col >= 'prefix' AND col < 'successor'`.
//
// The original LIKE predicate is preserved so the executor continues to
// evaluate it for correctness (e.g. `'foo%bar'` patterns). The redundant
// range predicates give the planner's tryRangeIndexScan a B-tree-compatible
// predicate form that activates index scan planning.
//
// NOT LIKE predicates are not transformed: a range cannot constrain NOT LIKE.
// The transformation is gated on C-collation byte ordering, which is correct
// for all text/varchar/char columns in goopg v0.
func injectLikeRangePredicates(where parser.Expr) parser.Expr {
	if where == nil {
		return nil
	}
	conjuncts := collectAndConjuncts(where)
	var extra []parser.Expr

	for _, conj := range conjuncts {
		b, ok := conj.(*parser.BinaryOp)
		if !ok || b.Op != parser.OpLike {
			continue
		}
		colRef, ok := b.Left.(*parser.ColumnRef)
		if !ok {
			continue
		}
		sc, ok := b.Right.(*parser.StringConst)
		if !ok {
			continue
		}
		prefix, _, ok := ExtractLikePrefix(sc.Value)
		if !ok {
			continue
		}
		// For both exact ('foo') and prefix ('foo%') patterns, inject a
		// range col >= 'foo' AND col < 'fop'. The LIKE post-filter in the
		// enclosing Filter node guarantees correctness: for exact patterns
		// it additionally rejects 'foob', 'fooz', etc. The range narrows
		// the B-tree scan so the executor reads only the candidate rows.
		extra = append(extra, &parser.BinaryOp{
			Op:    parser.OpGe,
			Left:  colRef,
			Right: &parser.StringConst{Value: prefix},
		})
		if succ, ok := IncrementString(prefix); ok {
			extra = append(extra, &parser.BinaryOp{
				Op:    parser.OpLt,
				Left:  colRef,
				Right: &parser.StringConst{Value: succ},
			})
		}
	}

	if len(extra) == 0 {
		return where
	}
	// Reconstruct: original WHERE AND (all extra predicates).
	result := where
	for _, e := range extra {
		result = &parser.BinaryOp{Op: parser.OpAnd, Left: result, Right: e}
	}
	return result
}
